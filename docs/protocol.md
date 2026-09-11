# Network and wallet protocol

This document covers peer connections, address encoding, wallet recovery and
the transaction fields added by QDAY. Block and transaction validity rules are
defined in [`consensus.md`](consensus.md); fixed mainnet values are listed in
[`parameters.md`](parameters.md).

## Network identity

QDAY mainnet is identified by both values below:

```text
P2P magic:  51 44 41 59 00 01 a7 4e
Genesis ID: d71aebcb687c2fca4d3a5819e6c632efa7d46731395970fce081f3dc57606a40
```

A connection is accepted only when both values match. This keeps Sia peers and
QDAY nodes built for another genesis outside the mainnet peer graph.

## Peer-to-peer transport

Peer-to-peer (P2P) traffic uses TCP port 19771. The initiator and responder
exchange the eight-byte QDAY network marker before the length-prefixed
connection header. The header contains:

- 32-byte genesis ID;
- random 8-byte node ID;
- advertised `host:port` string.

The header uses little-endian length encoding. After the handshake, one TCP
connection carries multiple requests. Peers exchange addresses,
checkpoints, headers, blocks, compact block outlines and transaction sets using
the QDAY binary encoding.

Blocks and transactions received from peers pass through the same validation
path as blocks found by the local miner and transactions created by the wallet.

## Bootstrap and discovery

Mainnet publishes three bootstrap endpoints:

```text
seed1.pqday.com:19771
seed2.pqday.com:19771
seed3.pqday.com:19771
```

Each DNS name may return several IPv4 or IPv6 addresses. A node treats every
answer as a separate connection candidate, refreshes DNS every five minutes and
keeps the last successful answers if DNS temporarily fails.

A wallet first tries peers saved from earlier sessions. It uses the bootstrap
nodes when it still needs connections. After at least two other peers have
stayed synchronized for 30 seconds and returned a fresh peer list, the wallet
closes its outbound bootstrap connections. If those peers disappear, it may
connect to the bootstrap nodes again.

A bootstrap node runs in seed mode. It keeps its configured links to the other
bootstrap nodes and maintains up to eight additional outbound peer connections.
Seed mode has no wallet keys and never starts mining by itself.
Each public seed accepts up to 256 inbound QDAY connections. Desktop nodes keep
the normal 64-connection inbound limit.

## Listeners and routers

The standalone node defaults to:

```text
P2P:       :19771
Wallet API: 127.0.0.1:19770
```

The standalone server uses the fixed ports above. The desktop wallet chooses a
free P2P port on first launch and stores it in `p2p.json`; later launches reuse
that port when it is available. Universal Plug and Play (UPnP) asks the router
to expose only the P2P TCP listener. The local HTTP API remains reachable only
from the same computer.

UPnP cannot open a port through carrier-grade network address translation
(CGNAT), a second upstream router or a VPN without port forwarding. A wallet in
that situation can still make outbound connections, synchronize blocks and
relay transactions. Other peers usually cannot initiate a connection back to
it.

## Address encoding

The canonical address is a 64-character Bech32m string:

```text
prefix:         qday
address format: 1
payload:        32-byte QDAY spending-rule hash
checksum:       6 Bech32m symbols
```

The parser rejects mixed case, another prefix, another address format,
noncanonical padding or an all-zero payload. It also accepts a 141-character
public-key form for compatibility and resolves it to the same spending-rule
hash. Software that displays or creates addresses must use the 64-character
form.

The 32-byte payload commits to a rule requiring both an Ed25519 key and an
SLH-DSA-SHA2-128s key. The address does not expose either public key. Spending
the output reveals both public keys and both signatures.

## Seed phrase and key derivation

The 24-word seed phrase is the wallet's recovery secret. Anyone who has it can
spend the wallet's coins. It uses the English BIP39 word list and checksum to
encode a 32-byte seed, but it does not use the standard BIP39 or BIP32 key
derivation. A general-purpose BIP39 wallet does not recreate a QDAY address.

QDAY derives both private keys directly from the decoded seed `S`:

```text
edMaterial = SHA-512("QDAY/ed25519/v1" || S)
pqMaterial = SHA-512("QDAY/slhdsa/v1" || S)
```

The first 32 bytes of `edMaterial` seed the Ed25519 key. `pqMaterial` is the
deterministic input for SLH-DSA-SHA2-128s key generation. The quoted labels are
literal bytes. One seed phrase always restores the same two private keys and
one QDAY address; the bundled wallet does not derive additional accounts.

The wallet passphrase only encrypts the local `wallet.key` file. It does not
change the address or replace the seed phrase. A restored seed phrase may be
protected with a new local passphrase. If the passphrase is forgotten, the
24-word seed phrase is required to restore access.

`wallet.key` encrypts the 32-byte seed with AES-256-GCM. Argon2id derives the
encryption key using a 16-byte salt, three iterations, 64 MiB of memory, two
lanes and a 32-byte result. The authenticated data binds the encrypted file to
its QDAY address. The passphrase must contain at least 12 characters.

## Transaction header

Every QDAY transaction identifies its role through this binary value in
`arbitraryData`:

```text
"QDAY" || 0x02 || kind || LE64(nonce) || LE16(keyCount)
|| keyDescriptors || optionalWitness
```

Each key descriptor is `Ed25519PublicKey || SLHDSAPublicKey`, 64 bytes. Only a
kind `3` challenge-proof transaction has the final field; it contains the
32-byte little-endian solution to the fixed challenge. Exact validation rules
are specified in
[`consensus.md`](consensus.md).

## Amount encoding

Atomic units are the integers stored by consensus and never change. QDAY
changes the number of atomic units represented by one displayed QDAY when the
challenge proof triggers the QDAY event:

```text
before the QDAY event: 1000000000000000000000000
after the QDAY event:  1000000000000000000
```

The wallet API sends a decimal QDAY amount together with the current `unit`
value. The node rejects a request if QDAY occurred after the screen prepared
it, preventing a 1,000,000× amount error.
