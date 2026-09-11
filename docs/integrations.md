# Integration guide

This guide is for mining pools, exchanges, custodians and block explorers that
already support Sia. QDAY keeps Sia's unspent-output model and much of its
synchronization code, but its network identity, addresses, signatures,
transactions and post-QDAY value rules are different.

A valid solution to the fixed Edwards25519 challenge triggers the QDAY event.
If miners include the proof in block `h`, the new rules start at block `h + 6`.
This guide calls `h + 6` the **QDAY block**.

## Compatibility summary

| Component | Reusable from Sia | Required QDAY change |
| --- | --- | --- |
| BLAKE2b hashing engine | Yes | Use the QDAY parent, target and block template |
| 80-byte mining header | Yes | Build the mandatory QDAY coinbase marker and block commitment |
| Sia P2P transport | Partly | Send QDAY magic and the mainnet genesis ID |
| Block/transaction encoding | Partly | Add the QDAY transaction header and two-key signatures |
| Sia addresses | No | Parse and emit 64-character `qday1p` Bech32m addresses |
| Sia wallet keys | No | Derive both QDAY keys and sign every input with both |
| Sia contracts and siafunds | No | QDAY consensus rejects them |
| Sia Stratum or pool endpoint | No | QDAY 0.5.0 does not ship a Stratum server |
| Sia explorer assumptions | No | Track the QDAY block, changing values, shields and decay |
| Sia JSON APIs | No | The shipped API is local and controls one wallet |

An unmodified Sia node, wallet, pool controller or indexer cannot join or
validate QDAY mainnet. Integrations should build with QDAY's copies of `core`,
`coreutils` and the required `node` packages. These packages retain their Sia
module paths, so the build must resolve those paths to the QDAY source tree
instead of public Sia releases.

## Mining pools

The proof-of-work engine can reuse a Sia-compatible BLAKE2b-256 implementation.
The hash input remains an 80-byte header:

```text
parentID[32] || nonceLE64 || timestampLE64 || commitment[32]
```

Every 64-bit nonce is eligible. Interpret the resulting 32-byte hash with
QDAY's work ordering and accept it when `types.BlockID.CmpWork(target) >= 0`.

A pool controller must construct a complete QDAY block candidate; a Sia
coinbase transaction is not valid on QDAY. The controller must:

1. Read the current chain state, parent block and mempool basis. The basis is
   the chain index against which the selected transactions are proven.
2. Create exactly one miner payout to a QDAY address controlled by both required
   keys.
3. Insert a QDAY coinbase marker as transaction index zero. Its QDAY header has
   kind `2`, a pool-selected uint64 marker nonce and exactly one key descriptor
   for the payout rule.
4. Revalidate selected transactions in order and add their fees to the miner
   payout.
5. Compute the state/transaction commitment for the payout address and
   selected transaction list.
6. Distribute the 80-byte header and target to workers.
7. Insert the returned nonce, reconstruct the complete block and submit it to a
   QDAY chain manager.

The reference candidate builder is `coreutils/qday.Candidate`; the reference
CPU nonce search is `coreutils/qday.Mine`. QDAY 0.5.0 has no
`getblocktemplate` or Stratum endpoint, so a pool must provide its own job and
share service. Existing ASIC or GPU controllers can reuse the raw BLAKE2b
search only when they accept an externally supplied 80-byte header and target.

Starting with the QDAY block, every transfer in a candidate must contain valid
20-bit DEFEND work, and each input contributes its value after any decay. Use
the QDAY chain manager to validate selected transactions; a Sia mempool cannot
perform these checks.

Pool accounting must read the current display unit from chain state. The atomic
block reward stays fixed, but its displayed amount changes from 8 QDAY to
8,000,000 QDAY at the QDAY block. Miner rewards become spendable after 60
blocks. A pool chooses its own additional confirmation threshold for chain
reorganizations.

## Exchanges and custodians

### Receivers

Emit the canonical 64-character Bech32m address with prefix `qday` and address
format byte `1`. Store its decoded 32-byte spending-rule hash as the receiver
identifier. The parser accepts a 141-character public-key form for compatibility,
but a deposit system should convert it to the 64-character form.

