package syncer_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"go.sia.tech/coreutils/syncer"
)

type seedResolver struct {
	mu  sync.Mutex
	ips []net.IPAddr
}

func (r *seedResolver) set(ips ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ips = nil
	for _, ip := range ips {
		r.ips = append(r.ips, net.IPAddr{IP: net.ParseIP(ip)})
	}
}

func (r *seedResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if host != "seed.example.invalid" {
		return nil, errors.New("unexpected DNS lookup")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ips), nil
}

// Only the listed synthetic DNS addresses may dial real local listeners. This
// also ensures that the test never contacts public seeds or the Internet.
type seedDialer struct {
	mu       sync.Mutex
	targets  map[string]string
	attempts map[string]int
}

func (d *seedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.attempts[address]++
	target := d.targets[address]
	d.mu.Unlock()
	if target == "" {
		return nil, errors.New("unreachable test endpoint")
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}

func (d *seedDialer) attempted(address string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.attempts[address] != 0
}

func TestBootstrapAllDNSAnswersAndRefresh(t *testing.T) {
	a, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer a.Close()
	b, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer b.Close()
	bad, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	go func() {
		for {
			conn, err := bad.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(time.Second))
			conn.Write([]byte("NOTQDAY!"))
			conn.Close()
		}
	}()
	r := new(seedResolver)
	r.set("192.0.2.1", "192.0.2.2", "192.0.2.3", "2001:db8::1", "192.0.2.1")
	d := &seedDialer{targets: map[string]string{
		"192.0.2.2:19771": bad.Addr().String(),
		"192.0.2.3:19771": a.Addr(),
		"192.0.2.4:19771": b.Addr(),
	}, attempts: make(map[string]int)}
	c, _ := newTestSyncer(t,
		syncer.WithBootstrapPeers([]string{"seed.example.invalid:19771"}),
		syncer.WithPeerResolver(r), syncer.WithDialer(d), syncer.WithSeedDNSRefresh(40*time.Millisecond),
		syncer.WithPeerDiscoveryInterval(30*time.Millisecond), syncer.WithConnectTimeout(200*time.Millisecond))
	defer c.Close()
	waitBootstrap(t, 5*time.Second, func() bool {
		seedCount, _ := bootstrapCounts(c)
		return seedCount == 1 && d.attempted("192.0.2.1:19771") && d.attempted("192.0.2.2:19771") && d.attempted("192.0.2.3:19771") && d.attempted("[2001:db8::1]:19771")
	}, "did not try all A/AAAA endpoints after connection or handshake failures")
	if d.attempted("seed.example.invalid:19771") {
		t.Fatal("hostname was dialed instead of its individual DNS answers")
	}
	// Add a server under the same name, without recreating or restarting c.
	r.set("192.0.2.4")
	a.Close()
	waitBootstrap(t, 5*time.Second, func() bool {
		count, _ := bootstrapCounts(c)
		return count == 1 && d.attempted("192.0.2.4:19771")
	}, "updated DNS record was not discovered or lost its bootstrap role")
}

func TestSeedServerDNSRefresh(t *testing.T) {
	a, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer a.Close()
	b, _ := newTestSyncer(t, syncer.WithSeedMode(nil))
	defer b.Close()
	r := new(seedResolver)
	r.set("192.0.2.1", "192.0.2.2")
	d := &seedDialer{targets: map[string]string{"192.0.2.2:19771": a.Addr(), "192.0.2.3:19771": b.Addr()}, attempts: make(map[string]int)}
	c, _ := newTestSyncer(t, syncer.WithSeedMode([]string{"seed.example.invalid:19771"}),
		syncer.WithPeerResolver(r), syncer.WithDialer(d), syncer.WithSeedDNSRefresh(40*time.Millisecond),
		syncer.WithPeerDiscoveryInterval(30*time.Millisecond), syncer.WithConnectTimeout(200*time.Millisecond))
	defer c.Close()
	waitBootstrap(t, 5*time.Second, func() bool { return len(c.Peers()) == 1 && d.attempted("192.0.2.2:19771") }, "seed server did not connect through a DNS pool")
	r.set("192.0.2.3")
	a.Close()
	waitBootstrap(t, 5*time.Second, func() bool { return len(c.Peers()) == 1 && d.attempted("192.0.2.3:19771") }, "seed server did not refresh its persistent peer DNS")
}
