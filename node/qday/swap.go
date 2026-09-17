package qday

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	mining "go.sia.tech/coreutils/qday"
	"go.sia.tech/walletd/v2/wallet"
)

// AtomicSwapKeys is the portable public-key pair used by DEX software. Both
// fields are canonical 32-byte lowercase hexadecimal strings.
type AtomicSwapKeys struct {
	Classical string `json:"classical"`
	Reserve   string `json:"reserve"`
	Address   string `json:"address,omitempty"`
}

// AtomicSwapContract contains everything needed to independently derive and
// audit an atomic-swap output address.
type AtomicSwapContract struct {
	Recipient    AtomicSwapKeys `json:"recipient"`
	Refund       AtomicSwapKeys `json:"refund"`
	SecretHash   string         `json:"secretHash"`
	RefundHeight uint64         `json:"refundHeight"`
}

type AtomicSwapOutput struct {
	ID              string `json:"id"`
	ValueAtomic     string `json:"valueAtomic"`
	SpendableAtomic string `json:"spendableAtomic"`
	MaturityHeight  uint64 `json:"maturityHeight"`
	Confirmations   uint64 `json:"confirmations"`
}

type AtomicSwapView struct {
	Address          string             `json:"address"`
	Height           uint64             `json:"height"`
	ActivationHeight uint64             `json:"activationHeight"`
	Active           bool               `json:"active"`
	Outputs          []AtomicSwapOutput `json:"outputs"`
}

type AtomicSwapSpendRequest struct {
	Contract  AtomicSwapContract `json:"contract"`
	Output    string             `json:"output"`
	FeeAtomic string             `json:"feeAtomic"`
	Secret    string             `json:"secret,omitempty"`
}

func atomicSwapKeys(public types.QdayKeys) AtomicSwapKeys {
	return AtomicSwapKeys{
		Classical: hex.EncodeToString(public.Classical[:]),
		Reserve:   hex.EncodeToString(public.Reserve[:]),
		Address:   public.Address().String(),
	}
}

func parseAtomicSwapKeys(name string, encoded AtomicSwapKeys) (types.QdayKeys, error) {
	var keys types.QdayKeys
	decode := func(field, value string, destination []byte) error {
		if value != strings.ToLower(value) || len(value) != 64 {
			return fmt.Errorf("%s.%s must be 32-byte lowercase hexadecimal data", name, field)
		}
		b, err := hex.DecodeString(value)
		if err != nil || len(b) != 32 {
			return fmt.Errorf("%s.%s must be 32-byte lowercase hexadecimal data", name, field)
		}
		copy(destination, b)
		return nil
	}
	if err := decode("classical", encoded.Classical, keys.Classical[:]); err != nil {
		return keys, err
	} else if err := decode("reserve", encoded.Reserve, keys.Reserve[:]); err != nil {
		return keys, err
	} else if err := keys.Validate(); err != nil {
		return keys, fmt.Errorf("%s: %w", name, err)
	} else if encoded.Address != "" && encoded.Address != keys.Address().String() {
		return keys, fmt.Errorf("%s.address does not match its public keys", name)
	}
	return keys, nil
}

func (contract AtomicSwapContract) swap() (types.QdayAtomicSwap, error) {
	recipient, err := parseAtomicSwapKeys("recipient", contract.Recipient)
	if err != nil {
		return types.QdayAtomicSwap{}, err
	}
	refund, err := parseAtomicSwapKeys("refund", contract.Refund)
	if err != nil {
		return types.QdayAtomicSwap{}, err
	}
	if contract.SecretHash != strings.ToLower(contract.SecretHash) || len(contract.SecretHash) != 64 {
		return types.QdayAtomicSwap{}, errors.New("secretHash must be 32-byte lowercase hexadecimal data")
	}
	secretHash, err := hex.DecodeString(contract.SecretHash)
	if err != nil || len(secretHash) != 32 {
		return types.QdayAtomicSwap{}, errors.New("secretHash must be 32-byte lowercase hexadecimal data")
	}
	swap := types.QdayAtomicSwap{Recipient: recipient, Refund: refund, RefundHeight: contract.RefundHeight}
	copy(swap.SecretHash[:], secretHash)
	if _, err := swap.Policy(); err != nil {
		return types.QdayAtomicSwap{}, err
	}
	return swap, nil
}

