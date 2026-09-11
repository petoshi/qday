# Local wallet API

QDAY 0.5.0 exposes an HTTP API for the bundled desktop wallet. It listens on
the local computer only, normally at `http://127.0.0.1:19770`. The API controls
one wallet and its node. It does not provide the block-history and multi-wallet
operations required by exchanges, explorers or mining pools.

The API uses two related QDAY values. `qdayHeight` is zero until a valid
challenge proof is confirmed; it then contains the scheduled QDAY block.
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
extra values after the request object and bodies larger than 8,192 bytes. The
session endpoint has a separate 256-byte limit.

The `Host` header must name the configured local listener as `127.0.0.1`,
`localhost` or `::1` with the correct port. If the request has an `Origin`
header, it must match the API origin. Every response uses
`Cache-Control: no-store`.

Semantic errors normally return HTTP 400:

```json
{"error":"error description"}
```

Authentication errors return 401. Host and Origin violations return 403.

`GET /api/network-status` is the only route that does not require the token. It
returns the chain height, synchronization state and connection counts for local
monitoring. It still requires the local Host and same-origin rules above and
does not return wallet data.

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
| `qdayHeight` | integer | QDAY block; zero until a challenge proof confirms |
| `qday` | boolean | chain has reached the QDAY block |
| `unit` | string | atomic units per displayed QDAY |
| `canary` | string | fixed Edwards25519 challenge point as 64 hex characters |
| `hasWallet` | boolean | encrypted wallet exists |
| `unlocked` | boolean | signing keys are loaded |
| `address` | string | active wallet address; omitted before wallet creation |
| `balanceReady` | boolean | monetary fields are a current indexed snapshot |
| `balance` | string or null | confirmed QDAY available to spend |
| `immature` | string or null | confirmed mining rewards still inside the 60-block wait |
| `pending` | string or null | outputs to this wallet in locally known unconfirmed transactions |
| `mode` | string | `STOP`, `MINE` or `DEFEND` |
| `threads` | integer | configured native CPU threads |
| `maxThreads` | integer | maximum accepted thread count |
| `hashrate` | number | session average hashes per second |
| `blocksFound` | integer | blocks found in this process session |
| `lastError` | string | last background error, or empty |
| `peers` | integer | current P2P connections |
| `mempoolTransactions` | integer | transactions in the local unconfirmed pool |
| `fee` | string | bundled-wallet transfer fee in the active unit |
| `proofFee` | string | proof fee in the active unit |
| `proofPending` | string | challenge-proof transaction ID in the local mempool, or empty |
| `activationDelay` | integer | blocks from the proof block to the QDAY block |
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

`STOP` means the local worker is off. `MINE` means it is mining blocks before
the QDAY event. `DEFEND` means it continues mining blocks after QDAY and also
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

## Transfers and QDAY proof

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
selects at most 64 confirmed inputs, adds change, applies the fixed fee, signs
with both keys, places the transaction in its mempool and broadcasts it.

```json
{"transaction":"<64-hex-character transaction ID>"}
```

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
normal mempool. The confirmed balance must cover the 1 QDAY proof fee. The
response contains its transaction ID.

### `POST /api/defend`

```json
{}
```

Builds a transaction that sends selected outputs back to the same wallet,
performs DEFEND work, signs it and broadcasts it. Once confirmed, the new
outputs receive new shields. The endpoint works only after the QDAY event and
returns an error when no output is within the final 360 blocks of its shield.

## Mining and node control

### `POST /api/start`

```json
{"threads":1}
```

Starts native BLAKE2b mining. It requires an unlocked wallet, at least one
synchronized peer, a thread count from 1 through `maxThreads`, and a local clock
at or after the mainnet start time. After the QDAY event, the reported mode
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
