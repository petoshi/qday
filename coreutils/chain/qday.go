package chain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.sia.tech/core/blake2b"
	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
)

// QdayManifest is the complete launch artifact. Changing any parameter changes
// the genesis ID, which the inherited P2P handshake enforces.
type QdayManifest struct {
	Network        consensus.Network `json:"network"`
	Genesis        types.Block       `json:"genesis"`
	Premine        types.QdayAddress `json:"premine"`
	GenesisMessage string            `json:"genesisMessage,omitempty"`
	Development    bool              `json:"development"`
}

// MarshalJSON omits the computed transaction/output IDs added by Sia's API
// JSON marshaler. Those read-only fields are not genesis inputs and would
// otherwise make the strict loader reject an exported manifest.
func (m QdayManifest) MarshalJSON() ([]byte, error) {
	type plainManifest QdayManifest
	type plainBlock types.Block
	type plainTransaction types.Transaction
	type genesisJSON struct {
		plainBlock
		Transactions []plainTransaction `json:"transactions,omitempty"`
	}
	transactions := make([]plainTransaction, len(m.Genesis.Transactions))
	for i, txn := range m.Genesis.Transactions {
		transactions[i] = plainTransaction(txn)
	}
	return json.Marshal(struct {
		plainManifest
		Genesis genesisJSON `json:"genesis"`
	}{plainManifest(m), genesisJSON{plainBlock(m.Genesis), transactions}})
}

// NewQdayManifest never generates a mainnet private key. The owner supplies a
// checksummed public address after saving a backup in their own wallet.
func NewQdayManifest(owner types.QdayAddress, timestamp time.Time, development bool) (QdayManifest, error) {
	return NewQdayManifestWithMessage(owner, timestamp, development, "")
}

// NewQdayManifestWithMessage commits a short UTF-8 inscription to the network
// domain and genesis transaction. The message is public forever.
func NewQdayManifestWithMessage(owner types.QdayAddress, timestamp time.Time, development bool, message string) (QdayManifest, error) {
	if err := owner.Validate(); err != nil {
		return QdayManifest{}, err
	}
	if timestamp.Unix() <= 0 || timestamp.Nanosecond() != 0 {
		return QdayManifest{}, errors.New("genesis timestamp must be positive whole UTC seconds")
	}
	if !utf8.ValidString(message) || len(message) > 256 || strings.TrimSpace(message) != message {
		return QdayManifest{}, errors.New("genesis message must be valid UTF-8, at most 256 bytes, without surrounding whitespace")
	}
	for _, r := range message {
		if unicode.IsControl(r) {
			return QdayManifest{}, errors.New("genesis message must not contain control characters")
		}
	}
	// Start at approximately 2^20 hashes per block for a CPU-accessible launch.
	n := consensus.Network{Name: "qday-mainnet", BlockInterval: time.Minute, MaturityDelay: 60, InitialTarget: types.BlockID{2: 16}}
	n.Qday = &consensus.QdayParams{RulesVersion: 2, Canary: consensus.QdayCanary(), Reward: types.Siacoins(8), ProofFee: types.Siacoins(1), MiningBlocks: 1_000_000, PremineAmount: types.Siacoins(500_000), ShieldBlocks: 1440, DecayBlocks: 10080, DecayStep: 60, ActivationDelay: 6, DefendBits: 20}
	if development {
		n.Name = "qday-devnet"
		n.BlockInterval = 2 * time.Second
		n.MaturityDelay = 2
		n.InitialTarget = types.BlockID{1: 15}
		n.Qday.ShieldBlocks = 12
		n.Qday.DecayBlocks = 24
		n.Qday.DecayStep = 2
		n.Qday.ActivationDelay = 1
		n.Qday.DefendBits = 8
		// Public devnet-only witness is scalar 1. This point cannot activate mainnet.
		n.Qday.Canary = [32]byte{0x58, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66}
	}
	n.HardforkDevAddr.Height = 0
	n.HardforkFoundation.Height = 0
	n.HardforkTax.Height = 0
	n.HardforkStorageProof.Height = 0
	n.HardforkOak.Height = 0
	n.HardforkOak.FixHeight = 0
	n.HardforkOak.GenesisTimestamp = timestamp.UTC()
	n.HardforkASIC.Height = 0
	n.HardforkASIC.NonceFactor = 1
	n.HardforkASIC.OakTarget = n.InitialTarget
	n.HardforkV2.AllowHeight = 1
	n.HardforkV2.RequireHeight = 1
	n.HardforkV2.FinalCutHeight = 1
	n.HardforkV2.EphemeralOutputHeight = 1
	// Domain and genesis both commit every parameter, timestamp and owner policy.
	var b []byte
	if message == "" {
		// Preserve the original domain encoding for manifests created before
		// genesis inscriptions were supported.
		b, _ = json.Marshal(struct {
			Network consensus.Network
			Owner   types.QdayAddress
		}{n, owner})
	} else {
		b, _ = json.Marshal(struct {
			Network        consensus.Network
			Owner          types.QdayAddress
			GenesisMessage string
		}{n, owner, message})
	}
	n.Qday.Domain = blake2b.Sum256(append([]byte("QDAY/network/v2"), b...))
	if err := n.Qday.Validate(); err != nil {
		return QdayManifest{}, err
	}
	// The premine is an exact fixed allocation, independent of mined issuance.
	premine := n.Qday.PremineAmount
	b, _ = json.Marshal(n)
	arbitraryData := [][]byte{append([]byte("QDAY/genesis/v2/"), b...)}
	if message != "" {
		arbitraryData = append(arbitraryData, []byte(message))
	}
	genesis := types.Block{Timestamp: timestamp.UTC(), Transactions: []types.Transaction{{SiacoinOutputs: []types.SiacoinOutput{{Value: premine, Address: types.Address(owner)}}, ArbitraryData: arbitraryData}}}
	return QdayManifest{Network: n, Genesis: genesis, Premine: owner, GenesisMessage: message, Development: development}, nil
}

func (m QdayManifest) Validate() error {
	expected, err := NewQdayManifestWithMessage(m.Premine, m.Genesis.Timestamp, m.Development, m.GenesisMessage)
	if err != nil {
		return err
	}
	a, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(expected)
	if !bytes.Equal(a, b) {
		return fmt.Errorf("QDAY manifest differs from fixed consensus rules; expected genesis %s", expected.Genesis.ID())
	}
	return nil
}

func QdayDevnet() QdayManifest {
	// This is public test money, never a mainnet premine address.
	keys, err := types.QdayKeysFromSeed([32]byte{0x51, 0x44, 0x41, 0x59})
	if err != nil {
		panic(err)
	}
	m, err := NewQdayManifest(keys.Public.Address(), time.Unix(1788998400, 0), true)
	if err != nil {
		panic(err)
	}
	return m
}