// AtomicSwapPublicKeys returns the unlocked wallet's public DEX descriptor.
func (s *Service) AtomicSwapPublicKeys() (AtomicSwapKeys, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return AtomicSwapKeys{}, errors.New("unlock your wallet first")
	}
	return atomicSwapKeys(s.keys.Public), nil
}

// DeriveAtomicSwap verifies a descriptor and derives its committed address.
func (s *Service) DeriveAtomicSwap(contract AtomicSwapContract) (AtomicSwapView, error) {
	swap, err := contract.swap()
	if err != nil {
		return AtomicSwapView{}, err
	}
	address, err := swap.Address()
	if err != nil {
		return AtomicSwapView{}, err
	}
	cs := s.CM.TipState()
	return AtomicSwapView{Address: address.String(), Height: cs.Index.Height, ActivationHeight: cs.Network.Qday.V1Height, Active: cs.QdayV1Active(cs.Index.Height + 1)}, nil
}

// WatchAtomicSwap registers the full policy in the local full index. It is
// safe before or after funding and persists across wallet restarts.
func (s *Service) WatchAtomicSwap(contract AtomicSwapContract) (AtomicSwapView, error) {
	swap, err := contract.swap()
	if err != nil {
		return AtomicSwapView{}, err
	}
	policy, err := swap.Policy()
	if err != nil {
		return AtomicSwapView{}, err
	}
	s.mu.Lock()
	unlocked, walletID := s.keys != nil, s.walletID
	s.mu.Unlock()
	if !unlocked {
		return AtomicSwapView{}, errors.New("unlock your wallet first")
	}
	if err := s.WM.AddAddresses(walletID, wallet.Address{Address: policy.Address(), SpendPolicy: &policy, Description: "QDAY atomic swap"}); err != nil {
		return AtomicSwapView{}, err
	}
	return s.AtomicSwapStatus(contract)
}

// AtomicSwapStatus returns every currently unspent output at the contract.
func (s *Service) AtomicSwapStatus(contract AtomicSwapContract) (AtomicSwapView, error) {
	swap, err := contract.swap()
	if err != nil {
		return AtomicSwapView{}, err
	}
	address, err := swap.Address()
	if err != nil {
		return AtomicSwapView{}, err
	}
	cs := s.CM.TipState()
	spent := make(map[types.SiacoinOutputID]bool)
	for _, txn := range s.CM.V2PoolTransactions() {
		for _, input := range txn.SiacoinInputs {
			spent[input.Parent.ID] = true
		}
	}
	view := AtomicSwapView{Address: address.String(), Height: cs.Index.Height, ActivationHeight: cs.Network.Qday.V1Height, Active: cs.QdayV1Active(cs.Index.Height + 1)}
	for offset := 0; ; offset += 500 {
		page, basis, err := s.WM.AddressSiacoinOutputs(types.Address(address), false, offset, 500)
		if err != nil {
			return AtomicSwapView{}, err
		} else if basis != cs.Index {
			return AtomicSwapView{}, errWalletSyncing
		}
		for _, output := range page {
			if spent[output.ID] {
				continue
			}
			view.Outputs = append(view.Outputs, AtomicSwapOutput{
				ID: output.ID.String(), ValueAtomic: output.SiacoinOutput.Value.ExactString(),
				SpendableAtomic: cs.QdayValue(output.SiacoinElement, cs.Index.Height+1).ExactString(),
				MaturityHeight:  output.MaturityHeight, Confirmations: output.Confirmations,
			})
		}
		if len(page) < 500 {
			break
		}
	}
	return view, nil
}

