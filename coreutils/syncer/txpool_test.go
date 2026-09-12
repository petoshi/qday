package syncer_test

import (
	"context"
	"net"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	qdaymining "go.sia.tech/coreutils/qday"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/testutil"
)

func newQdayTestSyncer(t *testing.T) (*syncer.Syncer, *chain.Manager, chain.QdayManifest) {
	t.Helper()
	manifest := chain.QdayDevnet()
	store, tip, err := chain.NewDBStore(chain.NewMemDB(), &manifest.Network, manifest.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(store, tip)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sy := syncer.New(listener, cm, testutil.NewEphemeralPeerStore(), gateway.Header{
		GenesisID:  manifest.Genesis.ID(),
		UniqueID:   gateway.GenerateUniqueID(),
		NetAddress: listener.Addr().String(),
	}, syncer.WithSyncInterval(25*time.Millisecond))
	go sy.Run()
	t.Cleanup(func() { sy.Close() })
	return sy, cm, manifest
}

func TestLatePeerReceivesTransactionPool(t *testing.T) {
	a, aCM, manifest := newQdayTestSyncer(t)
	b, bCM, _ := newQdayTestSyncer(t)

	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
	if err != nil {
		t.Fatal(err)
	}
	_, genesisUpdate := consensus.ApplyBlock(manifest.Network.GenesisState(), manifest.Genesis, consensus.V1BlockSupplement{
		Transactions: make([]consensus.V1TransactionSupplement, len(manifest.Genesis.Transactions)),
	}, time.Time{})
	premineID := manifest.Genesis.Transactions[0].SiacoinOutputID(0)
	var premine types.SiacoinElement
	for _, diff := range genesisUpdate.SiacoinElementDiffs() {
		if diff.SiacoinElement.ID == premineID {
			premine = diff.SiacoinElement.Copy()
			break
		}
	}
	if premine.ID != premineID {
		t.Fatal("premine output was not found in genesis")
	}

	txn := types.V2Transaction{
		SiacoinInputs: []types.V2SiacoinInput{{
			Parent:          premine,
			SatisfiedPolicy: types.SatisfiedPolicy{Policy: keys.Public.Policy()},
		}},
		SiacoinOutputs: []types.SiacoinOutput{{
			Value:   premine.SiacoinOutput.Value,
			Address: keys.Public.Policy().Address(),
		}},
		ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := qdaymining.SignTransfer(context.Background(), aCM.TipState(), &txn, &keys); err != nil {
		t.Fatal(err)
	} else if _, err := aCM.AddV2PoolTransactions(aCM.Tip(), []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	} else if len(bCM.V2PoolTransactions()) != 0 {
		t.Fatal("late peer unexpectedly had the transaction before connecting")
	}

	if _, err := b.Connect(context.Background(), a.Addr()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := bCM.V2PoolTransaction(txn.ID()); ok && got.ID() == txn.ID() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("late peer did not receive the existing transaction pool")
}
