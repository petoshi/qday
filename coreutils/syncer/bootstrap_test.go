package syncer_test

import (
	"context"
	"net"
	"testing"
	"time"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/testutil"
)

func waitBootstrap(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(description)
}

func bootstrapCounts(s *syncer.Syncer) (seeds, ordinary int) {
	for _, p := range s.Peers() {
		if p.Err() == nil && p.Synced() {
			if s.IsBootstrap(p) {
				if !p.Inbound {
					seeds++
				}
			} else {
				ordinary++
			}
		}
	}
	return
}

func TestBootstrapReleaseAndFallback(t *testing.T) {
	seed, seedCM := newTestSyncer(t, syncer.WithSeedMode(nil), syncer.WithPeerDiscoveryInterval(50*time.Millisecond))
	defer seed.Close()
	_, port, _ := net.SplitHostPort(seed.Addr())
	// DNS resolves to a canonical address in the store. The seed must retain
	// its role when subsequently learned through another peer as an IP:port.
	address := net.JoinHostPort("localhost", port)
	options := []syncer.Option{syncer.WithBootstrapPeers([]string{address}), syncer.WithBootstrapRelease(2, time.Second), syncer.WithPeerDiscoveryInterval(50 * time.Millisecond), syncer.WithConnectTimeout(500 * time.Millisecond), syncer.WithShareNodesTimeout(300 * time.Millisecond)}
	a, cmA := newTestSyncer(t, options...)
	defer a.Close()
	// This upstream fixture begins before v2. Reach the v2 relay regime before
	// testing block propagation, as QDAY itself enables v2 at its first block.
	testutil.MineBlocks(t, cmA, types.VoidAddress, int(cmA.TipState().Network.HardforkV2.RequireHeight))
	waitBootstrap(t, 5*time.Second, func() bool { s, _ := bootstrapCounts(a); return s == 1 }, "seed did not connect")
	b, cmB := newTestSyncer(t, options...)
	defer b.Close()
	waitBootstrap(t, 5*time.Second, func() bool { s, n := bootstrapCounts(a); return s == 1 && n == 1 }, "one replacement should not release the seed")
	time.Sleep(1200 * time.Millisecond)
	if s, _ := bootstrapCounts(a); s != 1 {
		t.Fatal("released seed with only one ordinary connection")
	}
	c, cmC := newTestSyncer(t, options...)
	defer c.Close()
	waitBootstrap(t, 5*time.Second, func() bool { s, n := bootstrapCounts(a); return s == 1 && n == 2 }, "seed was released before replacement connections stabilized")
	waitBootstrap(t, 8*time.Second, func() bool {
		for _, node := range []*syncer.Syncer{a, b, c} {
			s, n := bootstrapCounts(node)
			if s != 0 || n != 2 {
				return false
			}
		}
		return true
	}, "wallets did not release bootstrap connections")
	mineBlocks(t, a, cmA, 2)
	synced(t, syncedNode{a, cmA}, syncedNode{b, cmB}, syncedNode{c, cmC})
	// Seeds retain a bounded set of links they initiate themselves. Clients
	// preserve these inbound links, keeping seeds synchronized with the mesh.
	synced(t, syncedNode{a, cmA}, syncedNode{seed, seedCM})
	c.Close()
	waitBootstrap(t, 8*time.Second, func() bool {
		for _, p := range a.Peers() {
			if a.IsBootstrap(p) && p.Err() == nil && p.Synced() {
				return true
			}
		}
		return false
	}, "seed fallback did not recover after a replacement disconnected")
	// The surviving ordinary connection continues to relay blocks.
	mineBlocks(t, a, cmA, 1)
	synced(t, syncedNode{a, cmA}, syncedNode{b, cmB})
}

func TestBootstrapHandshakeIsNotEnough(t *testing.T) {
	seed, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer seed.Close()
	a, _ := newTestSyncer(t, syncer.WithBootstrapPeers([]string{seed.Addr()}), syncer.WithBootstrapRelease(2, 0), syncer.WithSyncInterval(time.Hour), syncer.WithPeerDiscoveryInterval(50*time.Millisecond))
	defer a.Close()
	b, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer b.Close()
	c, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer c.Close()
	for _, other := range []*syncer.Syncer{b, c} {
		if _, err := a.Connect(context.Background(), other.Addr()); err != nil {
			t.Fatal(err)
		}
	}
	waitBootstrap(t, 3*time.Second, func() bool { return len(a.Peers()) == 3 }, "missing bootstrap link")
	time.Sleep(300 * time.Millisecond)
	if len(a.Peers()) != 3 {
		t.Fatal("unsynchronized handshakes incorrectly replaced the seed")
	}
}

func TestBootstrapFallbackAfterAllOrdinaryPeersExit(t *testing.T) {
	// Passive seeds model a wallet outside a seed's bounded outbound mesh.
	// There must be no surviving inbound seed connection to mask fallback.
	seed1, _ := newTestSyncer(t, syncer.WithMaxOutboundPeers(0))
	defer seed1.Close()
	seed2, seedCM := newTestSyncer(t, syncer.WithMaxOutboundPeers(0))
	defer seed2.Close()
	address := func(s *syncer.Syncer) string {
		_, port, _ := net.SplitHostPort(s.Addr())
		return net.JoinHostPort("127.0.0.1", port)
	}
	// Keep the production five-second discovery and 30-second stability
	// defaults. Only the fixture's chain synchronization interval is shorter.
	a, cmA := newTestSyncer(t, syncer.WithBootstrapPeers([]string{address(seed1), address(seed2)}))
	defer a.Close()
	waitBootstrap(t, 20*time.Second, func() bool {
		n, _ := bootstrapCounts(a)
		return n == 2
	}, "wallet did not discover both seeds")
	b, _ := newTestSyncer(t, syncer.WithMaxOutboundPeers(0))
	defer b.Close()
	c, _ := newTestSyncer(t, syncer.WithMaxOutboundPeers(0))
	defer c.Close()
	d, offlineCM := newTestSyncer(t, syncer.WithMaxOutboundPeers(0))
	defer d.Close()
	for _, peer := range []*syncer.Syncer{b, c, d} {
		if _, err := a.Connect(context.Background(), address(peer)); err != nil {
			t.Fatal(err)
		}
	}
	waitBootstrap(t, 50*time.Second, func() bool {
		seeds, ordinary := bootstrapCounts(a)
		return seeds == 0 && ordinary == 3 && len(a.Peers()) == 3
	}, "wallet did not release every seed connection after finding three ordinary peers")
	t.Log("wallet has exactly three ordinary peers and no seed connection")
	seed1.Close()
	b.Close()
	c.Close()
	d.Close()
	lostAt := time.Now()
	// The seed receives a block prepared by an offline ordinary-node fixture.
	// No seed process mines. The wallet must download the missing block.
	testutil.MineBlocks(t, offlineCM, types.VoidAddress, 1)
	block, ok := offlineCM.Block(offlineCM.Tip().ID)
	if !ok {
		t.Fatal("missing fixture block")
	} else if err := seedCM.AddBlocks([]types.Block{block}); err != nil {
		t.Fatal(err)
	}
	waitBootstrap(t, 30*time.Second, func() bool {
		seeds, ordinary := bootstrapCounts(a)
		return seeds == 1 && ordinary == 0 && cmA.Tip() == seedCM.Tip()
	}, "wallet did not recover through the remaining seed and download its missing block")
	t.Logf("all three ordinary peers and the first seed are offline; fallback and block catch-up completed in %s", time.Since(lostAt).Round(time.Millisecond))
}
