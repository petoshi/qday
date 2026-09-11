package qday

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	mining "go.sia.tech/coreutils/qday"
	seedwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/walletd/v2/persist/sqlite"
	"go.sia.tech/walletd/v2/wallet"
)

func TestStatusBalanceDuringIndexCatchup(t *testing.T) {
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
	defer store.Close()
	wm, err := wallet.NewManager(cm, store, wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { wm.Close() }()
	s, err := NewService(context.Background(), dir, cm, wm, nil, m)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create(context.Background(), "status-test-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	status, err := s.Status()
	if err != nil || status["balanceReady"] != true || status["balance"] != "500000" {
		t.Fatalf("initial balance: %v, %v", status, err)
	}

	// Stop only the indexer. Its database remains readable while consensus
	// accepts a block, reproducing the normal gap between these two updates.
	wm.Close()
	cs := cm.TipState()
	block := mining.Candidate(cs, s.keys.Public, nil, cs.PrevTimestamps[0].Add(2*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	block, err = mining.Mine(ctx, cs, block, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cm.AddBlocks([]types.Block{block}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		status, err = s.Status()
		if err != nil || status["synced"] != false || status["balanceReady"] != false {
			t.Fatalf("catchup status: %v, %v", status, err)
		}
		for _, field := range []string{"balance", "immature", "pending"} {
			if status[field] != nil {
				t.Fatalf("unknown %s was reported as a monetary amount: %v", field, status[field])
			}
		}
	}

	wm, err = wallet.NewManager(cm, store, wallet.WithIndexMode(wallet.IndexModeFull))
	if err != nil {
		t.Fatal(err)
	}
	s.WM = wm
	synced(t, s)
	status, err = s.Status()
	if err != nil || status["balanceReady"] != true || status["balance"] != "500000" || status["immature"] != "8" {
		t.Fatalf("recovered balance: %v, %v", status, err)
	}

	// A real pending spend consumes the only mature output. Zero must still
	// be reported as a known amount, then replaced after confirmation.
	other, err := types.QdayKeysFromSeed([32]byte{99})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transfer(context.Background(), other.Public.Address(), types.Siacoins(1), false); err != nil {
		t.Fatal(err)
	}
	status, err = s.Status()
	if err != nil || status["balanceReady"] != true || status["balance"] != "0" {
		t.Fatalf("real zero balance was hidden: %v, %v", status, err)
	}
	mineForTest(t, s)
	status, err = s.Status()
	if err != nil || status["balanceReady"] != true || status["balance"] == "0" || status["balance"] == "500000" {
		t.Fatalf("confirmed transfer did not update balance: %v, %v", status, err)
	}
}
