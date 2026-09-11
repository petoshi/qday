package localapp

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentWalletPort(t *testing.T) {
	dir := t.TempDir()
	l, owner, err := ListenP2P(dir, "auto")
	if err != nil {
		t.Fatal(err)
	}
	first := l.Addr().String()
	l.Close()
	l, owner2, err := ListenP2P(dir, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if l.Addr().String() != first || owner2 != owner {
		t.Fatal("port or mapping owner changed on restart")
	}
	l.Close()
	// Another process takes the remembered port. Auto must find a free one,
	// while preserving the mapping identity and refusing explicit conflicts.
	busy, err := net.Listen("tcp", first)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if bad, _, err := ListenP2P(dir, first); err == nil {
		bad.Close()
		t.Fatal("explicit conflict was ignored")
	}
	l, owner2, err = ListenP2P(dir, "auto")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Addr().String() == first || owner2 != owner {
		t.Fatal("auto fallback did not preserve identity")
	}
	b, err := os.ReadFile(filepath.Join(dir, "p2p.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg p2pConfig
	if json.Unmarshal(b, &cfg) != nil || cfg.Port == 0 {
		t.Fatal("port was not persisted")
	}
	other, otherOwner, err := ListenP2P(t.TempDir(), "auto")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if otherOwner == owner {
		t.Fatal("independent wallets share a mapping identity")
	}
}
