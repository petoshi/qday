package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.sia.tech/coreutils/chain"
	"go.sia.tech/walletd/v2/internal/localapp"
)

func TestWalletDefaultAndDistributionSeeds(t *testing.T) {
	dir := t.TempDir()
	m, err := chain.NewQdayManifest(chain.QdayDevnet().Premine, time.Now().UTC().Truncate(time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	manifest := filepath.Join(dir, "qday-mainnet.json")
	if err := os.WriteFile(manifest, b, 0600); err != nil {
		t.Fatal(err)
	}
	want := []string{"seed1.pqday.com:19771", "seed2.pqday.com:19771", "seed3.pqday.com:19771"}
	o := options{network: manifest}
	if _, err := networkFor(&o, dir); err != nil || !slices.Equal(o.peers, want) {
		t.Fatalf("source wallet did not default to three seeds: %v", err)
	}
	for _, configured := range [][]string{nil, {}, {"127.0.0.1:19771"}} {
		b, _ := json.Marshal(distribution{Format: 1, Network: "mainnet", Seeds: configured})
		if err := os.WriteFile(filepath.Join(dir, "distribution.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
		o := options{}
		if _, err := networkFor(&o, dir); err != nil {
			t.Fatal(err)
		}
		expected := configured
		if configured == nil {
			expected = want
		}
		if !slices.Equal(o.peers, expected) {
			t.Fatal("distribution seed override changed")
		}
	}
	copy := localapp.MainnetSeeds()
	copy[0] = "modified.invalid:1"
	if !slices.Equal(localapp.MainnetSeeds(), want) {
		t.Fatal("caller modified the built-in seed list")
	}
}
