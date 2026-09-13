package qday

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	mining "go.sia.tech/coreutils/qday"
)

// This fixture has no network, wallet database, or background rebroadcaster.
func pendingTestService(t *testing.T) (*Service, types.QdayPrivateKeys, types.V2Transaction) {
	t.Helper()
	m := chain.QdayDevnet()
	store, cs, err := chain.NewDBStore(chain.NewMemDB(), &m.Network, m.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
	if err != nil {
		t.Fatal(err)
	}
	_, au := consensus.ApplyBlock(m.Network.GenesisState(), m.Genesis, consensus.V1BlockSupplement{Transactions: make([]consensus.V1TransactionSupplement, 1)}, time.Time{})
	var parent types.SiacoinElement
	for _, diff := range au.SiacoinElementDiffs() {
		if diff.SiacoinElement.ID == m.Genesis.Transactions[0].SiacoinOutputID(0) {
			parent = diff.SiacoinElement.Copy()
		}
	}
	fee := types.HastingsPerSiacoin.Div64(1000)
	txn := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: parent}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: parent.SiacoinOutput.Value.Sub(fee), Address: keys.Public.Policy().Address()}},
		MinerFee:       fee,
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := mining.SignTransfer(context.Background(), cs, &txn, &keys); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		CM: chain.NewManager(store, cs), Manifest: m, ctx: context.Background(),
		rebroadcastPath: filepath.Join(t.TempDir(), "pending-transactions.json"),
		rebroadcasts:    make(map[types.TransactionID]pendingBroadcast), rebroadcastWake: make(chan struct{}, 1),
	}
	return s, keys, txn
}

func minePendingTestBlock(t *testing.T, s *Service, keys types.QdayKeys, txns ...types.V2Transaction) types.Block {
	t.Helper()
	cs := s.CM.TipState()
	b := mining.Candidate(cs, keys, txns, cs.PrevTimestamps[0].Add(2*time.Second))
	if len(b.V2.Transactions) != len(txns)+1 {
		t.Fatal("candidate omitted a test transaction")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, err := mining.Mine(ctx, cs, b, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CM.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPendingTransactionSurvivesLongOfflineGap(t *testing.T) {
	s, keys, txn := pendingTestService(t)
	if err := s.rememberBroadcast(s.CM.Tip(), []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	// The chain advances while the sender is offline. This exceeds the P2P
	// proof-update limit, but is well within the wallet's 48-hour retry period.
	for range 300 {
		minePendingTestBlock(t, s, keys.Public)
	}
	s.rebroadcasts = make(map[types.TransactionID]pendingBroadcast)
	if err := s.loadPendingBroadcasts(); err != nil {
		t.Fatal(err)
	}
	s.rebroadcastPending()
	pool := s.CM.V2PoolTransactions()
	if len(pool) != 1 || pool[0].ID() != txn.ID() {
		t.Fatal("offline transaction was lost instead of updating its proofs")
	}
	if set := s.rebroadcasts[txn.ID()]; set.Basis != s.CM.Tip() {
		t.Fatal("pending transaction basis was not persisted at the current tip")
	}
	if len(mining.Candidate(s.CM.TipState(), keys.Public, pool, time.Now()).V2.Transactions) != 2 {
		t.Fatal("recovered transaction cannot enter a block")
	}
}

func TestPendingTransactionWaitsForMissingBasis(t *testing.T) {
	s, _, txn := pendingTestService(t)
	unknown := types.ChainIndex{Height: 10, ID: types.BlockID{1}}
	if err := s.rememberBroadcast(unknown, []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	s.rebroadcastPending()
	if _, ok := s.rebroadcasts[txn.ID()]; !ok {
		t.Fatal("temporarily unknown basis erased the persistent transaction")
	}
}

func TestTransactionSetConflictIsAtomic(t *testing.T) {
	s, keys, split := pendingTestService(t)
	split.SiacoinOutputs = []types.SiacoinOutput{
		{Value: types.Siacoins(10), Address: keys.Public.Policy().Address()},
		{Value: types.Siacoins(10), Address: keys.Public.Policy().Address()},
	}
	if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &split, &keys); err != nil {
		t.Fatal(err)
	}
	cs := s.CM.TipState()
	b := minePendingTestBlock(t, s, keys.Public, split)
	_, au := consensus.ApplyBlock(cs, b, consensus.V1BlockSupplement{}, time.Time{})
	var parents []types.SiacoinElement
	for _, diff := range au.SiacoinElementDiffs() {
		if diff.Created && diff.SiacoinElement.MaturityHeight == 1 {
			parents = append(parents, diff.SiacoinElement.Copy())
		}
	}
	if len(parents) != 2 {
		t.Fatal("missing test outputs")
	}
	spend := func(parent types.SiacoinElement, amount uint32) types.V2Transaction {
		t.Helper()
		txn := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: parent}}, SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(amount), Address: keys.Public.Policy().Address()}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
		if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &txn, &keys); err != nil {
			t.Fatal(err)
		}
		return txn
	}
	existing, first, conflict := spend(parents[0], 10), spend(parents[1], 10), spend(parents[0], 9)
	if _, err := s.CM.AddV2PoolTransactions(s.CM.Tip(), []types.V2Transaction{existing}); err != nil {
		t.Fatal(err)
	}
	notifications := 0
	stop := s.CM.OnPoolChange(func() { notifications++ })
	defer stop()
	if _, err := s.CM.AddV2PoolTransactions(s.CM.Tip(), []types.V2Transaction{first, conflict}); err == nil {
		t.Fatal("conflicting set was accepted")
	}
	if pool := s.CM.V2PoolTransactions(); len(pool) != 1 || pool[0].ID() != existing.ID() || notifications != 0 {
		t.Fatal("rejected set partially changed the pool")
	}
	if _, err := s.CM.AddV2PoolTransactions(s.CM.Tip(), []types.V2Transaction{first}); err != nil || notifications != 1 {
		t.Fatalf("rejected set poisoned later admission: %v", err)
	}
}

