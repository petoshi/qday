package qday

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.sia.tech/core/types"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func TestReplaceWallet(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	oldSeed, newSeed := [32]byte{0x51, 0x44, 0x41, 0x59}, [32]byte{87}
	oldPhrase, newPhrase := seedwallet.QdaySeedPhrase(oldSeed), seedwallet.QdaySeedPhrase(newSeed)
	if _, err := s.Create(ctx, "original-test-password", oldPhrase); err != nil {
		t.Fatal(err)
	}
	oldAddress := s.public.String()
	oldFile, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{
		{"new-test-password", strings.Repeat("abandon ", 24), oldAddress},
		{"short", newPhrase, oldAddress},
		{"new-test-password", newPhrase, ""},
	} {
		if _, err := s.Restore(ctx, args[0], args[1], args[2]); err == nil {
			t.Fatal("invalid or stale import was accepted")
		}
		b, err := os.ReadFile(s.path)
		if err != nil || !bytes.Equal(b, oldFile) || s.public.String() != oldAddress {
			t.Fatal("rejected import changed the active wallet")
		}
	}
	if err := s.Start(1); err != nil {
		t.Fatal(err)
	}
	address, err := s.Restore(ctx, "new-test-password", newPhrase, oldAddress)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := types.QdayKeysFromSeed(newSeed)
	if err != nil || address != keys.Public.String() || s.public.String() != address {
		t.Fatal("import did not activate the seed's address")
	}
	if s.mode != "STOP" || s.runCancel != nil || s.runDone != nil {
		t.Fatal("import left CPU activity running")
	}
	if _, err := s.submitFor(ctx, oldAddress, keys.Public.Address(), types.Siacoins(1), submitTransfer, nil); err == nil || !strings.Contains(err.Error(), "active wallet changed") {
		t.Fatal("a stale transaction review can spend from the imported wallet")
	}
	if got, err := ReadKey(s.path, "new-test-password"); err != nil || got != newSeed {
		t.Fatal("new encrypted wallet was not saved")
	}
	if _, err := ReadKey(s.path, "original-test-password"); err == nil {
		t.Fatal("old password still decrypts the replacement")
	}
	// Import must leave no staged files or automatic backup of the old wallet.
	entries, err := os.ReadDir(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.Contains(entry.Name(), "backup") || strings.HasPrefix(entry.Name(), ".qday-") {
			t.Fatalf("unexpected import residue: %s", entry.Name())
		}
		b, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), entry.Name()))
		if err != nil || bytes.Contains(b, oldFile) || bytes.Contains(b, []byte(oldPhrase)) || bytes.Contains(b, []byte(newPhrase)) {
			t.Fatal("old wallet or plaintext seed phrase was retained")
		}
	}
	// A stale tab cannot silently replace the new wallet.
	if _, err := s.Restore(ctx, "original-test-password", oldPhrase, oldAddress); err == nil {
		t.Fatal("stale replacement accepted")
	}
	// A fresh service sees the imported address while locked, before decryption.
	reopened, err := NewService(ctx, filepath.Dir(s.path), s.CM, s.WM, nil, s.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.keys != nil || reopened.public.String() != address {
		t.Fatal("imported wallet was not preserved for restart")
	}
	// The user's saved phrase is the only way back; no old password is needed.
	s.Lock()
	if got, err := s.Restore(ctx, "restored-old-password", oldPhrase, address); err != nil || got != oldAddress {
		t.Fatal("saved seed phrase could not restore the previous wallet")
	}
	synced(t, s)
	status, err := s.Status()
	if err != nil || status["balance"] != "500000" {
		t.Fatal("restoring the original seed did not recover its premine")
	}
}

func TestRestoreSameSeedResetsPassword(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	seed := [32]byte{0x51, 0x44, 0x41, 0x59, 5}
	phrase := seedwallet.QdaySeedPhrase(seed)
	if _, err := s.Create(ctx, "forgotten-test-password", phrase); err != nil {
		t.Fatal(err)
	}
	address := s.public.String()
	s.Lock()
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Restore(ctx, "replacement-test-password", phrase, address)
	if err != nil || got != address || s.keys == nil || s.public.String() != address {
		t.Fatal("seed phrase did not recover the same wallet with a new password")
	}
	if s.mode != "STOP" || s.runCancel != nil || s.runDone != nil {
		t.Fatal("password recovery left CPU activity running")
	}
	after, err := os.ReadFile(s.path)
	if err != nil || bytes.Equal(before, after) {
		t.Fatal("password recovery did not replace the encrypted wallet file")
	}
	if _, err := ReadKey(s.path, "forgotten-test-password"); err == nil {
		t.Fatal("forgotten password still decrypts the recovered wallet")
	}
	if restored, err := ReadKey(s.path, "replacement-test-password"); err != nil || restored != seed {
		t.Fatal("new password does not decrypt the recovered wallet")
	}
	entries, err := os.ReadDir(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.Contains(entry.Name(), "backup") || strings.HasPrefix(entry.Name(), ".qday-") {
			t.Fatalf("unexpected recovery residue: %s", entry.Name())
		}
	}
}

func TestRestoreHTTP(t *testing.T) {
	s := newTestService(t)
	phrase := seedwallet.QdaySeedPhrase([32]byte{22})
	body, err := json.Marshal(map[string]string{"password": "import-test-password", "phrase": phrase, "replaceAddress": ""})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler("test-token", "127.0.0.1:19770")
	for _, token := range []string{"", "test-token"} {
		r := httptest.NewRequest("POST", "http://127.0.0.1:19770/api/restore", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if token == "" {
			if w.Code != http.StatusUnauthorized || s.keys != nil {
				t.Fatal("unauthenticated import accepted")
			}
		} else if w.Code != http.StatusOK || s.keys == nil || bytes.Contains(w.Body.Bytes(), []byte(phrase)) {
			t.Fatal("authenticated import into an empty wallet failed or returned its secret")
		}
	}
}
