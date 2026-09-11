package qday

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"
)

// AddPeer remembers a manually entered IP:port and attempts a QDAY handshake.
// An unavailable peer stays in the normal discovery pool for later attempts.
func (s *Service) AddPeer(ctx context.Context, address string) (map[string]any, error) {
	peer, err := netip.ParseAddrPort(strings.TrimSpace(address))
	if err != nil || peer.Port() == 0 || peer.Addr().IsUnspecified() || peer.Addr().IsMulticast() || peer.Addr().Zone() != "" {
		return nil, errors.New("enter an IP address and TCP port, for example 203.0.113.10:19771 or [2001:db8::10]:19771")
	}
	if s.Syncer == nil {
		return nil, errors.New("the P2P service is unavailable")
	}
	address = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port()).String()
	if err := s.Syncer.RememberPeer(address); err != nil {
		return nil, err
	}
	result := map[string]any{"address": address, "saved": true, "connected": true}
	connected := func() bool {
		for _, p := range s.Syncer.Peers() {
			if p.Err() == nil && (p.ConnAddr == address || p.Addr() == address) {
				return true
			}
		}
		return false
	}
	if connected() {
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.Syncer.Connect(ctx, address); err != nil && !connected() {
		result["connected"] = false
		result["connectionError"] = err.Error()
	}
	return result, nil
}