func TestDependentTransactionsFollowEmptyBlocks(t *testing.T) {
	s, keys, parent := pendingTestService(t)
	child := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: parent.EphemeralSiacoinOutput(0)}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(1), Address: keys.Public.Policy().Address()}},
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	child.SiacoinInputs[0].Parent.MaturityHeight = s.CM.Tip().Height + 1
	if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &child, &keys); err != nil {
		t.Fatal(err)
	}
	basis := s.CM.Tip()
	original := []types.V2Transaction{parent.DeepCopy(), child.DeepCopy()}
	if _, err := s.CM.AddV2PoolTransactions(basis, original); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		minePendingTestBlock(t, s, keys.Public)
		pool := s.CM.V2PoolTransactions()
		if len(pool) != 2 || pool[1].ID() != child.ID() {
			t.Fatal("an empty block evicted a valid dependent transaction")
		}
	}
	updated, err := s.CM.UpdateV2TransactionSet(original, basis, s.CM.Tip())
	if err != nil {
		t.Fatal(err)
	}
	if len(mining.Candidate(s.CM.TipState(), keys.Public, updated, time.Now()).V2.Transactions) != 3 {
		t.Fatal("proof update lost the dependent transaction")
	}
	// Confirm just the parent. The child must retain the actual birth height
	// of the now-confirmed output, rather than guessing the next height.
	minePendingTestBlock(t, s, keys.Public, s.CM.V2PoolTransactions()[0])
	pool := s.CM.V2PoolTransactions()
	if len(pool) != 1 || pool[0].ID() != child.ID() {
		t.Fatal("parent confirmation evicted the child")
	}
	minePendingTestBlock(t, s, keys.Public, pool...)
}

