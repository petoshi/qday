package main

import (
	"crypto/ed25519"
	"crypto/sha512"
	"testing"

	"filippo.io/edwards25519"
)

// TestScalarCanProduceStandardEd25519Signature demonstrates the exact
// consequence used by FIND X: knowledge of a canonical scalar x satisfying
// x*G = C is sufficient to construct a standard Ed25519 signature that
// verifies against the compressed public point C.
func TestScalarCanProduceStandardEd25519Signature(t *testing.T) {
	var scalarBytes [32]byte
	scalarBytes[0] = 42
	x, err := new(edwards25519.Scalar).SetCanonicalBytes(scalarBytes[:])
	if err != nil {
		t.Fatal(err)
	}
	publicPoint := new(edwards25519.Point).ScalarBaseMult(x)
	publicKey := ed25519.PublicKey(publicPoint.Bytes())
	message := []byte("FIND X scalar signing test")

	nonceInput := make([]byte, 0, len(scalarBytes)+len(message)+32)
	nonceInput = append(nonceInput, "FINDX/Ed25519/scalar-signing-test/v1/"...)
	nonceInput = append(nonceInput, scalarBytes[:]...)
	nonceInput = append(nonceInput, message...)
	nonceDigest := sha512.Sum512(nonceInput)
	r, err := new(edwards25519.Scalar).SetUniformBytes(nonceDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	rPoint := new(edwards25519.Point).ScalarBaseMult(r)

	challengeInput := make([]byte, 0, 32+32+len(message))
	challengeInput = append(challengeInput, rPoint.Bytes()...)
	challengeInput = append(challengeInput, publicKey...)
	challengeInput = append(challengeInput, message...)
	challengeDigest := sha512.Sum512(challengeInput)
	k, err := new(edwards25519.Scalar).SetUniformBytes(challengeDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	s := new(edwards25519.Scalar).MultiplyAdd(k, x, r)

	signature := make([]byte, 0, ed25519.SignatureSize)
	signature = append(signature, rPoint.Bytes()...)
	signature = append(signature, s.Bytes()...)
	if !ed25519.Verify(publicKey, message, signature) {
		t.Fatal("signature constructed from the raw scalar did not verify")
	}
}
