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
	LongPollID string  `json:"longpollid,omitempty"`
	WorkNonce  *uint64 `json:"worknonce,omitempty"`
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
	Block           string   `json:"block"`
	Coinbase1       string   `json:"coinbase1"`
	Coinbase2       string   `json:"coinbase2"`
	ExtraNonce1Size uint8    `json:"extranonce1Size"`
	ExtraNonce2Size uint8    `json:"extranonce2Size"`
	MerkleBranch    []string `json:"merklebranch"`
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
	WorkNonce         uint64                      `json:"worknonce"`
	BlockRewardAtomic string                      `json:"blockRewardAtomic"`
	FeesAtomic        string                      `json:"feesAtomic"`
	PayoutAtomic      string                      `json:"payoutAtomic"`
	Stratum           MiningStratumTemplate       `json:"stratum"`
}

// MiningBlockStatus reports whether a submitted block is known and remains on
// the selected chain. MaturityHeight is the first height where its payout may
// be spent.
type MiningBlockStatus struct {
	Block          types.BlockID `json:"block"`
	Known          bool          `json:"known"`
	Canonical      bool          `json:"canonical"`
	Height         uint64        `json:"height,omitempty"`
	TipHeight      uint64        `json:"tipHeight"`
	Confirmations  uint64        `json:"confirmations"`
	MaturityHeight uint64        `json:"maturityHeight,omitempty"`
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

func (s *Service) buildMiningTemplate(workNonce *uint64) (cachedMiningTemplate, error) {
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
		if workNonce == nil {
			block = mining.Candidate(cs, public, s.CM.V2PoolTransactions(), types.CurrentTimestamp())
		} else {
			block = mining.CandidateWithNonce(cs, public, s.CM.V2PoolTransactions(), types.CurrentTimestamp(), *workNonce)
		}
		if s.CM.Tip() == cs.Index {
			break
		}
	}
	if block.V2 == nil || len(block.V2.Transactions) == 0 || len(block.MinerPayouts) != 1 {
		return cachedMiningTemplate{}, errors.New("candidate builder returned an incomplete QDAY block")
	}
	workIndex := 0
	if cs.QdayV1Active(cs.Index.Height + 1) {
		workIndex = len(block.V2.Transactions) - 1
	}
	marker, err := consensus.ParseQdayEnvelope(block.V2.Transactions[workIndex].ArbitraryData)
	if err != nil || marker.Kind != consensus.QdayCoinbase {
		if !cs.QdayV1Active(cs.Index.Height+1) || err != nil || marker.Kind != consensus.QdayMiningWork {
			return cachedMiningTemplate{}, errors.New("candidate builder returned an invalid QDAY mining marker")
		}
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
	coinbase1 := txns[len(txns)-1].Data
	coinbase2 := ""
	var extraNonce1Size, extraNonce2Size uint8
	if cs.QdayV1Active(cs.Index.Height + 1) {
		workBytes, err := hex.DecodeString(coinbase1)
		if err != nil || len(workBytes) != 33 {
			return cachedMiningTemplate{}, errors.New("candidate builder returned a non-compact QDAY mining marker")
		}
		// V2 transaction header (9), byte-slice length (8), QDAY prefix and
		// kind (6), then the eight-byte nonce and two-byte key count.
		const nonceOffset, nonceSize = 23, 8
		coinbase1 = hex.EncodeToString(workBytes[:nonceOffset])
		coinbase2 = hex.EncodeToString(workBytes[nonceOffset+nonceSize:])
		extraNonce1Size, extraNonce2Size = 4, 4
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
		WorkNonce:         marker.Nonce,
		BlockRewardAtomic: cs.BlockReward().ExactString(),
		FeesAtomic:        block.MinerPayouts[0].Value.Sub(cs.BlockReward()).ExactString(),
		PayoutAtomic:      block.MinerPayouts[0].Value.ExactString(),
		Stratum: MiningStratumTemplate{
			Block:           encodedBlock,
			Coinbase1:       coinbase1,
			Coinbase2:       coinbase2,
			ExtraNonce1Size: extraNonce1Size,
			ExtraNonce2Size: extraNonce2Size,
			MerkleBranch:    merkleBranch,
		},
	}
	return cachedMiningTemplate{response: response, created: time.Now()}, nil
}

// MiningTemplate returns a current template or waits for the chain, mempool or
// template age to change when longPollID names the current template.
func (s *Service) MiningTemplate(ctx context.Context, longPollID string) (MiningTemplateResponse, error) {
	return s.MiningTemplateForWork(ctx, longPollID, nil)
}

// MiningTemplateForWork returns a normal long-polled template when workNonce
// is nil. A non-nil nonce builds an uncached candidate immediately; this gives
// a pool a distinct commitment for each worker while preserving the exact
// payout and mempool selected by the node.
func (s *Service) MiningTemplateForWork(ctx context.Context, longPollID string, workNonce *uint64) (MiningTemplateResponse, error) {
	if workNonce != nil {
		if longPollID != "" {
			return MiningTemplateResponse{}, errors.New("worknonce cannot be combined with longpollid")
		} else if *workNonce == 0 {
			return MiningTemplateResponse{}, errors.New("worknonce must be nonzero")
		}
		template, err := s.buildMiningTemplate(workNonce)
		return template.response, err
	}
	for {
		s.miningMu.Lock()
		if s.miningTemplate == nil || time.Since(s.miningTemplate.created) >= miningTemplateMaxAge {
			template, err := s.buildMiningTemplate(nil)
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

// MiningStatus returns selected-chain and maturity information for a block.
func (s *Service) MiningStatus(id types.BlockID) MiningBlockStatus {
	tip := s.CM.Tip()
	status := MiningBlockStatus{Block: id, TipHeight: tip.Height}
	state, known := s.CM.State(id)
	if !known {
		return status
	}
	status.Known = true
	status.Height = state.Index.Height
	status.MaturityHeight = state.Index.Height + state.Network.MaturityDelay
	if best, ok := s.CM.BestIndex(state.Index.Height); ok && best.ID == id {
		status.Canonical = true
		if tip.Height >= state.Index.Height {
			status.Confirmations = tip.Height - state.Index.Height + 1
		}
	}
	return status
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
