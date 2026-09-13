package qday

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	mining "go.sia.tech/coreutils/qday"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func fundedBoundaryWallet(t *testing.T) *Service {
	t.Helper()
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "boundary-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	return s
}

func TestMaximumNativeInputBurn(t *testing.T) {
	s := fundedBoundaryWallet(t)
	outs, err := s.outputs(s.CM.TipState(), s.public)
	if err != nil || len(outs) != 1 {
		t.Fatal("test premine not available", err)
	}
	split := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: outs[0].SiacoinElement}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
	for range 128 {
		split.SiacoinOutputs = append(split.SiacoinOutputs, types.SiacoinOutput{Value: types.Siacoins(8), Address: s.keys.Public.Policy().Address()})
	}
	if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &split, s.keys); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s, split)
	fee := types.HastingsPerSiacoin.Div64(1000)
	id, err := s.Burn(context.Background(), types.Siacoins(1024).Sub(fee))
	if err != nil {
		t.Fatal(err)
	}
	txn, ok := s.CM.V2PoolTransaction(id)
	if !ok || len(txn.SiacoinInputs) != 128 {
		t.Fatal("wallet could not spend 128 reward-sized outputs")
	}
	encoded, err := encodePendingTransaction(txn)
	if err != nil || len(encoded) >= 5e6 {
		t.Fatal("native transaction exceeds P2P envelope", err)
	}
	t.Logf("128 inputs: %d encoded bytes, %d consensus weight", len(encoded), s.CM.TipState().V2TransactionWeight(txn))
	cs := s.CM.TipState()
	for name, mutate := range map[string]func(*types.V2Transaction){
		"inflated output": func(tx *types.V2Transaction) {
			tx.SiacoinOutputs[0].Value = tx.SiacoinOutputs[0].Value.Add(types.NewCurrency64(1))
		},
		"inflated parent": func(tx *types.V2Transaction) {
			tx.SiacoinInputs[0].Parent.SiacoinOutput.Value = tx.SiacoinInputs[0].Parent.SiacoinOutput.Value.Add(types.Siacoins(1))
		},
		"forged birth height": func(tx *types.V2Transaction) { tx.SiacoinInputs[0].Parent.MaturityHeight = 0 },
		"duplicate spend":     func(tx *types.V2Transaction) { tx.SiacoinInputs[1].Parent = tx.SiacoinInputs[0].Parent.Copy() },
		"currency overflow":   func(tx *types.V2Transaction) { tx.MinerFee = types.MaxCurrency },
	} {
		t.Run(name, func(t *testing.T) {
			bad := txn.DeepCopy()
			mutate(&bad)
			// Sign the mutation: rejection must come from monetary/UTXO rules,
			// not merely an obsolete signature on modified transaction fields.
			if err := mining.SignTransfer(context.Background(), cs, &bad, s.keys); err != nil {
				t.Fatal(err)
			}
			if consensus.ValidateV2Transaction(consensus.NewMidState(cs), bad) == nil {
				t.Fatal("consensus accepted the mutation")
			}
		})
	}
	// Known-ID announcements never replace previously verified witnesses.
	badWitness := txn.DeepCopy()
	badWitness.SiacoinInputs[0].SatisfiedPolicy.Preimages = nil
	if known, err := s.CM.AddV2PoolTransactions(cs.Index, []types.V2Transaction{badWitness}); !known || err != nil {
		t.Fatalf("duplicate announcement was not recognized: %v", err)
	}
	stored, _ := s.CM.V2PoolTransaction(id)
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), stored); err != nil {
		t.Fatalf("duplicate announcement replaced verified witnesses: %v", err)
	}
	template, err := s.MiningTemplate(context.Background(), "")
	if err != nil || len(template.Transactions) != 2 || template.Transactions[1].TxID != id.String() {
		t.Fatal("large transaction missing from template", err)
	}
	b := decodeTemplateBlock(t, template)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, err = mining.Mine(ctx, cs, b, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := encodeMiningObject(types.V2Block(b))
	if _, err := s.SubmitMiningBlock(raw); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	if len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("confirmed large transaction remained in the pool")
	}
}

func TestWalletSelectsLargeOutputBeforeDust(t *testing.T) {
	s := fundedBoundaryWallet(t)
	outs, err := s.outputs(s.CM.TipState(), s.public)
	if err != nil {
		t.Fatal(err)
	}
	txn := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: outs[0].SiacoinElement}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
	for range 127 {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{Value: types.Siacoins(1), Address: s.keys.Public.Policy().Address()})
	}
	txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{Value: types.Siacoins(499873), Address: s.keys.Public.Policy().Address()})
	if err := mining.SignTransfer(context.Background(), s.CM.TipState(), &txn, s.keys); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s, txn)
	id, err := s.Burn(context.Background(), types.Siacoins(10000))
	if err != nil {
		t.Fatal(err)
	}
	selected, ok := s.CM.V2PoolTransaction(id)
	if !ok || len(selected.SiacoinInputs) != 1 {
		t.Fatal("small outputs inflated a payment with a sufficient large output")
	}
}

