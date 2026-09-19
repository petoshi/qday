package qday

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/gateway"
	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
	mining "go.sia.tech/coreutils/qday"
	"go.sia.tech/coreutils/syncer"
	seedwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/walletd/v2/internal/portmap"
	"go.sia.tech/walletd/v2/wallet"
)

// Service owns the local wallet and explicit CPU activity. Secret backups
// require creation or an explicit, password-authenticated recovery request.
type Service struct {
	CM                *chain.Manager
	WM                *wallet.Manager
	Syncer            *syncer.Syncer
	PortMapping       *portmap.Manager
	RestartEnabled    bool
	Manifest          chain.QdayManifest
	path              string
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	control           sync.Mutex
	op                sync.Mutex
	keys              *types.QdayPrivateKeys
	public            types.QdayAddress
	walletID          wallet.ID
	runCancel         context.CancelFunc
	runDone           chan struct{}
	mode              string
	threads           int
	started           time.Time
	lastError         string
	lastRelayError    string
	hashes            atomic.Uint64
	blocks            atomic.Uint64
	restartRequested  atomic.Bool
	browser           browserSessions
	supplyMu          sync.Mutex
	supplyIndex       types.ChainIndex
	supplyStatus      SupplyStatus
	supplyCached      bool
	miningMu          sync.Mutex
	miningTemplate    *cachedMiningTemplate
	miningInvalidated chan struct{}
	stopMiningWatch   func()
	rebroadcastPath   string
	rebroadcastMu     sync.Mutex
	rebroadcasts      map[types.TransactionID]pendingBroadcast
	rebroadcastWake   chan struct{}
	rebroadcastDone   chan struct{}
	poolPayoutPath    string
	poolPayoutMu      sync.Mutex
	poolPayouts       map[string]poolPayoutRecord
}

type SupplyAmount struct {
	QDAY   string `json:"qday"`
	Atomic string `json:"atomic"`
}

// SupplyStatus is a chain-indexed monetary snapshot returned by the node.
type SupplyStatus struct {
	Network             string       `json:"network"`
	Height              uint64       `json:"height"`
	Synced              bool         `json:"synced"`
	UnitAtomic          string       `json:"unitAtomic"`
	IssuedSupply        SupplyAmount `json:"issuedSupply"`
	BurnedSupply        SupplyAmount `json:"burnedSupply"`
	CurrentSupply       SupplyAmount `json:"currentSupply"`
	ImmatureSupply      SupplyAmount `json:"immatureSupply"`
	CirculatingSupply   SupplyAmount `json:"circulatingSupply"`
	MaximumIssuedSupply SupplyAmount `json:"maximumIssuedSupply"`
	UnspentOutputs      uint64       `json:"unspentOutputs"`
}

const observedHashrateWindow = 60

func (s *Service) observedNetworkHashrate(tip types.ChainIndex, state consensus.State, window uint64) (float64, uint64) {
	if tip.Height == 0 || window == 0 {
		return 0, 0
	}
	window = min(window, tip.Height)
	startIndex, ok := s.CM.BestIndex(tip.Height - window)
	if !ok {
		return 0, 0
	}
	startState, ok := s.CM.State(startIndex.ID)
	if !ok {
		return 0, 0
	}
	startBlock, ok := s.CM.Block(startIndex.ID)
	if !ok {
		return 0, 0
	}
	endBlock, ok := s.CM.Block(tip.ID)
	if !ok {
		return 0, 0
	}
	seconds := endBlock.Timestamp.Unix() - startBlock.Timestamp.Unix()
	endWork, endOK := new(big.Int).SetString(state.TotalWork.String(), 10)
	startWork, startOK := new(big.Int).SetString(startState.TotalWork.String(), 10)
	if !endOK || !startOK || seconds < 1 || endWork.Cmp(startWork) <= 0 {
		return 0, 0
	}
	work := endWork.Sub(endWork, startWork)
	hashrate, _ := new(big.Float).Quo(new(big.Float).SetInt(work), new(big.Float).SetInt64(seconds)).Float64()
	return hashrate, window
}

