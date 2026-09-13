package qday

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.sia.tech/core/types"
)

func feeRequest(t *testing.T, s *Service, route, amount, fee string) *httptest.ResponseRecorder {
	t.Helper()
	status, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"fromAddress": s.keys.Public.String(), "address": s.public.String(), "amount": amount, "unit": status["unit"].(string)}
	if fee != "" {
		body["fee"] = fee
	}
	if route == "proof" {
		body["witness"] = "01" + strings.Repeat("00", 31)
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19770/api/"+route, bytes.NewReader(b))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	response := httptest.NewRecorder()
	s.Handler(strings.Repeat("a", 64), "127.0.0.1:19770").ServeHTTP(response, request)
	return response
}

func TestWalletCustomFees(t *testing.T) {
	for _, tc := range []struct{ route, fee, want string }{
		{"send", "", "0.001"}, {"burn", "", "0.001"}, {"proof", "", "1"},
		{"send", "2.75", "2.75"}, {"burn", "2.75", "2.75"}, {"proof", "2.75", "2.75"},
		{"send", "0", "0"}, {"burn", "0.000000000000000000000001", "0.000000000000000000000001"},
	} {
		t.Run(tc.route+"/"+tc.fee, func(t *testing.T) {
			s := fundedBoundaryWallet(t)
			response := feeRequest(t, s, tc.route, "1", tc.fee)
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body.String())
			}
			pool := s.CM.V2PoolTransactions()
			fee, err := ParseAmount(tc.want, types.HastingsPerSiacoin)
			if err != nil || len(pool) != 1 || pool[0].MinerFee != fee {
				t.Fatal("selected fee did not reach the signed transaction", err)
			}
			var total types.Currency
			for _, out := range pool[0].SiacoinOutputs {
				total = total.Add(out.Value)
			}
			if total.Add(fee) != types.Siacoins(500000) {
				t.Fatal("custom fee changed the amount or lost change")
			}
			b := mineForTest(t, s)
			if b.MinerPayouts[0].Value != types.Siacoins(8).Add(fee) {
				t.Fatal("miner did not receive the selected fee")
			}
		})
	}
}

func TestWalletRejectsInvalidFees(t *testing.T) {
	s := fundedBoundaryWallet(t)
	for _, fee := range []string{"-1", "1e3", "NaN", "  ", "1,5", "1.2.3", "0.0000000000000000000000001", "1000000", FormatAmount(types.MaxCurrency, types.HastingsPerSiacoin)} {
		if r := feeRequest(t, s, "burn", "1", fee); r.Code != http.StatusBadRequest {
			t.Fatal("accepted invalid or unaffordable fee", fee, r.Body.String())
		}
	}
	if r := feeRequest(t, s, "proof", "0", "0.999"); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "minimum proof") {
		t.Fatal("proof fee fell below the consensus minimum", r.Body.String())
	}
	if len(s.CM.V2PoolTransactions()) != 0 {
		t.Fatal("failed fee review left a transaction in the mempool")
	}
}

func TestWalletCustomFeeAfterPQDay(t *testing.T) {
	s := fundedBoundaryWallet(t)
	if _, err := s.PublishProof(context.Background(), [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	mineForTest(t, s)
	mineForTest(t, s)
	if r := feeRequest(t, s, "burn", "1", "2.5"); r.Code != http.StatusOK {
		t.Fatal(r.Code, r.Body.String())
	}
	pool := s.CM.V2PoolTransactions()
	want := types.HastingsPerSiacoin.Div64(1_000_000).Mul64(5).Div64(2)
	if len(pool) != 1 || pool[0].MinerFee != want {
		t.Fatal("custom fee was parsed in the old denomination")
	}
	mineForTest(t, s)
}