func TestTransactionRelayOrdersSharedAncestors(t *testing.T) {
	s, keys, a := pendingTestService(t)
	a.SiacoinOutputs = []types.SiacoinOutput{{Value: types.Siacoins(10), Address: keys.Public.Policy().Address()}, {Value: types.Siacoins(10), Address: keys.Public.Policy().Address()}}
	if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &a, &keys); err != nil {
		t.Fatal(err)
	}
	makeChild := func(parents ...types.SiacoinElement) types.V2Transaction {
		t.Helper()
		txn := types.V2Transaction{SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(10), Address: keys.Public.Policy().Address()}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
		for _, parent := range parents {
			parent.MaturityHeight = 1
			txn.SiacoinInputs = append(txn.SiacoinInputs, types.V2SiacoinInput{Parent: parent})
		}
		if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &txn, &keys); err != nil {
			t.Fatal(err)
		}
		return txn
	}
	b := makeChild(a.EphemeralSiacoinOutput(0))
	c := makeChild(a.EphemeralSiacoinOutput(1), b.EphemeralSiacoinOutput(0))
	basis := s.CM.Tip()
	if _, err := s.CM.AddV2PoolTransactions(basis, []types.V2Transaction{a, b, c}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		setBasis, set, err := s.CM.V2TransactionSet(basis, c)
		if err != nil {
			t.Fatal(err)
		} else if setBasis != s.CM.Tip() || len(set) != 3 || set[0].ID() != a.ID() || set[1].ID() != b.ID() || set[2].ID() != c.ID() {
			t.Fatal("relay set places a child before its parent")
		}
		if len(mining.Candidate(s.CM.TipState(), keys.Public, set, time.Now()).V2.Transactions) != 4 {
			t.Fatal("relay set cannot be mined")
		}
		minePendingTestBlock(t, s, keys.Public)
	}
}

func TestReorgRestoresTransactionsWithoutWalletOutbox(t *testing.T) {
	s, keys, parent := pendingTestService(t)
	minePendingTestBlock(t, s, keys.Public, parent)
	other, _, _ := pendingTestService(t)
	var branch []types.Block
	for range 3 {
		branch = append(branch, minePendingTestBlock(t, other, keys.Public))
	}
	if err := s.CM.AddBlocks(branch); err != nil {
		t.Fatal(err)
	}
	if pool := s.CM.V2PoolTransactions(); len(pool) != 1 || pool[0].ID() != parent.ID() {
		t.Fatal("reorg lost a valid signed transaction learned from a block")
	}
}

func TestReorgRestoresDependentBlocksAndRejectsConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "dependent blocks", true: "winning branch spends parent"}[conflict], func(t *testing.T) {
			s, keys, parent := pendingTestService(t)
			before := s.CM.TipState()
			b := minePendingTestBlock(t, s, keys.Public, parent)
			_, au := consensus.ApplyBlock(before, b, consensus.V1BlockSupplement{}, time.Time{})
			var input types.SiacoinElement
			for _, diff := range au.SiacoinElementDiffs() {
				if diff.SiacoinElement.ID == parent.SiacoinOutputID(parent.ID(), 0) {
					input = diff.SiacoinElement.Copy()
				}
			}
			child := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: input}}, SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(1), Address: keys.Public.Policy().Address()}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
			if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &child, &keys); err != nil {
				t.Fatal(err)
			}
			minePendingTestBlock(t, s, keys.Public, child)
			minePendingTestBlock(t, s, keys.Public) // newest reverted block is empty
			other, _, competing := pendingTestService(t)
			var branch []types.Block
			if conflict {
				competing.SiacoinOutputs[0].Value = types.Siacoins(100)
				if err := mining.SignTransfer(context.Background(), other.CM.TipState(), &competing, &keys); err != nil {
					t.Fatal(err)
				}
				branch = append(branch, minePendingTestBlock(t, other, keys.Public, competing))
			}
			for len(branch) < 6 {
				branch = append(branch, minePendingTestBlock(t, other, keys.Public))
			}
			if err := s.CM.AddBlocks(branch); err != nil {
				t.Fatal(err)
			}
			pool := s.CM.V2PoolTransactions()
			if conflict {
				if len(pool) != 0 {
					t.Fatal("reorg restored a conflicting spend")
				}
			} else if len(pool) != 2 || pool[0].ID() != parent.ID() || pool[1].ID() != child.ID() {
				t.Fatal("reorg lost dependent payments or restored them in the wrong order")
			} else {
				minePendingTestBlock(t, s, keys.Public, pool...)
			}
		})
	}
}
