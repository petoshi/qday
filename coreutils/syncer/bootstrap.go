package syncer

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"
	"lukechampine.com/frand"
)

// WithBootstrapPeers enables temporary seed connections. Saved ordinary peers
// are tried first. Seeds remain available when ordinary connections fail.
// Nodes providing a public seed service should use persistent peers instead.
func WithBootstrapPeers(peers []string) Option {
	return func(c *config) { c.BootstrapPeers = slices.Clone(peers) }
}

// WithSeedMode reserves outbound slots for the configured server peers and
// maintains up to eight outgoing links to ordinary nodes. These links keep the
// seed current even after clients release their initial bootstrap connections.
func WithSeedMode(peers []string) Option {
	return func(c *config) {
		c.SeedMode = true
		c.SeedPeers = slices.Clone(peers)
		c.MaxOutboundPeers = 8 + len(peers)
	}
}

// WithBootstrapRelease sets the minimum ordinary peer count and uninterrupted
// synchronization period before releasing seeds (defaults: 2 peers, 30 seconds).
// A fresh successful RPC is also required from each replacement connection.
func WithBootstrapRelease(peers int, stability time.Duration) Option {
	return func(c *config) {
		c.BootstrapMinPeers = max(1, peers)
		c.BootstrapStability = max(0, stability)
	}
}

// IsBootstrap reports whether a connection belongs to a configured seed. DNS
// aliases are tracked locally; a remote node cannot assign itself this role.
func (s *Syncer) IsBootstrap(p *Peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bootstrap[p.Addr()] || s.bootstrap[p.dialAddr]
}

// IPResolver resolves every A/AAAA answer for a seed name. Each resulting
// endpoint receives its own connection attempt, handshake and retry backoff.
type IPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

func WithPeerResolver(r IPResolver) Option {
	return func(c *config) { c.Resolver = r }
}

// WithSeedDNSRefresh overrides the five-minute DNS refresh interval.
func WithSeedDNSRefresh(d time.Duration) Option {
	return func(c *config) { c.SeedDNSRefresh = max(10*time.Millisecond, d) }
}

func (s *Syncer) lookupPeerIPs(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	return s.config.Resolver.LookupIPAddr(ctx, host)
}

// refreshSeedAddresses retains the last successful answers during DNS errors.
// Historical aliases keep their seed role, but only current answers are dialed
// as seeds. DNS I/O never runs while holding the peer mutex.
func (s *Syncer) refreshSeedAddresses(ctx context.Context) time.Time {
	names := append(slices.Clone(s.config.BootstrapPeers), s.config.SeedPeers...)
	slices.Sort(names)
	names = slices.Compact(names)
	type result struct {
		name      string
		addresses []string
		err       error
	}
	results := make(chan result, len(names))
	for _, name := range names {
		go func() {
			r := result{name: name}
			host, port, err := net.SplitHostPort(name)
			if err == nil {
				lookupCtx, cancel := context.WithTimeout(ctx, s.config.ConnectTimeout)
				ips, lookupErr := s.lookupPeerIPs(lookupCtx, host)
				cancel()
				err = lookupErr
				for _, ip := range ips {
					if ip.IP.To16() != nil {
						r.addresses = append(r.addresses, net.JoinHostPort(ip.String(), port))
					}
				}
				slices.Sort(r.addresses)
				r.addresses = slices.Compact(r.addresses)
			}
			if err == nil && len(r.addresses) == 0 {
				err = errors.New("seed resolved to no IP addresses")
			}
			r.err = err
			results <- r
		}()
	}
	retry := s.config.SeedDNSRefresh
	for range names {
		r := <-results
		if r.err != nil {
			retry = min(retry, 30*time.Second)
			s.log.Debug("seed DNS lookup failed", zap.String("seed", r.name), zap.Error(r.err))
			continue
		}
		s.mu.Lock()
		s.seedAddresses[r.name] = r.addresses
		for _, addr := range r.addresses {
			if slices.Contains(s.config.BootstrapPeers, r.name) {
				s.bootstrap[addr] = true
			}
			if slices.Contains(s.config.SeedPeers, r.name) {
				s.serverPeers[addr] = true
			}
		}
		s.mu.Unlock()
	}
	return time.Now().Add(retry)
}

func (s *Syncer) seedCandidates(names []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var addresses []string
	for _, name := range names {
		addresses = append(addresses, s.seedAddresses[name]...)
	}
	slices.Sort(addresses)
	addresses = slices.Compact(addresses)
	frand.Shuffle(len(addresses), func(i, j int) { addresses[i], addresses[j] = addresses[j], addresses[i] })
	return addresses
}

func (s *Syncer) rememberNodes(nodes []string) {
	for _, addr := range nodes {
		if validatePeer(addr) == nil {
			if err := s.pm.AddPeer(addr); err != nil {
				s.log.Debug("failed to remember peer", zap.Error(err))
			}
		}
	}
}

