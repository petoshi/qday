package qday

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"go.sia.tech/core/blake2b"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	mining "go.sia.tech/coreutils/qday"
)

const miningTemplateMaxAge = 30 * time.Second

// MiningTemplateRequest follows the long-poll field used by Sia minerd and
// BIP22-compatible pool controllers.
type MiningTemplateRequest struct {
	LongPollID string `json:"longpollid,omitempty"`
}

// MiningTemplateTransaction is a serialized transaction or miner payout in a
// block template. QDAY only creates version 2 blocks and transactions.
type MiningTemplateTransaction struct {
	Data    string  `json:"data"`
	Hash    string  `json:"hash"`
	TxID    string  `json:"txid"`
	Depends []int64 `json:"depends"`
	Fee     int64   `json:"fee"`
	SigOps  int64   `json:"sigops"`
	TxType  string  `json:"txtype"`
}

// MiningStratumTemplate contains the data a Sia Stratum bridge needs to turn
// the rightmost template transaction into an ordinary Sia mining job. Block is
// the complete encoded block; a bridge replaces its nonce and timestamp before
// submitting it.
type MiningStratumTemplate struct {
	Block        string   `json:"block"`
	MerkleBranch []string `json:"merklebranch"`
}

// MiningTemplateResponse contains a complete transaction-aware QDAY mining
// candidate. The header is included as an 80-byte hexadecimal work item for
// pool controllers that delegate only BLAKE2b nonce search.
type MiningTemplateResponse struct {
	Header            string                      `json:"header"`
	Commitment        types.Hash256               `json:"commitment"`
	Transactions      []MiningTemplateTransaction `json:"transactions"`
	MinerPayout       []MiningTemplateTransaction `json:"minerpayout"`
	PreviousBlockHash string                      `json:"previousblockhash"`
	LongPollID        string                      `json:"longpollid"`
	Target            string                      `json:"target"`
	Height            uint32                      `json:"height"`
	Timestamp         int64                       `json:"curtime"`
	Version           uint32                      `json:"version"`
	Bits              string                      `json:"bits"`
	Stratum           MiningStratumTemplate       `json:"stratum"`
}

// MiningSubmitBlockRequest follows Sia minerd's submitblock request shape.
type MiningSubmitBlockRequest struct {
	Params []string `json:"params"`
}

type cachedMiningTemplate struct {
	response MiningTemplateResponse
	created  time.Time
}

func encodeMiningObject(v types.EncoderTo) (string, error) {
	var buf bytes.Buffer
	e := types.NewEncoder(&buf)
	v.EncodeTo(e)
	if err := e.Flush(); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf.Bytes()), nil
}

func compactMiningDifficulty(w consensus.Work) string {
	var buf bytes.Buffer
	e := types.NewEncoder(&buf)
	w.EncodeTo(e)
	if err := e.Flush(); err != nil {
		panic(err)
	}
	n := new(big.Int).SetBytes(buf.Bytes())
	if n.Sign() == 0 {
		return "00000000"
	}
	exponent := uint(len(n.Bytes()))
	var mantissa uint32
	if exponent <= 3 {
		mantissa = uint32(n.Bits()[0]) << (8 * (3 - exponent))
	} else {
		mantissa = uint32(new(big.Int).Rsh(new(big.Int).Set(n), 8*(exponent-3)).Bits()[0])
	}
	if mantissa&0x00800000 != 0 {
		mantissa >>= 8
		exponent++
	}
	return fmt.Sprintf("%08X", uint32(exponent<<24)|mantissa)
}

func (s *Service) invalidateMiningTemplate() {
	s.miningMu.Lock()
	s.miningTemplate = nil
	close(s.miningInvalidated)
	s.miningInvalidated = make(chan struct{})
	s.miningMu.Unlock()
}

