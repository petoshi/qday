# QDAY core

This directory contains QDAY's consensus-critical types, validation rules,
binary encoding, proof of work and peer protocol. It began as a fork of
[Sia Core](https://github.com/SiaFoundation/core). The inherited
`go.sia.tech/core` module path remains in place so the QDAY packages can stay
close to the code they came from.

Do not replace this directory with the public Sia module when building QDAY.
An ordinary Sia node has a different genesis, network handshake, address format
and transaction rules and cannot validate QDAY mainnet.

The QDAY-specific code includes:

- dual Ed25519 and SLH-DSA spending policies;
- the QDAY transaction envelope and mainnet replay domain;
- the fixed Edwards25519 canary and PQ Day state transition;
- denomination, shield, decay and DEFEND rules;
- the block 9,100 mining marker and atomic-swap policies;
- the QDAY network handshake and compact block relay.

The inherited `rhp` packages remain for source compatibility. QDAY mainnet is a
coin chain and rejects Sia file contracts, siafunds and foundation updates.

Read the public [consensus rules](../docs/consensus.md), [network
protocol](../docs/protocol.md) and [mainnet parameters](../docs/parameters.md)
before integrating these packages.

## Test

From this directory:

```sh
go test ./...
```

From the repository root, `make test` runs this module together with the rest
of QDAY.

## License and origin

The code is available under the [MIT License](LICENSE). Its Sia ancestry and
third-party notices remain in the source history and the repository's
[`licenses`](../licenses) directory.
