package types

import (
	"bytes"
	"crypto/sha512"
	"errors"
	"fmt"

	"github.com/cloudflare/circl/sign/slhdsa"
)

// PolicyTypeSLHDSA is a FIPS 205 SLH-DSA-SHA2-128s public key.
type PolicyTypeSLHDSA [32]byte

func (PolicyTypeSLHDSA) isPolicy() {}

func PolicySLHDSA(pk [32]byte) SpendPolicy { return SpendPolicy{PolicyTypeSLHDSA(pk)} }

const QdaySignatureSize = 7856
const QdaySignatureChunks = (QdaySignatureSize + 31) / 32

var qdaySignatureContext = []byte("QDAY/reserve/v1")

// QdayKeys commits both independent keys into every native address. Requiring
// the reserve from genesis also covers an attacker concealing a canary solution.
type QdayKeys struct {
	Classical PublicKey `json:"classical"`
	Reserve   [32]byte  `json:"reserve"`
}

func (k QdayKeys) Policy() SpendPolicy {
	return PolicyThreshold(2, []SpendPolicy{PolicyPublicKey(k.Classical), PolicySLHDSA(k.Reserve)})
}

func (k QdayKeys) Bytes() []byte { return append(append([]byte{}, k.Classical[:]...), k.Reserve[:]...) }

// Address commits both keys and the requirement to satisfy both policies.
func (k QdayKeys) Address() QdayAddress { return QdayAddress(k.Policy().Address()) }

func (k QdayKeys) String() string { return k.Address().String() }

func (k QdayKeys) Validate() error {
	if k.Classical == (PublicKey{}) || k.Reserve == ([32]byte{}) {
		return errors.New("zero QDAY public key")
	}
	return nil
}

type QdayPrivateKeys struct {
	Classical PrivateKey
	Reserve   slhdsa.PrivateKey
	Public    QdayKeys
}

// QdayKeysFromSeed derives independent keys with distinct domain separators.
// The seed must have at least 256 bits of entropy and must be backed up.
func QdayKeysFromSeed(seed [32]byte) (k QdayPrivateKeys, err error) {
	ed := sha512.Sum512(append([]byte("QDAY/ed25519/v1"), seed[:]...))
	pq := sha512.Sum512(append([]byte("QDAY/slhdsa/v1"), seed[:]...))
	k.Classical = NewPrivateKeyFromSeed(ed[:32])
	k.Public.Classical = k.Classical.PublicKey()
	pub, priv, err := slhdsa.GenerateKey(bytes.NewReader(pq[:]), slhdsa.SHA2_128s)
	if err != nil {
		return k, err
	}
	k.Reserve = priv
	b, err := pub.MarshalBinary()
	if err != nil {
		return k, err
	}
	copy(k.Public.Reserve[:], b)
	return k, nil
}

func (k *QdayPrivateKeys) Sign(h Hash256) (SatisfiedPolicy, error) {
	sig, err := slhdsa.SignDeterministic(&k.Reserve, slhdsa.NewMessage(h[:]), qdaySignatureContext)
	if err != nil {
		return SatisfiedPolicy{}, err
	}
	if len(sig) != QdaySignatureSize {
		return SatisfiedPolicy{}, fmt.Errorf("unexpected SLH-DSA signature length %d", len(sig))
	}
	chunks := make([][32]byte, QdaySignatureChunks)
	for i := range chunks {
		copy(chunks[i][:], sig[min(i*32, len(sig)):min((i+1)*32, len(sig))])
	}
	return SatisfiedPolicy{Policy: k.Public.Policy(), Signatures: []Signature{k.Classical.SignHash(h)}, Preimages: chunks}, nil
}

func verifyQdayReserve(pk [32]byte, h Hash256, chunks [][32]byte) bool {
	if len(chunks) != QdaySignatureChunks {
		return false
	}
	buf := make([]byte, 0, QdaySignatureChunks*32)
	for _, c := range chunks {
		buf = append(buf, c[:]...)
	}
	for _, b := range buf[QdaySignatureSize:] {
		if b != 0 {
			return false
		}
	}
	pub := slhdsa.PublicKey{ID: slhdsa.SHA2_128s}
	return pub.UnmarshalBinary(pk[:]) == nil && slhdsa.Verify(&pub, slhdsa.NewMessage(h[:]), buf[:QdaySignatureSize], qdaySignatureContext)
}
