package qday

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	mining "go.sia.tech/coreutils/qday"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func TestBurn(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "burn-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)

	amount := types.Siacoins(100_000)
	id, err := s.Burn(context.Background(), amount)
	if err != nil {
		t.Fatal(err)
	}
	pool := s.CM.V2PoolTransactions()
	if len(pool) != 1 || pool[0].ID() != id {
		t.Fatal("burn was not added to the transaction pool")
	}
	txn := pool[0]
	if len(txn.SiacoinOutputs) != 2 {
		t.Fatalf("burn has %d outputs, want burn and change", len(txn.SiacoinOutputs))
	} else if txn.SiacoinOutputs[0].Address != types.VoidAddress || txn.SiacoinOutputs[0].Value != amount {
		t.Fatal("burn output does not pay the requested amount to the void address")
	}
	fee := types.HastingsPerSiacoin.Div64(1000)
	wantChange := types.Siacoins(400_000).Sub(fee)
	if txn.SiacoinOutputs[1].Address != s.keys.Public.Policy().Address() || txn.SiacoinOutputs[1].Value != wantChange {
		t.Fatal("burn returned the wrong change")
	}

	b := mineForTest(t, s)
	if len(b.V2.Transactions) != 2 || b.V2.Transactions[1].ID() != id {
		t.Fatal("burn was not confirmed")
	}
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	} else if status["balance"] != "399999.999" {
		t.Fatalf("wrong wallet balance after burn: %v", status["balance"])
	}
	supply, err := s.Supply()
	if err != nil {
		t.Fatal(err)
	} else if supply.IssuedSupply.QDAY != "500008" || supply.BurnedSupply.QDAY != "100000" || supply.CurrentSupply.QDAY != "400008" {
		t.Fatalf("wrong supply after burn: %+v", supply)
	} else if supply.ImmatureSupply.QDAY != "8.001" || supply.CirculatingSupply.QDAY != "399999.999" {
		t.Fatalf("wrong circulating supply after burn: %+v", supply)
	} else if supply.Height != 1 || !supply.Synced || supply.UnspentOutputs != 2 {
		t.Fatalf("wrong supply metadata after burn: %+v", supply)
	}

	// Replace the burn block with a longer branch. The transaction may return
	// to the mempool, but no selected-chain supply is burned after the reorg.
	branch, ok := s.CM.State(s.Manifest.Genesis.ID())
	if !ok {
		t.Fatal("genesis state unavailable")
	}
	other, err := types.QdayKeysFromSeed([32]byte{0x99})
	if err != nil {
		t.Fatal(err)
	}
	var alternative []types.Block
	for range 4 {
		candidate := mining.Candidate(branch, other.Public, nil, branch.PrevTimestamps[0].Add(3*time.Second))
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		candidate, err = mining.Mine(ctx, branch, candidate, 2, nil)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		branch, _ = consensus.ApplyBlock(branch, candidate, consensus.V1BlockSupplement{}, time.Time{})
		alternative = append(alternative, candidate)
	}
	if err = s.CM.AddBlocks(alternative); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	supply, err = s.Supply()
	if err != nil {
		t.Fatal(err)
	} else if supply.Height != 4 || supply.BurnedSupply.QDAY != "0" || supply.CurrentSupply.QDAY != "500032" {
		t.Fatalf("burn survived chain reorganization: %+v", supply)
	}
	if _, err := s.Burn(context.Background(), types.ZeroCurrency); err == nil {
		t.Fatal("zero-value burn was accepted")
	}
}

func TestBurnHTTP(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "burn-http-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)

	token := strings.Repeat("b", 64)
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = s.Handler(token, server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	body, _ := json.Marshal(map[string]string{
		"fromAddress": s.public.String(),
		"amount":      "123.5",
		"unit":        types.HastingsPerSiacoin.ExactString(),
	})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/burn", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("burn returned HTTP %d", response.StatusCode)
	}
	pool := s.CM.V2PoolTransactions()
	if len(pool) != 1 || pool[0].SiacoinOutputs[0].Address != types.VoidAddress || pool[0].SiacoinOutputs[0].Value != types.Siacoins(123).Add(types.HastingsPerSiacoin.Div64(2)) {
		t.Fatal("HTTP burn did not create the requested void output")
	}
}
