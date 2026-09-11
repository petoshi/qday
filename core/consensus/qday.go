package consensus

import (
	"bytes"
	"encoding/binary"
	"errors"

	"filippo.io/edwards25519"
	"go.sia.tech/core/blake2b"
	"go.sia.tech/core/types"
)

// QdayParams are immutable consensus parameters, committed by the genesis
// manifest. All heights are block heights, never local wall-clock timers.
type QdayParams struct {
	RulesVersion    uint8          `json:"rulesVersion"`
	Domain          types.Hash256  `json:"domain"`
	Canary          [32]byte       `json:"canary"`
	Reward          types.Currency `json:"reward"`
	ProofFee        types.Currency `json:"proofFee"`
	MiningBlocks    uint64         `json:"miningBlocks"`
	PremineAmount   types.Currency `json:"premineAmount"`
	ShieldBlocks    uint64         `json:"shieldBlocks"`
	DecayBlocks     uint64         `json:"decayBlocks"`
	DecayStep       uint64         `json:"decayStep"`
	ActivationDelay uint64         `json:"activationDelay"`
	DefendBits      uint8          `json:"defendBits"`
}

func (p QdayParams) Validate() error {
	if p.RulesVersion != 2 {
		return errors.New("unsupported QDAY consensus version")
	}
	if p.PremineAmount != types.Siacoins(500_000) {
		return errors.New("QDAY premine must be exactly 500000 coins")
	}
	if p.Domain == (types.Hash256{}) || p.Reward.IsZero() || p.ProofFee.IsZero() || p.MiningBlocks == 0 || p.MiningBlocks > 10_000_000 || p.ShieldBlocks == 0 || p.ShieldBlocks > 10_000_000 || p.DecayBlocks == 0 || p.DecayBlocks > 10_000_000 || p.DecayStep == 0 || p.DecayBlocks%p.DecayStep != 0 || p.ActivationDelay == 0 || p.ActivationDelay > 100_000 || p.DefendBits > 32 {
		return errors.New("invalid QDAY consensus parameters")
	}
	mined, overflow := p.Reward.Mul64WithOverflow(p.MiningBlocks)
	if overflow {
		return errors.New("QDAY emission overflows")
	}
	if _, overflow := mined.AddWithOverflow(p.PremineAmount); overflow {
		return errors.New("QDAY gross emission overflows")
	}
	point, err := new(edwards25519.Point).SetBytes(p.Canary[:])
	if err != nil || point.Equal(edwards25519.NewIdentityPoint()) == 1 {
		return errors.New("invalid canary point")
	}
	return nil
}

// QdayCanary transparently constructs a prime-order Edwards25519 point without
// choosing a scalar. Hashing a scalar and multiplying G would expose the answer.
// Recovering this point's discrete logarithm is the public QDAY challenge.
func QdayCanary() (out [32]byte) {
	for counter := uint64(0); ; counter++ {
		b := append([]byte("QDAY/Edwards25519/NUMS/canary/v1/"), make([]byte, 8)...)
		binary.LittleEndian.PutUint64(b[len(b)-8:], counter)
		h := blake2b.Sum256(b)
		p, err := new(edwards25519.Point).SetBytes(h[:])
		if err != nil || !bytes.Equal(p.Bytes(), h[:]) {
			continue
		}
		p.MultByCofactor(p)
		if p.Equal(edwards25519.NewIdentityPoint()) == 1 {
			continue
		}
		copy(out[:], p.Bytes())
		return
	}
}

func VerifyQdayProof(canary, witness [32]byte) bool {
	s, err := new(edwards25519.Scalar).SetCanonicalBytes(witness[:])
	return err == nil && s.Equal(edwards25519.NewScalar()) == 0 && bytes.Equal(new(edwards25519.Point).ScalarBaseMult(s).Bytes(), canary[:])
}

const (
	QdayTransfer    byte = 1
	QdayCoinbase    byte = 2
	QdayCanaryProof byte = 3
)

