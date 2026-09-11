package qday

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	mining "go.sia.tech/coreutils/qday"
	seedwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/walletd/v2/persist/sqlite"
	"go.sia.tech/walletd/v2/wallet"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	m := chain.QdayDevnet()
	db, tip, err := chain.NewDBStore(chain.NewMemDB(), &m.Network, m.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(db, tip)
	dir := t.TempDir()
	store, err := sqlite.OpenDatabase(filepath.Join(dir, "wallet.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	wm, err := wallet.NewManager(cm, store, wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewService(context.Background(), dir, cm, wm, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); wm.Close(); store.Close() })
	return s
}

func synced(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tip, err := s.WM.Tip()
		if err != nil {
			t.Fatal(err)
		}
		if tip == s.CM.Tip() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("wallet failed to index chain")
}

func mineForTest(t *testing.T, s *Service, extra ...types.V2Transaction) types.Block {
	t.Helper()
	cs := s.CM.TipState()
	txns := append(s.CM.V2PoolTransactions(), extra...)
	b := mining.Candidate(cs, s.keys.Public, txns, cs.PrevTimestamps[0].Add(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	b, err := mining.Mine(ctx, cs, b, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CM.AddBlocks([]types.Block{b}); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	return b
}

func TestNativeWallet(t *testing.T) {
	s := newTestService(t)
	synced(t, s)
	seed := [32]byte{0x51, 0x44, 0x41, 0x59}
	phrase := seedwallet.QdaySeedPhrase(seed)
	returned, err := s.Create(context.Background(), "test-only-password", phrase)
	if err != nil {
		t.Fatal(err)
	}
	if returned != phrase {
		t.Fatal("restore changed recovery words")
	}
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status["balance"] != "500000" {
		t.Fatalf("premine not restored: %v", status["balance"])
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("keystore permissions")
	}
	if _, err = s.Create(context.Background(), "another-password", ""); err == nil {
		t.Fatal("overwrote existing wallet")
	}
	s.Lock()
	if err = s.Unlock(context.Background(), "wrong"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	if err = s.Unlock(context.Background(), "test-only-password"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Recovery("test-only-password"); err != nil || got != phrase {
		t.Fatal("could not verify recovery backup")
	}
	if _, err := s.Recovery("wrong"); err == nil {
		t.Fatal("backup exported without the passphrase")
	}
	to, err := types.QdayKeysFromSeed([32]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Transfer(context.Background(), to.Public.Address(), types.Siacoins(10), false); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s)
	out, _, err := s.WM.AddressSiacoinOutputs(to.Public.Policy().Address(), false, 0, 100)
	if err != nil || len(out) != 1 || out[0].SiacoinOutput.Value != types.Siacoins(10) {
		t.Fatalf("recipient did not receive transfer: %v %d", err, len(out))
	}
	if _, err := s.PublishProof(context.Background(), [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s)
	mineForTest(t, s)
	status, err = s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status["qday"] != true {
		t.Fatal("native wallet missed QDAY")
	}
	for s.CM.Tip().Height < 17 {
		mineForTest(t, s)
	}
	if _, err = s.Transfer(context.Background(), types.QdayAddress{}, types.ZeroCurrency, true); err != nil {
		t.Fatal(err)
	}
	b := mineForTest(t, s)
	if len(b.V2.Transactions) < 2 {
		t.Fatal("DEFEND never confirmed")
	}
	// Lock/unlock must retain correct proofs and balance, not reapply history.
	before, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	s.Lock()
	if err = s.Unlock(context.Background(), "test-only-password"); err != nil {
		t.Fatal(err)
	}
	after, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if before["balance"] != after["balance"] {
		t.Fatal("unlock changed balance")
	}
	if err = s.Start(1); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for s.hashes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.Stop()
	h := s.hashes.Load()
	if h == 0 {
		t.Fatal("native miner never hashed")
	}
	time.Sleep(30 * time.Millisecond)
	if s.hashes.Load() != h {
		t.Fatal("STOP left mining active")
	}
}

func TestNativeMinerWaitsForGenesis(t *testing.T) {
	s := newTestService(t)
	synced(t, s)
	if _, err := s.Create(context.Background(), "test-only-password", seedwallet.QdaySeedPhrase([32]byte{71})); err != nil {
		t.Fatal(err)
	}
	s.Manifest.Genesis.Timestamp = time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status["genesisReady"] != false || status["genesisWaitSeconds"].(int64) < 3598 {
		t.Fatalf("future genesis was not exposed to the wallet: %v %v", status["genesisReady"], status["genesisWaitSeconds"])
	}
	if err = s.Start(1); err == nil || err.Error() != "waiting for the genesis start time" {
		t.Fatalf("native miner started before genesis: %v", err)
	}
	if s.mode != "STOP" || s.hashes.Load() != 0 {
		t.Fatal("rejected start performed CPU work")
	}
}

func TestWalletHTTPBoundary(t *testing.T) {
	s := newTestService(t)
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = s.Handler(strings.Repeat("a", 64), server.Listener.Addr().String())
	server.Start()
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	check := func(token, host, origin string, want int) {
		t.Helper()
		req, _ := http.NewRequest("GET", server.URL+"/api/status", nil)
		req.Host = host
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("got HTTP %d, want %d", res.StatusCode, want)
		}
	}
	check("", "127.0.0.1:"+port, "", 401)
	check(strings.Repeat("a", 64), "attacker.invalid:"+port, "", 403)
	check(strings.Repeat("a", 64), "127.0.0.1:"+port, "http://attacker.invalid", 403)
	check(strings.Repeat("a", 64), "127.0.0.1:"+port, "", 200)
}

func TestAmountAndRecovery(t *testing.T) {
	for _, seed := range [][32]byte{{}, {1}, {255, 3, 99}} {
		phrase := seedwallet.QdaySeedPhrase(seed)
		got, err := seedwallet.QdaySeedFromPhrase(phrase)
		if err != nil || got != seed {
			t.Fatal("recovery round trip")
		}
	}
	if seedwallet.QdaySeedPhrase([32]byte{}) != strings.TrimSpace(strings.Repeat("abandon ", 23)+"art") {
		t.Fatal("256-bit BIP39 known vector mismatch")
	}
	for _, unit := range []types.Currency{types.HastingsPerSiacoin, types.HastingsPerSiacoin.Div64(1_000_000)} {
		for _, str := range []string{"0", "1.25", "100000000000000", "0.000000000000000001"} {
			v, err := ParseAmount(str, unit)
			if err != nil || FormatAmount(v, unit) != str {
				t.Fatalf("amount %s: %v", str, err)
			}
		}
		for _, str := range []string{"-1", "1e6", "NaN", "1.2.3"} {
			if _, err := ParseAmount(str, unit); err == nil {
				t.Fatal("invalid amount accepted")
			}
		}
	}
}