func NewService(ctx context.Context, dir string, cm *chain.Manager, wm *wallet.Manager, sy *syncer.Syncer, manifest chain.QdayManifest) (*Service, error) {
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{
		CM:                cm,
		WM:                wm,
		Syncer:            sy,
		Manifest:          manifest,
		path:              filepath.Join(dir, "wallet.key"),
		ctx:               ctx,
		cancel:            cancel,
		mode:              "STOP",
		miningInvalidated: make(chan struct{}),
		rebroadcastPath:   filepath.Join(dir, "pending-transactions.json"),
		rebroadcasts:      make(map[types.TransactionID]pendingBroadcast),
		rebroadcastWake:   make(chan struct{}, 1),
		rebroadcastDone:   make(chan struct{}),
		poolPayoutPath:    filepath.Join(dir, "pool-payouts.json"),
		poolPayouts:       make(map[string]poolPayoutRecord),
	}
	s.stopMiningWatch = cm.OnPoolChange(s.invalidateMiningTemplate)
	if k, err := readKey(s.path); err == nil {
		s.public, err = types.ParseQdayAddress(k.Address)
		if err != nil {
			s.stopMiningWatch()
			cancel()
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.stopMiningWatch()
		cancel()
		return nil, err
	}
	if err := s.loadPendingBroadcasts(); err != nil {
		s.stopMiningWatch()
		cancel()
		return nil, err
	}
	if err := s.loadPoolPayouts(); err != nil {
		s.stopMiningWatch()
		cancel()
		return nil, err
	}
	go func() {
		defer close(s.rebroadcastDone)
		s.runRebroadcaster()
	}()
	return s, nil
}

func (s *Service) setError(err error) {
	// A relay can run before bootstrap connections are ready. The transaction
	// remains in the local pool and the background rebroadcaster retries it, so
	// having no peer at that instant is connection state, not a wallet error.
	if errors.Is(err, syncer.ErrNoPeers) {
		return
	}
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
	}
}

// Relay failures describe connectivity, not a failed local wallet operation.
// Expose them in network status and clear them on a successful retry.
func (s *Service) setRelayError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRelayError = ""
	if err != nil {
		s.lastRelayError = err.Error()
	}
}

func (s *Service) registerWallet(public types.QdayKeys) (wallet.ID, error) {
	ws, err := s.WM.Wallets()
	if err != nil {
		return 0, err
	}
	var w wallet.Wallet
	if len(ws) == 0 {
		w, err = s.WM.AddWallet(wallet.Wallet{Name: "QDAY"})
		if err != nil {
			return 0, err
		}
	} else {
		w = ws[0]
	}
	policy := public.Policy()
	if err = s.WM.AddAddresses(w.ID, wallet.Address{Address: policy.Address(), SpendPolicy: &policy}); err != nil {
		return 0, err
	}
	// Full indexing makes restoration independent of when an address is added.
	// Reapplying history to existing personal proofs would corrupt that index.
	if s.WM.IndexMode() != wallet.IndexModeFull {
		return 0, errors.New("native QDAY wallet requires full indexing")
	}
	return w.ID, nil
}

func (s *Service) attach(ctx context.Context, keys *types.QdayPrivateKeys) error {
	id, err := s.registerWallet(keys.Public)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.keys = keys
	s.public = keys.Public.Address()
	s.walletID = id
	s.mu.Unlock()
	s.invalidateMiningTemplate()
	return nil
}

func (s *Service) Create(ctx context.Context, password, phrase string) (string, error) {
	var seed [32]byte
	var err error
	if phrase == "" {
		seed = seedwallet.NewQdaySeed()
	} else {
		seed, err = seedwallet.QdaySeedFromPhrase(phrase)
		if err != nil {
			return "", err
		}
	}
	defer clear(seed[:])
	return s.createSeed(ctx, password, seed)
}

func (s *Service) createSeed(ctx context.Context, password string, seed [32]byte) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	defer clear(seed[:])
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("a wallet already exists in this data directory")
	}
	keys, err := types.QdayKeysFromSeed(seed)
	if err != nil {
		return "", err
	}
	if err = WriteKey(s.path, password, seed, keys.Public); err != nil {
		return "", err
	}
	if err = s.attach(ctx, &keys); err != nil {
		return "", err
	}
	return seedwallet.QdaySeedPhrase(seed), nil
}

func (s *Service) Unlock(ctx context.Context, password string) error {
	s.op.Lock()
	defer s.op.Unlock()
	seed, err := ReadKey(s.path, password)
	if err != nil {
		return err
	}
	defer clear(seed[:])
	keys, err := types.QdayKeysFromSeed(seed)
	if err != nil {
		return err
	}
	s.mu.Lock()
	expected := s.public
	s.mu.Unlock()
	if keys.Public.Address() != expected {
		return errors.New("keystore address mismatch")
	}
	return s.attach(ctx, &keys)
}

