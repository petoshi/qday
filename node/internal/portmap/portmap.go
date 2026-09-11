// Package portmap maintains an owned UPnP mapping for the native P2P listener.
package portmap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"lukechampine.com/upnp"
)

// Status describes router configuration, not a verified external connection.
type Status struct {
	State           string `json:"state"`
	Port            uint16 `json:"port"`
	ExternalAddress string `json:"externalAddress,omitempty"`
	PublicIP        bool   `json:"publicIP"`
	Detail          string `json:"detail"`
}

// Manager owns the lifecycle of a single TCP port mapping.
type Manager struct {
	mu     sync.Mutex
	status Status
	cancel context.CancelFunc
	done   chan struct{}
}

type router interface {
	InternalIP() string
	Location() string
	Mapping(context.Context, uint16) (upnp.PortMapping, error)
	Forward(context.Context, uint16, string, uint32) error
	Clear(context.Context, uint16) error
	ExternalIP(context.Context) (string, error)
}

type device struct{ upnp.Device }

func (d device) Mapping(ctx context.Context, port uint16) (upnp.PortMapping, error) {
	return d.WithContext(ctx).Mapping(port, "TCP")
}
func (d device) Forward(ctx context.Context, port uint16, owner string, lease uint32) error {
	return d.WithContext(ctx).ForwardLease(port, "TCP", owner, lease)
}
func (d device) Clear(ctx context.Context, port uint16) error {
	return d.WithContext(ctx).Clear(port, "TCP")
}
func (d device) ExternalIP(ctx context.Context) (string, error) {
	return d.WithContext(ctx).ExternalIP()
}

func discover(ctx context.Context) (router, error) {
	d, err := upnp.Discover(ctx)
	return device{d}, err
}

// Start attempts mapping asynchronously. Only a wildcard IPv4-capable or
// private IPv4 listener is mapped. The HTTP wallet is never passed here.
func Start(ctx context.Context, addr net.Addr, owner string, enabled bool) *Manager {
	host, portString, _ := net.SplitHostPort(addr.String())
	port, _ := strconv.ParseUint(portString, 10, 16)
	ip := net.ParseIP(host)
	initial := Status{State: "discovering", Port: uint16(port), Detail: "Looking for a UPnP router."}
	if !enabled {
		initial.State, initial.Detail = "disabled", "Automatic port mapping is disabled. Outbound connections remain available."
	} else if ip == nil || ip.IsLoopback() || (!ip.IsUnspecified() && (!ip.IsPrivate() || ip.To4() == nil)) {
		initial.State, initial.Detail = "not-needed", "This listener does not use automatic IPv4 router mapping."
	}
	m := &Manager{status: initial, done: make(chan struct{})}
	ctx, m.cancel = context.WithCancel(ctx)
	if initial.State != "discovering" {
		close(m.done)
	} else {
		go m.run(ctx, owner, discover, 15*time.Minute, time.Minute)
	}
	return m
}

// Status returns a snapshot safe to expose in the local wallet.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) setStatus(state, detail, external string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State, m.status.Detail, m.status.ExternalAddress = state, detail, external
	host, _, _ := net.SplitHostPort(external)
	m.status.PublicIP = publicIP(host)
}

func publicIP(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Is4() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

func hasCode(err error, code int) bool {
	var e *upnp.Error
	return errors.As(err, &e) && e.Code == code
}

func owns(rule upnp.PortMapping, d router, port uint16, owner string) bool {
	return rule.NewInternalClient == d.InternalIP() && rule.NewInternalPort == port && rule.NewPortMappingDescription == owner
}

func (m *Manager) ensure(ctx context.Context, d router, owner string) (owned bool, err error) {
	port := m.Status().Port
	rule, err := d.Mapping(ctx, port)
	if err != nil && !hasCode(err, 714) {
		return false, fmt.Errorf("could not inspect router mapping: %w", err)
	} else if err == nil && !owns(rule, d, port, owner) {
		// A manually configured rule for this listener is usable, but never
		// renewed or deleted by QDAY. Other destinations are a conflict.
		if !rule.NewEnabled || rule.NewInternalClient != d.InternalIP() || rule.NewInternalPort != port {
			return false, errors.New("P2P port is already mapped to another destination; the existing rule was preserved")
		}
	} else {
		err = d.Forward(ctx, port, owner, 3600)
		if hasCode(err, 725) {
			// Legacy IGDs may support only permanent leases. Ownership checks
			// still apply to renewal and graceful shutdown cleanup.
			err = d.Forward(ctx, port, owner, 0)
		}
		if err != nil {
			return false, err
		}
		owned = true // retain cleanup responsibility if verification fails
		rule, err = d.Mapping(ctx, port)
		if err != nil {
			return owned, err
		}
		if !rule.NewEnabled || !owns(rule, d, port, owner) {
			return owned, errors.New("router did not confirm the requested P2P mapping")
		}
	}
	ip, err := d.ExternalIP(ctx)
	if err != nil {
		return owned, err
	}
	external := net.JoinHostPort(ip, strconv.Itoa(int(port)))
	if publicIP(ip) {
		m.setStatus("mapped", "Router mapping is active. External reachability has not been verified.", external)
	} else {
		m.setStatus("upstream-nat", "Router mapping is active, but its WAN address is not public. Upstream NAT may still block incoming connections.", external)
	}
	return owned, nil
}

func (m *Manager) cleanup(d router, owner string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m.cleanupContext(ctx, d, owner)
}

func (m *Manager) cleanupContext(ctx context.Context, d router, owner string) {
	port := m.Status().Port
	if rule, err := d.Mapping(ctx, port); err == nil && owns(rule, d, port, owner) {
		d.Clear(ctx, port)
	}
}

func (m *Manager) run(ctx context.Context, owner string, find func(context.Context) (router, error), refresh, retry time.Duration) {
	defer close(m.done)
	// A router can create a rule even if its HTTP response is lost. Remember
	// every attempted router and recheck ownership during bounded cleanup.
	attempted := make(map[string]router)
	var last router
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if last != nil {
			m.cleanupContext(cleanupCtx, last, owner)
		}
		for _, previous := range attempted {
			if last == nil || previous.Location() != last.Location() || previous.InternalIP() != last.InternalIP() {
				m.cleanupContext(cleanupCtx, previous, owner)
			}
		}
	}()
	var d router
	for {
		operation, cancel := context.WithTimeout(ctx, 5*time.Second)
		var err error
		if d == nil {
			d, err = find(operation)
		}
		if err == nil {
			last = d
			attempted[d.Location()+"/"+d.InternalIP()] = d
			_, err = m.ensure(operation, d, owner)
		}
		cancel()
		delay := refresh
		if err != nil {
			m.setStatus("unavailable", "Automatic port mapping is unavailable. Outbound connections remain available. "+err.Error(), "")
			d, delay = nil, retry
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Close stops maintenance and removes only a mapping still owned by this node.
func (m *Manager) Close() { m.cancel(); <-m.done }
