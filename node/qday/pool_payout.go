package qday

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	mining "go.sia.tech/coreutils/qday"
)

const (
	maxPoolPayoutFile    = 128 << 20
	maxPoolPayoutRecords = 100_000
	maxPoolPayoutOutputs = 127
	poolPayoutFinality   = 6
)

// PoolPayoutOutput is one exact atomic payment in an idempotent pool batch.
type PoolPayoutOutput struct {
	Address      string `json:"address"`
	AmountAtomic string `json:"amountAtomic"`
}

// PoolPayoutRequest describes one retry-safe pool payout transaction. Reusing
// RequestID with identical fields returns the original transaction; changing
// any field is rejected.
type PoolPayoutRequest struct {
	RequestID          string             `json:"requestID"`
	FromAddress        string             `json:"fromAddress"`
	ExpectedUnitAtomic string             `json:"expectedUnitAtomic"`
	FeeAtomic          string             `json:"feeAtomic"`
	Outputs            []PoolPayoutOutput `json:"outputs"`
}

type PoolPayoutResponse struct {
	RequestID   string              `json:"requestID"`
	Transaction types.TransactionID `json:"transaction"`
	Outputs     int                 `json:"outputs"`
	Status      string              `json:"status"`
}

// PoolDefendResponse reports whether pool maintenance queued a renewal.
type PoolDefendResponse struct {
	Status      string `json:"status"`
	Transaction string `json:"transaction,omitempty"`
}

// PoolDefend renews pool-wallet outputs without enabling CPU mining. It is
// safe to poll: outputs that do not need renewal, including outputs already
// spent by a pending renewal, return idle.
func (s *Service) PoolDefend(ctx context.Context) (PoolDefendResponse, error) {
	id, err := s.Transfer(ctx, types.QdayAddress{}, types.ZeroCurrency, true)
	if errors.Is(err, errNothingToDefend) {
		return PoolDefendResponse{Status: "idle"}, nil
	} else if err != nil {
		return PoolDefendResponse{}, err
	}
	return PoolDefendResponse{Status: "queued", Transaction: id.String()}, nil
}

type poolPayoutRecord struct {
	RequestID     string              `json:"requestID"`
	Digest        string              `json:"digest"`
	Basis         types.ChainIndex    `json:"basis"`
	TransactionID types.TransactionID `json:"transactionID"`
	Transaction   []byte              `json:"transaction,omitempty"`
	Outputs       int                 `json:"outputs"`
	Status        string              `json:"status"`
	ConfirmedAt   types.ChainIndex    `json:"confirmedAt,omitempty"`
	CreatedAt     time.Time           `json:"createdAt"`
}

type poolPayoutFile struct {
	Version int                `json:"version"`
	Records []poolPayoutRecord `json:"records"`
}

type validatedPoolPayoutOutput struct {
	Address types.QdayAddress
	Amount  types.Currency
}

type validatedPoolPayout struct {
	RequestID          string
	FromAddress        string
	ExpectedUnitAtomic string
	Fee                types.Currency
	Outputs            []validatedPoolPayoutOutput
	Digest             string
}

func canonicalAtomic(name, value string, allowZero bool) (types.Currency, error) {
	if value == "" || len(value) > 80 {
		return types.ZeroCurrency, fmt.Errorf("%s must be a canonical atomic integer", name)
	}
	amount, err := types.ParseCurrency(value)
	if err != nil || amount.ExactString() != value {
		return types.ZeroCurrency, fmt.Errorf("%s must be a canonical atomic integer", name)
	} else if !allowZero && amount.IsZero() {
		return types.ZeroCurrency, fmt.Errorf("%s must be positive", name)
	}
	return amount, nil
}