// Recovery requires the disk-encryption passphrase even when signing is
// unlocked, allowing the owner to verify a backup after a failed initial UI.
func (s *Service) Recovery(password string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	seed, err := ReadKey(s.path, password)
	if err != nil {
		return "", err
	}
	defer clear(seed[:])
	return seedwallet.QdaySeedPhrase(seed), nil
}

func (s *Service) Stop() {
	s.control.Lock()
	defer s.control.Unlock()
	s.stop()
}

func (s *Service) stop() {
	s.mu.Lock()
	cancel, done := s.runCancel, s.runDone
	s.runCancel = nil
	s.runDone = nil
	s.mode = "STOP"
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	s.mu.Lock()
	s.mode = "STOP"
	s.mu.Unlock()
}

func (s *Service) Lock() {
	s.control.Lock()
	defer s.control.Unlock()
	s.stop()
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.Lock()
	if s.keys != nil {
		clear(s.keys.Classical)
		*s.keys = types.QdayPrivateKeys{}
	}
	s.keys = nil
	s.mu.Unlock()
	s.invalidateMiningTemplate()
}
func (s *Service) Close() {
	s.cancel()
	<-s.rebroadcastDone
	if s.stopMiningWatch != nil {
		s.stopMiningWatch()
		s.stopMiningWatch = nil
	}
	s.Lock()
}

// Done signals an authenticated request to quit the local application.
func (s *Service) Done() <-chan struct{} { return s.ctx.Done() }

// RequestRestart is available only when the desktop launcher supervises this node.
func (s *Service) RequestRestart() error {
	if !s.RestartEnabled {
		return errors.New("restart is managed by the desktop launcher; restart this standalone node using its process manager")
	}
	s.restartRequested.Store(true)
	return nil
}

func (s *Service) RestartRequested() bool { return s.restartRequested.Load() }

func (s *Service) networkSynced() bool {
	if s.Manifest.Development {
		return true
	}
	if s.Syncer != nil {
		for _, p := range s.Syncer.Peers() {
			if p.Synced() && p.Err() == nil {
				return true
			}
		}
	}
	return false
}

func (s *Service) networkStatus() map[string]any {
	connections, inbound := 0, 0
	if s.Syncer != nil {
		for _, p := range s.Syncer.Peers() {
			if p.Err() != nil {
				continue
			}
			connections++
			if p.Inbound {
				inbound++
			}
		}
	}
	cs := s.CM.TipState()
	return map[string]any{
		"network":             cs.Network.Name,
		"height":              cs.Index.Height,
		"synced":              s.networkSynced(),
		"connections":         connections,
		"inboundConnections":  inbound,
		"outboundConnections": connections - inbound,
	}
}

func (s *Service) Start(threads int) error {
	s.control.Lock()
	defer s.control.Unlock()
	if threads < 1 || threads > min(runtime.NumCPU(), 256) {
		return fmt.Errorf("choose between 1 and %d CPU threads", min(runtime.NumCPU(), 256))
	}
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		return errors.New("unlock your wallet first")
	}
	if !s.networkSynced() {
		return errors.New("waiting for synchronization with a network peer")
	}
	if time.Now().Before(s.Manifest.Genesis.Timestamp) {
		return errors.New("waiting for the genesis start time")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.runCancel = cancel
	s.runDone = done
	s.mode = "MINE"
	s.threads = threads
	s.started = time.Now()
	s.hashes.Store(0)
	s.lastError = ""
	go s.run(ctx, done, threads, s.keys.Public)
	return nil
}

