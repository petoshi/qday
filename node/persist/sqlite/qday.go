package sqlite

import (
	"errors"
	"fmt"

	"go.sia.tech/core/consensus"
	"go.sia.tech/core/types"
	"go.sia.tech/walletd/v2/wallet"
)

// QdaySupply returns the value that remains in every unspent, non-void output
// at state.Index, plus the part that has not matured. The full index is required
// because burns and protocol decay apply across the entire UTXO set, not merely
// addresses in one wallet.
func (s *Store) QdaySupply(state consensus.State) (supply, immature types.Currency, outputs uint64, err error) {
	if s.indexMode != wallet.IndexModeFull {
		return types.ZeroCurrency, types.ZeroCurrency, 0, errors.New("QDAY supply requires a full chain index")
	}
	err = s.transaction(func(tx *txn) error {
		basis, err := getScanBasis(tx)
		if err != nil {
			return fmt.Errorf("failed to get supply index: %w", err)
		} else if basis != state.Index {
			return fmt.Errorf("supply index is %v, requested state is %v", basis, state.Index)
		}

		rows, err := tx.Query(`SELECT se.siacoin_value, se.maturity_height, sa.sia_address
			FROM siacoin_elements se
			INNER JOIN sia_addresses sa ON (se.address_id = sa.id)
			WHERE se.spent_index_id IS NULL`)
		if err != nil {
			return fmt.Errorf("failed to query unspent outputs: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var value types.Currency
			var maturityHeight uint64
			var address types.Address
			if err := rows.Scan(decode(&value), &maturityHeight, decode(&address)); err != nil {
				return fmt.Errorf("failed to scan unspent output: %w", err)
			}
			if address == types.VoidAddress {
				continue
			}
			value = state.QdayValue(types.SiacoinElement{
				SiacoinOutput:  types.SiacoinOutput{Value: value, Address: address},
				MaturityHeight: maturityHeight,
			}, state.Index.Height)
			var overflow bool
			supply, overflow = supply.AddWithOverflow(value)
			if overflow {
				return errors.New("QDAY supply overflows currency range")
			}
			if maturityHeight > state.Index.Height {
				immature, overflow = immature.AddWithOverflow(value)
				if overflow {
					return errors.New("QDAY immature supply overflows currency range")
				}
			}
			outputs++
		}
		return rows.Err()
	})
	return
}
