package syncer

import (
	"errors"
	"sort"

	"go.sia.tech/core/types"
)

type encodedByteCount int

func (n *encodedByteCount) Write(p []byte) (int, error) {
	*n += encodedByteCount(len(p))
	return len(p), nil
}

// qdayPoolRelayPlan packs a verified, ordered pool snapshot into standalone
// sets. Shared parents appear once per packet, and transaction witnesses share
// the immutable snapshot instead of being copied for every descendant.
func qdayPoolRelayPlan(basis types.ChainIndex, txns []types.V2Transaction, maxRequestBytes int) ([]txpoolRelaySet, error) {
	const requestOverhead = 8 + 32 + 8 // chain index and transaction count
	limit := maxRequestBytes - requestOverhead
	sizes := make([]int, len(txns))
	parents := make([][]int, len(txns))
	outputs := make(map[types.SiacoinOutputID]int)
	for i, txn := range txns {
		var n encodedByteCount
		e := types.NewEncoder(&n)
		txn.EncodeTo(e)
		if err := e.Flush(); err != nil {
			return nil, err
		}
		sizes[i] = int(n)
		for _, in := range txn.SiacoinInputs {
			if in.Parent.StateElement.LeafIndex != types.UnassignedLeafIndex {
				continue
			}
			parent, ok := outputs[in.Parent.ID]
			if !ok {
				return nil, errors.New("transaction pool snapshot is missing an unconfirmed parent")
			}
			parents[i] = append(parents[i], parent)
		}
		id := txn.ID()
		for j := range txn.SiacoinOutputs {
			outputs[txn.SiacoinOutputID(id, j)] = i
		}
	}
	var sets []txpoolRelaySet
	var packet []types.V2Transaction
	inPacket := make(map[int]bool)
	packetBytes := 0
	flush := func() {
		if len(packet) > 0 {
			sets = append(sets, txpoolRelaySet{basis: basis, txns: packet})
		}
		packet, packetBytes = nil, 0
		clear(inPacket)
	}
	for i := range txns {
		for {
			needed := make(map[int]bool)
			stack, size := []int{i}, 0
			for len(stack) > 0 && size <= limit {
				j := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if inPacket[j] || needed[j] {
					continue
				}
				needed[j] = true
				size += sizes[j]
				stack = append(stack, parents[j]...)
			}
			if packetBytes+size > limit && len(packet) > 0 {
				flush()
				continue // this packet also needs the flushed ancestors
			} else if size > limit {
				// A dependency chain larger than one wire request cannot be
				// offered as a standalone set until some ancestors confirm.
				// Its valid prefixes and unrelated payments still propagate.
				break
			}
			indices := make([]int, 0, len(needed))
			for j := range needed {
				indices = append(indices, j)
			}
			sort.Ints(indices) // pool order puts every parent before its children
			for _, j := range indices {
				packet = append(packet, txns[j])
				inPacket[j] = true
			}
			packetBytes += size
			break
		}
	}
	flush()
	return sets, nil
}
