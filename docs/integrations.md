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
| Sia `getblocktemplate` client | Partly | Use QDAY's authenticated local endpoints and v2 templates |
| Sia Stratum miner | Yes, through a bridge | Run [`qday-stratum`](https://github.com/petoshi/qday-stratum) beside an unlocked QDAY node |
| Sia explorer assumptions | No | Track the QDAY block, changing values, shields and decay |
| Sia JSON APIs | No | The shipped API is local and controls one wallet |
| Supply RPC | No | Read QDAY issuance and burns from `GET /api/supply` |

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

Run the QDAY node and pool controller on the same machine. The node API remains
on loopback and requires the bearer token stored in its data directory. Create
or import the pool payout wallet and unlock it, then request work with:

```http
POST /api/miner/getblocktemplate
Authorization: Bearer <api.token>
Content-Type: application/json

{"longpollid":""}
```

The response follows Sia `minerd`'s version 2 template shape and includes an
additional `header` field containing the complete 80-byte work header as hex.
It already contains the mandatory QDAY miner marker, all selected mempool
transactions, their fees, the payout and the commitment. Repeating the request
with its `longpollid` returns when the chain or mempool changes, or after 30
seconds. The `stratum.block` and `stratum.merklebranch` fields let a controller
produce a SiaMining-compatible job without rebuilding QDAY state. A pool must
distribute fresh work when that request returns.

After finding a valid nonce, reconstruct the complete v2 block and submit its
Sia binary encoding as hexadecimal:

```http
POST /api/miner/submitblock
Authorization: Bearer <api.token>
Content-Type: application/json

{"params":["<hex-encoded complete block>"]}
```

The node validates the block and relays it. It rejects a header submitted
without the payout, marker and transactions committed by that header. QDAY does
not embed a Stratum listener. The separate
[`qday-stratum`](https://github.com/petoshi/qday-stratum) solo bridge translates
these endpoints to the SiaMining dialect. Pool controllers may use the same API
and implement their own share difficulty, worker accounting and payouts.

Pool software that constructs candidates without the API must perform the same
steps. A Sia coinbase transaction is not valid on QDAY:

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
CPU nonce search is `coreutils/qday.Mine`. Existing ASIC or GPU controllers can
reuse the raw BLAKE2b search when they accept an externally supplied 80-byte
header and target. See the [template and submission
implementation](../node/qday/mining.go) and [candidate
builder](../coreutils/qday/miner.go). The [end-to-end template
test](../node/qday/mining_test.go) decodes a response, searches the header and
submits the complete block.

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

### Dependent transactions

When a transaction spends an unconfirmed output, include its unconfirmed
ancestors before it in the submitted set. QDAY's `MaturityHeight` also records
an ordinary output's birth height. Set it to `basis.Height + 1` for an
unconfirmed parent; Sia's `EphemeralSiacoinOutput` helper alone leaves it at
zero. The QDAY chain manager updates this height while the parent is pending
and preserves its actual birth height after confirmation.

Use `V2TransactionSet` to assemble an ordered set and `UpdateV2TransactionSet`
to update its accumulator proofs. These updates preserve transaction IDs,
destinations, amounts and signatures. Source: [transaction pool and proof
updates](../coreutils/chain/manager.go).

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

### Burns and supply

The bundled wallet burns coins by creating a normal header-kind-`1` transfer
with an output to `types.VoidAddress`, the 32-byte all-zero address. It returns
change to the sender and pays the standard or explicitly selected wallet fee.
The wallet API accepts an optional decimal `fee` for sends, burns and proof
publication; see [wallet transaction fees](api.md#post-apisend). There is no burn key,
admin transaction or separate consensus opcode. Treat any confirmed output to
the void address as permanently unspendable. Do not credit a mempool burn as
final; a reorganization can remove its block.

Every QDAY node maintains a full UTXO index and serves `GET /api/supply` on its
local HTTP listener. Use that endpoint for supply reporting instead of trusting
an explorer. The response gives:

- `issuedSupply`: premine plus block rewards created through the reported
  height;
- `burnedSupply`: issued value lost to void outputs, omitted transaction value
  and protocol decay;
- `currentSupply`: issued supply minus burned supply;
- `immatureSupply`: confirmed miner payouts still inside their maturity wait;
- `circulatingSupply`: current supply minus immature supply;
- `maximumIssuedSupply`: premine plus every scheduled mining reward.

Each value contains `qday` for display and `atomic` for exact accounting. The
display denomination changes at the QDAY block, so integrations should persist
the atomic integer, record `height`, and require `synced: true` before
publishing the value. Current supply includes immature miner payouts.

Code: [burn construction](../node/qday/service.go), [node UTXO supply
query](../node/persist/sqlite/qday.go), [void address](../core/types/types.go),
and [decay evaluation](../core/consensus/qday.go).

The hosted explorer republishes its local node result at
[`https://explorer.pqday.com/api/supply`](https://explorer.pqday.com/api/supply).
Aggregators can read the synchronized display number directly from
[`/api/circulating-supply`](https://explorer.pqday.com/api/circulating-supply)
or current total supply from
[`/api/total-supply`](https://explorer.pqday.com/api/total-supply). The public
API permits a burst of 30 requests and refills at two requests per second;
excess requests receive HTTP 429 and `Retry-After`. Its implementation is in
the explorer's [API](https://github.com/petoshi/qday-explorer/blob/main/api.go)
and [rate limiter](https://github.com/petoshi/qday-explorer/blob/main/rate_limit.go).

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
- issued, burned and current supply from the node's `/api/supply` response.

The first transaction in each block is a coinbase marker with no monetary
inputs or outputs. Display it separately from transfers. QDAY header kind `3`
identifies the challenge proof. Kind `1` covers both payments and DEFEND
self-transfers; detect a renewal by its inputs and destination address.

An explicit burn is a transfer with a void-address output. Decay has no separate
transaction. The node accounts for both in `/api/supply`; explorers should show
that chain-indexed result and link explicit burn transactions normally.

The local node API does not return historical blocks or arbitrary-address
balances. Build an indexer against `coreutils/chain.Manager`, or use the hosted
explorer API linked above. A custom indexer must process both apply and revert
updates and retain the chain state and UTXO accumulator data needed to validate
later spends and handle reorganizations.

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
