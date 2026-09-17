# Local wallet API

QDAY exposes an HTTP API for the bundled desktop wallet. It listens on
the local computer only, normally at `http://127.0.0.1:19770`. The API controls
one wallet and its node. It also provides transaction-aware work to a pool
controller running on the same computer. It does not provide the block-history
or multi-wallet operations required by exchanges and explorers.

The API uses two related QDAY values. `qdayHeight` is zero until a valid
challenge proof is confirmed; it then contains the scheduled PQ Day block.
`qday` becomes true when the chain reaches that block. The event changes the
displayed denomination and starts DEFEND, shields and decay.

## Authentication and request rules

The node creates `api.token` in its data directory. This 64-character random
hexadecimal token grants full control of the local wallet. Send it as:

```http
Authorization: Bearer <token>
```

Browser session tokens use the same header. Every POST request must use
`Content-Type: application/json`. The server rejects unknown JSON fields,
extra values after the request object and ordinary bodies larger than 8,192
bytes. The session and block-status endpoints have a 256-byte limit,
`getblocktemplate` has a 1,024-byte limit, a pool payout batch has a 1 MiB
limit and `submitblock` accepts up to 8 MiB of binary block data encoded as
hexadecimal JSON.

The `Host` header must name the configured local listener as `127.0.0.1`,
`localhost` or `::1` with the correct port. If the request has an `Origin`
header, it must match the API origin. Every response uses
`Cache-Control: no-store`.

Semantic errors normally return HTTP 400:

```json
{"error":"error description"}
```

Authentication errors return 401. Host and Origin violations return 403.

`GET /api/network-status` and `GET /api/supply` do not require the token. They
return no wallet or key data. Both remain restricted by the local listener,
Host and same-origin rules above.

## Supply

### `GET /api/supply`

Returns supply calculated by the node from its fully validated, selected-chain
UTXO index. It does not depend on an explorer or an external service.

```json
{
  "network":"qday-mainnet",
  "height":1000,
  "synced":true,
  "unitAtomic":"1000000000000000000000000",
  "issuedSupply":{"qday":"508000","atomic":"508000000000000000000000000000"},
  "burnedSupply":{"qday":"50000","atomic":"50000000000000000000000000000"},
  "currentSupply":{"qday":"458000","atomic":"458000000000000000000000000000"},
  "immatureSupply":{"qday":"480","atomic":"480000000000000000000000000"},
  "circulatingSupply":{"qday":"457520","atomic":"457520000000000000000000000000"},
  "maximumIssuedSupply":{"qday":"8500000","atomic":"8500000000000000000000000000000"},
  "unspentOutputs":1234
}
```

`issuedSupply` is the premine plus rewards created through `height`.
`burnedSupply` is issued supply that is no longer spendable: confirmed void
outputs, value deliberately omitted from a transaction and protocol decay.
`currentSupply` is `issuedSupply - burnedSupply`. `immatureSupply` contains
miner payouts that exist but cannot be spent yet. `circulatingSupply` is
`currentSupply - immatureSupply`. `maximumIssuedSupply` is the premine plus all
one million scheduled rewards.

Every amount contains a display value under the denomination active at
`height` and the exact atomic integer. Accounting systems should store the
atomic value. Check `synced` before publishing the result. HTTP 503 means the
full index has not reached an available chain state yet.

The node caches the calculation for one chain index and recalculates it after
the indexed tip changes. Source: [API response and issuance
calculation](../node/qday/service.go), [full-UTXO supply
calculation](../node/persist/sqlite/qday.go), and [post-PQ-Day value
formula](../core/consensus/qday.go).

## Status

### `GET /api/status`

Returns node, chain, wallet, mining and network state.

