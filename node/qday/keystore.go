package qday

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"go.sia.tech/core/types"
	"golang.org/x/crypto/argon2"
)

type keyFile struct {
	Version    int    `json:"version"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Address    string `json:"address"`
}

func keyCipher(password string, salt []byte) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	defer clear(key)
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// WriteKey creates an encrypted keystore without overwriting existing keys.
// The recovery phrase is the backup; neither phrase nor password is persisted.
func WriteKey(path, password string, seed [32]byte, keys types.QdayKeys) error {
	if len(password) < 12 {
		return errors.New("use a wallet passphrase of at least 12 characters")
	}
	k := keyFile{Version: 1, Salt: make([]byte, 16), Nonce: make([]byte, 12), Address: keys.String()}
	rand.Read(k.Salt)
	rand.Read(k.Nonce)
	c, err := keyCipher(password, k.Salt)
	if err != nil {
		return err
	}
	k.Ciphertext = c.Seal(nil, k.Nonce, seed[:], []byte("QDAY/keystore/v1/"+k.Address))
	b, err := json.Marshal(k)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".qday-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(b); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Hard-linking a fully synced file publishes it atomically and refuses to
	// replace an existing wallet, including during competing create requests.
	if err = os.Link(tmp.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	} // Windows does not support directory fsync.
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func readKey(path string) (k keyFile, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return k, err
	}
	if len(b) > 4096 {
		return k, errors.New("invalid keystore size")
	}
	if err = json.Unmarshal(b, &k); err != nil {
		return k, err
	}
	if k.Version != 1 || len(k.Salt) != 16 || len(k.Nonce) != 12 || len(k.Ciphertext) != 48 {
		return k, errors.New("invalid keystore format")
	}
	_, err = types.ParseQdayAddress(k.Address)
	return k, err
}

func ReadKey(path, password string) (seed [32]byte, err error) {
	k, err := readKey(path)
	if err != nil {
		return seed, err
	}
	c, err := keyCipher(password, k.Salt)
	if err != nil {
		return seed, err
	}
	b, err := c.Open(nil, k.Nonce, k.Ciphertext, []byte("QDAY/keystore/v1/"+k.Address))
	if err != nil {
		return seed, errors.New("incorrect passphrase or damaged keystore")
	}
	copy(seed[:], b)
	clear(b)
	return
}
