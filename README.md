# gdx-go-sdk-examples

Self-contained darkpool trading examples for the GoDark Go SDK.

This repository is the **maintainer-grade** view: it builds the distribution
zip uploaded as a GitHub Release on every push to `main`, runs CI on every
PR, and tracks the pinned upstream `gq-godark/gdx-go-sdk` commit under
`sdk/UPSTREAM_REF`. Recipients receive the zip; see `bundle/README.md` for
the integrator-facing copy of these instructions.

## Layout

```text
gdx-go-sdk-examples/
├── go.mod                                module github.com/gq-godark/gdx-go-sdk-examples
├── go.sum                                with `replace godark => ./sdk`
├── README.md                             (this file)
├── SDK_REFERENCE.md                      maintainer view of the public surface
├── .env.example
├── bundle/
│   ├── README.md                         ships in the zip as ./README.md
│   └── SDK_REFERENCE.md                  ships in the zip as ./SDK_REFERENCE.md
├── examples/
│   ├── quickstart/main.go                place + cancel
│   ├── full_trader_example/main.go
│   ├── rest_client_example/main.go       REST residual reads       subscribe + place + modify + cancel + mass-quote + batch-cancel
│   └── internal/envloader/envloader.go   shared .env loader + OrderError printer
├── scripts/
│   ├── refresh_sdk.sh                    vendor a gdx-go-sdk checkout into sdk/
│   └── package.sh                        produce the source-only release zip
├── sdk/                                  vendored gdx-go-sdk source
│   ├── UPSTREAM_REF
│   ├── go.mod
│   ├── shared/symbols.json
│   ├── proto/                            committed proto bindings
│   └── *.go
└── .github/workflows/
    ├── auto-bump-sdk-pin.yml             Layer-2 listener (refresh on dispatch)
    └── release.yml                       PR CI + tagged Release on main
```

## Configure credentials

Copy `.env.example` to `.env` and set `GODARK_API_KEY_ID`, `GODARK_API_SECRET`,
and `GODARK_PASSPHRASE`. Public testnet needs only those three — the SDK
`EnvironmentTestnet` preset supplies the edge URL and Noise pin.

Optional overrides: `GODARK_EDGE_URL`, `GDX_NOISE_STATIC_PUBLIC_KEY` (not
required for testnet).

## Local development

```bash
go build ./examples/...                  # compile both example binaries
go vet ./...                             # static checks
go run ./examples/quickstart             # run quickstart against testnet
go run ./examples/full_trader_example    # run full trader against testnet
go run ./examples/rest_client_example   # REST auth + account/public MD reads
```

`quickstart` subscribes to `orders` before placing so default **book** confirmation
receives the private OPEN update (then cancel). Do not skip that subscribe when
copying the pattern into your own scripts.

The `replace github.com/gq-godark/gdx-go-sdk => ./sdk` directive in
`go.mod` resolves the SDK from the vendored copy, so `go build` never has
to fetch the godark module from a GOPROXY at all.

## Refreshing the vendored SDK

Either let the Layer-2 listener (`.github/workflows/auto-bump-sdk-pin.yml`)
open a rolling PR when `gdx-go-sdk@main` ships a new commit, or refresh
locally:

```bash
git clone git@github.com:gq-godark/gdx-go-sdk.git ../gdx-go-sdk
bash scripts/refresh_sdk.sh ../gdx-go-sdk
git add sdk/
git commit -m "chore(sdk): bump godark to <SHA>"
```

`scripts/refresh_sdk.sh`:
  - refuses dirty source worktrees (so the pin is reproducible),
  - rsyncs the Go source into `sdk/` minus `.git/`, `.github/`, `scripts/`,
    `gdx-proto/`, and `*_test.go` (size + scope hygiene),
  - writes the source commit SHA (or tag, when on one) into
    `sdk/UPSTREAM_REF`.

`scripts/package.sh` re-derives that pin and **parity-checks the vendored
sdk/ against a fresh upstream clone** before letting the build proceed, so
local hand-edits to `sdk/` can never silently make it into a release zip.

## Release pipeline

`.github/workflows/release.yml`:
  1. checks out this repo + the pinned upstream `gdx-go-sdk` ref,
  2. runs `scripts/package.sh`, which:
     - parity-checks `sdk/` against the upstream pin,
     - stages `<bundle>/{examples/, sdk/, README.md, SDK_REFERENCE.md, go.mod, go.sum, .env.example}`,
     - zips the staging dir.
  3. recipient-smoke-tests the zip: `unzip` into a clean dir and
     `go build ./examples/...` against the bundled go.mod (must produce
     working binaries with only GOPROXY hits for transitive deps).
  4. on `push` to `main`, attaches the zip to a tagged GitHub Release.

The GitHub App (`godark-ci`) used for cross-repo access only requires
`contents: read` on `gdx-go-sdk`; no PAT or SSH key is needed in CI.

## Concurrency contract

  - `GodarkClient` routes trading commands by correlation id, so multiple
    commands can be in flight concurrently (matching python / rust /
    java). Encrypted REST trading is not supported; all order flow goes
    over the WebSocket client.
  - Push streams expose buffered Go channels (default 256) and per-stream
    callback registration; both surfaces fire concurrently with command
    issuance.
  - The SDK does **not** auto-reconnect. `OnDisconnect` callbacks fire when
    the WS closes; the caller decides whether to `Connect` again.

## License

MIT. See `LICENSE` in the upstream `gdx-go-sdk` repository.
