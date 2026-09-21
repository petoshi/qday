# Mainnet canary derivation

The QDAY canary is a reproducible NUMS point. No secret scalar is used anywhere
in its construction. Anyone can verify that generating this point did not
reveal its discrete logarithm.

The point is committed by the mainnet genesis block. The scalar `x` satisfying
`x·G = C` is the solution that starts the six-block PQ Day fuse.

## Fixed procedure

The derivation has no private input.

| Item | Value |
| --- | --- |
| Group | prime-order Edwards25519 subgroup |
| Domain | ASCII `QDAY/Edwards25519/NUMS/canary/v1/` |
| Counter | unsigned 64-bit integer, little-endian |
| Hash | BLAKE2b-256 |
| Point encoding | canonical compressed Edwards25519 |
| Cofactor | `8` |
| Selection | first canonical point that is not the identity after cofactor clearing |

For counters starting at zero:

```text
input = ASCII("QDAY/Edwards25519/NUMS/canary/v1/") || LE64(counter)
h     = BLAKE2b-256(input)
P     = DecodeCanonicalEdwards25519(h)
```

If `h` is not a canonical compressed Edwards25519 point, reject it and increment
the counter. Otherwise calculate:

```text
C = 8·P
```

Reject the identity point. The first remaining `C` is the canary. This procedure
hashes directly to a curve point. It never hashes to a scalar and never creates
the point by multiplying a chosen scalar by the base point.

## Mainnet transcript

Counter `0`:

```text
counter bytes: 0000000000000000
input:         514441592f4564776172647332353531392f4e554d532f63616e6172792f76312f0000000000000000
BLAKE2b-256:   f4b17b5acddbac3db93e510d203c82ea9d6e869070a40f275581d950723dc174
result:        rejected, not a canonical Edwards25519 point encoding
```

Counter `1`:

```text
counter bytes: 0100000000000000
input:         514441592f4564776172647332353531392f4e554d532f63616e6172792f76312f0100000000000000
BLAKE2b-256:   29e995bdd3f9d8cdc0e6c85d7222f9d5e627010f1ae6a7b0a77eafb0c06aa123
result:        canonical Edwards25519 point encoding
```

After cofactor clearing, counter `1` produces:

```text
7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771
```

That value exactly matches `network.qday.canary` in
[`qday-mainnet.json`](../qday-mainnet.json).

## The same point as a Solana address

The QDAY manifest stores `C` as 32 compressed Edwards25519 bytes and this
document prints those bytes as hexadecimal. A Solana public key is also 32
bytes, displayed with unchecksummed Base58 encoding.

Decode the QDAY hexadecimal string to bytes and Base58-encode those exact
bytes. Do not hash them and do not perform another key derivation:

```text
QDAY hexadecimal
7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771

hex decode -> the same 32 bytes -> Base58 encode

Solana public key
8kwjLg5bY5i3XMAab6oUgnX82nPCedE3EgDSFkHWcCkY
```

The `qday-canary` verifier performs this encoding and prints the Solana public
key. Its Base58 implementation is in
[`core/cmd/qday-canary/main.go`](../core/cmd/qday-canary/main.go), next to the
independent point derivation.

The conversion can also be reproduced with only Python's standard library:

```sh
python3 - <<'PY'
point_hex = "7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771"
alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
raw = bytes.fromhex(point_hex)
leading_zeroes = len(raw) - len(raw.lstrip(b"\x00"))
value = int.from_bytes(raw, "big")
encoded = ""
while value:
    value, remainder = divmod(value, 58)
    encoded = alphabet[remainder] + encoded
address = "1" * leading_zeroes + encoded
print(address)
PY
```

Expected output:

```text
8kwjLg5bY5i3XMAab6oUgnX82nPCedE3EgDSFkHWcCkY
```

This is an encoding equivalence, not a bridge, wrapper, hash or second key.
Because `C` is a canonical point in the prime-order Edwards25519 subgroup, the
same scalar `x` satisfying `x·G = C` is the signing scalar for that public key.

The standard-library verification test in
[`core/cmd/qday-canary/solana_test.go`](../core/cmd/qday-canary/solana_test.go)
constructs an Ed25519 signature directly from a canonical test scalar and
verifies it against `x·G` with Go's standard `crypto/ed25519` verifier. It
demonstrates the signing consequence without knowing the mainnet canary scalar.

## Reproduce it

From the repository root:

```sh
go run ./core/cmd/qday-canary -manifest ./qday-mainnet.json
```

The final lines must be:

```text
derived challenge: 7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771
manifest challenge: 7343aaaab7bb999347740b9e1932f5487046f56b1566ab23cac0b129adefb771
Solana address: 8kwjLg5bY5i3XMAab6oUgnX82nPCedE3EgDSFkHWcCkY
match: true
```

For a machine-readable transcript:

```sh
go run ./core/cmd/qday-canary -manifest ./qday-mainnet.json -json
```

The verifier implements the derivation independently. It uses
`golang.org/x/crypto/blake2b` instead of calling the consensus canary generator.
It exits with status `1` if the derived point differs from the manifest.

## What this establishes

The transcript establishes that the published procedure requires no scalar and
produces the exact point committed by mainnet. Planting a chosen scalar in this
procedure would require finding a suitable BLAKE2b preimage or solving the
Edwards25519 discrete logarithm problem.

Non-knowledge itself cannot be proven. The transcript cannot establish that no
party has independently solved the discrete logarithm since the point was
created. A valid scalar remains the only consensus proof that the challenge has
been solved.

The consensus generator is in
[`core/consensus/qday.go`](../core/consensus/qday.go). The independent verifier
is in [`core/cmd/qday-canary`](../core/cmd/qday-canary).