| Field | Type | Meaning |
| --- | --- | --- |
| `network` | string | `qday-mainnet` |
| `development` | boolean | always false in the mainnet distribution |
| `genesis` | string | 64-hex-character genesis ID |
| `genesisTimestamp` | string | RFC3339 mainnet start time |
| `genesisReady` | boolean | local clock has reached the mainnet start time |
| `genesisWaitSeconds` | integer | seconds remaining before genesis, rounded up |
| `height` | integer | latest accepted block height |
| `scanHeight` | integer | latest block processed by the wallet index |
| `networkSynced` | boolean | at least one synchronized peer is connected |
| `synced` | boolean | peer requirement is met and wallet index has reached the latest block |
| `qdayHeight` | integer | PQ Day block; zero until a challenge proof confirms |
| `qday` | boolean | chain has reached the PQ Day block |
| `protocolActivationHeight` | integer | fixed block where v1.0.0 consensus rules begin |
| `protocolActive` | boolean | selected tip is at or beyond that block |
| `blocksUntilProtocolActivation` | integer | remaining blocks; zero once active |
| `unit` | string | atomic units per displayed QDAY |
| `canary` | string | fixed Edwards25519 challenge point as 64 hex characters |
| `hasWallet` | boolean | encrypted wallet exists |
| `unlocked` | boolean | signing keys are loaded |
| `address` | string | active wallet address; omitted before wallet creation |
| `balanceReady` | boolean | monetary fields are a current indexed snapshot |
| `balance` | string or null | confirmed QDAY available to spend |
| `immature` | string or null | confirmed mining rewards still inside the 60-block wait |
| `pending` | string or null | outputs to this wallet in the local mempool, excluding outputs already spent by another pending transaction |
| `mode` | string | `STOP`, `MINE` or `DEFEND` |
| `threads` | integer | configured native CPU threads |
| `maxThreads` | integer | maximum accepted thread count |
| `hashrate` | number | session average hashes per second |
| `blocksFound` | integer | blocks found in this process session |
| `lastError` | string | last background error, or empty |
| `relayError` | string | latest broadcast connection error; cleared after a successful broadcast |
| `peers` | integer | current P2P connections |
| `mempoolTransactions` | integer | transactions in the local unconfirmed pool |
| `fee` | string | default wallet transfer and burn fee in the active unit |
| `proofFee` | string | minimum proof publication fee in the active unit |
| `proofPending` | string | challenge-proof transaction ID in the local mempool, or empty |
| `activationDelay` | integer | blocks from the proof block to the PQ Day block |
| `blockReward` | string | reward for the next block in the active unit |
| `blockIntervalSeconds` | number | target block interval |
| `difficulty` | string | average hashes expected for the next block |
| `initialDifficulty` | string | average hashes expected at genesis |
| `powTarget` | string | target for the next block |
| `difficultyAlgorithm` | string | `Sia Oak / Final Cut` |
| `maturityBlocks` | integer | miner reward maturity |
| `shieldUntil` | integer | earliest shield end among spendable wallet outputs; omitted when unavailable |
| `canRestart` | boolean | desktop supervisor accepts API restart requests |
| `p2p` | object | listener, peer classes, wire magic and router mapping |

When `balanceReady` is false, `balance`, `immature` and `pending` are null and
must not be treated as zero.

`relayError` describes network delivery. It does not mean that unlocking failed
or that a locally accepted transaction was rejected. The wallet retries pending
transactions without requiring another send request.

`STOP` means the local worker is off. `MINE` means it is mining blocks before
the PQ Day event. `DEFEND` means it continues mining blocks after PQ Day and also
runs the automatic shield-renewal loop.

The `p2p` object contains `listenAddress`, `bootstrapPeers`,
`bootstrapOutbound`, `regularPeers`, `inboundPeers`, `wireMagic` and an optional
`mapping`. The mapping describes UPnP port forwarding with `state`, `port` and,
when available, `externalAddress`, `publicIP` and `detail`.

## Browser session

### `POST /api/launch`

Authenticated request body:

```json
{}
```

Returns a local URL containing a one-use launch code. The desktop launcher opens
this URL in the browser. The code expires after one minute.

```json
{"url":"http://127.0.0.1:19770/#launch=<code>"}
```

### `POST /api/session`

This endpoint exchanges the one-use code without an Authorization header:

