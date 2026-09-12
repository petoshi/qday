package qday

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"go.sia.tech/core/types"
)

const (
	rebroadcastPeriod    = 2 * time.Minute
	maxRebroadcastPeriod = 48 * time.Hour
	maxRebroadcastFile   = 64 << 20
	maxRebroadcastSets   = 1024
)

type pendingBroadcast struct {
	Basis         types.ChainIndex      `json:"basis"`
	BroadcastedAt time.Time             `json:"broadcastedAt"`
	Transactions  []types.V2Transaction `json:"transactions"`
}

type pendingBroadcastFile struct {
	Version      int                      `json:"version"`
	Transactions []storedPendingBroadcast `json:"transactions"`
}

type storedPendingBroadcast struct {
	Basis         types.ChainIndex `json:"basis"`
	BroadcastedAt time.Time        `json:"broadcastedAt"`
	Transactions  [][]byte         `json:"transactions"`
}

func encodePendingTransaction(txn types.V2Transaction) ([]byte, error) {
	var buf bytes.Buffer
	encoder := types.NewEncoder(&buf)
	txn.EncodeTo(encoder)
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodePendingTransaction(b []byte) (types.V2Transaction, error) {
	if len(b) == 0 || len(b) > 4<<20 {
		return types.V2Transaction{}, errors.New("invalid pending transaction size")
	}
	var txn types.V2Transaction
	decoder := types.NewBufDecoder(b)
	txn.DecodeFrom(decoder)
	if err := decoder.Err(); err != nil {
		return types.V2Transaction{}, err
	}
	canonical, err := encodePendingTransaction(txn)
	if err != nil {
		return types.V2Transaction{}, err
	} else if !bytes.Equal(b, canonical) {
		return types.V2Transaction{}, errors.New("non-canonical pending transaction")
	}
	return txn, nil
}

func (s *Service) loadPendingBroadcasts() error {
	info, err := os.Stat(s.rebroadcastPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if info.Size() > maxRebroadcastFile {
		return errors.New("pending transaction file is too large")
	}
	b, err := os.ReadFile(s.rebroadcastPath)
	if err != nil {
		return err
	}
	var stored pendingBroadcastFile
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return fmt.Errorf("invalid pending transaction file: %w", err)
	} else if decoder.Decode(new(any)) != io.EOF || stored.Version != 1 || len(stored.Transactions) > maxRebroadcastSets {
		return errors.New("invalid pending transaction file")
	}
	now := time.Now()
	for _, storedSet := range stored.Transactions {
		if storedSet.BroadcastedAt.IsZero() || now.Sub(storedSet.BroadcastedAt) > maxRebroadcastPeriod || len(storedSet.Transactions) == 0 {
			continue
		}
		set := pendingBroadcast{Basis: storedSet.Basis, BroadcastedAt: storedSet.BroadcastedAt, Transactions: make([]types.V2Transaction, len(storedSet.Transactions))}
		for i, encoded := range storedSet.Transactions {
			set.Transactions[i], err = decodePendingTransaction(encoded)
			if err != nil {
				return fmt.Errorf("invalid pending transaction: %w", err)
			}
		}
		id := set.Transactions[len(set.Transactions)-1].ID()
		s.rebroadcasts[id] = set
	}
	return nil
}

func (s *Service) savePendingBroadcastsLocked() error {
	ids := make([]types.TransactionID, 0, len(s.rebroadcasts))
	for id := range s.rebroadcasts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	stored := pendingBroadcastFile{Version: 1, Transactions: make([]storedPendingBroadcast, 0, len(ids))}
	for _, id := range ids {
		set := s.rebroadcasts[id]
		disk := storedPendingBroadcast{Basis: set.Basis, BroadcastedAt: set.BroadcastedAt, Transactions: make([][]byte, len(set.Transactions))}
		for i, txn := range set.Transactions {
			encoded, err := encodePendingTransaction(txn)
			if err != nil {
				return err
			}
			disk.Transactions[i] = encoded
		}
		stored.Transactions = append(stored.Transactions, disk)
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.rebroadcastPath), ".qday-pending-*")
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
	} else if err := os.Rename(name, s.rebroadcastPath); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(s.rebroadcastPath))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Service) rememberBroadcast(basis types.ChainIndex, txns []types.V2Transaction) error {
	if len(txns) == 0 {
		return nil
	}
	set := pendingBroadcast{Basis: basis, BroadcastedAt: time.Now(), Transactions: make([]types.V2Transaction, len(txns))}
	for i := range txns {
		set.Transactions[i] = txns[i].DeepCopy()
	}
	id := txns[len(txns)-1].ID()
	s.rebroadcastMu.Lock()
	s.rebroadcasts[id] = set
	err := s.savePendingBroadcastsLocked()
	s.rebroadcastMu.Unlock()
	select {
	case s.rebroadcastWake <- struct{}{}:
	default:
	}
	return err
}

func (s *Service) rebroadcastPending() {
	s.rebroadcastMu.Lock()
	sets := make(map[types.TransactionID]pendingBroadcast, len(s.rebroadcasts))
	for id, set := range s.rebroadcasts {
		sets[id] = set
	}
	s.rebroadcastMu.Unlock()

	tip := s.CM.Tip()
	now := time.Now()
	changed := false
	for id, set := range sets {
		if now.Sub(set.BroadcastedAt) > maxRebroadcastPeriod {
			s.rebroadcastMu.Lock()
			delete(s.rebroadcasts, id)
			s.rebroadcastMu.Unlock()
			changed = true
			continue
		}
		txns := make([]types.V2Transaction, len(set.Transactions))
		for i := range set.Transactions {
			txns[i] = set.Transactions[i].DeepCopy()
		}
		updated, err := s.CM.UpdateV2TransactionSet(txns, set.Basis, tip)
		if err != nil {
			s.rebroadcastMu.Lock()
			delete(s.rebroadcasts, id)
			s.rebroadcastMu.Unlock()
			changed = true
			continue
		} else if len(updated) == 0 {
			s.rebroadcastMu.Lock()
			delete(s.rebroadcasts, id)
			s.rebroadcastMu.Unlock()
			changed = true
			continue
		}
		if _, err := s.CM.AddV2PoolTransactions(tip, updated); err != nil {
			continue
		}
		set.Basis = tip
		set.Transactions = updated
		s.rebroadcastMu.Lock()
		s.rebroadcasts[id] = set
		s.rebroadcastMu.Unlock()
		changed = true
		if s.Syncer != nil {
			s.setError(s.Syncer.BroadcastV2TransactionSet(tip, updated))
		}
	}
	if changed {
		s.rebroadcastMu.Lock()
		if err := s.savePendingBroadcastsLocked(); err != nil {
			s.setError(fmt.Errorf("failed to update pending transactions: %w", err))
		}
		s.rebroadcastMu.Unlock()
	}
}

func (s *Service) runRebroadcaster() {
	reorg := make(chan struct{}, 1)
	stop := s.CM.OnReorg(func(types.ChainIndex) {
		select {
		case reorg <- struct{}{}:
		default:
		}
	})
	defer stop()
	ticker := time.NewTicker(rebroadcastPeriod)
	defer ticker.Stop()
	select {
	case reorg <- struct{}{}:
	default:
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-reorg:
			s.rebroadcastPending()
		case <-s.rebroadcastWake:
			s.rebroadcastPending()
		case <-ticker.C:
			s.rebroadcastPending()
		}
	}
}
