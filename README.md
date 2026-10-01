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
│   ├── full_trader_example/main.go       subscribe + place + modify + cancel + mass-quote + batch-cancel
│   ├── full_trader_rest/main.go          maintainer-only REST trading diagnostic
│   ├── rest_client_example/main.go       REST residual reads
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

Copy `.env.example` to `.env`. Set values in `.env` or the process environment.
This file lists names only.

Hosted key-pair auth: `GODARK_API_KEY_ID`, `GODARK_API_SECRET`,
`GODARK_PASSPHRASE`. Optional: `GODARK_ACCOUNT` (fallback when auth omits the
Solana account), `GODARK_EDGE_URL`, `GODARK_REST_URL`,
`GDX_HPKE_STATIC_PUBLIC_KEY` (required on localnet; baked in for testnet and
devnet).

## Localnet (`gdx up`)

Set `GODARK_EDGE_URL`, `GODARK_REST_URL`, `GODARK_API_KEY`, and
`GDX_HPKE_STATIC_PUBLIC_KEY` in `.env`. See the commented localnet block in
`.env.example`. Do not commit filled-in credentials.

## Local development

```bash
go build ./examples/...                  # compile every example binary
go vet ./...                             # static checks
go run ./examples/quickstart             # run quickstart against testnet
go run ./examples/full_trader_example    # run full trader against testnet
go run ./examples/rest_client_example   # REST auth + account/public MD reads
```

`quickstart` subscribes to `orders` before placing so default **book** confirmation
receives the private OPEN update (then cancel). Do not skip that subscribe when
copying the pattern into your own scripts.

Participant path (also in `bundle/README.md`): install Go, set the env names
above, REST `Connect` (`POST /api/v1/auth/token`), WebSocket `Connect` (login
uses that `access_token`), `Subscribe` to `orders` and `positions`, place with
string `Price` / `Quantity`, read positions (`GetPositions` or
`PositionsSnapshots`), then `CancelOrder`.

`/ws/v1` channels: `orders`, `positions`, `volume`, `open_interest`,
`funding_rate`. Unknown channels, including `trades`, error on the subscribe
waiter. There is no trades channel on `/ws/v1`.

`ClientOrderID` is registered only after a successful WebSocket place, and
only when `POST /orders/_register_coid` returns HTTP 200. REST place does not
register it. `SlippageBps` is only for `MARKET` and `STOP_MARKET`. `PEG` is
incompatible with post-only.

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

  - `GodarkClient` serializes trading command sends (one command in flight
    on the transport). Push-stream channels and callbacks still run
    alongside that send path.
  - `GodarkRestClient` supports one-shot HPKE place / modify / cancel,
    mass-quote, batch cancel / modify, leverage updates, encrypted open-order /
    position / account snapshots, authenticated order / profile / balance /
    leverage reads, and public funding / open-interest / volume reads. REST
    does not provide private push streams, subscription replay, automatic
    reconnect, or a persistent HPKE session; use `GodarkClient` when those
    WebSocket capabilities are required.
  - Push streams expose buffered Go channels (default 256) and per-stream
    callback registration; both surfaces fire concurrently with command
    issuance.
  - The SDK auto-reconnects after unexpected WebSocket disconnects by default
    (re-auth, HPKE setup, subscription replay). Set `DisableAutoReconnect: true`
    on `ClientConfig` or `MarketDataConfig` to keep caller-managed reconnect.
    `OnError` reports stale heartbeat disconnects; `OnReconnect` fires after a
    successful automatic reconnect. Manual `Disconnect()` does not reconnect.

## License

MIT. See `LICENSE` in the upstream `gdx-go-sdk` repository.
