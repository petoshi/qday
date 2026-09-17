package qday

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/blake2b"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	mining "go.sia.tech/coreutils/qday"
	seedwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/walletd/v2/persist/sqlite"
	"go.sia.tech/walletd/v2/wallet"
)

func decodeTemplateBlock(t *testing.T, template MiningTemplateResponse) types.Block {
	t.Helper()
	var parent types.BlockID
	if err := parent.UnmarshalText([]byte(template.PreviousBlockHash)); err != nil {
		t.Fatal(err)
	}
	payoutBytes, err := hex.DecodeString(template.MinerPayout[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	var payout types.V2SiacoinOutput
	payout.DecodeFrom(types.NewBufDecoder(payoutBytes))
	txns := make([]types.V2Transaction, 0, len(template.Transactions))
	for _, encoded := range template.Transactions {
		b, err := hex.DecodeString(encoded.Data)
		if err != nil {
			t.Fatal(err)
		}
		var txn types.V2Transaction
		decoder := types.NewBufDecoder(b)
		txn.DecodeFrom(decoder)
		if err := decoder.Err(); err != nil {
			t.Fatal(err)
		}
		txns = append(txns, txn)
	}
	return types.Block{
		ParentID:     parent,
		Timestamp:    time.Unix(template.Timestamp, 0),
		MinerPayouts: []types.SiacoinOutput{payout.Cast()},
		V2: &types.V2BlockData{
			Height:       uint64(template.Height),
			Commitment:   template.Commitment,
			Transactions: txns,
		},
	}
}

func verifyStratumTemplate(t *testing.T, template MiningTemplateResponse) {
	t.Helper()
	if len(template.Transactions) == 0 {
		t.Fatal("template has no rightmost transaction")
	}
	txn, err := hex.DecodeString(template.Transactions[len(template.Transactions)-1].Data)
	if err != nil {
		t.Fatal(err)
	}
	root := blake2b.Sum256(append([]byte{0}, txn...))
	for _, encoded := range template.Stratum.MerkleBranch {
		branch, err := hex.DecodeString(encoded)
		if err != nil || len(branch) != 32 {
			t.Fatalf("invalid Stratum Merkle branch %q", encoded)
		}
		var left [32]byte
		copy(left[:], branch)
		root = blake2b.SumPair(left, root)
	}
	if root != template.Commitment {
		t.Fatalf("Stratum Merkle root %x does not match commitment %x", root, template.Commitment)
	}
	raw, err := hex.DecodeString(template.Stratum.Block)
	if err != nil {
		t.Fatal(err)
	}
	var block types.V2Block
	decoder := types.NewBufDecoder(raw)
	block.DecodeFrom(decoder)
	if err := decoder.Err(); err != nil {
		t.Fatal(err)
	}
	decodedBlock := block.Cast()
	if decodedBlock.Header().Commitment != template.Commitment {
		t.Fatal("encoded Stratum block does not match template commitment")
	}
}

func TestPendingTransactionReloadsAfterRestart(t *testing.T) {
	manifest := chain.QdayDevnet()
	chainDB := chain.NewMemDB()
	dir := t.TempDir()
	walletPath := filepath.Join(dir, "wallet.sqlite3")

	open := func() (*Service, *wallet.Manager, *sqlite.Store) {
		store, tip, err := chain.NewDBStore(chainDB, &manifest.Network, manifest.Genesis, nil)
		if err != nil {
			t.Fatal(err)
		}
		cm := chain.NewManager(store, tip)
		db, err := sqlite.OpenDatabase(walletPath)
		if err != nil {
			t.Fatal(err)
		}
		wm, err := wallet.NewManager(cm, db, wallet.WithIndexMode(wallet.IndexModeFull))
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		s, err := NewService(context.Background(), dir, cm, wm, nil, manifest)
		if err != nil {
			wm.Close()
			db.Close()
			t.Fatal(err)
		}
		return s, wm, db
	}
	closeAll := func(s *Service, wm *wallet.Manager, db *sqlite.Store) {
		s.Close()
		wm.Close()
		db.Close()
	}

	s, wm, db := open()
	if _, err := s.Create(context.Background(), "restart-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	id, err := s.Burn(context.Background(), types.Siacoins(1))
	if err != nil {
		t.Fatal(err)
	}
	closeAll(s, wm, db)

	s, wm, db = open()
	defer closeAll(s, wm, db)
	deadline := time.Now().Add(2 * time.Second)
	for len(s.CM.V2PoolTransactions()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pool := s.CM.V2PoolTransactions()
	if len(pool) != 1 || pool[0].ID() != id {
		t.Fatal("locally-created transaction was not restored to the mempool")
	}
}

func TestMiningTemplateTracksPoolAndSubmits(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "mining-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)

	token := strings.Repeat("c", 64)
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = s.Handler(token, server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/miner/getblocktemplate", bytes.NewReader([]byte(`{}`)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var initial MiningTemplateResponse
	if err := json.NewDecoder(response.Body).Decode(&initial); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(initial.Header) != 160 || len(initial.Transactions) != 1 {
		t.Fatalf("invalid initial mining template: %+v", initial)
	}
	verifyStratumTemplate(t, initial)
	workNonce := uint64(0x51444159)
	custom, err := s.MiningTemplateForWork(context.Background(), "", &workNonce)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := consensus.ParseQdayEnvelope(decodeTemplateBlock(t, custom).V2.Transactions[0].ArbitraryData)
	if err != nil || marker.Nonce != workNonce || custom.WorkNonce != workNonce {
		t.Fatalf("custom work nonce was not committed: %+v %v", custom, err)
	} else if custom.Commitment == initial.Commitment {
		t.Fatal("custom work nonce did not produce independent work")
	}
	verifyStratumTemplate(t, custom)
	zeroWorkNonce := uint64(0)
	if _, err := s.MiningTemplateForWork(context.Background(), "", &zeroWorkNonce); err == nil {
		t.Fatal("zero work nonce was accepted")
	}
	if _, err := s.MiningTemplateForWork(context.Background(), initial.LongPollID, &workNonce); err == nil {
		t.Fatal("work nonce and long poll ID were accepted together")
	}

	next := make(chan MiningTemplateResponse, 1)
	failure := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() {
		template, err := s.MiningTemplate(ctx, initial.LongPollID)
		if err != nil {
			failure <- err
		} else {
			next <- template
		}
	}()

	burnID, err := s.Burn(context.Background(), types.Siacoins(100_000))
	if err != nil {
		t.Fatal(err)
	}
	var template MiningTemplateResponse
	select {
	case err := <-failure:
		t.Fatal(err)
	case template = <-next:
	case <-ctx.Done():
		t.Fatal("long poll did not return after the mempool changed")
	}
	if template.LongPollID == initial.LongPollID || len(template.Transactions) != 2 || template.Transactions[1].TxID != burnID.String() {
		t.Fatalf("updated template omitted pool transaction: %+v", template)
	}
	verifyStratumTemplate(t, template)

	block := decodeTemplateBlock(t, template)
	mineContext, stopMining := context.WithTimeout(context.Background(), time.Second)
	block, err = mining.Mine(mineContext, s.CM.TipState(), block, 2, nil)
	stopMining()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeMiningObject(types.V2Block(block))
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(MiningSubmitBlockRequest{Params: []string{encoded}})
	request, _ = http.NewRequest(http.MethodPost, server.URL+"/api/miner/submitblock", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure map[string]string
		json.NewDecoder(response.Body).Decode(&failure)
		t.Fatalf("submitblock returned HTTP %d: %v", response.StatusCode, failure)
	}
	if s.CM.Tip().ID != block.ID() || len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("submitted block did not confirm the mempool transaction")
	}
	status := s.MiningStatus(block.ID())
	if !status.Known || !status.Canonical || status.Height != s.CM.Tip().Height || status.MaturityHeight != status.Height+s.CM.TipState().Network.MaturityDelay {
		t.Fatalf("wrong submitted block status: %+v", status)
	}
	if got := block.MinerPayouts[0].Value; got != s.CM.TipState().BlockReward().Add(types.HastingsPerSiacoin.Div64(1000)) {
		t.Fatal("template did not pay the transaction fee to the miner")
	}
}

func TestV1MiningTemplateUsesCompactSiaWork(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "v1-mining-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	s.Manifest.Network.Qday.V1Height = 1

	workNonce := uint64(1)
	template, err := s.MiningTemplateForWork(context.Background(), "", &workNonce)
	if err != nil {
		t.Fatal(err)
	}
	if len(template.Transactions) != 2 {
		t.Fatalf("v1 template has %d transactions, expected two markers", len(template.Transactions))
	} else if template.Stratum.ExtraNonce1Size != 4 || template.Stratum.ExtraNonce2Size != 4 {
		t.Fatalf("wrong extranonce split: %d+%d", template.Stratum.ExtraNonce1Size, template.Stratum.ExtraNonce2Size)
	}
	coinbase1, err := hex.DecodeString(template.Stratum.Coinbase1)
	if err != nil {
		t.Fatal(err)
	}
	coinbase2, err := hex.DecodeString(template.Stratum.Coinbase2)
	if err != nil {
		t.Fatal(err)
	}
	if len(coinbase1) != 23 || len(coinbase2) != 2 {
		t.Fatalf("compact transaction split is %d+8+%d bytes, expected 23+8+2", len(coinbase1), len(coinbase2))
	}
	extraNonce1 := []byte{1, 2, 3, 4}
	extraNonce2 := []byte{5, 6, 7, 8}
	encodedWork := append(append(append([]byte{}, coinbase1...), extraNonce1...), extraNonce2...)
	encodedWork = append(encodedWork, coinbase2...)
	if len(encodedWork) != 33 {
		t.Fatalf("compact work transaction is %d bytes", len(encodedWork))
	}
	var work types.V2Transaction
	decoder := types.NewBufDecoder(encodedWork)
	work.DecodeFrom(decoder)
	var canonical bytes.Buffer
	encoder := types.NewEncoder(&canonical)
	work.EncodeTo(encoder)
	if decoder.Err() != nil || encoder.Flush() != nil || !bytes.Equal(canonical.Bytes(), encodedWork) {
		t.Fatalf("ASIC work did not reconstruct a canonical transaction: %v", decoder.Err())
	}
	envelope, err := consensus.ParseQdayEnvelope(work.ArbitraryData)
	if err != nil || envelope.Kind != consensus.QdayMiningWork || envelope.Nonce != binary.LittleEndian.Uint64(append(extraNonce1, extraNonce2...)) {
		t.Fatalf("wrong reconstructed mining envelope: %+v, %v", envelope, err)
	}

	root := work.MerkleLeafHash()
	for _, encoded := range template.Stratum.MerkleBranch {
		branch, err := hex.DecodeString(encoded)
		if err != nil || len(branch) != 32 {
			t.Fatalf("invalid Merkle branch %q", encoded)
		}
		var left types.Hash256
		copy(left[:], branch)
		root = blake2b.SumPair(left, root)
	}
	block := decodeTemplateBlock(t, template)
	block.V2.Transactions[len(block.V2.Transactions)-1] = work
	block.V2.Commitment = root
	block = mineHeaderForTemplate(t, s.CM.TipState(), block)
	if err := consensus.ValidateBlock(s.CM.TipState(), block, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal("reconstructed Sia Stratum block failed consensus: ", err)
	}

	recipient, err := types.QdayKeysFromSeed([32]byte{0x33})
	if err != nil {
		t.Fatal(err)
	}
	transactionID, err := s.Transfer(context.Background(), recipient.Public.Address(), types.Siacoins(1), false)
	if err != nil {
		t.Fatal(err)
	}
	workNonce++
	withMempool, err := s.MiningTemplateForWork(context.Background(), "", &workNonce)
	if err != nil {
		t.Fatal(err)
	}
	if withMempool.MempoolTransactions != 1 || len(withMempool.Transactions) != 3 {
		t.Fatalf("template counts %d mempool transactions across %d entries", withMempool.MempoolTransactions, len(withMempool.Transactions))
	} else if withMempool.Transactions[1].TxID != transactionID.String() {
		t.Fatalf("template omitted pending transaction %v", transactionID)
	}
	coinbase1, err = hex.DecodeString(withMempool.Stratum.Coinbase1)
	if err != nil {
		t.Fatal(err)
	}
	coinbase2, err = hex.DecodeString(withMempool.Stratum.Coinbase2)
	if err != nil {
		t.Fatal(err)
	}
	encodedWork = append(append(append([]byte{}, coinbase1...), extraNonce1...), extraNonce2...)
	encodedWork = append(encodedWork, coinbase2...)
	decoder = types.NewBufDecoder(encodedWork)
	work = types.V2Transaction{}
	work.DecodeFrom(decoder)
	if decoder.Err() != nil {
		t.Fatal(decoder.Err())
	}
	root = work.MerkleLeafHash()
	for _, encoded := range withMempool.Stratum.MerkleBranch {
		branch, err := hex.DecodeString(encoded)
		if err != nil || len(branch) != 32 {
			t.Fatalf("invalid Merkle branch %q", encoded)
		}
		var left types.Hash256
		copy(left[:], branch)
		root = blake2b.SumPair(left, root)
	}
	block = decodeTemplateBlock(t, withMempool)
	block.V2.Transactions[len(block.V2.Transactions)-1] = work
	block.V2.Commitment = root
	block = mineHeaderForTemplate(t, s.CM.TipState(), block)
	if err := consensus.ValidateBlock(s.CM.TipState(), block, consensus.V1BlockSupplement{}); err != nil {
		t.Fatal("transaction-aware Sia Stratum block failed consensus: ", err)
	}
}

func mineHeaderForTemplate(t *testing.T, cs consensus.State, block types.Block) types.Block {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	block, err := mining.Mine(ctx, cs, block, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	return block
}
