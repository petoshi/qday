package types

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const bech32Alphabet = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
const bech32mConstant uint32 = 0x2bc830a3

// QdayAddress is the full 256-bit spend-policy hash. Its canonical text is
// 64-character Bech32m: HRP qday, version 1, the hash, and six checksum symbols.
// Public keys and both signatures are revealed and checked when spending.
type QdayAddress Address

func (a QdayAddress) Validate() error {
	if a == (QdayAddress{}) {
		return errors.New("zero QDAY address")
	}
	return nil
}

func (a QdayAddress) String() string {
	data, _ := bech32ConvertBits(a[:], 8, 5, true)
	return bech32mEncode("qday", append([]byte{1}, data...))
}

func (a QdayAddress) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

func (a *QdayAddress) UnmarshalText(b []byte) (err error) {
	*a, err = ParseQdayAddress(string(b))
	return
}

// ParseQdayAddress accepts canonical Bech32m and the legacy public-key address.
// Both resolve to the same full policy hash; no address registry is needed.
func ParseQdayAddress(s string) (a QdayAddress, err error) {
	if len(s) == 141 && strings.HasPrefix(s, "qday1") {
		b, err := hex.DecodeString(s[5:])
		if err != nil {
			return a, errors.New("invalid legacy QDAY address encoding")
		}
		h := sha256.Sum256(append([]byte("QDAY/address/v1"), b[:64]...))
		if !bytes.Equal(h[:4], b[64:]) {
			return a, errors.New("QDAY address checksum mismatch")
		}
		var k QdayKeys
		copy(k.Classical[:], b[:32])
		copy(k.Reserve[:], b[32:64])
		if err := k.Validate(); err != nil {
			return a, err
		}
		return k.Address(), nil
	}
	if len(s) != 64 {
		return a, errors.New("QDAY addresses must contain 64 characters")
	}
	hrp, data, err := bech32mDecode(s)
	if err != nil {
		return a, err
	}
	if hrp != "qday" || len(data) != 53 || data[0] != 1 {
		return a, errors.New("unsupported QDAY address network or version")
	}
	b, err := bech32ConvertBits(data[1:], 5, 8, false)
	if err != nil || len(b) != 32 {
		return a, errors.New("invalid QDAY address padding or payload")
	}
	copy(a[:], b)
	return a, a.Validate()
}

// Bech32m follows BIP 350, including mixed-case rejection and the 90-character
// limit. QDAY adds its own fixed payload length and address version above.
func bech32Polymod(values []byte) uint32 {
	gen := [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, value := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(value)
		for i, g := range gen {
			if top>>i&1 != 0 {
				chk ^= g
			}
		}
	}
	return chk
}

func bech32Expand(hrp string) []byte {
	values := make([]byte, 0, 2*len(hrp)+1)
	for i := range len(hrp) {
		values = append(values, hrp[i]>>5)
	}
	values = append(values, 0)
	for i := range len(hrp) {
		values = append(values, hrp[i]&31)
	}
	return values
}

func bech32mEncode(hrp string, data []byte) string {
	values := append(bech32Expand(hrp), data...)
	check := bech32Polymod(append(values, make([]byte, 6)...)) ^ bech32mConstant
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, v := range data {
		b.WriteByte(bech32Alphabet[v])
	}
	for i := 5; i >= 0; i-- {
		b.WriteByte(bech32Alphabet[check>>uint(5*i)&31])
	}
	return b.String()
}

func bech32mDecode(s string) (string, []byte, error) {
	if len(s) < 8 || len(s) > 90 || (s != strings.ToLower(s) && s != strings.ToUpper(s)) {
		return "", nil, errors.New("invalid Bech32m length or mixed case")
	}
	for i := range len(s) {
		if s[i] < 33 || s[i] > 126 {
			return "", nil, errors.New("invalid Bech32m character")
		}
	}
	s = strings.ToLower(s)
	sep := strings.LastIndexByte(s, '1')
	if sep < 1 || sep+7 > len(s) {
		return "", nil, errors.New("invalid Bech32m separator")
	}
	hrp := s[:sep]
	data := make([]byte, len(s)-sep-1)
	for i := range data {
		v := strings.IndexByte(bech32Alphabet, s[sep+1+i])
		if v < 0 {
			return "", nil, errors.New("invalid Bech32m data character")
		}
		data[i] = byte(v)
	}
	if bech32Polymod(append(bech32Expand(hrp), data...)) != bech32mConstant {
		return "", nil, errors.New("QDAY address checksum mismatch")
	}
	return hrp, data[:len(data)-6], nil
}

func bech32ConvertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var acc uint32
	var bits uint
	mask := uint32(1<<to - 1)
	var result []byte
	for _, value := range data {
		if uint32(value)>>from != 0 {
			return nil, errors.New("invalid Bech32m data value")
		}
		acc = (acc<<from | uint32(value)) & (1<<(from+to-1) - 1)
		bits += from
		for bits >= to {
			bits -= to
			result = append(result, byte(acc>>bits&mask))
		}
	}
	if pad && bits != 0 {
		result = append(result, byte(acc<<(to-bits)&mask))
	} else if !pad && (bits >= from || acc<<(to-bits)&mask != 0) {
		return nil, errors.New("noncanonical Bech32m padding")
	}
	return result, nil
}