func (s *Syncer) bootstrapPeerLoop(ctx context.Context) error {
	lastTried := make(map[string]time.Time)
	stableSince := make(map[*Peer]time.Time)
	var nextResolve time.Time
	detached := false

	// Probe in parallel so one unresponsive peer cannot delay all the others.
	probe := func(peers []*Peer) []*Peer {
		results := make(chan *Peer, len(peers))
		for _, p := range peers {
			go func() {
				nodes, err := p.ShareNodes(s.config.ShareNodesTimeout)
				if err != nil {
					p.setErr(err)
					results <- nil
				} else {
					s.rememberNodes(nodes)
					results <- p
				}
			}()
		}
		var working []*Peer
		for range peers {
			if p := <-results; p != nil && p.Err() == nil && p.Synced() {
				working = append(working, p)
			}
		}
		return working
	}

	ticker := time.NewTicker(s.config.PeerDiscoveryInterval)
	defer ticker.Stop()
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if !time.Now().Before(nextResolve) {
			nextResolve = s.refreshSeedAddresses(ctx)
		}

		connected := s.Peers()
		var stable, seeds []*Peer
		current := make(map[*Peer]bool)
		for _, p := range connected {
			if s.IsBootstrap(p) {
				seeds = append(seeds, p)
			} else if p.Err() == nil && p.Synced() {
				current[p] = true
				if stableSince[p].IsZero() {
					stableSince[p] = time.Now()
				}
				if time.Since(stableSince[p]) >= s.config.BootstrapStability {
					stable = append(stable, p)
				}
			}
		}
		for p := range stableSince {
			if !current[p] {
				delete(stableSince, p)
			}
		}
		// Prefer outbound connections: inbound peers alone must not displace
		// existing outbound peers in selecting the replacement connections.
		slices.SortStableFunc(stable, func(a, b *Peer) int {
			if a.Inbound == b.Inbound {
				return 0
			}
			if a.Inbound {
				return 1
			}
			return -1
		})
		working := []*Peer(nil)
		if len(stable) >= s.config.BootstrapMinPeers {
			working = probe(stable[:s.config.BootstrapMinPeers])
		}
		if len(working) >= s.config.BootstrapMinPeers {
			// Recheck after the RPCs: a replacement might have disconnected or
			// started syncing a new tip while another RPC was in flight.
			ready := true
			for _, p := range working {
				ready = ready && p.Err() == nil && p.Synced()
			}
			if ready {
				released := 0
				for _, p := range seeds {
					// A seed maintains its own small mesh of ordinary nodes.
					// Releasing only connections we initiated preserves those
					// inbound links and prevents isolating the seed network.
					if !p.Inbound {
						p.Close()
						released++
					}
				}
				if released != 0 {
					s.log.Info("released bootstrap connections", zap.Int("ordinaryPeers", len(working)))
				}
				detached = true
			} else {
				detached = false
			}
		} else {
			if detached {
				// A deliberately released seed is immediately eligible on loss
				// of connectivity; failed attempts still have a retry backoff.
				for addr := range lastTried {
					s.mu.Lock()
					seed := s.bootstrap[addr]
					s.mu.Unlock()
					if seed {
						delete(lastTried, addr)
					}
				}
			}
			detached = false
		}

		// Discovery continues even with a full outbound set, allowing ordinary
		// peers to replace seeds and keeping fallback addresses current.
		connected = s.Peers()
		frand.Shuffle(len(connected), func(i, j int) { connected[i], connected[j] = connected[j], connected[i] })
		probe(connected[:min(3, len(connected))])

		candidates, err := s.pm.Peers()
		if err != nil {
			return err
		}
		frand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
		// Give previously successful addresses a first chance on restart.
		slices.SortStableFunc(candidates, func(a, b PeerInfo) int {
			if a.LastConnect.IsZero() == b.LastConnect.IsZero() {
				return 0
			}
			if a.LastConnect.IsZero() {
				return 1
			}
			return -1
		})
		var ordinary []string
		for _, info := range candidates {
			s.mu.Lock()
			seed := s.bootstrap[info.Address]
			s.mu.Unlock()
			if !seed {
				ordinary = append(ordinary, info.Address)
			}
		}
		dial := func(addresses []string, retry time.Duration, seedBatch bool) {
			var selected []string
			s.mu.Lock()
			outbound, seedOutbound := 0, 0
			active := make(map[string]bool)
			for _, p := range s.peers {
				if p.Err() == nil {
					if !p.Inbound {
						outbound++
						if s.bootstrap[p.Addr()] || s.bootstrap[p.dialAddr] {
							seedOutbound++
						}
					}
					active[p.Addr()], active[p.dialAddr] = true, true
				}
			}
			s.mu.Unlock()
			limit := min(4, s.config.MaxOutboundPeers-outbound)
			if seedBatch {
				// Large DNS pools must leave room for ordinary connections.
				seedLimit := min(3, max(1, s.config.MaxOutboundPeers-s.config.BootstrapMinPeers))
				limit = min(limit, seedLimit-seedOutbound)
			}
			for _, addr := range addresses {
				if len(selected) >= limit {
					break
				}
				if validatePeer(addr) != nil || active[addr] || time.Since(lastTried[addr]) < retry {
					continue
				}
				lastTried[addr] = time.Now()
				selected = append(selected, addr)
			}
			var wg sync.WaitGroup
			for _, addr := range selected {
				wg.Go(func() {
					dialCtx, cancel := context.WithTimeout(ctx, s.config.ConnectTimeout)
					defer cancel()
					if s.allowConnect(dialCtx, addr, false) == nil {
						s.Connect(dialCtx, addr)
					}
				})
			}
			wg.Wait()
		}
		dial(ordinary, 5*time.Minute, false)
		if !detached {
			dial(s.seedCandidates(s.config.BootstrapPeers), 30*time.Second, true)
		}
	}
}