func validatePoolPayoutRequest(req PoolPayoutRequest) (validatedPoolPayout, error) {
	req.RequestID = strings.TrimSpace(req.RequestID)
	if len(req.RequestID) < 1 || len(req.RequestID) > 128 {
		return validatedPoolPayout{}, errors.New("requestID must contain 1..128 printable ASCII characters")
	}
	for _, r := range req.RequestID {
		if r < 0x21 || r > 0x7e {
			return validatedPoolPayout{}, errors.New("requestID must contain 1..128 printable ASCII characters")
		}
	}
	from, err := types.ParseQdayAddress(strings.TrimSpace(req.FromAddress))
	if err != nil {
		return validatedPoolPayout{}, fmt.Errorf("invalid fromAddress: %w", err)
	}
	unit, err := canonicalAtomic("expectedUnitAtomic", req.ExpectedUnitAtomic, false)
	if err != nil {
		return validatedPoolPayout{}, err
	}
	fee, err := canonicalAtomic("feeAtomic", req.FeeAtomic, true)
	if err != nil {
		return validatedPoolPayout{}, err
	}
	if len(req.Outputs) == 0 || len(req.Outputs) > maxPoolPayoutOutputs {
		return validatedPoolPayout{}, fmt.Errorf("outputs must contain 1..%d payments", maxPoolPayoutOutputs)
	}
	outputs := make([]validatedPoolPayoutOutput, len(req.Outputs))
	seen := make(map[types.QdayAddress]bool, len(req.Outputs))
	for i, output := range req.Outputs {
		address, err := types.ParseQdayAddress(strings.TrimSpace(output.Address))
		if err != nil {
			return validatedPoolPayout{}, fmt.Errorf("output %d address: %w", i, err)
		} else if seen[address] {
			return validatedPoolPayout{}, fmt.Errorf("output %d repeats address %s", i, address)
		}
		amount, err := canonicalAtomic(fmt.Sprintf("output %d amountAtomic", i), output.AmountAtomic, false)
		if err != nil {
			return validatedPoolPayout{}, err
		}
		seen[address] = true
		outputs[i] = validatedPoolPayoutOutput{Address: address, Amount: amount}
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Address.String() < outputs[j].Address.String() })
	canonical := struct {
		RequestID          string             `json:"requestID"`
		FromAddress        string             `json:"fromAddress"`
		ExpectedUnitAtomic string             `json:"expectedUnitAtomic"`
		FeeAtomic          string             `json:"feeAtomic"`
		Outputs            []PoolPayoutOutput `json:"outputs"`
	}{RequestID: req.RequestID, FromAddress: from.String(), ExpectedUnitAtomic: unit.ExactString(), FeeAtomic: fee.ExactString(), Outputs: make([]PoolPayoutOutput, len(outputs))}
	for i, output := range outputs {
		canonical.Outputs[i] = PoolPayoutOutput{Address: output.Address.String(), AmountAtomic: output.Amount.ExactString()}
	}
	b, _ := json.Marshal(canonical)
	digest := sha256.Sum256(b)
	return validatedPoolPayout{
		RequestID:          canonical.RequestID,
		FromAddress:        canonical.FromAddress,
		ExpectedUnitAtomic: canonical.ExpectedUnitAtomic,
		Fee:                fee,
		Outputs:            outputs,
		Digest:             hex.EncodeToString(digest[:]),
	}, nil
}

func (s *Service) loadPoolPayouts() error {
	info, err := os.Stat(s.poolPayoutPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if info.Size() > maxPoolPayoutFile {
		return errors.New("pool payout file is too large")
	}
	b, err := os.ReadFile(s.poolPayoutPath)
	if err != nil {
		return err
	}
	var stored poolPayoutFile
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return fmt.Errorf("invalid pool payout file: %w", err)
	} else if decoder.Decode(new(any)) != io.EOF || stored.Version != 1 || len(stored.Records) > maxPoolPayoutRecords {
		return errors.New("invalid pool payout file")
	}
	for _, record := range stored.Records {
		if record.RequestID == "" || record.Digest == "" || record.CreatedAt.IsZero() || (record.Status != "queued" && record.Status != "confirming" && record.Status != "confirmed") {
			return errors.New("invalid pool payout record")
		}
		if record.Status == "confirmed" {
			if len(record.Transaction) != 0 || record.TransactionID == (types.TransactionID{}) {
				return errors.New("invalid confirmed pool payout record")
			}
		} else {
			txn, err := decodePendingTransaction(record.Transaction)
			if err != nil {
				return fmt.Errorf("invalid pool payout transaction: %w", err)
			} else if txn.ID() != record.TransactionID {
				return errors.New("pool payout transaction ID mismatch")
			}
			id := txn.ID()
			if _, exists := s.rebroadcasts[id]; !exists {
				s.rebroadcasts[id] = pendingBroadcast{Basis: record.Basis, BroadcastedAt: record.CreatedAt, Transactions: []types.V2Transaction{txn}}
			}
		}
		if _, exists := s.poolPayouts[record.RequestID]; exists {
			return errors.New("duplicate pool payout requestID")
		}
		s.poolPayouts[record.RequestID] = record
	}
	return nil
}