func TestPendingBalanceExcludesSpentUnconfirmedOutputs(t *testing.T) {
	s := fundedBoundaryWallet(t)
	cs := s.CM.TipState()
	outs, err := s.outputs(cs, s.public)
	if err != nil || len(outs) != 1 {
		t.Fatal("test premine not available", err)
	}
	parent := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: outs[0].SiacoinElement}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(499999), Address: s.keys.Public.Policy().Address()}},
		MinerFee:       types.Siacoins(1),
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := mining.SignTransfer(context.Background(), cs, &parent, s.keys); err != nil {
		t.Fatal(err)
	}
	ephemeral := parent.EphemeralSiacoinOutput(0)
	ephemeral.MaturityHeight = cs.Index.Height + 1
	child := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: ephemeral}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(499998), Address: s.keys.Public.Policy().Address()}},
		MinerFee:       types.Siacoins(1),
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := mining.SignTransfer(context.Background(), cs, &child, s.keys); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CM.AddV2PoolTransactions(cs.Index, []types.V2Transaction{parent, child}); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status()
	if err != nil || status["balance"] != "0" || status["pending"] != "499998" {
		t.Fatal("pending chain double-counted its spent intermediate output", status, err)
	}
	minePendingTestBlock(t, s, s.keys.Public, parent)
	synced(t, s)
	status, err = s.Status()
	if err != nil || status["pending"] != "499998" {
		t.Fatal("confirming the parent changed the child's pending balance", status, err)
	}
	minePendingTestBlock(t, s, s.keys.Public, s.CM.V2PoolTransactions()...)
	synced(t, s)
	status, err = s.Status()
	if err != nil || status["pending"] != "0" {
		t.Fatal("confirmed child remained in the pending balance", status, err)
	}
}

func TestReviewedDenominationCheckedAfterSigningLock(t *testing.T) {
	s := fundedBoundaryWallet(t)
	if _, err := s.PublishProof(context.Background(), [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s)
	cs := s.CM.TipState()
	unit := cs.QdayUnits(cs.Index.Height).ExactString()
	if cs.QdayActive(cs.Index.Height) {
		t.Fatal("test must begin before activation")
	}
	result := make(chan error, 1)
	func() {
		// A browser review is queued behind another wallet operation while
		// the chain crosses the denomination change.
		s.op.Lock()
		defer s.op.Unlock()
		go func() {
			_, err := s.submitReviewed(context.Background(), s.keys.Public.String(), unit, "2", types.QdayAddress{}, types.Siacoins(1), submitBurn, nil)
			result <- err
		}()
		for !s.CM.TipState().QdayActive(s.CM.Tip().Height) {
			mineForTest(t, s)
		}
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "denomination changed") {
			t.Fatal("stale reviewed denomination was not rejected", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued wallet operation did not finish")
	}
	if len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("stale reviewed amount entered the mempool")
	}
}

func TestPendingConfirmationSurvivesRestartAndReorg(t *testing.T) {
	s, keys, txn := pendingTestService(t)
	if err := s.rememberBroadcast(s.CM.Tip(), []types.V2Transaction{txn}); err != nil {
		t.Fatal(err)
	}
	minePendingTestBlock(t, s, keys.Public, txn)
	s.rebroadcastPending()
	s.rebroadcasts = make(map[types.TransactionID]pendingBroadcast)
	if err := s.loadPendingBroadcasts(); err != nil {
		t.Fatal(err)
	}
	s.rebroadcastPending()
	if len(s.rebroadcasts) != 1 || len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("confirmed transaction was forgotten or rebroadcast as unconfirmed")
	}
	other, _, _ := pendingTestService(t)
	var branch []types.Block
	for range 4 {
		branch = append(branch, minePendingTestBlock(t, other, keys.Public))
	}
	if err := s.CM.AddBlocks(branch); err != nil {
		t.Fatal(err)
	}
	s.rebroadcastPending()
	if pool := s.CM.V2PoolTransactions(); len(pool) != 1 || pool[0].ID() != txn.ID() {
		t.Fatal("reorg did not restore persistent transaction")
	}
	set := s.rebroadcasts[txn.ID()]
	set.BroadcastedAt = time.Now().Add(-maxRebroadcastPeriod - time.Second)
	s.rebroadcasts[txn.ID()] = set
	s.rebroadcastPending()
	if len(s.rebroadcasts) != 0 || len(s.CM.V2PoolTransactions()) != 1 {
		t.Fatal("retry deadline incorrectly canceled a valid pending transaction")
	}
}
