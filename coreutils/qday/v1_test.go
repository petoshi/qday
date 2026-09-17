package qday_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/qday"
)

func qdayV1Genesis(t *testing.T, activation uint64) (chain.QdayManifest, consensus.State, types.QdayPrivateKeys, types.SiacoinElement) {
	t.Helper()
	m := chain.QdayDevnet()
	m.Network.Qday.V1Height = activation
	owner, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
	if err != nil {
		t.Fatal(err)
	}
	cs, au := consensus.ApplyBlock(m.Network.GenesisState(), m.Genesis, consensus.V1BlockSupplement{Transactions: make([]consensus.V1TransactionSupplement, 1)}, time.Time{})
	for _, diff := range au.SiacoinElementDiffs() {
		if diff.Created {
			return m, cs, owner, diff.SiacoinElement.Copy()
		}
	}
	t.Fatal("genesis premine output not found")
	return m, cs, owner, types.SiacoinElement{}
}

func mineV1Block(t *testing.T, cs consensus.State, keys types.QdayKeys, txns ...types.V2Transaction) types.Block {
	t.Helper()
	b := qday.CandidateWithNonce(cs, keys, txns, cs.PrevTimestamps[0].Add(2*time.Second), 0x0807060504030201)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	b, err = qday.Mine(ctx, cs, b, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestQdayV1MiningMarkerActivation(t *testing.T) {
	_, cs, owner, _ := qdayV1Genesis(t, 2)
	before := mineV1Block(t, cs, owner.Public)
	if len(before.V2.Transactions) != 1 {
		t.Fatalf("pre-activation block has %d transactions, expected only the payout marker", len(before.V2.Transactions))
	} else if err := consensus.ValidateBlock(cs, before, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal(err)
	}
	cs, _ = consensus.ApplyBlock(cs, before, consensus.V1BlockSupplement{}, time.Time{})

	after := mineV1Block(t, cs, owner.Public)
	if len(after.V2.Transactions) != 2 {
		t.Fatalf("activation block has %d transactions, expected two markers", len(after.V2.Transactions))
	}
	work := after.V2.Transactions[1]
	envelope, err := consensus.ParseQdayEnvelope(work.ArbitraryData)
	if err != nil || envelope.Kind != consensus.QdayMiningWork || len(work.ArbitraryData) != 16 {
		t.Fatalf("invalid compact mining marker: %+v, %v", envelope, err)
	}
	if err := consensus.ValidateBlock(cs, after, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*types.Block){
		"missing": func(b *types.Block) { b.V2.Transactions = b.V2.Transactions[:1] },
		"wrong kind": func(b *types.Block) {
			b.V2.Transactions[1].ArbitraryData = (consensus.QdayEnvelope{Kind: consensus.QdayTransfer, Nonce: envelope.Nonce}).Encode()
		},
		"not empty": func(b *types.Block) {
			b.V2.Transactions[1].SiacoinOutputs = []types.SiacoinOutput{{Value: types.NewCurrency64(1), Address: owner.Public.Policy().Address()}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := after
			bad.V2 = &types.V2BlockData{Height: after.V2.Height, Transactions: append([]types.V2Transaction(nil), after.V2.Transactions...)}
			mutate(&bad)
			bad.V2.Commitment = cs.Commitment(bad.MinerPayouts[0].Address, nil, bad.V2.Transactions)
			bad = mineHeader(t, cs, bad)
			if err := consensus.ValidateBlock(cs, bad, consensus.V1BlockSupplement{}); err == nil || !strings.Contains(err.Error(), "mining work marker") {
				t.Fatalf("malformed activation block accepted: %v", err)
			}
		})
	}
}

func TestMainnetV1ActivationBoundary(t *testing.T) {
	_, cs, owner, premine := qdayV1Genesis(t, chain.QdayV1ActivationHeight)
	recipient, err := types.QdayKeysFromSeed([32]byte{0x91})
	if err != nil {
		t.Fatal(err)
	}
	fee := types.HastingsPerSiacoin.Div64(1000)
	pending := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: premine}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: premine.SiacoinOutput.Value.Sub(fee), Address: recipient.Public.Policy().Address()}},
		MinerFee:       fee,
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := qday.SignTransfer(context.Background(), cs, &pending, &owner); err != nil {
		t.Fatal(err)
	}
	wantID := pending.ID()

	cs.Index.Height = chain.QdayV1ActivationHeight - 2
	legacy := mineV1Block(t, cs, owner.Public)
	if legacy.V2.Height != chain.QdayV1ActivationHeight-1 || len(legacy.V2.Transactions) != 1 {
		t.Fatalf("block %d did not use the legacy layout", legacy.V2.Height)
	} else if err := consensus.ValidateBlock(cs, legacy, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal("last legacy block failed consensus: ", err)
	}

	cs.Index.Height = chain.QdayV1ActivationHeight - 1
	cs.Index.ID = legacy.ID()
	upgraded := mineV1Block(t, cs, owner.Public, pending)
	if upgraded.V2.Height != chain.QdayV1ActivationHeight || len(upgraded.V2.Transactions) != 3 {
		t.Fatalf("block %d has %d transactions, expected payout, pending transfer and work marker", upgraded.V2.Height, len(upgraded.V2.Transactions))
	} else if upgraded.V2.Transactions[1].ID() != wantID {
		t.Fatal("activation changed the pending transaction ID")
	} else if err := consensus.ValidateBlock(cs, upgraded, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal("activation block failed consensus: ", err)
	}
}