// QdayEnvelope uses fixed-width, bounded binary fields. Its nonce is committed
// by transaction signatures. Only coinbase markers include public keys;
// transfer destinations are policy hashes, revealed and authorized at spend.
type QdayEnvelope struct {
	Kind    byte
	Nonce   uint64
	Keys    []types.QdayKeys
	Witness [32]byte
}

func (e QdayEnvelope) Encode() []byte {
	b := append([]byte("QDAY\x02"), e.Kind)
	b = binary.LittleEndian.AppendUint64(b, e.Nonce)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(e.Keys)))
	for _, k := range e.Keys {
		b = append(b, k.Bytes()...)
	}
	if e.Kind == QdayCanaryProof {
		b = append(b, e.Witness[:]...)
	}
	return b
}

func ParseQdayEnvelope(b []byte) (e QdayEnvelope, err error) {
	if len(b) < 16 || !bytes.Equal(b[:5], []byte("QDAY\x02")) {
		return e, errors.New("missing QDAY envelope")
	}
	e.Kind = b[5]
	e.Nonce = binary.LittleEndian.Uint64(b[6:14])
	n := int(binary.LittleEndian.Uint16(b[14:16]))
	w := 0
	if e.Kind == QdayCanaryProof {
		w = 32
	}
	if e.Kind < QdayTransfer || e.Kind > QdayCanaryProof || n > 128 || len(b) != 16+64*n+w {
		return e, errors.New("invalid QDAY envelope")
	}
	e.Keys = make([]types.QdayKeys, n)
	for i := range e.Keys {
		copy(e.Keys[i].Classical[:], b[16+64*i:48+64*i])
		copy(e.Keys[i].Reserve[:], b[48+64*i:80+64*i])
		if err := e.Keys[i].Validate(); err != nil {
			return e, err
		}
	}
	if w != 0 {
		copy(e.Witness[:], b[len(b)-32:])
	}
	return e, nil
}

func (s State) QdayActive(height uint64) bool {
	return s.Network.Qday != nil && s.QdayHeight != 0 && height >= s.QdayHeight
}

// QdayValue is the spendable value after protocol decay. MaturityHeight is an
// authenticated output birth/maturity height, including for ordinary outputs.
// Lazy evaluation avoids a full UTXO scan; spent nominal value is never revived.
func (s State) QdayValue(e types.SiacoinElement, height uint64) types.Currency {
	if !s.QdayActive(height) {
		return e.SiacoinOutput.Value
	}
	p := s.Network.Qday
	start := max(s.QdayHeight, e.MaturityHeight)
	if height <= start || height-start <= p.ShieldBlocks {
		return e.SiacoinOutput.Value
	}
	age := (height - start - p.ShieldBlocks) / p.DecayStep * p.DecayStep
	if age >= p.DecayBlocks {
		return types.ZeroCurrency
	}
	remaining := p.DecayBlocks - age
	// Quotient and remainder avoid multiplication overflow, including max uint128.
	v := e.SiacoinOutput.Value
	q := v.Div64(p.DecayBlocks)
	r := v.Sub(q.Mul64(p.DecayBlocks))
	return q.Mul64(remaining).Add(r.Mul64(remaining).Div64(p.DecayBlocks))
}

func (s State) QdayUnits(height uint64) types.Currency {
	if s.QdayActive(height) {
		return types.HastingsPerSiacoin.Div64(1_000_000)
	}
	return types.HastingsPerSiacoin
}

// QdayWorkIntent excludes the nonce and signatures but binds every spend,
// destination, fee and network. Calculate once, then search the nonce cheaply.
func (s State) QdayWorkIntent(txn types.V2Transaction) types.Hash256 {
	txn.ArbitraryData = append([]byte(nil), txn.ArbitraryData...)
	if len(txn.ArbitraryData) >= 14 {
		clear(txn.ArbitraryData[6:14])
	}
	return hashAll("qday/defend/intent/v1", s.Network.Qday.Domain, txn.ID())
}

