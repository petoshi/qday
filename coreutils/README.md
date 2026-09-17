# QDAY coreutils

This directory supplies the chain manager, transaction pool, P2P syncer,
wallet index and native QDAY transaction and mining helpers used by the QDAY
node. It is derived from [Sia CoreUtils](https://github.com/SiaFoundation/coreutils)
and retains the `go.sia.tech/coreutils` module path.

Do not substitute the public Sia module in a QDAY build. QDAY uses its own
mainnet manifest, handshake, two-key spending rules, PQ Day state and block
9,100 mining format.

The main QDAY packages are:

- `chain`, which validates and selects the accumulated-work chain, maintains
  the mempool and loads the fixed QDAY manifest;
- `syncer`, which discovers QDAY peers and relays blocks and transaction sets;
- `wallet`, which tracks QDAY outputs and derives the native seed format;
- `qday`, which signs transfers, builds block candidates, prepares DEFEND work
  and implements the compact SiaMining marker.

The inherited RHP and test utility packages remain for source compatibility.
They do not make Sia storage contracts valid on QDAY mainnet.

Integration behavior is documented in the public [integration
guide](../docs/integrations.md). Fixed values are listed in [mainnet
parameters](../docs/parameters.md).

## Test

From this directory:

```sh
go test ./...
```

From the repository root, `make test` runs all QDAY modules.

## License and origin

The code is available under the [MIT License](LICENSE). Its Sia ancestry and
third-party notices remain in the source history and the repository's
[`licenses`](../licenses) directory.
