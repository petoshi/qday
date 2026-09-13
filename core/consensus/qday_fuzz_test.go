package consensus

import (
	"bytes"
	"testing"

	"go.sia.tech/core/types"
)

func FuzzQdayEnvelope(f *testing.F) {
	f.Add((QdayEnvelope{Kind: QdayTransfer}).Encode())
	f.Add((QdayEnvelope{Kind: QdayCanaryProof, Witness: [32]byte{1}}).Encode())
	f.Add((QdayEnvelope{Kind: QdayCoinbase, Keys: []types.QdayKeys{{Classical: types.PublicKey{1}, Reserve: types.Hash256{2}}}}).Encode())
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 16384 {
			return
		}
		envelope, err := ParseQdayEnvelope(data)
		if err != nil {
			return
		}
		if !bytes.Equal(envelope.Encode(), data) {
			t.Fatal("decoder accepted a non-canonical QDAY envelope")
		}
	})
}
