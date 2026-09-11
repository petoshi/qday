// testgenesis creates disposable mainnet-parameter fixtures for local tests.
// It is not a wallet command and is never included in release archives.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: testgenesis TEMPORARY_DIRECTORY")
		os.Exit(1)
	}
	dir := os.Args[1]
	seed := seedwallet.NewQdaySeed()
	defer clear(seed[:])
	keys, err := types.QdayKeysFromSeed(seed)
	if err != nil {
		panic(err)
	}
	m, err := chain.NewQdayManifestWithMessage(keys.Public.Address(), time.Now().UTC().Truncate(time.Second), false, "PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME.")
	if err != nil {
		panic(err)
	}
	for name, value := range map[string]any{"test-mainnet.json": m, "test-owner.json": map[string]string{"phrase": seedwallet.QdaySeedPhrase(seed)}} {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			panic(err)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		if _, err = f.Write(append(b, '\n')); err != nil {
			f.Close()
			panic(err)
		}
		if err = f.Close(); err != nil {
			panic(err)
		}
	}
}
