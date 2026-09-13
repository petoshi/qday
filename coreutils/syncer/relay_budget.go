package syncer

import "sync"

// Large unsolicited payloads must not scale with the connection limit. QDAY
// witnesses are several KiB per input, before decoded objects and proofs.
const (
	maxRelayPayloads     = 8
	maxPeerRelayPayloads = 2
)

type relayPayloadBudget struct {
	mu     sync.Mutex
	active int
	peers  map[*Peer]int
}

func (b *relayPayloadBudget) acquire(p *Peer) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active >= maxRelayPayloads || b.peers[p] >= maxPeerRelayPayloads {
		return false
	}
	if b.peers == nil {
		b.peers = make(map[*Peer]int)
	}
	b.peers[p]++
	b.active++
	return true
}

func (b *relayPayloadBudget) release(p *Peer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active--
	b.peers[p]--
	if b.peers[p] == 0 {
		delete(b.peers, p)
	}
}