func (s *Service) savePoolPayoutsLocked() error {
	ids := make([]string, 0, len(s.poolPayouts))
	for id := range s.poolPayouts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	stored := poolPayoutFile{Version: 1, Records: make([]poolPayoutRecord, 0, len(ids))}
	for _, id := range ids {
		stored.Records = append(stored.Records, s.poolPayouts[id])
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return err
	} else if len(b) > maxPoolPayoutFile || len(ids) > maxPoolPayoutRecords {
		return errors.New("pool payout storage limit reached")
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.poolPayoutPath), ".qday-payouts-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	} else if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	} else if err := tmp.Close(); err != nil {
		return err
	} else if err := os.Rename(name, s.poolPayoutPath); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(s.poolPayoutPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) savePoolPayoutRecord(record poolPayoutRecord) (poolPayoutRecord, error) {
	s.poolPayoutMu.Lock()
	previous, existed := s.poolPayouts[record.RequestID]
	if !existed && len(s.poolPayouts) >= maxPoolPayoutRecords {
		s.poolPayoutMu.Unlock()
		return record, errors.New("pool payout storage limit reached")
	}
	s.poolPayouts[record.RequestID] = record
	err := s.savePoolPayoutsLocked()
	if err != nil {
		// The in-memory idempotency table must never claim a record is durable
		// when its atomic file replacement failed. Restore the last persisted
		// state so a retry cannot relay an unrecorded payment.
		if existed {
			s.poolPayouts[record.RequestID] = previous
		} else {
			delete(s.poolPayouts, record.RequestID)
		}
	}
	s.poolPayoutMu.Unlock()
	return record, err
}

func (s *Service) ensurePoolPayout(ctx context.Context, record poolPayoutRecord) (poolPayoutRecord, error) {
	if record.Status == "confirmed" {
		return record, nil
	}
	tip := s.CM.Tip()
	if record.ConfirmedAt != (types.ChainIndex{}) {
		if best, ok := s.CM.BestIndex(record.ConfirmedAt.Height); ok && best == record.ConfirmedAt {
			if tip.Height < record.ConfirmedAt.Height+poolPayoutFinality-1 {
				return record, nil
			}
			record.Status = "confirmed"
			record.Transaction = nil
			s.rebroadcastMu.Lock()
			delete(s.rebroadcasts, record.TransactionID)
			rebroadcastErr := s.savePendingBroadcastsLocked()
			s.rebroadcastMu.Unlock()
			if rebroadcastErr != nil {
				return record, rebroadcastErr
			}
			return s.savePoolPayoutRecord(record)
		}
		record.ConfirmedAt = types.ChainIndex{}
		record.Status = "queued"
	}
	txn, err := decodePendingTransaction(record.Transaction)
	if err != nil {
		return record, err
	}
	updated, err := s.updatePendingProofs([]types.V2Transaction{txn}, record.Basis, tip)
	if err != nil {
		return record, err
	} else if len(updated) == 0 {
		record.Status = "confirming"
		record.ConfirmedAt = tip
		return s.savePoolPayoutRecord(record)
	}
	if _, err := s.CM.AddV2PoolTransactions(tip, updated); err != nil {
		return record, err
	}
	if err := s.rememberBroadcast(tip, updated); err != nil {
		s.setError(fmt.Errorf("failed to save pool payout for rebroadcast: %w", err))
	}
	if s.Syncer != nil {
		s.setRelayError(s.Syncer.BroadcastV2TransactionSet(tip, updated))
	}
	encoded, err := encodePendingTransaction(updated[0])
	if err != nil {
		return record, err
	}
	record.Basis, record.Transaction = tip, encoded
	record.TransactionID = updated[0].ID()
	record.Status = "queued"
	return s.savePoolPayoutRecord(record)
}

