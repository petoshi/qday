package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// Published format vectors from https://bips.dev/350/#test-vectors-for-bech32m.
func TestBech32mVectors(t *testing.T) {
	valid := []string{
		"A1LQFN3A", "a1lqfn3a",
		"an83characterlonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber11sg7hg6",
		"abcdef1l7aum6echk45nj3s0wdvt2fg8x9yrzpqzd3ryx",
		"11" + strings.Repeat("l", 83) + "udsr8",
		"split1checkupstagehandshakeupstreamerranterredcaperredlc445v", "?1v759aa",
	}
	for _, value := range valid {
		hrp, data, err := bech32mDecode(value)
		if err != nil || bech32mEncode(hrp, data) != strings.ToLower(value) {
			t.Fatalf("valid vector rejected: %q: %v", value, err)
		}
	}
	invalid := []string{
		"\x201xj0phk", "\x7f1g6xzxy", "\x801vctc34",
		"an84characterslonghumanreadablepartthatcontainsthetheexcludedcharactersbioandnumber11d6pts4",
		"qyrz8wqd2c9m", "1qyrz8wqd2c9m", "y1b0jsk6g", "lt1igcx5c0", "in1muywd",
		"mm1crxm3i", "au1s5cgom", "M1VUXWEZ", "16plkw9", "1p2gdwpf", "a1LQFN3A",
		// This is the old Bech32 checksum, not Bech32m.
		"A12UEL5L",
	}
	for _, value := range invalid {
		if _, _, err := bech32mDecode(value); err == nil {
			t.Fatalf("invalid vector accepted: %q", value)
		}
	}
}

func TestQdayAddressCompatibilityAndErrors(t *testing.T) {
	k := QdayKeys{Classical: PublicKey{1, 2, 3}, Reserve: [32]byte{4, 5, 6}}
	a := k.Address()
	encoded := a.String()
	if len(encoded) != 64 || !strings.HasPrefix(encoded, "qday1p") {
		t.Fatal("unexpected address format")
	}
	b := k.Bytes()
	h := sha256.Sum256(append([]byte("QDAY/address/v1"), b...))
	legacy := "qday1" + hex.EncodeToString(append(b, h[:4]...))
	for _, value := range []string{encoded, strings.ToUpper(encoded), legacy} {
		got, err := ParseQdayAddress(value)
		if err != nil || got != a || Address(got) != k.Policy().Address() {
			t.Fatalf("address changed ownership: %v", err)
		}
	}
	jsonBytes, _ := json.Marshal(a)
	var restored QdayAddress
	if err := json.Unmarshal(jsonBytes, &restored); err != nil || restored != a {
		t.Fatal("address JSON changed the policy hash")
	}
	for i := range len(encoded) {
		for _, c := range bech32Alphabet {
			if byte(c) == encoded[i] {
				continue
			}
			mutated := []byte(encoded)
			mutated[i] = byte(c)
			if _, err := ParseQdayAddress(string(mutated)); err == nil {
				t.Fatalf("single-symbol error at %d accepted", i)
			}
		}
	}
	data, _ := bech32ConvertBits(a[:], 8, 5, true)
	badPadding := append([]byte{1}, data...)
	badPadding[len(badPadding)-1] |= 1
	for _, value := range []string{
		encoded[:63], encoded + "q", "Q" + encoded[1:], " " + encoded,
		bech32mEncode("other", append([]byte{1}, data...)),
		bech32mEncode("qday", append([]byte{2}, data...)),
		bech32mEncode("qday", badPadding), (QdayAddress{}).String(),
		legacy[:140] + "z", Address(a).String(),
	} {
		if _, err := ParseQdayAddress(value); err == nil {
			t.Fatalf("invalid address accepted: %q", value)
		}
	}
}

func FuzzQdayAddress(f *testing.F) {
	f.Add((QdayAddress{1, 2, 3}).String())
	f.Add("qday1")
	f.Fuzz(func(t *testing.T, value string) {
		if a, err := ParseQdayAddress(value); err == nil {
			got, err := ParseQdayAddress(a.String())
			if err != nil || got != a || len(a.String()) != 64 {
				t.Fatal("accepted address failed canonical round trip")
			}
		}
	})
}
