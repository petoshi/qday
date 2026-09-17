# QDAY consensus rules

Consensus rules decide which blocks and transactions every QDAY node accepts.
QDAY records coins as unspent transaction outputs (UTXOs): a transaction spends
existing outputs and creates new ones. Fixed mainnet values are listed in
[`parameters.md`](parameters.md).

The chain has two periods. It begins with ordinary proof-of-work mining while a
fixed Edwards25519 challenge remains unsolved. If a valid solution enters block
`h`, the PQ Day event is scheduled for block `h + 6`. Starting with that block,
balances display with a 1,000,000× denomination change, every spend carries
DEFEND proof of work, and unrenewed outputs can decay. Blocks and mining rewards
continue under the same schedule.

QDAY v1.0.0 also contains a protocol upgrade at block `9,100`. This height is
independent of PQ Day. It changes the final
mining marker so standard SiaMining hardware can build unique work, and it
enables hash-and-time-locked outputs for atomic swaps. The genesis block,
existing addresses, ordinary transactions, balances and transaction IDs do not
change.

## Chain and proof of work

A block header is 80 bytes:

```text
offset  size  field
0       32    parent block ID
32       8    nonce, uint64 little-endian
40       8    Unix timestamp, uint64 little-endian
48      32    block commitment
```

The block ID is `BLAKE2b-256(header)`. A header is valid when its ID is no
greater than the target calculated for that block. The nonce factor is one, so
a miner may test every uint64 nonce.

The initial expected work is 1,048,575 hashes. Difficulty is adjusted after
each block by Oak / Final Cut toward a 60-second interval. The adjustment is
clamped to approximately 0.4% of the preceding difficulty per block and never
falls below one unit of work.

The timestamp must not precede the median of up to eleven previous block
timestamps. A node also rejects blocks dated too far ahead of the time it
receives them. These checks prevent the first mainnet block from being accepted
before the genesis timestamp:

- a timestamp below genesis fails the median rule;
- a timestamp at or above genesis fails the future-block policy until the node
  clock reaches genesis.

After genesis, a node accepts a timestamp no more than three hours ahead of its
local receipt time. This local-clock limit is admission policy; deterministic
consensus validation still uses the timestamps recorded in the chain. Because
the genesis block template is public, the time gate cannot prevent private
nonce searching before the configured start time. It prevents early blocks from
being accepted or relayed by conforming nodes.

Nodes compare chains by accumulated proof of work. They switch to a competing
chain only when its total work exceeds the current chain by more than 20% of
the work expected for the next block.

## Genesis

The genesis ID is:

```text
d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40
```

Genesis creates one output worth 500,000 QDAY under the original denomination.
Its transaction has two data records:

1. The literal byte prefix `QDAY/genesis/v2/`, followed by the canonical JSON
   network record.
2. `PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME.`

The network-domain hash commits the network record, premine address and
inscription. A change to any committed value produces another network.

## Block structure

Every block after genesis contains:

- exactly one miner payout;
- a transaction list whose first item is a QDAY coinbase marker;
- a valid state and transaction commitment;
- total transaction weight no greater than 2,000,000.

The coinbase marker records the two public keys behind the miner's payout
address. It has:

- QDAY header kind `2`;
- exactly one 64-byte key descriptor: a 32-byte Ed25519 public key followed
  by a 32-byte SLH-DSA public key;
- no inputs, outputs, fee, contracts, attestations or foundation update.

The hash of this two-key spending rule must equal the miner payout address.
The payout equals the block reward plus all transaction fees. Heights 1 through
1,000,000 issue `8 × 10^24` atomic units. This displays as 8 QDAY before
PQ Day and 8,000,000 QDAY afterward. Later heights issue no new units and
may pay fees. A miner cannot spend a payout until 60 more blocks are added.

Starting at block `9,100`, every block also ends with exactly one
mining-work marker. It has QDAY header kind `4`, no keys, witness, inputs,
outputs, fee or other fields. Its eight-byte nonce is mutable mining data. The
first coinbase marker remains unchanged; ordinary mempool transactions appear
between the two markers. A block without the final marker, with it in another
position, or with any nonempty transaction field is invalid.

The encoded work marker is exactly 33 bytes. SiaMining Stratum divides it as
23 fixed bytes, four bytes assigned by the server, four bytes searched by the
miner and two fixed trailing bytes. This only changes the block commitment and
mining header. The marker creates and spends no value.

## Addresses and signatures

A QDAY address is the hash of this spending rule:

```text
threshold 2 of:
  Ed25519 public key
  SLH-DSA-SHA2-128s public key
```

An ordinary spend reveals both public keys and supplies both signatures. The
transaction signature hash is separated from every other network and purpose:

```text
hashAll("qday/sig/input/v1", networkDomain, transactionSemantics)
```

`State.InputSigHash` implements this operation. The quoted label is a literal
byte string committed by signatures. `transactionSemantics` means the canonical
transaction contents with signature material excluded.

The SLH-DSA signature uses the literal context `QDAY/reserve/v1` and contains
7,856 bytes. The inherited transaction encoding carries it in 246 fixed
32-byte fields. The final 16 padding bytes must be zero. A missing signature, a
different spending rule or nonzero padding invalidates the transaction.

QDAY accepts coin transfers only. Siafund inputs and outputs, file contracts,
contract revisions and resolutions, attestations and foundation updates are
invalid. A normal transfer requires at least one input and one output, with a
maximum of 128 inputs and 128 outputs.

Starting at block `9,100`, an input may instead reveal one branch
of the atomic-swap policy defined below. Both possible branches still require
an Ed25519 signature and an SLH-DSA signature.

## QDAY transaction header

Each transaction carries a small binary QDAY header in its `arbitraryData`
field:

