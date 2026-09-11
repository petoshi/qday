package sqlite

import (
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils"
	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/v2/wallet"
)

func TestQdaySupply(t *testing.T) {
	db := newTestStore(t)
	manifest := chain.QdayDevnet()
	if _, _, _, err := db.QdaySupply(manifest.Network.GenesisState()); err == nil {
		t.Fatal("personal index was accepted for a network-wide supply query")
	}
	if err := db.SetIndexMode(wallet.IndexModeFull); err != nil {
		t.Fatal(err)
	}

	bdb, err := coreutils.OpenBoltChainDB(filepath.Join(t.TempDir(), "consensus.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	chainStore, tip, err := chain.NewDBStore(bdb, &manifest.Network, manifest.Genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	cm := chain.NewManager(chainStore, tip)
	syncDB(t, db, cm)

	supply, immature, outputs, err := db.QdaySupply(cm.TipState())
	if err != nil {
		t.Fatal(err)
	} else if supply != types.Siacoins(500_000) || !immature.IsZero() || outputs != 1 {
		t.Fatalf("got supply %v, immature %v in %d outputs", supply, immature, outputs)
	}

	if _, err := db.db.Exec(`UPDATE sia_addresses SET sia_address=$1`, encode(types.VoidAddress)); err != nil {
		t.Fatal(err)
	}
	supply, immature, outputs, err = db.QdaySupply(cm.TipState())
	if err != nil {
		t.Fatal(err)
	} else if !supply.IsZero() || !immature.IsZero() || outputs != 0 {
		t.Fatalf("void output counted toward supply: %v in %d outputs", supply, outputs)
	}
}
