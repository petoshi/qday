package qday

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func testSwapContract(t *testing.T, recipient, refund types.QdayKeys, secret [32]byte, refundHeight uint64) AtomicSwapContract {
	t.Helper()
	hash := sha256.Sum256(secret[:])
	return AtomicSwapContract{
		Recipient: atomicSwapKeys(recipient), Refund: atomicSwapKeys(refund),
		SecretHash: hex.EncodeToString(hash[:]), RefundHeight: refundHeight,
	}
}

func TestAtomicSwapWalletClaimAndRefund(t *testing.T) {
	t.Run("claim", func(t *testing.T) {
		s := newTestService(t)
		if _, err := s.Create(context.Background(), "swap-claim-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
			t.Fatal(err)
		}
		synced(t, s)
		s.Manifest.Network.Qday.V1Height = 1
		refund, err := types.QdayKeysFromSeed([32]byte{9, 9, 9})
		if err != nil {
			t.Fatal(err)
		}
		secret := [32]byte{1, 3, 3, 7}
		contract := testSwapContract(t, s.keys.Public, refund.Public, secret, 100)
		view, err := s.WatchAtomicSwap(contract)
		if err != nil {
			t.Fatal(err)
		}
		address, err := types.ParseQdayAddress(view.Address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Transfer(context.Background(), address, types.Siacoins(25), false); err != nil {
			t.Fatal(err)
		}
		mineForTest(t, s)
		view, err = s.AtomicSwapStatus(contract)
		if err != nil || len(view.Outputs) != 1 || view.Outputs[0].ValueAtomic != types.Siacoins(25).ExactString() {
			t.Fatalf("funded swap missing: %+v, %v", view, err)
		}
		id, err := s.ClaimAtomicSwap(context.Background(), AtomicSwapSpendRequest{
			Contract: contract, Output: view.Outputs[0].ID, FeeAtomic: "0", Secret: hex.EncodeToString(secret[:]),
		})
		if err != nil {
			t.Fatal(err)
		}
		pool := s.CM.V2PoolTransactions()
		if len(pool) != 1 || pool[0].ID() != id {
			t.Fatalf("claim was not added to mempool: %v", pool)
		}
		policy := pool[0].SiacoinInputs[0].SatisfiedPolicy.Policy
		if !types.IsQdaySwapSpendPolicy(policy) {
			t.Fatal("claim did not use the atomic-swap policy")
		}
		if err := consensus.ValidateV2Transaction(consensus.NewMidState(s.CM.TipState()), pool[0]); err != nil {
			t.Fatal("mempool claim failed consensus: ", err)
		}
		retried, err := s.ClaimAtomicSwap(context.Background(), AtomicSwapSpendRequest{
			Contract: contract, Output: view.Outputs[0].ID, FeeAtomic: "0", Secret: hex.EncodeToString(secret[:]),
		})
		if err != nil || retried != id || len(s.CM.V2PoolTransactions()) != 1 {
			t.Fatalf("claim retry was not idempotent: %v %v", retried, err)
		}
	})

	t.Run("refund", func(t *testing.T) {
		s := newTestService(t)
		if _, err := s.Create(context.Background(), "swap-refund-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
			t.Fatal(err)
		}
		synced(t, s)
		s.Manifest.Network.Qday.V1Height = 1
		recipient, err := types.QdayKeysFromSeed([32]byte{8, 8, 8})
		if err != nil {
			t.Fatal(err)
		}
		secret := [32]byte{4, 2}
		contract := testSwapContract(t, recipient.Public, s.keys.Public, secret, 2)
		view, err := s.WatchAtomicSwap(contract)
		if err != nil {
			t.Fatal(err)
		}
		address, _ := types.ParseQdayAddress(view.Address)
		if _, err := s.Transfer(context.Background(), address, types.Siacoins(10), false); err != nil {
			t.Fatal(err)
		}
		mineForTest(t, s)
		view, err = s.AtomicSwapStatus(contract)
		if err != nil || len(view.Outputs) != 1 {
			t.Fatalf("funded refund swap missing: %+v, %v", view, err)
		}
		request := AtomicSwapSpendRequest{Contract: contract, Output: view.Outputs[0].ID, FeeAtomic: "0"}
		if _, err := s.RefundAtomicSwap(context.Background(), request); err == nil {
			t.Fatal("refund succeeded before its height")
		}
		mineForTest(t, s)
		id, err := s.RefundAtomicSwap(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		pool := s.CM.V2PoolTransactions()
		if len(pool) != 1 || pool[0].ID() != id {
			t.Fatalf("refund was not added to mempool: %v", pool)
		}
		if err := consensus.ValidateV2Transaction(consensus.NewMidState(s.CM.TipState()), pool[0]); err != nil {
			t.Fatal("mempool refund failed consensus: ", err)
		}
	})
}

func TestAtomicSwapWatchAfterFunding(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "swap-late-watch-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	s.Manifest.Network.Qday.V1Height = 1
	other, err := types.QdayKeysFromSeed([32]byte{7, 7, 7})
	if err != nil {
		t.Fatal(err)
	}
	contract := testSwapContract(t, other.Public, s.keys.Public, [32]byte{9, 9}, 100)
	derived, err := s.DeriveAtomicSwap(contract)
	if err != nil {
		t.Fatal(err)
	}
	address, err := types.ParseQdayAddress(derived.Address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transfer(context.Background(), address, types.Siacoins(3), false); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s)
	view, err := s.WatchAtomicSwap(contract)
	if err != nil || len(view.Outputs) != 1 || view.Outputs[0].ValueAtomic != types.Siacoins(3).ExactString() {
		t.Fatalf("late watch did not discover funded output: %+v, %v", view, err)
	}
	if again, err := s.WatchAtomicSwap(contract); err != nil || len(again.Outputs) != 1 {
		t.Fatalf("repeated watch was not idempotent: %+v, %v", again, err)
	}
}

func TestAtomicSwapHTTPDerivation(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "swap-http-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	other, err := types.QdayKeysFromSeed([32]byte{6, 6, 6})
	if err != nil {
		t.Fatal(err)
	}
	contract := testSwapContract(t, s.keys.Public, other.Public, [32]byte{5}, 200)
	body, _ := json.Marshal(contract)
	token := "atomic-swap-test-token"
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19770/api/swap/derive", bytes.NewReader(body))
	request.Host = "127.0.0.1:19770"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	s.Handler(token, "127.0.0.1:19770").ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("derive API returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var view AtomicSwapView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil || view.Address == "" {
		t.Fatalf("invalid derive API response: %+v, %v", view, err)
	}
}
