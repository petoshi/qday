package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.sia.tech/coreutils/chain"
)

func TestMainnetOnly(t *testing.T) {
	if run([]string{"--devnet"}) == nil {
		t.Fatal("removed development flag accepted")
	}
	if run(nil) == nil {
		t.Fatal("node started without a manifest")
	}
	m := chain.QdayDevnet()
	path := filepath.Join(t.TempDir(), "network.json")
	b, _ := json.Marshal(m)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if run([]string{"--network", path, "--validate"}) == nil {
		t.Fatal("test consensus accepted as mainnet")
	}
	main, err := chain.NewQdayManifest(m.Premine, time.Now().UTC().Truncate(time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(main)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--network", path, "--validate"}); err != nil {
		t.Fatal(err)
	}
	main.Genesis.Timestamp = main.Genesis.Timestamp.Add(time.Second)
	b, _ = json.Marshal(main)
	os.WriteFile(path, b, 0600)
	if run([]string{"--network", path, "--validate"}) == nil {
		t.Fatal("editing the timestamp without rebuilding genesis was accepted")
	}
}

func TestGenesisRetimePreservesLaunchSettings(t *testing.T) {
	dir := t.TempDir()
	first, final := filepath.Join(dir, "rehearsal.json"), filepath.Join(dir, "final.json")
	const message = "PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME."
	if err := genesis([]string{"--address", chain.QdayDevnet().Premine.String(), "--out", first}); err != nil {
		t.Fatal(err)
	}
	before, err := loadManifest(first)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := before.Genesis.Timestamp.Add(time.Hour)
	if err := genesis([]string{"--from", first, "--timestamp", timestamp.Format(time.RFC3339), "--message", message, "--out", final}); err != nil {
		t.Fatal(err)
	}
	after, err := loadManifest(final)
	if err != nil {
		t.Fatal(err)
	}
	if before.Premine != after.Premine || before.Genesis.ID() == after.Genesis.ID() || !after.Genesis.Timestamp.Equal(timestamp) || after.GenesisMessage != message {
		t.Fatal("retiming changed ownership or failed to create a fresh genesis")
	}
	if len(after.Genesis.Transactions[0].ArbitraryData) != 2 || string(after.Genesis.Transactions[0].ArbitraryData[1]) != message {
		t.Fatal("genesis command did not preserve the requested inscription")
	}
	before.Network.Qday.Domain = after.Network.Qday.Domain
	before.Network.HardforkOak.GenesisTimestamp = after.Network.HardforkOak.GenesisTimestamp
	if !reflect.DeepEqual(before.Network, after.Network) {
		t.Fatal("retiming changed a consensus setting")
	}
	unchanged, _ := os.ReadFile(first)
	if string(original) != string(unchanged) {
		t.Fatal("source manifest was overwritten")
	}
	if genesis([]string{"--from", first, "--out", final}) == nil {
		t.Fatal("existing final manifest was overwritten")
	}
}