func (s *Service) buildMiningTemplate() (cachedMiningTemplate, error) {
	s.mu.Lock()
	keys := s.keys
	if keys == nil {
		s.mu.Unlock()
		return cachedMiningTemplate{}, errors.New("unlock the payout wallet before requesting mining work")
	}
	public := keys.Public
	s.mu.Unlock()
	if !s.networkSynced() {
		return cachedMiningTemplate{}, errors.New("waiting for synchronization with a network peer")
	}
	if time.Now().Before(s.Manifest.Genesis.Timestamp) {
		return cachedMiningTemplate{}, errors.New("waiting for the genesis start time")
	}

	var block types.Block
	var cs consensus.State
	for {
		cs = s.CM.TipState()
		block = mining.Candidate(cs, public, s.CM.V2PoolTransactions(), types.CurrentTimestamp())
		if s.CM.Tip() == cs.Index {
			break
		}
	}
	if block.V2 == nil || len(block.V2.Transactions) == 0 || len(block.MinerPayouts) != 1 {
		return cachedMiningTemplate{}, errors.New("candidate builder returned an incomplete QDAY block")
	}

	header, err := encodeMiningObject(block.Header())
	if err != nil {
		return cachedMiningTemplate{}, err
	}
	if len(header) != 160 {
		return cachedMiningTemplate{}, fmt.Errorf("encoded mining header has %d bytes, expected 80", len(header)/2)
	}
	encodedBlock, err := encodeMiningObject(types.V2Block(block))
	if err != nil {
		return cachedMiningTemplate{}, err
	}
	payout, err := encodeMiningObject(types.V2SiacoinOutput(block.MinerPayouts[0]))
	if err != nil {
		return cachedMiningTemplate{}, err
	}
	txns := make([]MiningTemplateTransaction, 0, len(block.V2.Transactions))
	for _, txn := range block.V2.Transactions {
		data, err := encodeMiningObject(txn)
		if err != nil {
			return cachedMiningTemplate{}, err
		}
		txns = append(txns, MiningTemplateTransaction{Data: data, TxID: txn.ID().String(), TxType: "2"})
	}
	// Sia Stratum miners place their arbitrary transaction at the right edge of
	// the commitment tree, then fold these left-side roots into its leaf hash.
	// QDAY's candidate always contains at least its mandatory miner marker.
	var acc blake2b.Accumulator
	acc.AddLeaf(cs.MerkleLeafHash(public.Policy().Address()))
	for i := 0; i < len(block.V2.Transactions)-1; i++ {
		acc.AddLeaf(block.V2.Transactions[i].MerkleLeafHash())
	}
	merkleBranch := make([]string, 0, 64)
	for height := 0; height < len(acc.Trees); height++ {
		if acc.NumLeaves&(uint64(1)<<height) != 0 {
			merkleBranch = append(merkleBranch, hex.EncodeToString(acc.Trees[height][:]))
		}
	}
	var longPollEntropy [16]byte
	if _, err := rand.Read(longPollEntropy[:]); err != nil {
		return cachedMiningTemplate{}, err
	}
	response := MiningTemplateResponse{
		Header:            header,
		Commitment:        block.Header().Commitment,
		Transactions:      txns,
		MinerPayout:       []MiningTemplateTransaction{{Data: payout}},
		PreviousBlockHash: block.ParentID.String(),
		LongPollID:        hex.EncodeToString(longPollEntropy[:]),
		Target:            cs.PoWTarget().String(),
		Height:            uint32(cs.Index.Height + 1),
		Timestamp:         block.Timestamp.Unix(),
		Version:           2,
		Bits:              compactMiningDifficulty(cs.Difficulty),
		Stratum: MiningStratumTemplate{
			Block:        encodedBlock,
			MerkleBranch: merkleBranch,
		},
	}
	return cachedMiningTemplate{response: response, created: time.Now()}, nil
}

// MiningTemplate returns a current template or waits for the chain, mempool or
// template age to change when longPollID names the current template.
func (s *Service) MiningTemplate(ctx context.Context, longPollID string) (MiningTemplateResponse, error) {
	for {
		s.miningMu.Lock()
		if s.miningTemplate == nil || time.Since(s.miningTemplate.created) >= miningTemplateMaxAge {
			template, err := s.buildMiningTemplate()
			if err != nil {
				s.miningMu.Unlock()
				return MiningTemplateResponse{}, err
			}
			s.miningTemplate = &template
		}
		response := s.miningTemplate.response
		invalidated := s.miningInvalidated
		age := time.Since(s.miningTemplate.created)
		s.miningMu.Unlock()

		if response.LongPollID != longPollID {
			return response, nil
		}
		wait := miningTemplateMaxAge - age
		if wait <= 0 {
			s.invalidateMiningTemplate()
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return MiningTemplateResponse{}, ctx.Err()
		case <-s.ctx.Done():
			timer.Stop()
			return MiningTemplateResponse{}, s.ctx.Err()
		case <-invalidated:
			timer.Stop()
		case <-timer.C:
			s.invalidateMiningTemplate()
		}
	}
}

// SubmitMiningBlock validates and relays a Sia-encoded QDAY v2 block.
func (s *Service) SubmitMiningBlock(encoded string) (types.BlockID, error) {
	if len(encoded) == 0 || len(encoded) > 16<<20 {
		return types.BlockID{}, errors.New("encoded block must contain at most 8 MiB")
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return types.BlockID{}, errors.New("block must be hexadecimal QDAY v2 encoding")
	}
	decoder := types.NewBufDecoder(raw)
	var encodedBlock types.V2Block
	encodedBlock.DecodeFrom(decoder)
	if err := decoder.Err(); err != nil {
		return types.BlockID{}, fmt.Errorf("invalid QDAY v2 block encoding: %w", err)
	}
	canonical, err := encodeMiningObject(encodedBlock)
	if err != nil {
		return types.BlockID{}, err
	} else if canonical != strings.ToLower(encoded) {
		return types.BlockID{}, errors.New("trailing or non-canonical bytes after QDAY v2 block")
	}
	block := encodedBlock.Cast()
	if block.V2 == nil {
		return types.BlockID{}, errors.New("QDAY requires a v2 block")
	}
	if err := s.CM.AddBlocks([]types.Block{block}); err != nil {
		return types.BlockID{}, err
	}
	if s.Syncer != nil {
		s.setRelayError(s.Syncer.BroadcastV2Header(block.Header()))
		s.setRelayError(s.Syncer.BroadcastV2BlockOutline(gateway.OutlineBlock(block, s.CM.PoolTransactions(), s.CM.V2PoolTransactions())))
	}
	return block.ID(), nil
}
