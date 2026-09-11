package qday

import (
	"context"
	"testing"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	mining "go.sia.tech/coreutils/qday"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func TestShortAddressRecipientCanSpendWithBothKeys(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "short-address-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	recipient, err := types.QdayKeysFromSeed([32]byte{10, 20, 30})
	if err != nil {
		t.Fatal(err)
	}
	to, err := types.ParseQdayAddress(recipient.Public.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transfer(context.Background(), to, types.Siacoins(10), false); err != nil {
		t.Fatal(err)
	}
	pool := s.CM.V2PoolTransactions()
	e, err := consensus.ParseQdayEnvelope(pool[0].ArbitraryData)
	if err != nil || len(e.Keys) != 0 {
		t.Fatal("transfer still needs the recipient public keys")
	}
	mineForTest(t, s)
	outputs, _, err := s.WM.AddressSiacoinOutputs(types.Address(to), false, 0, 10)
	if err != nil || len(outputs) != 1 {
		t.Fatal("short-address payment was not indexed")
	}
	cs := s.CM.TipState()
	txn := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: outputs[0].SiacoinElement}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(10), Address: types.Address(s.public)}},
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := mining.SignTransfer(context.Background(), cs, &txn, &recipient); err != nil {
		t.Fatal(err)
	}
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), txn); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*types.V2Transaction){
		"classical only": func(tx *types.V2Transaction) { tx.SiacoinInputs[0].SatisfiedPolicy.Preimages = nil },
		"reserve only":   func(tx *types.V2Transaction) { tx.SiacoinInputs[0].SatisfiedPolicy.Signatures = nil },
		"weaker threshold": func(tx *types.V2Transaction) {
			tx.SiacoinInputs[0].SatisfiedPolicy.Policy = types.PolicyThreshold(1, []types.SpendPolicy{types.PolicyPublicKey(recipient.Public.Classical), types.PolicySLHDSA(recipient.Public.Reserve)})
		},
		"different policy hash": func(tx *types.V2Transaction) { tx.SiacoinInputs[0].SatisfiedPolicy.Policy = s.keys.Public.Policy() },
	} {
		bad := txn.DeepCopy()
		mutate(&bad)
		if consensus.ValidateV2Transaction(consensus.NewMidState(cs), bad) == nil {
			t.Fatal("accepted " + name)
		}
	}
	// A well-formed but weaker receiving policy cannot later bypass the native
	// spend rule, even with a valid classical signature for that policy.
	weak := types.PolicyPublicKey(recipient.Public.Classical)
	if _, err := s.Transfer(context.Background(), types.QdayAddress(weak.Address()), types.Siacoins(1), false); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s)
	weakOutputs, _, err := s.WM.AddressSiacoinOutputs(weak.Address(), false, 0, 10)
	if err != nil || len(weakOutputs) != 1 {
		t.Fatal("weak-address fixture was not indexed")
	}
	cs = s.CM.TipState()
	weakTxn := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: weakOutputs[0].SiacoinElement}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: types.Siacoins(1), Address: types.Address(s.public)}},
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	weakTxn.SiacoinInputs[0].SatisfiedPolicy = types.SatisfiedPolicy{Policy: weak, Signatures: []types.Signature{recipient.Classical.SignHash(cs.InputSigHash(weakTxn))}}
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), weakTxn) == nil {
		t.Fatal("classical-only policy bypassed the native spend rule")
	}
}
