package qday

import (
	"context"
	"testing"
	"time"

	"go.sia.tech/core/types"
	seedwallet "go.sia.tech/coreutils/wallet"
)

func TestPoolPayoutPersistenceFailureRollsBackMemory(t *testing.T) {
	s := newTestService(t)
	s.poolPayoutPath = t.TempDir() // atomic rename onto a directory must fail

	created := time.Unix(1, 0).UTC()
	record := poolPayoutRecord{RequestID: "new", Digest: "digest", Status: "queued", CreatedAt: created}
	if _, err := s.savePoolPayoutRecord(record); err == nil {
		t.Fatal("expected payout persistence failure")
	}
	s.poolPayoutMu.Lock()
	_, exists := s.poolPayouts[record.RequestID]
	s.poolPayoutMu.Unlock()
	if exists {
		t.Fatal("failed payout persistence left a phantom idempotency record")
	}

	previous := poolPayoutRecord{RequestID: "existing", Digest: "old", Status: "queued", CreatedAt: created}
	s.poolPayoutMu.Lock()
	s.poolPayouts[previous.RequestID] = previous
	s.poolPayoutMu.Unlock()
	updated := previous
	updated.Digest = "new"
	if _, err := s.savePoolPayoutRecord(updated); err == nil {
		t.Fatal("expected payout update persistence failure")
	}
	s.poolPayoutMu.Lock()
	restored := s.poolPayouts[previous.RequestID]
	s.poolPayoutMu.Unlock()
	if restored.Digest != previous.Digest {
		t.Fatalf("failed payout update was kept in memory: %+v", restored)
	}
}

func TestPoolPayoutIsAtomicAndIdempotent(t *testing.T) {
	s := newTestService(t)
	if _, err := s.Create(context.Background(), "pool-payout-password", seedwallet.QdaySeedPhrase([32]byte{0x51, 0x44, 0x41, 0x59})); err != nil {
		t.Fatal(err)
	}
	synced(t, s)
	if defend, err := s.PoolDefend(context.Background()); err != nil || defend.Status != "idle" || defend.Transaction != "" {
		t.Fatalf("idle pool DEFEND failed: %+v %v", defend, err)
	}
	a, err := types.QdayKeysFromSeed([32]byte{0xa1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := types.QdayKeysFromSeed([32]byte{0xb2})
	if err != nil {
		t.Fatal(err)
	}
	unit := s.CM.TipState().QdayUnits(s.CM.Tip().Height)
	req := PoolPayoutRequest{
		RequestID:          "block-100-batch-1",
		FromAddress:        s.keys.Public.String(),
		ExpectedUnitAtomic: unit.ExactString(),
		FeeAtomic:          types.HastingsPerSiacoin.Div64(1000).ExactString(),
		Outputs: []PoolPayoutOutput{
			{Address: a.Public.String(), AmountAtomic: types.Siacoins(10).ExactString()},
			{Address: b.Public.String(), AmountAtomic: types.Siacoins(20).ExactString()},
		},
	}
	first, err := s.PoolPayout(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PoolPayout(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	} else if second.Transaction != first.Transaction || second.Outputs != 2 {
		t.Fatalf("idempotent retry changed payout: %+v %+v", first, second)
	} else if len(s.CM.V2PoolTransactions()) != 1 {
		t.Fatalf("idempotent retry created %d transactions", len(s.CM.V2PoolTransactions()))
	}
	changed := req
	changed.Outputs = append([]PoolPayoutOutput(nil), req.Outputs...)
	changed.Outputs[0].AmountAtomic = types.Siacoins(11).ExactString()
	if _, err := s.PoolPayout(context.Background(), changed); err == nil {
		t.Fatal("requestID accepted different payout contents")
	}

	mineForTest(t, s)
	for _, check := range []struct {
		address types.Address
		value   types.Currency
	}{{a.Public.Policy().Address(), types.Siacoins(10)}, {b.Public.Policy().Address(), types.Siacoins(20)}} {
		outputs, _, err := s.WM.AddressSiacoinOutputs(check.address, false, 0, 10)
		if err != nil || len(outputs) != 1 || outputs[0].SiacoinOutput.Value != check.value {
			t.Fatalf("pool recipient output mismatch: %v %+v", err, outputs)
		}
	}
	confirmed, err := s.PoolPayout(context.Background(), req)
	if err != nil || confirmed.Transaction != first.Transaction || confirmed.Status != "confirming" {
		t.Fatalf("confirmed payout retry failed: %+v %v", confirmed, err)
	}
	for i := 0; i < poolPayoutFinality-1; i++ {
		mineForTest(t, s)
	}
	final, err := s.PoolPayout(context.Background(), req)
	if err != nil || final.Transaction != first.Transaction || final.Status != "confirmed" {
		t.Fatalf("final payout status failed: %+v %v", final, err)
	}
	s.poolPayoutMu.Lock()
	record := s.poolPayouts[req.RequestID]
	s.poolPayoutMu.Unlock()
	if record.Status != "confirmed" || len(record.Transaction) != 0 {
		t.Fatalf("confirmed payout was not compacted: %+v", record)
	}
}
