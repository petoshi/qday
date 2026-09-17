package types

import (
	"crypto/sha256"
	"errors"
)

// QdayAtomicSwap describes a SHA-256 hashlock with a block-height refund.
// Claiming requires the recipient's Ed25519 and SLH-DSA keys plus the secret;
// refunding requires the sender's two keys after RefundHeight.
type QdayAtomicSwap struct {
	Recipient    QdayKeys
	Refund       QdayKeys
	SecretHash   Hash256
	RefundHeight uint64
}

func (s QdayAtomicSwap) validate() error {
	if err := s.Recipient.Validate(); err != nil {
		return errors.New("invalid atomic-swap recipient keys")
	} else if err := s.Refund.Validate(); err != nil {
		return errors.New("invalid atomic-swap refund keys")
	} else if s.SecretHash == (Hash256{}) {
		return errors.New("atomic-swap secret hash is zero")
	} else if s.RefundHeight == 0 {
		return errors.New("atomic-swap refund height is zero")
	}
	return nil
}

func (s QdayAtomicSwap) claimBranch() SpendPolicy {
	return PolicyThreshold(3, []SpendPolicy{
		PolicyPublicKey(s.Recipient.Classical),
		PolicySLHDSA(s.Recipient.Reserve),
		PolicyHash(s.SecretHash),
	})
}

func (s QdayAtomicSwap) refundBranch() SpendPolicy {
	return PolicyThreshold(3, []SpendPolicy{
		PolicyPublicKey(s.Refund.Classical),
		PolicySLHDSA(s.Refund.Reserve),
		PolicyAbove(s.RefundHeight),
	})
}

// Policy returns the full policy committed by an atomic-swap output.
func (s QdayAtomicSwap) Policy() (SpendPolicy, error) {
	if err := s.validate(); err != nil {
		return SpendPolicy{}, err
	}
	return PolicyThreshold(1, []SpendPolicy{s.claimBranch(), s.refundBranch()}), nil
}

// Address returns the output address for an atomic swap.
func (s QdayAtomicSwap) Address() (QdayAddress, error) {
	p, err := s.Policy()
	return QdayAddress(p.Address()), err
}

// ClaimPolicy reveals only the claim branch and keeps the refund branch opaque.
func (s QdayAtomicSwap) ClaimPolicy() (SpendPolicy, error) {
	if err := s.validate(); err != nil {
		return SpendPolicy{}, err
	}
	return PolicyThreshold(1, []SpendPolicy{s.claimBranch(), PolicyOpaque(s.refundBranch())}), nil
}

// RefundPolicy reveals only the refund branch and keeps the claim branch opaque.
func (s QdayAtomicSwap) RefundPolicy() (SpendPolicy, error) {
	if err := s.validate(); err != nil {
		return SpendPolicy{}, err
	}
	return PolicyThreshold(1, []SpendPolicy{PolicyOpaque(s.claimBranch()), s.refundBranch()}), nil
}

// Claim signs an atomic-swap spend and appends the 32-byte hash preimage.
func (s QdayAtomicSwap) Claim(sigHash Hash256, keys *QdayPrivateKeys, secret [32]byte) (SatisfiedPolicy, error) {
	if keys == nil || keys.Public != s.Recipient {
		return SatisfiedPolicy{}, errors.New("atomic-swap claim keys do not match recipient")
	} else if Hash256(sha256.Sum256(secret[:])) != s.SecretHash {
		return SatisfiedPolicy{}, errors.New("atomic-swap secret does not match hashlock")
	}
	witness, err := keys.Sign(sigHash)
	if err != nil {
		return SatisfiedPolicy{}, err
	}
	witness.Policy, err = s.ClaimPolicy()
	witness.Preimages = append(witness.Preimages, secret)
	return witness, err
}

// Refund signs an atomic-swap refund. Consensus enforces RefundHeight.
func (s QdayAtomicSwap) RefundSpend(sigHash Hash256, keys *QdayPrivateKeys) (SatisfiedPolicy, error) {
	if keys == nil || keys.Public != s.Refund {
		return SatisfiedPolicy{}, errors.New("atomic-swap refund keys do not match sender")
	}
	witness, err := keys.Sign(sigHash)
	if err != nil {
		return SatisfiedPolicy{}, err
	}
	witness.Policy, err = s.RefundPolicy()
	return witness, err
}

// IsQdayNativePolicy reports whether p requires the standard Ed25519 and
// SLH-DSA pair in the canonical order.
func IsQdayNativePolicy(p SpendPolicy) bool {
	t, ok := p.Type.(PolicyTypeThreshold)
	if !ok || t.N != 2 || len(t.Of) != 2 {
		return false
	}
	_, classical := t.Of[0].Type.(PolicyTypePublicKey)
	_, reserve := t.Of[1].Type.(PolicyTypeSLHDSA)
	return classical && reserve
}

// IsQdaySwapSpendPolicy accepts exactly one revealed hybrid claim or refund
// branch and one opaque sibling. The opaque address still commits the hidden
// branch through the parent output address checked by generic validation.
func IsQdaySwapSpendPolicy(p SpendPolicy) bool {
	outer, ok := p.Type.(PolicyTypeThreshold)
	if !ok || outer.N != 1 || len(outer.Of) != 2 {
		return false
	}
	var branch SpendPolicy
	opaque := 0
	for _, sub := range outer.Of {
		if _, ok := sub.Type.(PolicyTypeOpaque); ok {
			opaque++
		} else {
			branch = sub
		}
	}
	if opaque != 1 || branch.Type == nil {
		return false
	}
	inner, ok := branch.Type.(PolicyTypeThreshold)
	if !ok || inner.N != 3 || len(inner.Of) != 3 {
		return false
	}
	if _, ok := inner.Of[0].Type.(PolicyTypePublicKey); !ok {
		return false
	}
	if _, ok := inner.Of[1].Type.(PolicyTypeSLHDSA); !ok {
		return false
	}
	switch lock := inner.Of[2].Type.(type) {
	case PolicyTypeHash:
		return Hash256(lock) != (Hash256{})
	case PolicyTypeAbove:
		return uint64(lock) != 0
	default:
		return false
	}
}
