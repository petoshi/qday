package qday

import (
	"context"
	"net"
	"testing"
	"time"

	"go.sia.tech/core/gateway"
	"go.sia.tech/coreutils/syncer"
	"go.sia.tech/coreutils/testutil"
	seedwallet "go.sia.tech/coreutils/wallet"
)

// The soluble canary exists only in internal consensus fixtures. The shipped
// node rejects this manifest; its real mainnet canary has no known witness.
func TestProofRelayWithQdayWire(t *testing.T) {
	a, b := newTestService(t), newTestService(t)
	for _, s := range []*Service{a, b} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		s.Syncer = syncer.New(l, s.CM, testutil.NewEphemeralPeerStore(), gateway.Header{GenesisID: s.Manifest.Genesis.ID(), UniqueID: gateway.GenerateUniqueID(), NetAddress: l.Addr().String()}, syncer.WithSyncInterval(30*time.Millisecond))
		go s.Syncer.Run()
		t.Cleanup(func() { s.Syncer.Close() })
	}
	if _, err := a.Syncer.Connect(context.Background(), b.Syncer.Addr()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create(context.Background(), "temporary-test-passphrase", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, a)
	id, err := a.PublishProof(context.Background(), [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("QDAY proof relay did not converge")
	}
	wait(func() bool { return b.pendingProof() == id.String() })
	if b.CM.TipState().QdayHeight != 0 {
		t.Fatal("mempool proof activated QDAY")
	}
	block := mineForTest(t, a)
	if err := a.Syncer.BroadcastV2BlockOutline(gateway.OutlineBlock(block, nil, nil)); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		return b.CM.TipState().QdayHeight == a.CM.TipState().QdayHeight && b.CM.TipState().QdayHeight != 0
	})
	block = mineForTest(t, a)
	if err := a.Syncer.BroadcastV2BlockOutline(gateway.OutlineBlock(block, nil, nil)); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { cs := b.CM.TipState(); return cs.QdayActive(cs.Index.Height) })
}