func (s *Service) spendAtomicSwap(ctx context.Context, request AtomicSwapSpendRequest, claim bool) (types.TransactionID, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := ctx.Err(); err != nil {
		return types.TransactionID{}, err
	}
	swap, err := request.Contract.swap()
	if err != nil {
		return types.TransactionID{}, err
	}
	fee, err := canonicalAtomic("feeAtomic", request.FeeAtomic, true)
	if err != nil {
		return types.TransactionID{}, err
	}
	var outputID types.SiacoinOutputID
	if err := outputID.UnmarshalText([]byte(strings.TrimSpace(request.Output))); err != nil {
		return types.TransactionID{}, errors.New("output must be a 32-byte hexadecimal siacoin output ID")
	}
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	if keys == nil {
		return types.TransactionID{}, errors.New("unlock your wallet first")
	}
	if claim && keys.Public != swap.Recipient {
		return types.TransactionID{}, errors.New("active wallet is not the swap recipient")
	} else if !claim && keys.Public != swap.Refund {
		return types.TransactionID{}, errors.New("active wallet is not the swap refund owner")
	}
	cs := s.CM.TipState()
	if !cs.QdayV1Active(cs.Index.Height + 1) {
		return types.TransactionID{}, fmt.Errorf("atomic swaps activate at block %d", cs.Network.Qday.V1Height)
	}
	address, _ := swap.Address()
	var parent *wallet.UnspentSiacoinElement
	for offset := 0; ; offset += 500 {
		page, basis, err := s.WM.AddressSiacoinOutputs(types.Address(address), false, offset, 500)
		if err != nil {
			return types.TransactionID{}, err
		} else if basis != cs.Index {
			return types.TransactionID{}, errWalletSyncing
		}
		for i := range page {
			if page[i].ID == outputID {
				copy := page[i]
				parent = &copy
				break
			}
		}
		if parent != nil || len(page) < 500 {
			break
		}
	}
	if parent == nil {
		return types.TransactionID{}, errors.New("atomic-swap output is not watched, unspent and confirmed")
	} else if parent.MaturityHeight > cs.Index.Height+1 {
		return types.TransactionID{}, errors.New("atomic-swap output is not mature")
	}
	value := cs.QdayValue(parent.SiacoinElement, cs.Index.Height+1)
	if value.Cmp(fee) <= 0 {
		return types.TransactionID{}, errors.New("atomic-swap output does not cover the fee")
	}
	txn := types.V2Transaction{
		SiacoinInputs:  []types.V2SiacoinInput{{Parent: parent.SiacoinElement}},
		SiacoinOutputs: []types.SiacoinOutput{{Value: value.Sub(fee), Address: keys.Public.Policy().Address()}},
		MinerFee:       fee,
		ArbitraryData:  (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode(),
	}
	if claim {
		if request.Secret != strings.ToLower(request.Secret) || len(request.Secret) != 64 {
			return types.TransactionID{}, errors.New("secret must be 32-byte lowercase hexadecimal data")
		}
		secretBytes, err := hex.DecodeString(request.Secret)
		if err != nil || len(secretBytes) != 32 {
			return types.TransactionID{}, errors.New("secret must be 32-byte lowercase hexadecimal data")
		}
		var secret [32]byte
		copy(secret[:], secretBytes)
		err = mining.SignAtomicSwapClaim(ctx, cs, &txn, swap, keys, secret)
		if err != nil {
			return types.TransactionID{}, err
		}
	} else {
		if request.Secret != "" {
			return types.TransactionID{}, errors.New("refund request must not contain a secret")
		}
		if cs.Index.Height < swap.RefundHeight {
			return types.TransactionID{}, fmt.Errorf("refund is locked until height %d", swap.RefundHeight)
		}
		if err := mining.SignAtomicSwapRefund(ctx, cs, &txn, swap, keys); err != nil {
			return types.TransactionID{}, err
		}
	}
	id := txn.ID()
	for _, pending := range s.CM.V2PoolTransactions() {
		for _, input := range pending.SiacoinInputs {
			if input.Parent.ID != outputID {
				continue
			}
			if pending.ID() != id {
				return types.TransactionID{}, errors.New("atomic-swap output is already spent by another pending transaction")
			}
			if err := s.rememberBroadcast(s.CM.Tip(), []types.V2Transaction{pending}); err != nil {
				s.setError(fmt.Errorf("failed to save pending atomic swap for rebroadcast: %w", err))
			}
			return id, nil
		}
	}
	current := s.CM.TipState()
	basis := current.Index
	txns, err := s.CM.UpdateV2TransactionSet([]types.V2Transaction{txn}, cs.Index, basis)
	if err != nil {
		return types.TransactionID{}, err
	}
	if _, err := s.CM.AddV2PoolTransactions(basis, txns); err != nil {
		return types.TransactionID{}, err
	}
	if err := s.rememberBroadcast(basis, txns); err != nil {
		s.setError(fmt.Errorf("failed to save pending atomic swap for rebroadcast: %w", err))
	}
	return id, nil
}

func (s *Service) ClaimAtomicSwap(ctx context.Context, request AtomicSwapSpendRequest) (types.TransactionID, error) {
	return s.spendAtomicSwap(ctx, request, true)
}

func (s *Service) RefundAtomicSwap(ctx context.Context, request AtomicSwapSpendRequest) (types.TransactionID, error) {
	return s.spendAtomicSwap(ctx, request, false)
}
