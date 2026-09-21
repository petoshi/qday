// qday-canary independently reproduces the QDAY mainnet NUMS canary and
// compares it with the point committed by a network manifest.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"

	"filippo.io/edwards25519"
	"golang.org/x/crypto/blake2b"
)

const (
	domain         = "QDAY/Edwards25519/NUMS/canary/v1/"
	maxCounter     = 1 << 20
	base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

type attempt struct {
	Counter         uint64 `json:"counter"`
	CounterLE       string `json:"counterLE"`
	InputHex        string `json:"inputHex"`
	BLAKE2b256      string `json:"blake2b256"`
	CanonicalPoint  bool   `json:"canonicalPoint"`
	SubgroupPoint   string `json:"subgroupPoint,omitempty"`
	Accepted        bool   `json:"accepted"`
	RejectionReason string `json:"rejectionReason,omitempty"`
}

type transcript struct {
	Scheme            string    `json:"scheme"`
	Domain            string    `json:"domain"`
	CounterEncoding   string    `json:"counterEncoding"`
	Hash              string    `json:"hash"`
	PointEncoding     string    `json:"pointEncoding"`
	Cofactor          uint8     `json:"cofactor"`
	SelectionRule     string    `json:"selectionRule"`
	Attempts          []attempt `json:"attempts"`
	SelectedCounter   uint64    `json:"selectedCounter"`
	DerivedChallenge  string    `json:"derivedChallenge"`
	SolanaAddress     string    `json:"solanaAddress"`
	ManifestChallenge string    `json:"manifestChallenge"`
	Match             bool      `json:"match"`
}

// base58Encode encodes raw bytes using the unchecksummed Bitcoin alphabet used
// for Solana public keys. No hash, checksum, or key derivation is applied.
func base58Encode(src []byte) string {
	if len(src) == 0 {
		return ""
	}

	leadingZeroes := 0
	for leadingZeroes < len(src) && src[leadingZeroes] == 0 {
		leadingZeroes++
	}

	value := new(big.Int).SetBytes(src)
	base := big.NewInt(58)
	remainder := new(big.Int)
	encoded := make([]byte, 0, len(src)*138/100+1)
	for value.Sign() > 0 {
		value.DivMod(value, base, remainder)
		encoded = append(encoded, base58Alphabet[remainder.Int64()])
	}
	for i := 0; i < leadingZeroes; i++ {
		encoded = append(encoded, base58Alphabet[0])
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded)
}

func derive() (transcript, error) {
	result := transcript{
		Scheme:          "QDAY Edwards25519 NUMS canary v1",
		Domain:          domain,
		CounterEncoding: "unsigned 64-bit little-endian",
		Hash:            "BLAKE2b-256",
		PointEncoding:   "canonical compressed Edwards25519",
		Cofactor:        8,
		SelectionRule:   "first canonical non-identity point after cofactor clearing",
	}

	for counter := uint64(0); counter < maxCounter; counter++ {
		input := make([]byte, len(domain)+8)
		copy(input, domain)
		binary.LittleEndian.PutUint64(input[len(domain):], counter)
		hash := blake2b.Sum256(input)
		entry := attempt{
			Counter:    counter,
			CounterLE:  hex.EncodeToString(input[len(domain):]),
			InputHex:   hex.EncodeToString(input),
			BLAKE2b256: hex.EncodeToString(hash[:]),
		}

		point, err := new(edwards25519.Point).SetBytes(hash[:])
		if err != nil || !bytes.Equal(point.Bytes(), hash[:]) {
			entry.RejectionReason = "hash is not a canonical Edwards25519 point encoding"
			result.Attempts = append(result.Attempts, entry)
			continue
		}
		entry.CanonicalPoint = true
		point.MultByCofactor(point)
		entry.SubgroupPoint = hex.EncodeToString(point.Bytes())
		if point.Equal(edwards25519.NewIdentityPoint()) == 1 {
			entry.RejectionReason = "cofactor-cleared point is the identity"
			result.Attempts = append(result.Attempts, entry)
			continue
		}

		entry.Accepted = true
		result.Attempts = append(result.Attempts, entry)
		result.SelectedCounter = counter
		result.DerivedChallenge = entry.SubgroupPoint
		result.SolanaAddress = base58Encode(point.Bytes())
		return result, nil
	}
	return transcript{}, errors.New("no canary point found within the counter limit")
}

func readManifestCanary(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var manifest struct {
		Network struct {
			Qday struct {
				Canary []byte `json:"canary"`
			} `json:"qday"`
		} `json:"network"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	if len(manifest.Network.Qday.Canary) != 32 {
		return "", fmt.Errorf("manifest canary is %d bytes, expected 32", len(manifest.Network.Qday.Canary))
	}
	return hex.EncodeToString(manifest.Network.Qday.Canary), nil
}

func printText(result transcript) {
	fmt.Println("QDAY mainnet canary derivation")
	fmt.Println("domain:", result.Domain)
	fmt.Println("counter encoding:", result.CounterEncoding)
	fmt.Println("hash:", result.Hash)
	fmt.Println("point encoding:", result.PointEncoding)
	fmt.Println("cofactor:", result.Cofactor)
	for _, entry := range result.Attempts {
		fmt.Printf("counter %d\n", entry.Counter)
		fmt.Println("  input:", entry.InputHex)
		fmt.Println("  BLAKE2b-256:", entry.BLAKE2b256)
		if entry.Accepted {
			fmt.Println("  canonical point: yes")
			fmt.Println("  subgroup point:", entry.SubgroupPoint)
			fmt.Println("  result: accepted")
		} else {
			fmt.Println("  canonical point:", map[bool]string{true: "yes", false: "no"}[entry.CanonicalPoint])
			fmt.Println("  result: rejected: " + entry.RejectionReason)
		}
	}
	fmt.Println("derived challenge:", result.DerivedChallenge)
	fmt.Println("manifest challenge:", result.ManifestChallenge)
	fmt.Println("Solana address:", result.SolanaAddress)
	fmt.Println("match:", result.Match)
}

func run() error {
	manifestPath := flag.String("manifest", "qday-mainnet.json", "QDAY network manifest to verify")
	jsonOutput := flag.Bool("json", false, "write the complete derivation transcript as JSON")
	flag.Parse()

	result, err := derive()
	if err != nil {
		return err
	}
	result.ManifestChallenge, err = readManifestCanary(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	result.Match = result.DerivedChallenge == result.ManifestChallenge
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return err
		}
	} else {
		printText(result)
	}
	if !result.Match {
		return errors.New("derived challenge does not match the manifest")
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "qday-canary:", err)
		os.Exit(1)
	}
}
