package qday

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"go.sia.tech/core/types"
	seedwallet "go.sia.tech/coreutils/wallet"
)

// Restore replaces the active encrypted wallet without retaining a backup.
// replaceAddress identifies the wallet the caller explicitly chose to replace,
// preventing a stale browser tab from replacing a subsequently imported wallet.
func (s *Service) Restore(ctx context.Context, password, phrase, replaceAddress string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("use a wallet passphrase of at least 12 characters")
	}
	seed, err := seedwallet.QdaySeedFromPhrase(phrase)
	if err != nil {
		return "", err
	}
	defer clear(seed[:])
	keys, err := types.QdayKeysFromSeed(seed)
	if err != nil {
		return "", err
	}
	adopted := false
	defer func() {
		if !adopted {
			clear(keys.Classical)
			keys = types.QdayPrivateKeys{}
		}
	}()
	s.control.Lock()
	defer s.control.Unlock()
	// A running DEFEND operation may need op, so stop it before taking op.
	s.stop()
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	current := ""
	if s.public != (types.QdayAddress{}) {
		current = s.public.String()
	}
	s.mu.Unlock()
	if replaceAddress != current {
		return "", errors.New("the active wallet changed; reopen Import seed phrase and try again")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Complete encryption and address registration before replacing the file.
	// Only the new encrypted key is staged; the old wallet is never copied.
	dir, err := os.MkdirTemp(filepath.Dir(s.path), ".qday-import-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, "wallet.key")
	if err = WriteKey(staged, password, seed, keys.Public); err != nil {
		return "", err
	}
	id, err := s.registerWallet(keys.Public)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err = os.Rename(staged, s.path); err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.keys != nil {
		clear(s.keys.Classical)
		*s.keys = types.QdayPrivateKeys{}
	}
	s.keys, s.public, s.walletID = &keys, keys.Public.Address(), id
	s.lastError = ""
	s.mu.Unlock()
	adopted = true
	address := keys.Public.String()
	if runtime.GOOS != "windows" {
		d, err := os.Open(filepath.Dir(s.path))
		if err == nil {
			err = d.Sync()
			d.Close()
		}
		if err != nil {
			return address, fmt.Errorf("wallet imported, but saving the directory failed: %w", err)
		}
	}
	return address, nil
}