func (s *Service) run(ctx context.Context, done chan struct{}, threads int, pub types.QdayKeys) {
	defer close(done)
	defendDone := make(chan struct{})
	go func() {
		defer close(defendDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.CM.TipState().QdayActive(s.CM.Tip().Height + 1) {
					if _, err := s.Transfer(ctx, types.QdayAddress{}, types.ZeroCurrency, true); err != nil && !errors.Is(err, errNothingToDefend) && !errors.Is(err, errWalletSyncing) && !errors.Is(err, context.Canceled) {
						s.setError(err)
					}
				}
			}
		}
	}()
	defer func() { <-defendDone }()
	for ctx.Err() == nil {
		cs := s.CM.TipState()
		s.mu.Lock()
		if cs.QdayActive(cs.Index.Height + 1) {
			s.mode = "DEFEND"
		} else {
			s.mode = "MINE"
		}
		s.mu.Unlock()
		if !s.networkSynced() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
		b := mining.Candidate(cs, pub, s.CM.V2PoolTransactions(), types.CurrentTimestamp())
		if s.CM.Tip() != cs.Index {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		b, err := mining.Mine(attempt, cs, b, threads, &s.hashes)
		cancel()
		if err != nil {
			continue
		}
		if s.CM.Tip() != cs.Index {
			continue
		}
		if err = s.CM.AddBlocks([]types.Block{b}); err != nil {
			s.setError(err)
			continue
		}
		s.blocks.Add(1)
		if s.Syncer != nil {
			s.setRelayError(s.Syncer.BroadcastV2Header(b.Header()))
			s.setRelayError(s.Syncer.BroadcastV2BlockOutline(gateway.OutlineBlock(b, nil, nil)))
		}
		if s.Manifest.Development {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

var errNothingToDefend = errors.New("no outputs need DEFEND yet")
var errWalletSyncing = errors.New("wallet is synchronizing; retry shortly")

// outputs returns a consistent, paginated snapshot and excludes spends already
// in the mempool. No fixed first-page balance or selection limit hides coins.
func (s *Service) outputs(cs consensus.State, pub types.QdayAddress) ([]wallet.UnspentSiacoinElement, error) {
	return s.outputsAtPool(cs, pub, s.CM.V2PoolTransactions())
}

func (s *Service) outputsAtPool(cs consensus.State, pub types.QdayAddress, pool []types.V2Transaction) ([]wallet.UnspentSiacoinElement, error) {
	spent := make(map[types.SiacoinOutputID]bool)
	for _, txn := range pool {
		for _, in := range txn.SiacoinInputs {
			spent[in.Parent.ID] = true
		}
	}
	var result []wallet.UnspentSiacoinElement
	for offset := 0; ; offset += 500 {
		page, basis, err := s.WM.AddressSiacoinOutputs(types.Address(pub), false, offset, 500)
		if err != nil {
			return nil, err
		}
		if basis != cs.Index {
			return nil, errWalletSyncing
		}
		for _, e := range page {
			if !spent[e.ID] {
				result = append(result, e)
			}
		}
		if len(page) < 500 {
			break
		}
	}
	if s.CM.Tip() != cs.Index {
		return nil, errWalletSyncing
	}
	return result, nil
}

// Transfer renews shields by spending to self. Burned input value cannot be
// reintroduced as change or a miner fee. A small fee is paid for inclusion.
func (s *Service) Transfer(ctx context.Context, to types.QdayAddress, amount types.Currency, defend bool) (types.TransactionID, error) {
	mode := submitTransfer
	if defend {
		mode = submitDefend
	}
	return s.submitFor(ctx, "", to, amount, mode, nil)
}

// Burn permanently sends coins to the protocol's unspendable void address.
func (s *Service) Burn(ctx context.Context, amount types.Currency) (types.TransactionID, error) {
	return s.submitFor(ctx, "", types.QdayAddress{}, amount, submitBurn, nil)
}

// PublishProof funds and signs the proof before relaying it through the same
// transaction pool and P2P path as an ordinary transfer.
func (s *Service) PublishProof(ctx context.Context, witness [32]byte) (types.TransactionID, error) {
	return s.submitFor(ctx, "", types.QdayAddress{}, types.ZeroCurrency, submitProof, &witness)
}

type submitMode uint8

const (
	submitTransfer submitMode = iota
	submitDefend
	submitBurn
	submitProof
)

// Browser reviews name their source wallet so an import in another tab cannot
// silently change which wallet pays for the reviewed transaction.
func (s *Service) submitFor(ctx context.Context, from string, to types.QdayAddress, amount types.Currency, mode submitMode, witness *[32]byte) (types.TransactionID, error) {
	return s.submitReviewed(ctx, from, "", "", to, amount, mode, witness)
}

func (s *Service) submitReviewed(ctx context.Context, from, unit, customFee string, to types.QdayAddress, amount types.Currency, mode submitMode, witness *[32]byte) (types.TransactionID, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := ctx.Err(); err != nil {
		return types.TransactionID{}, err
	}
	s.mu.Lock()
	keys := s.keys
	s.mu.Unlock()
	if keys == nil {
		return types.TransactionID{}, errors.New("unlock your wallet first")
	}
	if from != "" && from != keys.Public.String() {
		return types.TransactionID{}, errors.New("the active wallet changed; review the transaction again")
	}
	cs := s.CM.TipState()
	checkUnit := func(state consensus.State) error {
		if unit != "" && unit != state.QdayUnits(state.Index.Height).ExactString() {
			return errors.New("QDAY denomination changed; review the amount and fee again")
		}
		return nil
	}
	if err := checkUnit(cs); err != nil {
		return types.TransactionID{}, err
	}
	fee := types.HastingsPerSiacoin.Div64(1000)
	if mode == submitProof {
		fee = cs.Network.Qday.ProofFee
	}
	if customFee != "" {
		var err error
		fee, err = ParseAmount(customFee, cs.QdayUnits(cs.Index.Height))
		if err != nil {
			return types.TransactionID{}, fmt.Errorf("invalid transaction fee: %w", err)
		}
	}
	if mode == submitProof && fee.Cmp(cs.Network.Qday.ProofFee) < 0 {
		return types.TransactionID{}, errors.New("fee is below the minimum proof publication fee")
	}
	if mode == submitProof {
		if witness == nil {
			return types.TransactionID{}, errors.New("missing QDAY proof")
		}
		if cs.QdayHeight != 0 {
			return types.TransactionID{}, errors.New("a QDAY proof is already confirmed")
		}
		if !consensus.VerifyQdayProof(cs.Network.Qday.Canary, *witness) {
			return types.TransactionID{}, errors.New("solution does not solve this network's challenge")
		}
		if s.pendingProof() != "" {
			return types.TransactionID{}, errors.New("a QDAY proof is already in the local mempool")
		}
	}
	outs, err := s.outputs(cs, keys.Public.Address())
	if err != nil {
		return types.TransactionID{}, err
	}
	// Ordinary payments prefer larger outputs: tiny old rewards must not hide
	// a sufficient newer output behind the input limit. DEFEND renews the
	// oldest shields first.
	sort.Slice(outs, func(i, j int) bool {
		if mode != submitDefend {
			vi := cs.QdayValue(outs[i].SiacoinElement, cs.Index.Height+2)
			vj := cs.QdayValue(outs[j].SiacoinElement, cs.Index.Height+2)
			if cmp := vi.Cmp(vj); cmp != 0 {
				return cmp > 0
			}
		}
		return outs[i].MaturityHeight < outs[j].MaturityHeight
	})
	required, overflow := amount.AddWithOverflow(fee)
	if overflow {
		return types.TransactionID{}, errors.New("amount plus fee overflows")
	}
	var total types.Currency
	txn := types.V2Transaction{MinerFee: fee}
	for _, out := range outs {
		v := cs.QdayValue(out.SiacoinElement, cs.Index.Height+2)
		if v.IsZero() {
			continue
		}
		if mode == submitDefend {
			if !cs.QdayActive(cs.Index.Height + 1) {
				return types.TransactionID{}, errNothingToDefend
			}
			start := max(out.MaturityHeight, cs.QdayHeight)
			if cs.Index.Height+max(uint64(2), cs.Network.Qday.ShieldBlocks/4) < start+cs.Network.Qday.ShieldBlocks {
				continue
			}
		}
		total = total.Add(v)
		txn.SiacoinInputs = append(txn.SiacoinInputs, types.V2SiacoinInput{Parent: out.SiacoinElement, SatisfiedPolicy: types.SatisfiedPolicy{Policy: keys.Public.Policy()}})
		if len(txn.SiacoinInputs) == 128 || (mode != submitDefend && total.Cmp(required) >= 0) {
			break
		}
	}
	var envelope consensus.QdayEnvelope
	envelope.Kind = consensus.QdayTransfer
	if mode == submitProof {
		if total.Cmp(fee) < 0 {
			return types.TransactionID{}, errors.New("insufficient confirmed balance to pay the proof fee")
		}
		envelope.Kind = consensus.QdayCanaryProof
		envelope.Witness = *witness
		if change := total.Sub(fee); !change.IsZero() {
			txn.SiacoinOutputs = []types.SiacoinOutput{{Value: change, Address: keys.Public.Policy().Address()}}
		}
	} else if mode == submitDefend {
		if total.Cmp(fee) <= 0 {
			return types.TransactionID{}, errNothingToDefend
		}
		txn.SiacoinOutputs = []types.SiacoinOutput{{Value: total.Sub(fee), Address: keys.Public.Policy().Address()}}
	} else if mode == submitBurn {
		if amount.IsZero() {
			return types.TransactionID{}, errors.New("burn amount must be positive")
		}
		if total.Cmp(required) < 0 {
			return types.TransactionID{}, errors.New("insufficient confirmed spendable balance (including fee); at most 128 inputs per burn")
		}
		txn.SiacoinOutputs = []types.SiacoinOutput{{Value: amount, Address: types.VoidAddress}}
		if change := total.Sub(amount).Sub(fee); !change.IsZero() {
			txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{Value: change, Address: keys.Public.Policy().Address()})
		}
	} else {
		if err = to.Validate(); err != nil {
			return types.TransactionID{}, err
		}
		if amount.IsZero() {
			return types.TransactionID{}, errors.New("amount must be positive")
		}
		if total.Cmp(required) < 0 {
			return types.TransactionID{}, errors.New("insufficient confirmed spendable balance (including fee); at most 128 inputs per send")
		}
		txn.SiacoinOutputs = []types.SiacoinOutput{{Value: amount, Address: types.Address(to)}}
		if change := total.Sub(amount).Sub(fee); !change.IsZero() {
			txn.SiacoinOutputs = append(txn.SiacoinOutputs, types.SiacoinOutput{Value: change, Address: keys.Public.Policy().Address()})
		}
	}
	txn.ArbitraryData = envelope.Encode()
	if err = mining.SignTransfer(ctx, cs, &txn, keys); err != nil {
		return types.TransactionID{}, err
	}
	select {
	case <-ctx.Done():
		return types.TransactionID{}, ctx.Err()
	default:
	}
	// Accumulator proofs may advance during signing; update them before relay.
	current := s.CM.TipState()
	if err := checkUnit(current); err != nil {
		return types.TransactionID{}, err
	}
	basis := current.Index
	txns, err := s.CM.UpdateV2TransactionSet([]types.V2Transaction{txn}, cs.Index, basis)
	if err != nil {
		return types.TransactionID{}, err
	}
	if _, err = s.CM.AddV2PoolTransactions(basis, txns); err != nil {
		return types.TransactionID{}, err
	}
	if err := s.rememberBroadcast(basis, txns); err != nil {
		s.setError(fmt.Errorf("failed to save pending transaction for rebroadcast: %w", err))
	}
	// rememberBroadcast wakes the persistent relay worker. Network I/O must
	// not hold the signing lock or turn an accepted payment into a UI failure.
	return txn.ID(), nil
}

func (s *Service) pendingProof() string {
	for _, txn := range s.CM.V2PoolTransactions() {
		if envelope, err := consensus.ParseQdayEnvelope(txn.ArbitraryData); err == nil && envelope.Kind == consensus.QdayCanaryProof {
			return txn.ID().String()
		}
	}
	return ""
}

func FormatAmount(v, unit types.Currency) string {
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(v.Big(), unit.Big(), r)
	if r.Sign() == 0 {
		return q.String()
	}
	decimals := len(unit.ExactString()) - 1
	return q.String() + "." + strings.TrimRight(strings.Repeat("0", decimals-len(r.String()))+r.String(), "0")
}

func ParseAmount(str string, unit types.Currency) (types.Currency, error) {
	decimals := len(unit.ExactString()) - 1
	p := strings.Split(str, ".")
	if len(p) > 2 || len(str) > 80 || len(p[0]) == 0 {
		return types.ZeroCurrency, errors.New("invalid amount")
	}
	frac := ""
	if len(p) == 2 {
		frac = p[1]
	}
	if len(frac) > decimals {
		return types.ZeroCurrency, errors.New("too many decimal places")
	}
	digits := p[0] + frac + strings.Repeat("0", decimals-len(frac))
	for _, c := range digits {
		if c < '0' || c > '9' {
			return types.ZeroCurrency, errors.New("invalid amount")
		}
	}
	return types.ParseCurrency(digits)
}

func supplyAmount(value, unit types.Currency) SupplyAmount {
	return SupplyAmount{QDAY: FormatAmount(value, unit), Atomic: value.ExactString()}
}

// Supply returns issuance and confirmed burns at one indexed chain state.
// BurnedSupply includes explicit void outputs, discarded transaction value and
// QDAY decay. CurrentSupply includes immature outputs because they already
// exist, but excludes every value that can no longer be spent.
func (s *Service) Supply() (SupplyStatus, error) {
	index, err := s.WM.Tip()
	if err != nil {
		return SupplyStatus{}, err
	}
	state, ok := s.CM.State(index.ID)
	if !ok || state.Index != index {
		return SupplyStatus{}, errors.New("supply chain state is unavailable")
	}

	s.supplyMu.Lock()
	defer s.supplyMu.Unlock()
	if s.supplyCached && s.supplyIndex == index {
		status := s.supplyStatus
		status.Synced = index == s.CM.Tip() && s.networkSynced()
		return status, nil
	}

	current, immature, outputs, err := s.WM.QdaySupply(state)
	if err != nil {
		return SupplyStatus{}, err
	}
	p := state.Network.Qday
	if p == nil {
		return SupplyStatus{}, errors.New("QDAY supply is unavailable on this network")
	}
	minedBlocks := min(index.Height, p.MiningBlocks)
	issued := p.PremineAmount.Add(p.Reward.Mul64(minedBlocks))
	if current.Cmp(issued) > 0 {
		return SupplyStatus{}, errors.New("current supply exceeds issued supply")
	} else if immature.Cmp(current) > 0 {
		return SupplyStatus{}, errors.New("immature supply exceeds current supply")
	}
	maximum := p.PremineAmount.Add(p.Reward.Mul64(p.MiningBlocks))
	unit := state.QdayUnits(index.Height)
	status := SupplyStatus{
		Network:             state.Network.Name,
		Height:              index.Height,
		Synced:              index == s.CM.Tip() && s.networkSynced(),
		UnitAtomic:          unit.ExactString(),
		IssuedSupply:        supplyAmount(issued, unit),
		BurnedSupply:        supplyAmount(issued.Sub(current), unit),
		CurrentSupply:       supplyAmount(current, unit),
		ImmatureSupply:      supplyAmount(immature, unit),
		CirculatingSupply:   supplyAmount(current.Sub(immature), unit),
		MaximumIssuedSupply: supplyAmount(maximum, unit),
		UnspentOutputs:      outputs,
	}
	s.supplyIndex, s.supplyStatus, s.supplyCached = index, status, true
	return status, nil
}

func (s *Service) Status() (map[string]any, error) {
	cs := s.CM.TipState()
	now := time.Now()
	genesisWait := s.Manifest.Genesis.Timestamp.Sub(now)
	if genesisWait < 0 {
		genesisWait = 0
	}
	s.mu.Lock()
	pub, unlocked, mode, threads, started, last, relayError := s.public, s.keys != nil, s.mode, s.threads, s.started, s.lastError, s.lastRelayError
	s.mu.Unlock()
	peers := 0
	if s.Syncer != nil {
		peers = len(s.Syncer.Peers())
	}
	scan, err := s.WM.Tip()
	if err != nil {
		return nil, err
	}
	unit := cs.QdayUnits(cs.Index.Height)
	r := map[string]any{"network": cs.Network.Name, "development": s.Manifest.Development, "height": cs.Index.Height, "genesis": s.Manifest.Genesis.ID(), "genesisTimestamp": s.Manifest.Genesis.Timestamp.Format(time.RFC3339), "genesisReady": genesisWait == 0, "genesisWaitSeconds": int64((genesisWait + time.Second - 1) / time.Second), "scanHeight": scan.Height, "synced": scan == cs.Index, "qdayHeight": cs.QdayHeight, "qday": cs.QdayActive(cs.Index.Height), "canary": fmt.Sprintf("%x", cs.Network.Qday.Canary), "unlocked": unlocked, "hasWallet": pub != (types.QdayAddress{}), "mode": mode, "threads": threads, "maxThreads": min(runtime.NumCPU(), 256), "peers": peers, "blocksFound": s.blocks.Load(), "lastError": last, "hashrate": float64(0), "balanceReady": false, "balance": nil, "immature": nil, "pending": nil, "fee": FormatAmount(types.HastingsPerSiacoin.Div64(1000), unit)}
	upgradeHeight := cs.Network.Qday.V1Height
	upgradeActive := cs.QdayV1Active(cs.Index.Height)
	var blocksUntilUpgrade uint64
	if upgradeHeight > cs.Index.Height {
		blocksUntilUpgrade = upgradeHeight - cs.Index.Height
	}
	r["protocolActivationHeight"] = upgradeHeight
	r["protocolActive"] = upgradeActive
	r["blocksUntilProtocolActivation"] = blocksUntilUpgrade
	r["unit"] = unit.ExactString()
	r["relayError"] = relayError
	r["canRestart"] = s.RestartEnabled
	pool := s.CM.V2PoolTransactions()
	r["mempoolTransactions"] = len(pool)
	if s.Syncer != nil {
		var bootstrap, bootstrapOutbound, regular, inbound int
		for _, p := range s.Syncer.Peers() {
			if p.Err() != nil {
				continue
			}
			if s.Syncer.IsBootstrap(p) {
				bootstrap++
				if !p.Inbound {
					bootstrapOutbound++
				}
			} else {
				regular++
			}
			if p.Inbound {
				inbound++
			}
		}
		network := map[string]any{"listenAddress": s.Syncer.Addr(), "bootstrapPeers": bootstrap, "bootstrapOutbound": bootstrapOutbound, "regularPeers": regular, "inboundPeers": inbound, "wireMagic": fmt.Sprintf("%x", gateway.QdayMagic())}
		if s.PortMapping != nil {
			network["mapping"] = s.PortMapping.Status()
		}
		r["p2p"] = network
	}
	r["proofFee"] = FormatAmount(cs.Network.Qday.ProofFee, unit)
	r["proofPending"] = s.pendingProof()
	r["activationDelay"] = cs.Network.Qday.ActivationDelay
	r["blockReward"] = FormatAmount(cs.BlockReward(), unit)
	r["blockIntervalSeconds"] = cs.Network.BlockInterval.Seconds()
	r["difficulty"] = cs.Difficulty.String()
	r["initialDifficulty"] = cs.Network.GenesisState().Difficulty.String()
	r["powTarget"] = cs.PoWTarget().String()
	r["observedHashrate"], r["hashrateWindowBlocks"] = s.observedNetworkHashrate(cs.Index, cs, observedHashrateWindow)
	r["difficultyAlgorithm"] = "Sia Oak / Final Cut"
	r["maturityBlocks"] = cs.Network.MaturityDelay
	networkSynced := s.networkSynced()
	r["networkSynced"] = networkSynced
	r["synced"] = scan == cs.Index && networkSynced
	if !started.IsZero() && mode != "STOP" {
		r["hashrate"] = float64(s.hashes.Load()) / time.Since(started).Seconds()
	}
	if pub == (types.QdayAddress{}) {
		r["balanceReady"], r["balance"], r["immature"], r["pending"] = true, "0", "0", "0"
		return r, nil
	}
	r["address"] = pub.String()
	outs, err := s.outputsAtPool(cs, pub, pool)
	if errors.Is(err, errWalletSyncing) {
		// An index catching up or a block arriving during pagination makes the
		// amounts unknown, not zero. Clients may retain a labelled prior snapshot.
		r["synced"] = false
		return r, nil
	} else if err != nil {
		return nil, err
	}
	var available types.Currency
	var next uint64
	for _, out := range outs {
		v := cs.QdayValue(out.SiacoinElement, cs.Index.Height)
		available = available.Add(v)
		if cs.QdayHeight != 0 && !v.IsZero() {
			expiry := max(out.MaturityHeight, cs.QdayHeight) + cs.Network.Qday.ShieldBlocks
			if next == 0 || expiry < next {
				next = expiry
			}
		}
	}
	bal, err := s.WM.AddressBalance(types.Address(pub))
	if err != nil {
		return nil, err
	}
	var pending types.Currency
	spentPending := make(map[types.SiacoinOutputID]bool)
	for _, tx := range pool {
		for _, input := range tx.SiacoinInputs {
			spentPending[input.Parent.ID] = true
		}
	}
	for _, tx := range pool {
		id := tx.ID()
		for i, o := range tx.SiacoinOutputs {
			if o.Address == types.Address(pub) && !spentPending[tx.SiacoinOutputID(id, i)] {
				pending = pending.Add(o.Value)
			}
		}
	}
	// Mature outputs and immature totals must refer to the same chain index.
	endScan, err := s.WM.Tip()
	if err != nil {
		return nil, err
	} else if endScan != cs.Index || s.CM.Tip() != cs.Index {
		r["synced"] = false
		return r, nil
	}
	r["balanceReady"] = true
	r["balance"] = FormatAmount(available, unit)
	r["immature"] = FormatAmount(bal.ImmatureSiacoins, unit)
	r["shieldUntil"] = next
	r["pending"] = FormatAmount(pending, unit)
	return r, nil
}