```json
{"code":"<code>"}
```

It returns a separate browser token valid for 24 hours:

```json
{"token":"<browser-token>"}
```

The launch code is deleted on the first exchange attempt and cannot be replayed.

## Wallet lifecycle

### `POST /api/create`

```json
{"password":"at-least-12-characters","phrase":""}
```

An empty `phrase` creates a new wallet. The response contains the new 24-word
seed phrase:

```json
{"phrase":"word1 word2 ... word24"}
```

Supplying a valid QDAY seed phrase imports that wallet instead. The endpoint
refuses to overwrite an existing wallet. The `password` encrypts the local key
file and must contain at least 12 characters.

### `POST /api/unlock`

```json
{"password":"wallet-passphrase"}
```

Returns `{"ok":true}` and loads both signing keys.

### `POST /api/lock`

```json
{}
```

Stops mining and DEFEND, clears loaded private keys and returns `{"ok":true}`.

### `POST /api/recovery`

```json
{"password":"wallet-passphrase"}
```

Checks the passphrase against the encrypted key file and returns the seed
phrase:

```json
{"phrase":"word1 word2 ... word24"}
```

### `POST /api/restore`

Imports another seed phrase and replaces the local wallet. It does not retain a
backup of the wallet being replaced:

```json
{
  "password":"new-wallet-passphrase",
  "phrase":"word1 word2 ... word24",
  "replaceAddress":"qday1p..."
}
```

`replaceAddress` must equal the address currently returned by `/api/status`, or
be empty when no wallet exists. This check prevents an old browser tab from
replacing a wallet that another tab has already changed. The response contains
the address derived from the imported phrase:

```json
{"address":"qday1p..."}
```

## Transfers and PQ Day proof

### `POST /api/send`

```json
{
  "fromAddress":"qday1p...",
  "address":"qday1p...",
  "amount":"12.5",
  "unit":"1000000000000000000000000"
}
```

`fromAddress` must equal the address currently returned by `/api/status`.
`unit` must equal the current status unit. These checks stop a reviewed request
from being sent after the wallet or QDAY denomination changes. The wallet
selects at most 128 confirmed inputs, preferring larger outputs, adds change,
applies the selected fee and signs with both keys. It places the transaction in its
mempool and schedules persistent broadcast. The response does not wait for a
remote peer or for confirmation.

The optional `fee` field is a decimal string in QDAY, using the same `unit` as
`amount`. For example, `"fee":"0.25"` pays the miner a total of 0.25 QDAY.
It is added to the amount deducted from the wallet, not subtracted from the
recipient's payment. Omitting `fee`, or sending an empty string, uses the
standard wallet fee returned by `/api/status`. The GUI calls this mode Auto.

Custom fees must be non-negative, fit the atomic currency range and be covered
by the selected confirmed inputs along with the amount. Exponents, commas and
excess decimal places are rejected. Zero is allowed for ordinary transfers and
burns. A custom fee creates a new transaction; it does not modify or replace a
transaction already in the mempool.

```json
{"transaction":"<64-hex-character transaction ID>"}
```

The wallet keeps signed local transactions for 48 hours and broadcasts pending
ones after chain changes, periodically, and after a node restart. Confirmation
pauses broadcast; if a reorganization removes that confirmation within the
retry period, broadcast resumes. Proofs are updated in batches when the wallet
has been offline for many blocks. Missing chain history postpones the retry.

The wallet stops rebroadcasting after 48 hours. The chain mempool has no fixed expiry;
a valid transaction can remain pending longer if no miner includes it. See the
[rebroadcast implementation](../node/qday/rebroadcast.go).

### `POST /api/burn`

```json
{
  "fromAddress":"qday1p...",
  "amount":"50000",
  "unit":"1000000000000000000000000"
}
```

Builds and broadcasts an irreversible transfer of `amount` to the protocol's
all-zero void address. The wallet adds change, pays its normal transfer fee or
the optional custom `fee`, and signs every input with both
wallet keys. At most 128 inputs are selected. Ordinary sends and burns select
larger outputs first.

