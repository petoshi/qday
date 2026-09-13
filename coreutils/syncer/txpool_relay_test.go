package syncer

import (
	"context"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/qday"
)

func TestQdayRelayPacksSharedParents(t *testing.T) {
	m := chain.QdayDevnet()
	newManager := func() *chain.Manager {
		t.Helper()
		store, cs, err := chain.NewDBStore(chain.NewMemDB(), &m.Network, m.Genesis, nil)
		if err != nil {
			t.Fatal(err)
		}
		return chain.NewManager(store, cs)
	}
	cm := newManager()
	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
	if err != nil {
		t.Fatal(err)
	}
	_, au := consensus.ApplyBlock(m.Network.GenesisState(), m.Genesis, consensus.V1BlockSupplement{Transactions: make([]consensus.V1TransactionSupplement, 1)}, time.Time{})
	var premine types.SiacoinElement
	for _, d := range au.SiacoinElementDiffs() {
		if d.SiacoinElement.ID == m.Genesis.Transactions[0].SiacoinOutputID(0) {
			premine = d.SiacoinElement.Copy()
		}
	}
	parent := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: premine}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
	for range 8 {
		parent.SiacoinOutputs = append(parent.SiacoinOutputs, types.SiacoinOutput{Value: types.Siacoins(100), Address: keys.Public.Policy().Address()})
	}
	if err := qday.SignTransfer(context.Background(), cm.TipState(), &parent, &keys); err != nil {
		t.Fatal(err)
	}
	txns := []types.V2Transaction{parent}
	for i := range 8 {
		input := parent.EphemeralSiacoinOutput(i)
		input.MaturityHeight = 1
		child := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: input}}, SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(99), Address: keys.Public.Policy().Address()}}, MinerFee: types.Siacoins(1), ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
		if err := qday.SignTransfer(context.Background(), cm.TipState(), &child, &keys); err != nil {
			t.Fatal(err)
		}
		txns = append(txns, child)
	}
	if _, err := cm.AddV2PoolTransactions(cm.Tip(), txns); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{20000, 40000, 5e6} {
		sets, err := qdayPoolRelayPlan(cm.Tip(), cm.V2PoolTransactions(), limit)
		if err != nil {
			t.Fatal(err)
		} else if limit == 5e6 && (len(sets) != 1 || len(sets[0].txns) != len(txns)) {
			t.Fatal("shared ancestors were copied into separate relay sets")
		}
		receiver := newManager()
		for _, set := range sets {
			var size encodedByteCount
			e := types.NewEncoder(&size)
			set.basis.EncodeTo(e)
			types.EncodeSlice(e, set.txns)
			if err := e.Flush(); err != nil || int(size) > limit {
				t.Fatal("relay packet exceeds wire limit", int(size), err)
			}
			if _, err := receiver.AddV2PoolTransactions(set.basis, set.txns); err != nil {
				t.Fatal("packed relay set is not independently valid", err)
			}
		}
		if len(receiver.V2PoolTransactions()) != len(txns) {
			t.Fatal("packing omitted a payment")
		}
	}
}