func QdayWorkValid(intent types.Hash256, nonce uint64, difficulty uint8) bool {
	var b [40]byte
	copy(b[:32], intent[:])
	binary.LittleEndian.PutUint64(b[32:], nonce)
	h := blake2b.Sum256(b[:])
	for i := uint8(0); i < difficulty; i++ {
		if h[i/8]&(1<<(7-i%8)) != 0 {
			return false
		}
	}
	return true
}

func qdayUnsupported(txn types.V2Transaction) bool {
	return len(txn.SiafundInputs)+len(txn.SiafundOutputs)+len(txn.FileContracts)+len(txn.FileContractRevisions)+len(txn.FileContractResolutions)+len(txn.Attestations) != 0 || txn.NewFoundationAddress != nil
}

func validateQdayTransaction(ms *MidState, txn types.V2Transaction) error {
	e, err := ParseQdayEnvelope(txn.ArbitraryData)
	if err != nil {
		return err
	}
	if qdayUnsupported(txn) {
		return errors.New("QDAY supports coin transfers only")
	}
	if e.Kind == QdayCoinbase {
		return errors.New("coinbase marker is only valid first in a block")
	}
	if e.Kind == QdayCanaryProof {
		if ms.base.QdayHeight != 0 || ms.qdayProved || e.Nonce != 0 || !VerifyQdayProof(ms.base.Network.Qday.Canary, e.Witness) {
			return errors.New("invalid or repeated QDAY proof")
		}
		if txn.MinerFee.Cmp(ms.base.Network.Qday.ProofFee) < 0 {
			return errors.New("QDAY proof does not pay the minimum network fee")
		}
	}
	// Proof fees spend real coins and require the same hybrid authorization as
	// transfers. Exact-fee proofs may have no change output.
	if len(txn.SiacoinInputs) == 0 || len(txn.SiacoinInputs) > 128 || (len(txn.SiacoinOutputs) == 0 && e.Kind != QdayCanaryProof) || len(txn.SiacoinOutputs) > 128 || len(e.Keys) != 0 {
		return errors.New("invalid QDAY transfer shape")
	}
	// Every spend reveals the policy committed by the parent output. Generic
	// validation checks its hash and both signatures; weaker policies cannot
	// spend native coins, even when supplied with otherwise valid signatures.
	for _, in := range txn.SiacoinInputs {
		p, ok := in.SatisfiedPolicy.Policy.Type.(types.PolicyTypeThreshold)
		if !ok || p.N != 2 || len(p.Of) != 2 {
			return errors.New("input must use native hybrid policy")
		}
		if _, ok := p.Of[0].Type.(types.PolicyTypePublicKey); !ok {
			return errors.New("missing classical policy")
		}
		if _, ok := p.Of[1].Type.(types.PolicyTypeSLHDSA); !ok {
			return errors.New("missing reserve policy")
		}
	}
	if ms.base.QdayActive(ms.base.childHeight()) && !QdayWorkValid(ms.base.QdayWorkIntent(txn), e.Nonce, ms.base.Network.Qday.DefendBits) {
		return errors.New("insufficient DEFEND work")
	}
	return nil
}

func validateQdayCoinbase(b types.Block) error {
	if b.V2 == nil || len(b.V2.Transactions) == 0 || len(b.Transactions) != 0 || len(b.MinerPayouts) != 1 {
		return errors.New("missing QDAY coinbase marker")
	}
	txn := b.V2.Transactions[0]
	e, err := ParseQdayEnvelope(txn.ArbitraryData)
	if err != nil || e.Kind != QdayCoinbase || len(e.Keys) != 1 || e.Keys[0].Policy().Address() != b.MinerPayouts[0].Address || len(txn.SiacoinInputs)+len(txn.SiacoinOutputs) != 0 || !txn.MinerFee.IsZero() || qdayUnsupported(txn) {
		return errors.New("invalid QDAY coinbase marker")
	}
	return nil
}
