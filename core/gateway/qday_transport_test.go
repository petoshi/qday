package gateway

import (
	"net"
	"testing"
	"time"

	"go.sia.tech/core/types"
)

func TestQdayWireIsolation(t *testing.T) {
	qday := Header{ProtocolMagic: QdayMagic(), GenesisID: types.BlockID{1}, UniqueID: GenerateUniqueID(), NetAddress: "127.0.0.1:19771"}
	for _, tc := range []struct {
		name    string
		magic   [8]byte
		genesis types.BlockID
		ok      bool
	}{
		{"qday", QdayMagic(), qday.GenesisID, true},
		{"sia", [8]byte{}, qday.GenesisID, false},
		{"wrong-magic", [8]byte{1, 2, 3}, qday.GenesisID, false},
		{"different-genesis", QdayMagic(), types.BlockID{2}, false},
	} {
		for _, reverse := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/dial", true: "/accept"}[reverse], func(t *testing.T) {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				other := Header{ProtocolMagic: tc.magic, GenesisID: tc.genesis, UniqueID: GenerateUniqueID(), NetAddress: "127.0.0.1:29771"}
				dialHeader, acceptHeader := qday, other
				if reverse {
					dialHeader, acceptHeader = other, qday
				}
				done := make(chan error, 1)
				go func() {
					c, err := l.Accept()
					if err != nil {
						done <- err
						return
					}
					defer c.Close()
					c.SetDeadline(time.Now().Add(time.Second))
					transport, err := Accept(c, acceptHeader)
					if transport != nil {
						defer transport.Close()
					}
					done <- err
				}()
				c, err := net.DialTimeout("tcp", l.Addr().String(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				c.SetDeadline(time.Now().Add(time.Second))
				transport, err := Dial(c, dialHeader)
				if transport != nil {
					transport.Close()
				}
				c.Close()
				acceptErr := <-done
				if tc.ok && (err != nil || acceptErr != nil) {
					t.Fatalf("QDAY peers failed: %v / %v", err, acceptErr)
				}
				if !tc.ok && (err == nil || acceptErr == nil) {
					t.Fatalf("foreign network accepted: %v / %v", err, acceptErr)
				}
			})
		}
	}
}
