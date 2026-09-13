package qday_test

import (
	"math/big"
	"testing"

	"go.sia.tech/core/types"
	"go.sia.tech/coreutils/chain"
)

func TestQdayRewardAndDecayBoundaries(t *testing.T) {
	m := chain.QdayDevnet()
	s := m.Network.GenesisState()
	p := s.Network.Qday
	for _, height := range []uint64{0, p.MiningBlocks - 2, p.MiningBlocks - 1, p.MiningBlocks, p.MiningBlocks + 1} {
		s.Index.Height = height
		want := p.Reward
		if height >= p.MiningBlocks {
			want = types.ZeroCurrency
		}
		if s.BlockReward() != want {
			t.Fatalf("incorrect next-block reward at height %d", height)
		}
	}
	s.QdayHeight = 100
	if s.QdayUnits(99) != types.HastingsPerSiacoin || s.QdayUnits(100) != types.HastingsPerSiacoin.Div64(1_000_000) {
		t.Fatal("denomination changed on the wrong block")
	}
	for _, birth := range []uint64{0, 99, 100, 105} {
		start := max(birth, s.QdayHeight)
		for _, value := range []types.Currency{types.NewCurrency64(1), types.Siacoins(8), types.MaxCurrency} {
			output := types.SiacoinElement{MaturityHeight: birth, SiacoinOutput: types.SiacoinOutput{Value: value}}
			previous := value
			for height := uint64(99); height <= start+p.ShieldBlocks+p.DecayBlocks+1; height++ {
				want := value.Big()
				if height > start+p.ShieldBlocks {
					age := (height - start - p.ShieldBlocks) / p.DecayStep * p.DecayStep
					remaining := p.DecayBlocks - min(age, p.DecayBlocks)
					want.Mul(want, new(big.Int).SetUint64(remaining)).Div(want, new(big.Int).SetUint64(p.DecayBlocks))
				}
				got := s.QdayValue(output, height)
				if got.Big().Cmp(want) != 0 || got.Cmp(previous) > 0 {
					t.Fatalf("decay changed value incorrectly at height %d (birth %d)", height, birth)
				}
				previous = got
			}
		}
	}
}
