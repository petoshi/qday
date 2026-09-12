package main

import "testing"

func TestMainnetCanaryVector(t *testing.T) {
	result, err := derive()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("expected two attempts, got %d", len(result.Attempts))
	}
	if first := result.Attempts[0]; first.Counter != 0 || first.CanonicalPoint || first.Accepted || first.BLAKE2b256 != "f4b17b5acddbac3db93e510d203c82ea9d6e869070a40f275581d950723dc174" {
		t.Fatalf("unexpected counter 0 transcript: %+v", first)
	}
	if second := result.Attempts[1]; second.Counter != 1 || !second.CanonicalPoint || !second.Accepted || second.BLAKE2b256 != "29e995bdd3f9d8cdc0e6c85d7222f9d5e627010f1ae6a7b0a77eafb0c06aa123" {
		t.Fatalf("unexpected counter 1 transcript: %+v", second)
	}
	const challenge = "7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771"
	if result.SelectedCounter != 1 || result.DerivedChallenge != challenge {
		t.Fatalf("unexpected challenge: counter=%d point=%s", result.SelectedCounter, result.DerivedChallenge)
	}
}
