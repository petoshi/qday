package qday

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func TestProofPublication(t *testing.T) {
	s := newTestService(t)
	synced(t, s)
	server := httptest.NewUnstartedServer(nil)
	token := strings.Repeat("a", 64)
	server.Config.Handler = s.Handler(token, server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	request := func(path string, body any, want int) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", server.URL+"/api/"+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s returned HTTP %d, want %d", path, res.StatusCode, want)
		}
		var result map[string]any
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	witness := "01" + strings.Repeat("00", 31)
	unit := types.HastingsPerSiacoin.ExactString()
	request("proof/verify", map[string]string{"witness": "02" + strings.Repeat("00", 31)}, 400)
	request("proof/verify", map[string]string{"witness": "01"}, 400)
	verified := request("proof/verify", map[string]string{"witness": witness}, 200)
	if verified["valid"] != true || verified["fee"] != "1" || len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("verification must be free, with a one-QDAY publication quote")
	}
	request("proof", map[string]string{"witness": witness, "unit": unit}, 400)
	phrase := seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})
	if _, err := s.Create(context.Background(), "proof-test-passphrase", phrase); err != nil {
		t.Fatal(err)
	}
	request("proof", map[string]string{"witness": witness, "unit": "1"}, 400)
	request("proof/verify", map[string]string{"witness": witness}, 200)
	if len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("verification spent coins or submitted a transaction")
	}
	published := request("proof", map[string]string{"witness": witness, "unit": unit}, 200)
	pool := s.CM.V2PoolTransactions()
	if len(pool) != 1 || pool[0].MinerFee != types.Siacoins(1) || len(pool[0].SiacoinInputs) != 1 || len(pool[0].SiacoinOutputs) != 1 || pool[0].SiacoinOutputs[0].Value != types.Siacoins(500_000).Sub(types.Siacoins(1)) {
		t.Fatal("proof fee was not paid with real wallet funds and change")
	}
	if published["transaction"] != pool[0].ID().String() || s.pendingProof() != pool[0].ID().String() {
		t.Fatal("published proof not reported in the mempool")
	}
	if err := consensus.ValidateV2Transaction(consensus.NewMidState(s.CM.TipState()), pool[0]); err != nil {
		t.Fatal(err)
	}
	request("proof", map[string]string{"witness": witness, "unit": unit}, 400)
	b := mineForTest(t, s)
	if len(b.V2.Transactions) != 2 || b.MinerPayouts[0].Value != types.Siacoins(9) || s.CM.TipState().QdayHeight != 2 || s.pendingProof() != "" {
		t.Fatal("proof inclusion, miner fee or activation delay failed")
	}
	request("proof", map[string]string{"witness": witness, "unit": unit}, 400)
	if s.CM.TipState().QdayActive(1) {
		t.Fatal("event skipped activation delay")
	}
}

func TestProofNeedsFundsAndRecoveryRestoresAddress(t *testing.T) {
	a := newTestService(t)
	b := newTestService(t)
	ctx := context.Background()
	phrase, err := a.Create(ctx, "first-wallet-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Create(ctx, "different-wallet-password", phrase); err != nil {
		t.Fatal(err)
	}
	if a.public != b.public || a.public == (types.QdayAddress{}) {
		t.Fatal("same recovery words did not restore both keys and the same address")
	}
	a.Lock()
	if _, err := a.Recovery("incorrect-password"); err == nil {
		t.Fatal("recovery backup exposed without password verification")
	}
	if restored, err := a.Recovery("first-wallet-password"); err != nil || restored != phrase {
		t.Fatal("could not recover a locked wallet with the correct password")
	}
	if err := a.Unlock(ctx, "first-wallet-password"); err != nil {
		t.Fatal(err)
	}
	synced(t, a)
	if _, err := a.PublishProof(ctx, [32]byte{1}); err == nil || !strings.Contains(err.Error(), "insufficient confirmed balance") {
		t.Fatal("unfunded wallet published a proof")
	}
	if _, err := b.Create(ctx, "overwrite-password", phrase); err == nil {
		t.Fatal("restore overwrote an existing wallet")
	}
}
