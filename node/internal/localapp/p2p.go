package localapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

const (
	MainnetP2PPort = 19771
	MainnetAPIPort = 19770
)

type p2pConfig struct {
	Port      uint16 `json:"port"`
	MappingID string `json:"mappingID"`
}

// ListenP2P reserves the P2P listener and returns a persistent router mapping
// owner. "auto" chooses a random free port once, then reuses it on restart.
// Explicit addresses fail on conflicts; only auto may select a replacement.
func ListenP2P(dir, addr string) (net.Listener, string, error) {
	path := filepath.Join(dir, "p2p.json")
	var c p2pConfig
	if b, err := os.ReadFile(path); errors.Is(err, os.ErrNotExist) {
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return nil, "", err
		}
		c.MappingID = hex.EncodeToString(id[:])
	} else if err != nil {
		return nil, "", err
	} else if len(b) > 4096 || json.Unmarshal(b, &c) != nil {
		return nil, "", errors.New("invalid p2p.json")
	}
	if id, err := hex.DecodeString(c.MappingID); err != nil || len(id) != 16 {
		return nil, "", errors.New("invalid P2P mapping identity")
	}
	automatic := addr == "auto"
	if automatic {
		addr = net.JoinHostPort("", strconv.Itoa(int(c.Port)))
	}
	l, err := net.Listen("tcp", addr)
	if err != nil && automatic {
		l, err = net.Listen("tcp", ":0")
	}
	if err != nil {
		return nil, "", err
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	n, _ := strconv.ParseUint(port, 10, 16)
	c.Port = uint16(n)
	b, _ := json.MarshalIndent(c, "", "  ")
	err = func() error {
		f, err := os.CreateTemp(dir, ".p2p-*")
		if err != nil {
			return err
		}
		defer f.Close()
		defer os.Remove(f.Name())
		if _, err = f.Write(append(b, '\n')); err != nil {
			return err
		}
		if err = f.Sync(); err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		return os.Rename(f.Name(), path)
	}()
	if err != nil {
		l.Close()
		return nil, "", fmt.Errorf("could not save P2P configuration: %w", err)
	}
	return l, "QDAY P2P " + c.MappingID, nil
}