QDAY has no destination tag or memo. The bundled wallet derives one address
from one 24-word seed phrase; it does not derive a tree of accounts. A custodian
that needs one address per customer must build its own key manager and indexer.

### Signing

Each input requires both an Ed25519 signature and an SLH-DSA-SHA2-128s
signature. A Sia signer that produces only a classical signature cannot spend
QDAY. The reserve signature is 7,856 bytes and occupies 246 fixed 32-byte fields
in the inherited spending-policy encoding. Transaction size limits, signing
time, memory limits and hardware wallet protocols must account for it.

Use `State.InputSigHash` from QDAY core. It commits the mainnet network domain
and supplies QDAY's replay protection; the Sia signature hash is different.
QDAY accepts coin transfers only. It rejects file contracts, siafunds,
attestations and foundation updates.

### Amounts

Store balances as atomic integers. Before the QDAY block, one displayed QDAY is
`10^24` atomic units. Starting with the QDAY block, one displayed QDAY is
`10^18` atomic units. Stored atomic balances do not change; the wallet therefore
displays the same balance as 1,000,000 times as many QDAY.

Read `unit` and `qdayHeight` from the current chain state before constructing,
reviewing and submitting a withdrawal. The bundled API rejects a request whose
`unit` no longer matches the chain. An exchange should enforce the same check.

Starting with the QDAY block, every output receives a 1,440-block shield and
then loses spendable value over 10,080 blocks unless its owner renews it. An
indexer must calculate `State.QdayValue` at the spend height. The value stored
in an output is therefore insufficient for solvency or withdrawal accounting.
A transaction creates fresh outputs and starts new shields, but it pays a fee
and requires both signatures and DEFEND work.

### Deposits and confirmations

Index outputs and spends from the chain selected by accumulated work. Treat
mempool entries as unconfirmed. Miner payouts are unavailable for 60 blocks.
The exchange chooses how many additional blocks it requires before crediting a
deposit.

Monitor `qdayHeight` and the confirmed challenge-proof transaction. A chain
reorganization can remove an unfinalized proof or change the scheduled QDAY
block. Systems must derive `qdayHeight` from the currently selected chain
instead of caching the first proof they see.

The bundled local API controls one wallet. It does not generate deposit
addresses, accept raw transactions, return historical blocks or send webhook
notifications. Production custody therefore needs a separate service built on
the QDAY chain and wallet libraries.

## Block explorers

An explorer should index:

- selected-chain block height, ID, parent, timestamp, nonce, target and accumulated
  work;
- miner payout address, reward, fees and the block where it becomes spendable;
- transaction IDs, inputs, outputs, fees and QDAY header kind;
- stored output value and spendable value at the queried height;
- output maturity height, shield start, shield deadline and decay progress;
- pending and confirmed challenge proof, scheduled QDAY block and active
  denomination;
- gross issuance, burned value and current spendable supply as separate values.

The first transaction in each block is a coinbase marker with no monetary
inputs or outputs. Display it separately from transfers. QDAY header kind `3`
identifies the challenge proof. Kind `1` covers both payments and DEFEND
self-transfers; detect a renewal by its inputs and destination address.

No burn transaction appears when an output decays. To report spendable supply,
calculate the current value of every unspent output or maintain an equivalent
height-indexed model using the formula in
[`consensus.md`](consensus.md).

QDAY 0.5.0 does not expose a public explorer API, and the local wallet API does
not return historical blocks or arbitrary-address balances. Build the indexer
against `coreutils/chain.Manager`. Process both apply and revert updates, and
retain the chain state and UTXO accumulator data needed to validate later spends
and handle reorganizations.

## P2P integration

Custom services connecting directly to peers must send the QDAY magic
`514441590001a74e` and mainnet genesis ID
`d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40`.
The three bootstrap endpoints are listed in
[`parameters.md`](parameters.md). Multiple A and AAAA answers must be treated as
separate candidates.

QDAY uses the gateway request protocol inherited from Sia after its own
network-identity handshake. Use QDAY's `core/gateway` and `coreutils/syncer`
packages when possible. A client that matches only the TCP port or sends an
unmodified Sia handshake is rejected.
