package syncer_test

import (
	"context"
	"net"
	"testing"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/qday"
	"go.sia.tech/coreutils/syncer"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

func TestRelayPayloadBudgetKeepsControlRPCsAvailable(t *testing.T) {
	for _, peers := range []int{1, 4} {
		t.Run(map[int]string{1: "per-peer", 4: "global"}[peers], func(t *testing.T) {
			sy, cm, manifest := newQdayTestSyncer(t, syncer.WithLogger(zaptest.NewLogger(t, zaptest.Level(zap.DebugLevel))))
			keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
			if err != nil {
				t.Fatal(err)
			}
			_, au := consensus.ApplyBlock(manifest.Network.GenesisState(), manifest.Genesis, consensus.V1BlockSupplement{Transactions: make([]consensus.V1TransactionSupplement, 1)}, time.Time{})
			var parent types.SiacoinElement
			for _, d := range au.SiacoinElementDiffs() {
				if d.SiacoinElement.ID == manifest.Genesis.Transactions[0].SiacoinOutputID(0) {
					parent = d.SiacoinElement.Copy()
				}
			}
			txn := types.V2Transaction{SiacoinInputs: []types.V2SiacoinInput{{Parent: parent}}, SiacoinOutputs: []types.SiacoinOutput{{Value: parent.SiacoinOutput.Value, Address: keys.Public.Policy().Address()}}, ArbitraryData: (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()}
			if err := qday.SignTransfer(context.Background(), cm.TipState(), &txn, &keys); err != nil {
				t.Fatal(err)
			}
			dial := func() *gateway.Transport {
				t.Helper()
				conn, err := net.DialTimeout("tcp", sy.Addr(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				transport, err := gateway.Dial(conn, gateway.Header{GenesisID: manifest.Genesis.ID(), UniqueID: gateway.GenerateUniqueID(), NetAddress: conn.LocalAddr().String(), ProtocolMagic: gateway.QdayMagic()})
				if err != nil {
					conn.Close()
					t.Fatal(err)
				}
				conn.SetDeadline(time.Time{})
				t.Cleanup(func() { transport.Close() })
				// Drain requests initiated by the node. An unread incoming mux
				// stream can otherwise block this test client's entire reader.
				go func() {
					for {
						stream, err := transport.AcceptStream()
						if err != nil {
							return
						}
						go func() {
							defer stream.Close()
							stream.SetDeadline(time.Now().Add(time.Second))
							id, err := stream.ReadID()
							if err != nil {
								return
							}
							r := gateway.ObjectForID(id)
							if r != nil && stream.ReadRequest(r) == nil {
								stream.WriteResponse(r)
							}
						}()
					}
				}()
				return transport
			}
			var stalled []*gateway.Stream
			var sender *gateway.Transport
			for range peers {
				sender = dial()
				for range 2 {
					stream, err := sender.DialStream()
					if err != nil {
						t.Fatal(err)
					}
					if err := stream.WriteID(&gateway.RPCRelayV2TransactionSet{}); err != nil {
						t.Fatal(err)
					}
					stalled = append(stalled, stream)
				}
			}
			if peers == 4 {
				sender = dial() // a fresh identity cannot bypass the global cap
			}
			// The incomplete payloads are sent first on established connections.
			time.Sleep(100 * time.Millisecond)
			relay := func() {
				t.Helper()
				stream, err := sender.DialStream()
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				stream.SetDeadline(time.Now().Add(time.Second))
				r := &gateway.RPCRelayV2TransactionSet{Index: cm.Tip(), Transactions: []types.V2Transaction{txn}}
				if stream.WriteID(r) == nil {
					stream.WriteRequest(r) // saturation may close this stream
				}
			}
			relay()
			time.Sleep(250 * time.Millisecond)
			if len(cm.V2PoolTransactions()) != 0 {
				t.Fatal("large payload bypassed the saturated receive budget")
			}
			control, err := sender.DialStream()
			if err != nil {
				t.Fatal(err)
			}
			control.SetDeadline(time.Now().Add(time.Second))
			r := &gateway.RPCShareNodes{}
			if err := control.WriteID(r); err != nil {
				t.Fatal(err)
			} else if err := control.WriteRequest(r); err != nil {
				t.Fatal(err)
			} else if err := control.ReadResponse(r); err != nil {
				t.Fatal("payload saturation blocked control traffic:", err)
			}
			control.Close()
			for _, stream := range stalled {
				stream.Close()
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				relay()
				time.Sleep(25 * time.Millisecond)
				if _, ok := cm.V2PoolTransaction(txn.ID()); ok {
					return
				}
			}
			t.Fatal("closing incomplete streams did not release the payload budget")
		})
	}
}