// PoolPayout signs and queues an idempotent multi-recipient payment for a pool
// controller. Amounts are exact atomic integers so a PQ Day denomination change
// cannot silently alter a prepared batch.
func (s *Service) PoolPayout(ctx context.Context, req PoolPayoutRequest) (PoolPayoutResponse, error) {
	validated, err := validatePoolPayoutRequest(req)
	if err != nil {
		return PoolPayoutResponse{}, err
	}
	s.op.Lock()
	defer s.op.Unlock()

	s.poolPayoutMu.Lock()
	record, exists := s.poolPayouts[validated.RequestID]
	s.poolPayoutMu.Unlock()
	if exists {
		if record.Digest != validated.Digest {
			return PoolPayoutResponse{}, errors.New("requestID was already used for a different pool payout")
		}
		record, err = s.ensurePoolPayout(ctx, record)
		if err != nil {
			return PoolPayoutResponse{}, err
		}
		return PoolPayoutResponse{RequestID: record.RequestID, Transaction: record.TransactionID, Outputs: record.Outputs, Status: record.Status}, nil
	}

	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	if keys == nil {
		return PoolPayoutResponse{}, errors.New("unlock your wallet first")
	} else if validated.FromAddress != keys.Public.String() {
		return PoolPayoutResponse{}, errors.New("the active wallet changed; review the pool payout again")
	}
	cs := s.CM.TipState()
	if cs.QdayUnits(cs.Index.Height).ExactString() != validated.ExpectedUnitAtomic {
		return PoolPayoutResponse{}, errors.New("QDAY denomination changed; rebuild the pool payout")
	}
	var required types.Currency
	for _, output := range validated.Outputs {
		var overflow bool
		required, overflow = required.AddWithOverflow(output.Amount)
		if overflow {
			return PoolPayoutResponse{}, errors.New("pool payout amount overflows")
		}
	}
	required, overflow := required.AddWithOverflow(validated.Fee)
	if overflow {
		return PoolPayoutResponse{}, errors.New("pool payout amount plus fee overflows")
	}
	outs, err := s.outputs(cs, keys.Public.Address())
	if err != nil {
		return PoolPayoutResponse{}, err
	}
	sort.Slice(outs, func(i, j int) bool {
		vi := cs.QdayValue(outs[i].SiacoinElement, cs.Index.Height+2)
		vj := cs.QdayValue(outs[j].SiacoinElement, cs.Index.Height+2)
		if cmp := vi.Cmp(vj); cmp != 0 {
			return cmp > 0
		}
		return outs[i].MaturityHeight < outs[j].MaturityHeight
	})
	txn := types.V2Transaction{MinerFee: validated.Fee}
	var total types.Currency
	for _, output := range outs {
		value := cs.QdayValue(output.SiacoinElement, cs.Index.Height+2)
		if value.IsZero() {
			continue
		}
		total = total.Add(value)
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.V2SiacoinInput{Parent: output.SiacoinElement, SatisfiedPolicy: types.SatisfiedPolicy{Policy: keys.Public.Policy()}})
		if len(txn.SiacoinInputs) == 128 || total.Cmp(required) >= 0 {
			break
		}
	}
	if total.Cmp(required) < 0 {
		return PoolPayoutResponse{}, errors.New("insufficient confirmed spendable balance for pool payout")
	}
	for _, output := range validated.Outputs {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{Value: output.Amount, Address: types.Address(output.Address)})
	}
	if change := total.Sub(required); !change.IsZero() {
		txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{Value: change, Address: keys.Public.Policy().Address()})
	}
	txn.ArbitraryData = (consensus.QdayEnvelope{Kind: consensus.QdayTransfer}).Encode()
	if err := mining.SignTransfer(ctx, cs, &txn, keys); err != nil {
		return PoolPayoutResponse{}, err
	}
	current := s.CM.TipState()
	if current.QdayUnits(current.Index.Height).ExactString() != validated.ExpectedUnitAtomic {
		return PoolPayoutResponse{}, errors.New("QDAY denomination changed while signing; rebuild the pool payout")
	}
	updated, err := s.CM.UpdateV2TransactionSet([]types.V2Transaction{txn}, cs.Index, current.Index)
	if err != nil || len(updated) != 1 {
		return PoolPayoutResponse{}, err
	}
	encoded, err := encodePendingTransaction(updated[0])
	if err != nil {
		return PoolPayoutResponse{}, err
	}
	record = poolPayoutRecord{RequestID: validated.RequestID, Digest: validated.Digest, Basis: current.Index, TransactionID: updated[0].ID(), Transaction: encoded, Outputs: len(validated.Outputs), Status: "queued", CreatedAt: time.Now().UTC()}
	_, err = s.savePoolPayoutRecord(record)
	if err != nil {
		return PoolPayoutResponse{}, err
	}
	record, err = s.ensurePoolPayout(ctx, record)
	if err != nil {
		return PoolPayoutResponse{}, err
	}
	return PoolPayoutResponse{RequestID: record.RequestID, Transaction: record.TransactionID, Outputs: record.Outputs, Status: record.Status}, nil
}
