# GoDark Go SDK -- MM Distribution

This bundle is a self-contained darkpool trading distribution for the GoDark
DEX. Two ready-to-run example binaries are included, plus the Go source
they were built from and the vendored `godark` module so you can rebuild
locally with only a standard Go toolchain (`go 1.22+`).

```
.
├── quickstart                 prebuilt -- place + cancel happy path
├── full_trader                prebuilt -- subscribe + place + modify + cancel + push drain
├── go.mod                     workspace manifest (`replace godark => ./sdk`)
├── go.sum
├── .env.example
├── README.md                  (this file)
├── SDK_REFERENCE.md           API reference for the godark package
├── examples/
│   ├── quickstart/main.go
│   ├── full_trader/main.go
│   └── internal/envloader/    .env loader + OrderError pretty-print
└── sdk/
    ├── UPSTREAM_REF           pinned upstream commit
    ├── go.mod
    ├── shared/symbols.json
    ├── proto/                 pre-generated protobuf bindings
    └── *.go                   godark package source
```

## 1. Configure credentials

```bash
cp .env.example .env
# then edit .env to fill in:
#   GODARK_API_KEY_ID=gdk_...
#   GODARK_API_SECRET=...
#   # GODARK_EDGE_URL=wss://api.godark-dex.com   (optional override)
```

Credentials come from your GoDark dashboard. The OS environment always
wins over `.env`, so CI / container deployments can pass them in via
`-e GODARK_API_KEY_ID=... -e GODARK_API_SECRET=...` etc.

## 2. Run the prebuilt binaries

The prebuilt binaries are static-ish Linux x86_64 ELFs (`CGO_ENABLED=0`)
that link only against the resolver and standard system libs.

```bash
./quickstart
./full_trader
```

`quickstart` places a far-from-mid SELL and immediately cancels it -- it's
the smallest possible round-trip against the encrypted trading API.

`full_trader` walks through the full lifecycle: subscribe to private
order + position streams, place a LIMIT BUY, modify its price, place +
cancel a SELL, cancel the BUY, then drain all the buffered push streams
(positions snapshots, system health, balance updates, margin alerts,
funding rates, settlement batches) before disconnecting.

## 3. Rebuild from source (optional)

The bundle ships every Go file the prebuilt binaries were compiled from,
so you can rebuild against the bundled `sdk/` without network access to
the upstream repo:

```bash
go build ./examples/quickstart   # produces ./quickstart
go build ./examples/full_trader  # produces ./full_trader
```

The `replace github.com/gq-godark/gdx-go-sdk => ./sdk` directive in
`go.mod` ensures `go build` resolves the SDK from the bundled vendored
copy (no GOPROXY hit required for the godark module itself; transitive
dependencies like `github.com/coder/websocket` come from `go.sum` and your
configured GOPROXY).

## 4. Use the SDK in your own code

Copy the bundled `sdk/` into your project (or unpack the bundle next to
your repository) and add the same `replace` directive to your own
`go.mod`:

```go
module github.com/your-org/your-mm-bot

go 1.22

require github.com/gq-godark/gdx-go-sdk v0.1.0

replace github.com/gq-godark/gdx-go-sdk => ./vendor/godark/sdk
```

Then import:

```go
import "github.com/gq-godark/gdx-go-sdk"
```

See `SDK_REFERENCE.md` for the full API surface (trading client, REST
client, market-data client, push streams, error types).

## 5. Pinned upstream

The Go sources under `sdk/` came from a specific commit of the public
`gq-godark/gdx-go-sdk` repository. The exact SHA is recorded in
`sdk/UPSTREAM_REF`; the release pipeline parity-checks the vendored copy
against that pin on every build so this bundle is reproducible from
upstream source.

## 6. Support

If you hit a wire-format issue, a session/handshake error, or a missing
SDK feature, please contact your GoDark Capital integrator contact with:

  - the contents of `sdk/UPSTREAM_REF` (so we know which SDK revision)
  - the operation that failed
  - any error code or symbolic reason the SDK surfaced
  - whether you can reproduce against `quickstart` or `full_trader`
