package qday

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"go.sia.tech/core/gateway"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/walletd/v2/persist/sqlite"
)

func TestManualPeerConnectionAndPersistence(t *testing.T) {
	a, b := newTestService(t), newTestService(t)
	var peerStore syncer.PeerStore
	for _, s := range []*Service{a, b} {
		store, err := sqlite.OpenDatabase(filepath.Join(filepath.Dir(s.path), "manual-peer-test.sqlite3"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		ps, err := sqlite.NewPeerStore(store)
		if err != nil {
			t.Fatal(err)
		}
		if s == a {
			peerStore = ps
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s.Syncer = syncer.New(listener, s.CM, ps, gateway.Header{GenesisID: s.Manifest.Genesis.ID(), UniqueID: gateway.GenerateUniqueID(), NetAddress: listener.Addr().String()}, syncer.WithMaxOutboundPeers(0))
		go s.Syncer.Run()
		t.Cleanup(func() { s.Syncer.Close() })
	}
	for _, invalid := range []string{"", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "0.0.0.0:19771", "[::]:19771", "239.1.1.1:19771", "https://127.0.0.1:19771", "example.com:19771"} {
		if _, err := a.AddPeer(context.Background(), invalid); err == nil {
			t.Fatalf("invalid manual peer accepted: %q", invalid)
		}
	}
	for i := 0; i < 2; i++ {
		result, err := a.AddPeer(context.Background(), b.Syncer.Addr())
		if err != nil || result["saved"] != true || result["connected"] != true {
			t.Fatalf("manual QDAY connection failed: %v, %v", result, err)
		}
	}
	if len(a.Syncer.Peers()) != 1 {
		t.Fatal("adding the same address duplicated the connection")
	}
	if _, err := peerStore.PeerInfo(b.Syncer.Addr()); err != nil {
		t.Fatal("manual peer was not saved")
	}
	// Loading a fresh peer store from disk must retain the manual address.
	reopened, err := sqlite.OpenDatabase(filepath.Join(filepath.Dir(a.path), "manual-peer-test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ps, err := sqlite.NewPeerStore(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ps.PeerInfo(b.Syncer.Addr()); err != nil {
		t.Fatal("manual peer missing after store reload")
	}
}
