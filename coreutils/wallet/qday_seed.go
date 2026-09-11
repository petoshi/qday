package wallet

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"
)

// QdaySeedPhrase encodes 256 bits of entropy and its eight-bit BIP39 checksum
// using the upstream English word list. QDAY derives keys directly from this
// entropy; it does not use Bitcoin's PBKDF2/BIP32 derivation or BIP39 passphrases.
func QdaySeedPhrase(seed [32]byte) string {
	h := sha256.Sum256(seed[:])
	b := append(seed[:], h[0])
	words := make([]string, 24)
	for i := range words {
		var n uint16
		for j := 0; j < 11; j++ {
			bit := i*11 + j
			n = n<<1 | uint16((b[bit/8]>>(7-bit%8))&1)
		}
		words[i] = bip39EnglishWordList[n]
	}
	return strings.Join(words, " ")
}

func NewQdaySeed() (seed [32]byte) { rand.Read(seed[:]); return }

func QdaySeedFromPhrase(phrase string) (seed [32]byte, err error) {
	w := strings.Fields(phrase)
	if len(w) != 24 {
		return seed, errors.New("A QDAY seed phrase must contain exactly 24 words")
	}
	var b [33]byte
	for i, s := range w {
		n, ok := wordMap[s]
		if !ok {
			return seed, errors.New("unknown word in seed phrase")
		}
		for j := 0; j < 11; j++ {
			bit := i*11 + j
			b[bit/8] |= byte((n>>uint(10-j))&1) << uint(7-bit%8)
		}
	}
	copy(seed[:], b[:32])
	h := sha256.Sum256(seed[:])
	if h[0] != b[32] {
		clear(seed[:])
		return seed, errors.New("seed phrase checksum mismatch")
	}
	return
}
