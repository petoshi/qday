# QDAY mainnet parameters

This file is the short reference for values fixed by QDAY mainnet. Block-time
conversions are estimates based on the 60-second target; consensus always uses
block heights.

## Network identity

| Parameter | Value |
| --- | --- |
| Network name | `qday-mainnet` |
| Software release | `0.5.0` |
| Mainnet start time (UTC) | `2026-09-11T06:59:00Z` |
| Genesis ID | `d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40` |
| Manifest | `qday-mainnet.json` |
| Manifest SHA-256 | `14d4a47a850f5ba9d81129142d8718c323d826b5f9459516374c4f0f92eaa65b` |
| Network domain | `710a192c2abef41f39cb23ef575df376436d61aa54ca84c6ade4831f6fba2244` |
| P2P network marker | `514441590001a74e` |
| P2P TCP port | `19771` |
| Local wallet API TCP port | `19770` |

Genesis inscription:

```text
PQ DAY IS INEVITABLE. YOU'RE CELEBRATING IT WITH ME.
```

The genesis transaction stores the network parameters, the 500,000 QDAY
premine and this inscription. Changing any of them produces a different
genesis ID and therefore a different network.

## Proof of work

| Parameter | Value |
| --- | --- |
| Function | BLAKE2b-256 |
| Header length | 80 bytes |
| Header layout | parent ID, nonce, Unix timestamp, commitment |
| Integer encoding | unsigned 64-bit little-endian |
| Initial target | `0000100000000000000000000000000000000000000000000000000000000000` |
| Initial expected hashes | `1,048,575` |
| Eligible nonces | every unsigned 64-bit value |
| Target block interval | `60 seconds` |
| Difficulty algorithm | Oak / Final Cut |
| Maximum per-block adjustment | approximately `0.4%` |
| Maximum transaction weight per block | `2,000,000` |
| Wait before a mining reward can be spent | `60 blocks` |

The expected-hashes figure is a statistical average at the initial target. An
individual CPU may find a block immediately or run much longer than the
average. The difficulty changes after every block to keep the long-term block
interval near 60 seconds.

## Issuance

The amounts in this table use the original denomination, where one QDAY equals
`10^24` atomic units.

| Parameter | Value |
| --- | --- |
| Genesis premine | `500,000 QDAY` |
| Premine address | `qday1pdsa0ezy7y3nnmnxm0tx74q2kd9acvzs8329wfd2n6ycqnmygtt9stpwtsr` |
| Block reward | `8 QDAY + transaction fees` |
| Rewarded block heights | `1–1,000,000` |
| Total mined rewards | `8,000,000 QDAY` |
| Maximum supply created by premine and rewards | `8,500,000 QDAY` |
| Premine share of created supply | approximately `5.88%` |
| Reward after height 1,000,000 | transaction fees only |

The 1,000,000× QDAY change does not create atomic units; it changes their
display denomination. After the event, the same maximum created supply is
displayed as 8,500,000,000,000 QDAY, and the same atomic block reward is
displayed as 8,000,000 QDAY. Mining and the reward schedule continue after the
event. Fees move existing units. Decay can permanently reduce spendable supply.

## Transactions and keys

| Parameter | Value |
| --- | --- |
| Canonical address | 64-character Bech32m with `qday` prefix and format byte `1` |
| Address payload | 32-byte hash of the required two-key spending rule |
| Classical signature | Ed25519 |
| Reserve signature | FIPS 205 SLH-DSA-SHA2-128s |
| Spending requirement | both Ed25519 and SLH-DSA signatures |
| SLH-DSA signature length | `7,856 bytes` |
| Seed phrase | 24 words, English BIP39 word list |
| Seed entropy/checksum | 256-bit entropy + 8-bit checksum |
| Wallet addresses per seed phrase | `1` |
| Wallet transfer fee | `0.001 QDAY` before QDAY; `1,000 QDAY` after QDAY |
| Inputs accepted by consensus | at most `128` |
| Outputs accepted by consensus | at most `128` |
| Inputs selected by the bundled wallet | at most `64` per transfer |

The fee has the same atomic value on both sides of the QDAY event. Its displayed
amount changes because the denomination changes.

## The QDAY event

Mainnet contains one fixed Edwards25519 challenge point. Its solution is the
scalar `x` for which `x·G` equals that point. The first valid proof transaction
starts the QDAY countdown. If miners include it in block `h`, QDAY begins at
block `h + 6`.

| Parameter | Value |
| --- | --- |
| Challenge group | prime-order Edwards25519 subgroup |
| Challenge point | `7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771` |
| Solution encoding | nonzero scalar, canonical 32-byte little-endian form |
| Minimum proof fee | `1 QDAY` before the QDAY event |
| Proof countdown | `6 blocks` |
| Denomination multiplier | `1,000,000` |
| Shield duration | `1,440 blocks` |
| Decay duration | `10,080 blocks` |
| Decay step | `60 blocks` |
| DEFEND work | `20 leading zero bits` |

At the QDAY block, one displayed QDAY changes from `10^24` to `10^18` atomic
units, making every unchanged atomic balance display as 1,000,000 times as many
QDAY. Every output is fully spendable for 1,440 blocks, about 24 hours at the
target interval. It then loses spendable value over 10,080 blocks, about seven
days. A transaction creates new outputs and starts new shields for them. Every
spend after QDAY must also perform the fixed 20-bit DEFEND proof of work, which
takes about 1,048,576 hashes on average.

## Bootstrap nodes

New nodes use these addresses to find their first peers. They keep the peers
they discover and return to the bootstrap nodes if those connections disappear.

| Name | Endpoint |
| --- | --- |
| Seed 1 | `seed1.pqday.com:19771` |
| Seed 2 | `seed2.pqday.com:19771` |
| Seed 3 | `seed3.pqday.com:19771` |
