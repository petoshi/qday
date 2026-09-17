# QDAY node and desktop wallet

This directory contains the QDAY full node, local wallet service and desktop
launcher used by the official releases.

The distributed programs are:

- `cmd/qday`, the validating node with the authenticated local QDAY API;
- `cmd/qday-wallet`, the launcher that starts the node and opens the bundled
  browser interface.

Both validate the fixed QDAY mainnet manifest. From block 9,100 onward they
require QDAY v1.0.0 consensus rules. An upstream Sia `walletd` binary cannot
join QDAY or replace either program.

The directory was forked from [Sia walletd](https://github.com/SiaFoundation/walletd)
and still contains inherited wallet, database and low-level API packages. The
old `cmd/walletd` command and [`openapi.yml`](openapi.yml) describe that
inherited Sia service. They are not built into QDAY release archives and are
not the QDAY wallet API.

## Run QDAY

Build from the repository root:

```sh
make build
./build/qday --network ./qday-mainnet.json
```

The standalone node listens on TCP `19771` for QDAY peers and on
`127.0.0.1:19770` for its authenticated local API. It creates `api.token` in
the network data directory. Keep that API and token private.

To open the desktop wallet from a source build:

```sh
./build/qday-wallet --network ./qday-mainnet.json --node ./build/qday
```

Release archives, operating-system data paths and first-run instructions are
in the repository [README](../README.md). API routes are documented in
[`docs/api.md`](../docs/api.md). Pools, exchanges and explorers should start
with [`docs/integrations.md`](../docs/integrations.md).

## Test

From the repository root:

```sh
make test
make smoke
make browser
```

## License and origin

The code is available under the [MIT License](LICENSE). Its Sia ancestry and
third-party notices remain in the source history and the repository's
[`licenses`](../licenses) directory.