func mineHeader(t *testing.T, cs consensus.State, b types.Block) types.Block {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := qday.Mine(ctx, cs, b, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestQdayAtomicSwapPolicies(t *testing.T) {
	_, cs, sender, premine := qdayV1Genesis(t, 1)
	recipient, err := types.QdayKeysFromSeed([32]byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	refunder, err := types.QdayKeysFromSeed([32]byte{4, 5, 6})
	if err != nil {
		t.Fatal(err)
	}
	secret := [32]byte{7, 8, 9}
	swap := types.QdayAtomicSwap{
		Recipient: recipient.Public, Refund: refunder.Public,
		SecretHash: types.Hash256(sha256.Sum256(secret[:])), RefundHeight: 2,
	}
	swapAddress, err := swap.Address()
	if err != nil {
		t.Fatal(err)
	}
	fund := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: premine}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: premine.SiacoinOutput.Value, Address: types.Address(swapAddress)}},
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if err := qday.SignTransfer(context.Background(), cs, &fund, &sender); err != nil {
		t.Fatal(err)
	}
	fundingBlock := mineV1Block(t, cs, sender.Public, fund)
	if err := consensus.ValidateBlock(cs, fundingBlock, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal(err)
	}
	cs, update := consensus.ApplyBlock(cs, fundingBlock, consensus.V1BlockSupplement{}, time.Time{})
	var locked types.SiacoinElement
	for _, diff := range update.SiacoinElementDiffs() {
		if diff.Created && diff.SiacoinElement.SiacoinOutput.Address == types.Address(swapAddress) {
			locked = diff.SiacoinElement.Copy()
		}
	}
	if locked.ID == (types.SiacoinOutputID{}) {
		t.Fatal("atomic-swap output not created")
	}

	spend := func(destination types.QdayKeys) types.V2Transaction {
		return types.V2Transaction{
			SiacoinInputs:  []types.V2SiacoinInput{{Parent: locked}},
			SiacoinOutputs: []types.SiacoinOutput{{Value: locked.SiacoinOutput.Value, Address: destination.Policy().Address()}},
			ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
		}
	}
	claim := spend(recipient.Public)
	claimWitness, err := swap.Claim(cs.InputSigHash(claim), &recipient, secret)
	if err != nil {
		t.Fatal(err)
	}
	claim.SiacoinInputs[0].SatisfiedPolicy = claimWitness
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), claim); err != nil {
		t.Fatal("valid atomic-swap claim rejected: ", err)
	}
	wrongSecret := secret
	wrongSecret[0]++
	if _, err := swap.Claim(cs.InputSigHash(claim), &recipient, wrongSecret); err == nil {
		t.Fatal("wrong atomic-swap secret accepted")
	}
	weak := claim.DeepCopy()
	inner := types.PolicyThreshold(2, []types.SpendPolicy{types.PolicyPublicKey(recipient.Public.Classical), types.PolicyHash(swap.SecretHash)})
	weakPolicy, err := swap.ClaimPolicy()
	if err != nil {
		t.Fatal(err)
	}
	outer := weakPolicy.Type.(types.PolicyTypeThreshold)
	outer.Of[0] = inner
	weak.SiacoinInputs[0].SatisfiedPolicy.Policy = types.SpendPolicy{Type: outer}
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), weak); err == nil {
		t.Fatal("atomic swap without reserve policy accepted")
	}

	refund := spend(refunder.Public)
	refundWitness, err := swap.RefundSpend(cs.InputSigHash(refund), &refunder)
	if err != nil {
		t.Fatal(err)
	}
	refund.SiacoinInputs[0].SatisfiedPolicy = refundWitness
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), refund); err == nil {
		t.Fatal("atomic-swap refund accepted before timeout")
	}
	empty := mineV1Block(t, cs, sender.Public)
	if err := consensus.ValidateBlock(cs, empty, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal(err)
	}
	cs, update = consensus.ApplyBlock(cs, empty, consensus.V1BlockSupplement{}, time.Time{})
	update.UpdateElementProof(&locked.StateElement)
	refund = spend(refunder.Public)
	refundWitness, err = swap.RefundSpend(cs.InputSigHash(refund), &refunder)
	if err != nil {
		t.Fatal(err)
	}
	refund.SiacoinInputs[0].SatisfiedPolicy = refundWitness
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), refund); err != nil {
		t.Fatal("valid atomic-swap refund rejected at timeout: ", err)
	}

	cs.QdayHeight = 1
	cs.Network.Qday.DefendBits = 8
	protectedClaim := spend(recipient.Public)
	if err := qday.SignAtomicSwapClaim(context.Background(), cs, &protectedClaim, swap, &recipient, secret); err != nil {
		t.Fatal(err)
	}
	envelope, err := consensus.ParseQdayEnvelope(protectedClaim.ArbitraryData)
	if err != nil || !consensus.QdayWorkValid(cs.QdayWorkIntent(protectedClaim), envelope.Nonce, cs.Network.Qday.DefendBits) {
		t.Fatal("atomic-swap claim is missing DEFEND work")
	} else if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), protectedClaim); err != nil {
		t.Fatal("post-PQ-Day atomic-swap claim rejected: ", err)
	}
}
