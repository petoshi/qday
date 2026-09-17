// Package qday builds native QDAY transactions and CPU mining candidates.
package qday

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

// Candidate revalidates transactions at the next height, dropping expired or
// reorged pool entries. The marker commits the miner's full protected policy.
func Candidate(s consensus.State, miner types.QdayKeys, txns []types.V2Transaction, timestamp time.Time) types.Block {
	var entropy [8]byte
	rand.Read(entropy[:])
	return CandidateWithNonce(s, miner, txns, timestamp, binary.LittleEndian.Uint64(entropy[:]))
}

// CandidateWithNonce builds a candidate with a caller-selected coinbase marker
// nonce. Pool controllers use the nonce to give independent work to miners
// without changing the payout or transaction set.
func CandidateWithNonce(s consensus.State, miner types.QdayKeys, txns []types.V2Transaction, timestamp time.Time, markerNonce uint64) types.Block {
	marker := types.V2Transaction{ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayCoinbase, Nonce: markerNonce, Keys: []types.QdayKeys{miner}}).Encode()}
	b := types.Block{ParentID: s.Index.ID, Timestamp: timestamp.UTC().Truncate(time.Second), MinerPayouts: []types.SiacoinOutput{{Value: s.BlockReward(), Address: miner.Policy().Address()}}, V2: &types.V2BlockData{Height: s.Index.Height + 1, Transactions: []types.V2Transaction{marker}}}
	ms := consensus.NewMidState(s)
	weight := s.V2TransactionWeight(marker) + 128
	for _, txn := range txns {
		w := s.V2TransactionWeight(txn)
		if weight+w > s.MaxBlockWeight() || consensus.ValidateV2Transaction(ms, txn) != nil {
			continue
		}
		ms.ApplyV2Transaction(txn)
		weight += w
		b.V2.Transactions = append(b.V2.Transactions, txn)
		b.MinerPayouts[0].Value = b.MinerPayouts[0].Value.Add(txn.MinerFee)
	}
	b.V2.Commitment = s.Commitment(miner.Policy().Address(), nil, b.V2.Transactions)
	return b
}

// Mine searches disjoint nonce sequences on native CPU threads. Cancellation
// and hash accounting occur every 1024 hashes, without a clock call per hash.
func Mine(ctx context.Context, s consensus.State, b types.Block, threads int, hashes *atomic.Uint64) (types.Block, error) {
	if threads < 1 || threads > 256 {
		return b, errors.New("threads must be 1..256")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	found := make(chan uint64, 1)
	var wg sync.WaitGroup
	target := s.PoWTarget()
	factor := s.NonceFactor()
	for worker := 0; worker < threads; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			bh := b.Header()
			bh.Nonce = uint64(worker) * factor
			var pending uint64
			defer func() {
				if hashes != nil {
					hashes.Add(pending)
				}
			}()
			for {
				pending++
				if bh.ID().CmpWork(target) >= 0 {
					select {
					case found <- bh.Nonce:
						cancel()
					default:
					}
					return
				}
				bh.Nonce += uint64(threads) * factor
				if pending == 1024 {
					if hashes != nil {
						hashes.Add(pending)
					}
					pending = 0
					select {
					case <-ctx.Done():
						return
					default:
					}
				}
			}
		}(worker)
	}
	wg.Wait()
	select {
	case b.Nonce = <-found:
		return b, nil
	default:
		return b, ctx.Err()
	}
}

// SignTransfer binds the complete transaction to both keys. Every input in a
// native single-address wallet has the same message, so one signature suffices
// computationally; the witness is attached independently to each input.
func SignTransfer(ctx context.Context, s consensus.State, txn *types.V2Transaction, keys *types.QdayPrivateKeys) error {
	e, err := consensus.ParseQdayEnvelope(txn.ArbitraryData)
	if err != nil {
		return err
	}
	if s.QdayActive(s.Index.Height + 1) {
		intent := s.QdayWorkIntent(*txn)
		for !consensus.QdayWorkValid(intent, e.Nonce, s.Network.Qday.DefendBits) {
			e.Nonce++
			if e.Nonce%1024 == 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
				}
			}
		}
		txn.ArbitraryData = e.Encode()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	sp, err := keys.Sign(s.InputSigHash(*txn))
	if err != nil {
		return err
	}
	for i := range txn.SiacoinInputs {
		txn.SiacoinInputs[i].SatisfiedPolicy = sp
	}
	return nil
}