```text
offset  size       field
0        5         ASCII "QDAY" followed by format byte 0x02
5        1         kind
6        8         nonce, uint64 little-endian
14       2         key descriptor count, uint16 little-endian
16       64 × n    key descriptors
...      32        proof witness; present only for kind 3
```

Kinds are:

| Kind | Name | Use |
| --- | --- | --- |
| `1` | transfer | transfer and DEFEND renewal |
| `2` | coinbase | first transaction in every non-genesis block |
| `3` | challenge proof | one funded proof that starts the PQ Day countdown |
| `4` | mining work | empty final marker in blocks from `9,100` |

Transfer and proof headers contain no key descriptors. A coinbase header
contains exactly one. A challenge proof uses nonce zero. Starting with the QDAY
block, the transfer nonce carries DEFEND work.

## Value conservation

The sum of outputs plus the miner fee may not exceed the sum of inputs.
Starting with the PQ Day block, each input is evaluated at its decayed value.
Any positive difference between evaluated inputs and outputs plus fee is
permanently destroyed.

Transaction fees move existing units to the miner and do not increase supply.
Consensus sets a minimum only for a challenge-proof transaction. The bundled
wallet uses a fixed transfer and DEFEND fee of `10^21` atomic units. This is
displayed as 0.001 QDAY before the event and 1,000 QDAY afterward.

## Atomic swaps

QDAY atomic swaps use a SHA-256 hashlock and an absolute block-height refund.
A contract commits the recipient's two public keys, the refund owner's two
public keys, a 32-byte SHA-256 secret hash and a nonzero refund height. Its
output address is the hash of this policy:

```text
threshold 1 of:
  threshold 3 of:
    recipient Ed25519 signature
    recipient SLH-DSA signature
    SHA-256 preimage
  threshold 3 of:
    refund Ed25519 signature
    refund SLH-DSA signature
    chain tip height at least refundHeight
```

The spender reveals one branch and the opaque hash of the other. The parent
output address commits both complete branches, so neither party can replace
the hidden branch while spending. A claim exposes the 32-byte secret in the
input witness. A refund becomes valid once the selected parent tip has reached
`refundHeight`; the transaction can therefore enter the following block.

Atomic-swap transactions use the ordinary transfer header, value-conservation
rules, fees, maturity checks and replay protection. After PQ Day they also
perform DEFEND work and spend the output's value after any decay. Consensus
does not create an order book, price feed or bridge asset; exchange software
coordinates the matching contract on the other chain.

## Intentional burns

An intentional burn is an ordinary QDAY transfer with an output addressed to
`types.VoidAddress`, the 32-byte all-zero address. No signing policy exists for
that address, so its output can never become an input. The bundled wallet pays
the requested amount to the void address, returns change to the sender and pays
the normal transfer fee. The transaction uses QDAY header kind `1`; there is no
special burn opcode or privileged key.

A burn changes network supply when its block joins the selected chain. A
mempool transaction has not burned anything yet, and a chain reorganization
that removes the confirming block restores the previous supply. Protocol decay
and any input value omitted from outputs also reduce current supply.

The implementation is in the [wallet transaction
builder](../node/qday/service.go), the [void-address
definition](../core/types/types.go) and the [node supply
calculation](../node/persist/sqlite/qday.go).

## Challenge proof and the PQ Day event

The fixed challenge is a point `C` in the prime-order Edwards25519 group:

```text
C = 7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771
```

`C` is derived from a fixed label and counter without first choosing a secret
scalar. The challenge is to recover the discrete logarithm: a nonzero scalar
`x` for which `x·G = C`.

The API calls the submitted solution a `witness`. It is valid when it is the
canonical 32-byte little-endian encoding of `x`. A proof transaction must:

- use QDAY header kind `3` and nonce zero;
- contain a valid witness;
- spend at least one native QDAY input;
- pay a miner fee of at least 1 QDAY under the original denomination;
- contain no key descriptors;
- appear before any proof is confirmed;
- be the only proof transaction in its block.

The proof travels through the unconfirmed transaction pool (mempool) and the
normal block-relay path. If miners include it in block `h`, consensus records
`qdayHeight = h + 6`. That value is the PQ Day block, where the denomination,
shield, decay and DEFEND rules begin. A chain reorganization that removes the
proof also cancels the scheduled event.

## Denomination change

Before the PQ Day block, one displayed QDAY is `10^24` atomic units. Starting with
the PQ Day block, one displayed QDAY is `10^18` atomic units. Stored output values
do not change at that block. The same atomic balance is therefore displayed as
1,000,000 times as many QDAY.

Clients must read the active unit from chain state. A transaction review made
under one unit must not be submitted under the other.

## Shields, decay and DEFEND

For an output with stored value `V`, let `M` be its recorded creation or miner
payout maturity height. Define:

```text
start = max(qdayHeight, M)
elapsed = currentHeight - start
```

The output keeps its full value through `start + 1,440`. Decay is then measured
in 60-block steps:

```text
age = floor((elapsed - 1,440) / 60) × 60
remaining = 10,080 - age
```

The spendable value is `floor(V × remaining / 10,080)`. It becomes zero at
`start + 11,520`, approximately eight days after `start` at the target block
interval. Nodes calculate the loss when the output is spent; no recurring burn
transaction changes the UTXO set.

Every transfer starting with the PQ Day block must also satisfy
transaction-bound BLAKE2b work:

```text
intent = hashAll("qday/defend/intent/v1", networkDomain, transactionIDWithZeroNonce)
work = BLAKE2b-256(intent || LE64(nonce))
```

The work hash must have 20 leading zero bits, requiring 1,048,576 hashes on
average. The nonce and both signatures are bound to that exact transaction and
network. Every newly created output starts a new shield. A spend back to the
same address solely to restart its shield is a DEFEND renewal.
