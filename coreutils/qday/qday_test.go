package qday_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/coreutils/qday"
)

func TestQdayLifecycle(t *testing.T) {
	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
	if err != nil {
		t.Fatal(err)
	}
	m := chain.QdayDevnet()
	if err = m.Validate(); err != nil {
		t.Fatal(err)
	}
	cs, au := consensus.ApplyBlock(m.Network.GenesisState(), m.Genesis, consensus.V1BlockSupplement{Transactions: make([]consensus.V1TransactionSupplement, 1)}, time.Time{})
	utxos := map[types.SiacoinOutputID]types.SiacoinElement{}
	for _, d := range au.SiacoinElementDiffs() {
		utxos[d.SiacoinElement.ID] = d.SiacoinElement.Copy()
	}
	apply := func(txns ...types.V2Transaction) types.Block {
		t.Helper()
		b := qday.Candidate(cs, keys.Public, txns, cs.PrevTimestamps[0].Add(2*time.Second))
		if len(b.V2.Transactions) != len(txns)+1 {
			t.Fatal("candidate dropped a valid test transaction")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b, err = qday.Mine(ctx, cs, b, 2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = consensus.ValidateBlock(cs, b, consensus.V1BlockSupplement{}); err != nil {
			t.Fatal(err)
		}
		cs, au = consensus.ApplyBlock(cs, b, consensus.V1BlockSupplement{}, time.Time{})
		for id, e := range utxos {
			au.UpdateElementProof(&e.StateElement)
			utxos[id] = e
		}
		for _, d := range au.SiacoinElementDiffs() {
			if d.Spent {
				delete(utxos, d.SiacoinElement.ID)
			} else if d.Created {
				utxos[d.SiacoinElement.ID] = d.SiacoinElement.Copy()
			}
		}
		return b
	}
	premineID := m.Genesis.Transactions[0].SiacoinOutputID(0)
	if utxos[premineID].SiacoinOutput.Value != types.Siacoins(500_000) {
		t.Fatal("premine is not exactly 500000 QDAY")
	}
	premine := utxos[premineID].Copy()
	makeProof := func(state consensus.State, parent types.SiacoinElement) types.V2Transaction {
		t.Helper()
		txn := types.V2Transaction{
			SiacoinInputs:  []types.V2SiacoinInput{{Parent: parent, SatisfiedPolicy: types.SatisfiedPolicy{Policy: keys.Public.Policy()}}},
			SiacoinOutputs: []types.SiacoinOutput{{Value: parent.SiacoinOutput.Value.Sub(state.Network.Qday.ProofFee), Address: keys.Public.Policy().Address()}},
			MinerFee:       state.Network.Qday.ProofFee,
			ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayCanaryProof, Witness: [32]byte{1}}).Encode(),
		}
		if err := qday.SignTransfer(context.Background(), state, &txn, &keys); err != nil {
			t.Fatal(err)
		}
		return txn
	}
	send := func(e types.SiacoinElement, value types.Currency) types.V2Transaction {
		t.Helper()
		txn := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: e, SatisfiedPolicy: types.SatisfiedPolicy{Policy: keys.Public.Policy()}}}, SiacoinOutputs: []types.SiacoinOutput{{Value: value, Address: keys.Public.Policy().Address()}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
		if err := qday.SignTransfer(context.Background(), cs, &txn, &keys); err != nil {
			t.Fatal(err)
		}
		return txn
	}
	transfer := send(utxos[premineID], utxos[premineID].SiacoinOutput.Value)
	apply(transfer)
	currentID := transfer.SiacoinOutputID(transfer.ID(), 0)
	if utxos[currentID].MaturityHeight != 1 {
		t.Fatal("ordinary output shield birth is not authenticated")
	}
	proof := makeProof(cs, utxos[currentID])
	proofState := consensus.NewMidState(cs)
	if err := consensus.ValidateV2Transaction(proofState, proof); err != nil {
		t.Fatal(err)
	}
	proofState.ApplyV2Transaction(proof)
	if consensus.ValidateV2Transaction(proofState, proof) == nil {
		t.Fatal("two proofs in one block accepted")
	}
	bad := proof.DeepCopy()
	bad.ArbitraryData[len(bad.ArbitraryData)-32] = 2
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), bad) == nil {
		t.Fatal("false canary accepted")
	}
	for _, fee := range []types.Currency{types.ZeroCurrency, proof.MinerFee.Sub(types.NewCurrency64(1))} {
		bad := proof.DeepCopy()
		bad.MinerFee = fee
		if err := qday.SignTransfer(context.Background(), cs, &bad, &keys); err != nil {
			t.Fatal(err)
		}
		if consensus.ValidateV2Transaction(consensus.NewMidState(cs), bad) == nil {
			t.Fatal("proof bypassed the minimum fee")
		}
	}
	for name, mutate := range map[string]func(*types.V2Transaction){
		"unfunded proof":              func(txn *types.V2Transaction) { txn.SiacoinInputs = nil },
		"missing reserve signature":   func(txn *types.V2Transaction) { txn.SiacoinInputs[0].SatisfiedPolicy.Preimages = nil },
		"missing classical signature": func(txn *types.V2Transaction) { txn.SiacoinInputs[0].SatisfiedPolicy.Signatures = nil },
	} {
		bad := proof.DeepCopy()
		mutate(&bad)
		if consensus.ValidateV2Transaction(consensus.NewMidState(cs), bad) == nil {
			t.Fatal("accepted " + name)
		}
	}
	bad = proof.DeepCopy()
	bad.SiacoinOutputs[0].Value = bad.SiacoinInputs[0].Parent.SiacoinOutput.Value
	if err := qday.SignTransfer(context.Background(), cs, &bad, &keys); err != nil {
		t.Fatal(err)
	}
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), bad) == nil {
		t.Fatal("proof created its fee without paying for it")
	}
	exactFee := proof.DeepCopy()
	exactFee.MinerFee = exactFee.SiacoinInputs[0].Parent.SiacoinOutput.Value
	exactFee.SiacoinOutputs = nil
	exactFee.ArbitraryData = (consensus.QdayEnvelope{Kind: consensus.QdayCanaryProof, Witness: [32]byte{1}}).Encode()
	if err := qday.SignTransfer(context.Background(), cs, &exactFee, &keys); err != nil {
		t.Fatal(err)
	}
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(cs), exactFee); err != nil {
		t.Fatal("funded proof without change rejected: ", err)
	}
	proofBlock := apply(proof)
	if proofBlock.MinerPayouts[0].Value != m.Network.Qday.Reward.Add(proof.MinerFee) {
		t.Fatal("proof fee not paid to the miner")
	}
	currentID = proof.SiacoinOutputID(proof.ID(), 0)
	if cs.QdayHeight != 3 || cs.QdayActive(cs.Index.Height) {
		t.Fatal("activation delay incorrect")
	}
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), proof) == nil {
		t.Fatal("repeat proof accepted")
	}
	apply()
	if !cs.QdayActive(3) || cs.QdayUnits(3) != types.HastingsPerSiacoin.Div64(1_000_000) {
		t.Fatal("QDAY denomination failed")
	}
	parent := utxos[currentID]
	good := send(parent, parent.SiacoinOutput.Value)
	forgedHeight := good.DeepCopy()
	forgedHeight.SiacoinInputs[0].Parent.MaturityHeight = 0
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), forgedHeight) == nil {
		t.Fatal("unauthenticated shield birth accepted")
	}
	padding := good.DeepCopy()
	padding.SiacoinInputs[0].SatisfiedPolicy.Preimages[types.QdaySignatureChunks-1][31] = 1
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), padding) == nil {
		t.Fatal("noncanonical reserve padding accepted")
	}
	forged := good.DeepCopy()
	forged.SiacoinInputs[0].SatisfiedPolicy.Preimages = nil
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), forged) == nil {
		t.Fatal("classical key alone spent guarded coins")
	}
	mutated := good.DeepCopy()
	mutated.SiacoinOutputs[0].Value = mutated.SiacoinOutputs[0].Value.Sub(types.NewCurrency64(1))
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), mutated) == nil {
		t.Fatal("signature did not bind outputs")
	}
	badWork := good.DeepCopy()
	env, _ := consensus.ParseQdayEnvelope(badWork.ArbitraryData)
	intent := cs.QdayWorkIntent(badWork)
	for consensus.QdayWorkValid(intent, env.Nonce, cs.Network.Qday.DefendBits) {
		env.Nonce++
	}
	badWork.ArbitraryData = env.Encode()
	sp, err := keys.Sign(cs.InputSigHash(badWork))
	if err != nil {
		t.Fatal(err)
	}
	badWork.SiacoinInputs[0].SatisfiedPolicy = sp
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), badWork) == nil {
		t.Fatal("signed transfer bypassed DEFEND work")
	}
	for cs.Index.Height < 19 {
		apply()
	}
	parent = utxos[currentID]
	value := cs.QdayValue(parent, cs.Index.Height+1)
	if value.IsZero() || value.Cmp(parent.SiacoinOutput.Value) >= 0 {
		t.Fatal("offline balance did not decay")
	}
	resurrect := send(parent, parent.SiacoinOutput.Value)
	if consensus.ValidateV2Transaction(consensus.NewMidState(cs), resurrect) == nil {
		t.Fatal("burned value resurrected")
	}
	defend := send(parent, value)
	apply(defend)
	fresh := utxos[defend.SiacoinOutputID(defend.ID(), 0)]
	if cs.QdayValue(fresh, cs.Index.Height+12) != value {
		t.Fatal("DEFEND did not renew shield")
	}
	if !cs.QdayValue(fresh, cs.Index.Height+12+24).IsZero() {
		t.Fatal("full offline decay did not burn output")
	}
	// The fork without the proof retains the pre-event state; serialize/reload
	// and real manager reorganization must both preserve this distinction.
	var buf bytes.Buffer
	enc := types.NewEncoder(&buf)
	cs.EncodeTo(enc)
	enc.Flush()
	var decoded consensus.State
	decoded.DecodeFrom(types.NewBufDecoder(buf.Bytes()))
	if decoded.QdayHeight != cs.QdayHeight {
		t.Fatal("activation lost in state serialization")
	}
	db, tip, err := chain.NewDBStore(chain.NewMemDB(), &m.Network, m.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(db, tip)
	// Build two valid branches starting directly from genesis for a real reorg.
	a := qday.Candidate(tip, keys.Public, []types.V2Transaction{makeProof(tip, premine)}, m.Genesis.Timestamp.Add(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a, err = qday.Mine(ctx, tip, a, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cm.AddBlocks([]types.Block{a}); err != nil {
		t.Fatal(err)
	}
	if cm.TipState().QdayHeight == 0 {
		t.Fatal("proof not applied by manager")
	}
	branch := tip
	var alternative []types.Block
	for i := 0; i < 4; i++ {
		b := qday.Candidate(branch, keys.Public, nil, branch.PrevTimestamps[0].Add(3*time.Second))
		b, err = qday.Mine(ctx, branch, b, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		branch, _ = consensus.ApplyBlock(branch, b, consensus.V1BlockSupplement{}, time.Time{})
		alternative = append(alternative, b)
	}
	if err = cm.AddBlocks(alternative); err != nil {
		t.Fatal(err)
	}
	if cm.TipState().QdayHeight != 0 {
		t.Fatal("QDAY survived removal of its proof in a reorg")
	}
}

func TestQdayMainnetIsolation(t *testing.T) {
	dev := chain.QdayDevnet()
	main, err := chain.NewQdayManifest(dev.Premine, dev.Genesis.Timestamp, false)
	if err != nil {
		t.Fatal(err)
	}
	if main.Genesis.ID() == dev.Genesis.ID() || main.Network.Qday.Canary == dev.Network.Qday.Canary {
		t.Fatal("devnet not isolated")
	}
	if main.Network.Qday.Reward != types.Siacoins(8) || main.Network.Qday.ProofFee != types.Siacoins(1) || main.Network.Qday.MiningBlocks != 1_000_000 {
		t.Fatal("launch reward, proof fee or mining duration changed")
	}
	premine := main.Genesis.Transactions[0].SiacoinOutputs[0].Value
	mined := main.Network.Qday.Reward.Mul64(main.Network.Qday.MiningBlocks)
	if mined != types.Siacoins(8_000_000) || premine != types.Siacoins(500_000) || main.Network.Qday.PremineAmount != premine || premine.Add(mined) != types.Siacoins(8_500_000) {
		t.Fatal("fixed premine or 8500000 gross supply is incorrect")
	}
	wrong := main
	params := *main.Network.Qday
	params.PremineAmount = types.Siacoins(500_001)
	wrong.Network.Qday = &params
	if wrong.Validate() == nil {
		t.Fatal("a different premine amount was accepted")
	}
	if main.Network.GenesisState().Difficulty.String() != "1048575" || main.Network.BlockInterval != time.Minute {
		t.Fatal("CPU launch difficulty or target interval changed")
	}
	if consensus.VerifyQdayProof(main.Network.Qday.Canary, [32]byte{1}) {
		t.Fatal("known dev secret activates mainnet")
	}
	main.Network.Qday.DefendBits = 0
	if main.Validate() == nil {
		t.Fatal("modified mainnet parameters accepted")
	}
	_, err = chain.NewQdayManifest(types.QdayAddress{}, time.Now().Truncate(time.Second), false)
	if err == nil {
		t.Fatal("unset premine accepted")
	}
	b, _ := json.Marshal(dev)
	var restored chain.QdayManifest
	if err = json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.Validate(); err != nil {
		t.Fatal(err)
	}
	s := dev.Network.GenesisState()
	s.Index.Height = 1_000_000
	if !s.BlockReward().IsZero() {
		t.Fatal("reward past emission cap")
	}
}

func TestQdayGenesisMessage(t *testing.T) {
	owner := chain.QdayDevnet().Premine
	timestamp := time.Unix(1789109940, 0).UTC()
	plain, err := chain.NewQdayManifest(owner, timestamp, false)
	if err != nil {
		t.Fatal(err)
	}
	const message = "PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME."
	inscribed, err := chain.NewQdayManifestWithMessage(owner, timestamp, false, message)
	if err != nil {
		t.Fatal(err)
	}
	if inscribed.GenesisMessage != message || len(inscribed.Genesis.Transactions) != 1 || len(inscribed.Genesis.Transactions[0].ArbitraryData) != 2 || string(inscribed.Genesis.Transactions[0].ArbitraryData[1]) != message {
		t.Fatal("genesis inscription was not committed verbatim")
	}
	if inscribed.Genesis.ID() == plain.Genesis.ID() || inscribed.Network.Qday.Domain == plain.Network.Qday.Domain {
		t.Fatal("genesis inscription did not isolate the network and genesis")
	}
	if err = inscribed.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(inscribed)
	if err != nil {
		t.Fatal(err)
	}
	var restored chain.QdayManifest
	if err = json.Unmarshal(b, &restored); err != nil || restored.Validate() != nil || restored.GenesisMessage != message {
		t.Fatal("inscribed manifest did not survive a strict JSON round trip")
	}
	tampered := inscribed
	tampered.GenesisMessage += " ALTERED"
	if tampered.Validate() == nil {
		t.Fatal("altered genesis inscription was accepted")
	}
	for _, invalid := range []string{" surrounding whitespace ", "line\nbreak", string(bytes.Repeat([]byte{'x'}, 257))} {
		if _, err = chain.NewQdayManifestWithMessage(owner, timestamp, false, invalid); err == nil {
			t.Fatal("invalid genesis inscription was accepted")
		}
	}
}

func TestInscribedGenesisMinesFirstMainnetBlock(t *testing.T) {
	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59, 9})
	if err != nil {
		t.Fatal(err)
	}
	const message = "PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME."
	m, err := chain.NewQdayManifestWithMessage(keys.Public.Address(), time.Unix(1700000000, 0).UTC(), false, message)
	if err != nil {
		t.Fatal(err)
	}
	db, tip, err := chain.NewDBStore(chain.NewMemDB(), &m.Network, m.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(db, tip)
	candidate := qday.Candidate(tip, keys.Public, nil, m.Genesis.Timestamp.Add(time.Minute))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var hashes atomic.Uint64
	mined, err := qday.Mine(ctx, tip, candidate, 1, &hashes)
	if err != nil {
		t.Fatal(err)
	}
	if hashes.Load() == 0 || mined.ParentID != m.Genesis.ID() || mined.Header().ID().CmpWork(tip.PoWTarget()) < 0 {
		t.Fatal("native BLAKE2b miner did not produce valid work on the inscribed genesis")
	}
	if err = cm.AddBlocks([]types.Block{mined}); err != nil {
		t.Fatal(err)
	}
	if cm.Tip().Height != 1 || cm.Tip().ID != mined.ID() {
		t.Fatal("consensus manager did not accept the first inscribed-mainnet block")
	}
}

func TestQdayGenesisCannotStartEarly(t *testing.T) {
	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59, 10})
	if err != nil {
		t.Fatal(err)
	}
	genesisTime := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	m, err := chain.NewQdayManifestWithMessage(keys.Public.Address(), genesisTime, false, "PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME.")
	if err != nil {
		t.Fatal(err)
	}
	db, tip, err := chain.NewDBStore(chain.NewMemDB(), &m.Network, m.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(db, tip)

	justBefore := genesisTime.Add(-time.Nanosecond)
	if got := tip.MaxFutureTimestamp(justBefore); !got.Equal(justBefore) {
		t.Fatalf("pre-genesis future limit is %v, want %v", got, justBefore)
	}
	if got := tip.MaxFutureTimestamp(genesisTime); !got.Equal(genesisTime.Add(3 * time.Hour)) {
		t.Fatalf("genesis boundary did not open: got %v", got)
	}

	mine := func(timestamp time.Time) types.Block {
		t.Helper()
		candidate := qday.Candidate(tip, keys.Public, nil, timestamp)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		candidate, err = qday.Mine(ctx, tip, candidate, 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}

	// Forging the scheduled timestamp does not bypass the node's wall clock.
	if err = cm.AddBlocks([]types.Block{mine(genesisTime)}); !errors.Is(err, chain.ErrFutureBlock) {
		t.Fatalf("scheduled first block was accepted before genesis: %v", err)
	}
	if cm.Tip().Height != 0 {
		t.Fatal("rejected future block changed the chain")
	}
	// Using the current time cannot bypass the inherited genesis median.
	if err = cm.AddBlocks([]types.Block{mine(time.Now().UTC())}); err == nil || !bytes.Contains([]byte(err.Error()), []byte("timestamp too far in the past")) {
		t.Fatalf("pre-genesis timestamp was accepted: %v", err)
	}
	if cm.Tip().Height != 0 {
		t.Fatal("rejected early block changed the chain")
	}
}