`fromAddress` and `unit` have the same stale-review protection as `/api/send`.
The response contains the transaction ID. The amount appears in
`burnedSupply` only after confirmation on the selected chain.

Burn transactions use the ordinary QDAY transfer header and are identified by
an output whose address equals `types.VoidAddress`. See the [transaction
builder](../node/qday/service.go) and [consensus
description](consensus.md#intentional-burns).

### `POST /api/proof/verify`

```json
{"witness":"<64 hex characters>"}
```

The API field is named `witness`; its value is the proposed solution encoded as
a 32-byte little-endian scalar. Successful verification does not spend coins or
broadcast anything:

```json
{
  "valid":true,
  "network":"qday-mainnet",
  "fee":"1",
  "unit":"1000000000000000000000000",
  "activationDelay":6,
  "qdayHeight":0,
  "proofPending":""
}
```

### `POST /api/proof`

```json
{
  "fromAddress":"qday1p...",
  "witness":"<64 hex characters>",
  "unit":"1000000000000000000000000"
}
```

Builds, funds, signs and broadcasts the challenge-proof transaction through the
normal mempool. The optional `fee` field uses the same decimal format as sends
and burns, but cannot be below the minimum publication fee returned as
`proofFee` by `/api/status` (1 QDAY before PQ Day). Omitting it uses that minimum.
The confirmed balance must cover the selected fee. The
response contains its transaction ID.

### `POST /api/defend`

```json
{}
```

Builds a transaction that sends selected outputs back to the same wallet,
performs DEFEND work, signs it and broadcasts it. Once confirmed, the new
outputs receive new shields. The endpoint works only after the PQ Day event and
returns an error when no output is within the final 360 blocks of its shield.

## Atomic swaps

These authenticated endpoints expose the hashlock and height-refund policy
enabled at `protocolActivationHeight`. Public keys, hashes, output IDs, heights
and atomic amounts are encoded without floating point.

### `GET /api/swap/keys`

Returns the active wallet's public descriptor. Unlock the wallet first.

```json
{
  "classical":"<32-byte lowercase hex>",
  "reserve":"<32-byte lowercase hex>",
  "address":"qday1p..."
}
```

The private Ed25519 and SLH-DSA keys never leave the wallet.

### `POST /api/swap/derive`

Verifies a complete portable contract and returns its deterministic QDAY
address without writing wallet state:

```json
{
  "recipient":{
    "classical":"<32-byte lowercase hex>",
    "reserve":"<32-byte lowercase hex>",
    "address":"qday1p..."
  },
  "refund":{
    "classical":"<32-byte lowercase hex>",
    "reserve":"<32-byte lowercase hex>",
    "address":"qday1p..."
  },
  "secretHash":"<SHA-256 digest as 32-byte lowercase hex>",
  "refundHeight":12000
}
```

The two optional `address` fields are checked against their public keys when
present. A successful response is:

```json
{
  "address":"qday1p...",
  "height":8000,
  "activationHeight":9000,
  "active":false,
  "outputs":null
}
```

Both parties should derive the same address independently before funding it.

### `POST /api/swap/watch`

Accepts the same contract, registers its complete spending policy in the
unlocked wallet and returns its status. Registration is persistent and
idempotent. It may happen before or after funding.

Fund the returned address with an ordinary `/api/send`. Wait for the deposit's
required confirmations before releasing value on the other chain. A watched
confirmed output appears as:

```json
{
  "address":"qday1p...",
  "height":9100,
  "activationHeight":9000,
  "active":true,
  "outputs":[{
    "id":"<64 hex characters>",
    "valueAtomic":"1000000000000000000000000",
    "spendableAtomic":"1000000000000000000000000",
    "maturityHeight":0,
    "confirmations":7
  }]
}
```

`spendableAtomic` applies the value rules at the next block and can be lower
than `valueAtomic` after PQ Day.

### `POST /api/swap/status`

Accepts the same contract and returns its current unspent confirmed outputs.
Outputs already spent by a local mempool transaction are omitted. A caller
should still use `/api/swap/watch` once so the wallet retains the full policy
needed to spend after a restart.

### `POST /api/swap/claim`

The recipient spends one confirmed contract output by revealing the secret:

```json
{
  "contract":{ "recipient":{}, "refund":{}, "secretHash":"...", "refundHeight":12000 },
  "output":"<64 hex character output ID>",
  "feeAtomic":"1000000000000000000000",
  "secret":"<32-byte lowercase hex preimage>"
}
```

The active wallet must match the recipient descriptor. The node verifies the
SHA-256 preimage, signs with both wallet keys, performs DEFEND work when needed,
adds the transaction to the mempool and persists it for rebroadcast. Retrying
the same spend returns the same transaction ID.

### `POST /api/swap/refund`

The refund owner spends the output after the timeout:

```json
{
  "contract":{ "recipient":{}, "refund":{}, "secretHash":"...", "refundHeight":12000 },
  "output":"<64 hex character output ID>",
  "feeAtomic":"1000000000000000000000"
}
```

The selected chain tip must be at least `refundHeight`, so the refund can enter
the following block. Supplying a `secret` to this endpoint is rejected. Both
claim and refund return `{"transaction":"<64 hex characters>"}`.

## Mining and node control

### `POST /api/miner/getblocktemplate`

Returns a QDAY v2 block candidate built from the current selected tip and local
mempool. The payout wallet must be unlocked. An empty request returns
immediately:

```json
{"longpollid":""}
```

A public pool can request an uncached candidate with a distinct marker nonce:

```json
{"worknonce":1844674407370955161}
```

`worknonce` and `longpollid` are mutually exclusive. A controller should use
one long-poll request to watch the chain and mempool, then use a different
nonzero `worknonce` for each independently assigned miner template.

The response follows Sia `minerd`'s BIP22-shaped template and adds `header`, the
complete 80-byte BLAKE2b work header encoded as 160 hexadecimal characters:

```json
{
  "header":"<160 hex characters>",
  "commitment":"<64 hex characters>",
  "transactions":[
    {"data":"<hex>","hash":"","txid":"<64 hex characters>","depends":null,"fee":0,"sigops":0,"txtype":"2"}
  ],
  "minerpayout":[{"data":"<hex>","hash":"","txid":"","depends":null,"fee":0,"sigops":0,"txtype":""}],
  "previousblockhash":"<64 hex characters>",
  "longpollid":"<32 hex characters>",
  "target":"<64 hex characters>",
  "height":1001,
  "curtime":1789234567,
  "version":2,
  "bits":"07012345",
  "worknonce":1844674407370955161,
  "blockRewardAtomic":"8000000000000000000000000",
  "feesAtomic":"1000000000000000000000",
  "payoutAtomic":"8001000000000000000000000",
  "mempoolTransactions":12,
  "stratum":{
    "block":"<hex-encoded complete QDAY v2 block>",
    "coinbase1":"<hex-encoded fixed prefix>",
    "coinbase2":"<hex-encoded fixed suffix>",
    "extranonce1Size":4,
    "extranonce2Size":4,
    "merklebranch":["<64 hex characters>"]
  }
}
```

Transaction zero is the mandatory QDAY payout marker. Starting at
`protocolActivationHeight`, the final entry is the compact mining-work marker;
entries between them are valid mempool transactions selected in dependency
order. `mempoolTransactions` counts only those user transactions. Their fees
are added to `minerpayout`. Supplying the current `longpollid` holds the request
until the tip or mempool changes, or until the 30-second template age expires.

`stratum.block` is the complete Sia-encoded block represented by the template.
A controller may replace its nonce and timestamp and send it to `submitblock`.
After protocol activation, the rightmost transaction is reconstructed as
`coinbase1 || extranonce1 || extranonce2 || coinbase2`; the two extranonces are
exactly four bytes each. The complete result is 33 bytes. Before activation,
both extranonce sizes are zero and the final transaction is fixed.
`stratum.merklebranch` contains the left-side Merkle roots needed to use the
last entry in `transactions` as Sia Stratum's rightmost arbitrary transaction.
Start with `BLAKE2b-256(0x00 || transaction.data)`, then replace the root with
`BLAKE2b-256(0x01 || branch || root)` for each branch in order. The result must
equal `commitment`.

### `POST /api/miner/submitblock`

Submits a solved, Sia-encoded QDAY v2 block and relays it to peers:

```json
{"params":["<hex-encoded complete block>"]}
```

The node performs full QDAY consensus validation before returning the block ID.
The endpoint does not accept a header without its corresponding payout, miner
marker and transactions.

Source: [template construction and block submission](../node/qday/mining.go).

### `POST /api/miner/blockstatus`

Returns selected-chain and maturity state for a submitted block:

```json
{"block":"<64 hex characters>"}
```

```json
{
  "block":"<64 hex characters>",
  "known":true,
  "canonical":true,
  "height":6000,
  "tipHeight":6020,
  "confirmations":21,
  "maturityHeight":6060
}
```

`known` means the local chain manager has the block. `canonical` means it is
on the selected chain. A miner payout is spendable at `maturityHeight`.

## Public pool accounting

These authenticated endpoints are for a pool controller on the same machine.
They expose wallet signing and must never be published through the pool
dashboard or reverse proxy.

### `POST /api/pool/payout`

Creates or resumes one exact multi-recipient payout batch:

```json
{
  "requestID":"payout-4c584cd7f3929c35",
  "fromAddress":"qday1p...",
  "expectedUnitAtomic":"1000000000000000000000000",
  "feeAtomic":"1000000000000000000000",
  "outputs":[
    {"address":"qday1p...","amountAtomic":"42000000000000000000000000"}
  ]
}
```

All monetary fields are canonical base-10 atomic integers. The request accepts
1 through 127 unique output addresses. `expectedUnitAtomic` must match the
current display unit, and `fromAddress` must match the unlocked wallet.

```json
{
  "requestID":"payout-4c584cd7f3929c35",
  "transaction":"<64 hex characters>",
  "outputs":1,
  "status":"queued"
}
```

The node signs and persists the transaction before adding it to the mempool.
It updates accumulator proofs, rebroadcasts after restarts and tracks it
through reorganizations. Status moves from `queued` to `confirming`, then to
`confirmed` after six blocks. Repeating the same request ID and contents
returns the same transaction. Reusing the ID with different contents fails.

### `POST /api/pool/defend`

```json
{}
```

Runs one wallet maintenance pass without starting CPU mining. Before PQ Day,
or when no output needs renewal, it returns `{"status":"idle"}`. A queued
renewal returns its transaction ID. Pool controllers may poll this operation;
it does not create duplicate renewals for outputs already being spent.

### `POST /api/start`

```json
{"threads":1}
```

Starts native BLAKE2b mining. It requires an unlocked wallet, at least one
synchronized peer, a thread count from 1 through `maxThreads`, and a local clock
at or after the mainnet start time. After the PQ Day event, the reported mode
changes to `DEFEND`; block mining continues while the wallet also renews outputs
that are approaching their shield deadline.

### `POST /api/stop`

```json
{}
```

Stops mining and automatic DEFEND. Returns `{"ok":true}`.

### `POST /api/peers`

```json
{"peer":"203.0.113.10:19771"}
```

Only a literal IPv4 address or a bracketed IPv6 address followed by a nonzero
TCP port is accepted. The node saves it for later sessions and immediately
tries to connect.

```json
{
  "address":"203.0.113.10:19771",
  "saved":true,
  "connected":false,
  "connectionError":"optional error"
}
```

### `POST /api/restart`

```json
{}
```

This endpoint is available only when the desktop launcher supervises the node.
It returns `{"ok":true}`, replaces the node process and reconnects the browser.
Mining stops, and the restarted wallet is locked.

### `POST /api/shutdown`

```json
{}
```

Returns `{"ok":true}` and terminates the local node shortly afterward.
