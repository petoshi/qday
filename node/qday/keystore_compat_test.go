package qday

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.sia.tech/core/types"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func TestLegacyKeystoreRetainsRecoveryAndOwnership(t *testing.T) {
	s := newTestService(t)
	seed := [32]byte{31, 41, 59}
	keys, err := types.QdayKeysFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	payload := keys.Public.Bytes()
	check := sha256.Sum256(append([]byte("QDAY/address/v1"), payload...))
	legacy := "qday1" + hex.EncodeToString(append(payload, check[:4]...))
	k := keyFile{Version: 1, Salt: bytes.Repeat([]byte{1}, 16), Nonce: bytes.Repeat([]byte{2}, 12), Address: legacy}
	cipher, err := keyCipher("legacy-test-passphrase", k.Salt)
	if err != nil {
		t.Fatal(err)
	}
	k.Ciphertext = cipher.Seal(nil, k.Nonce, seed[:], []byte("QDAY/keystore/v1/"+legacy))
	encoded, _ := json.Marshal(k)
	if err := os.WriteFile(s.path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewService(context.Background(), filepath.Dir(s.path), s.CM, s.WM, nil, s.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	synced(t, loaded)
	status, err := loaded.Status()
	if err != nil || status["address"] != keys.Public.String() || len(status["address"].(string)) != 64 || status["unlocked"] != false {
		t.Fatalf("legacy locked wallet did not retain its short address: %v", err)
	}
	if err := loaded.Unlock(context.Background(), "legacy-test-passphrase"); err != nil {
		t.Fatal(err)
	}
	if got, err := loaded.Recovery("legacy-test-passphrase"); err != nil || got != seedwallet.QdaySeedPhrase(seed) {
		t.Fatal("legacy recovery changed")
	}
	if loaded.keys.Public != keys.Public {
		t.Fatal("legacy wallet keys changed")
	}
	after, _ := os.ReadFile(s.path)
	if !bytes.Equal(encoded, after) {
		t.Fatal("legacy encrypted file was rewritten")
	}
}
