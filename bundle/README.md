# GoDark Go SDK

This package provides two market-maker example sources for the GoDark Go
SDK, **plus the vendored `godark` module**, so you can build the examples
or scaffold your own bot directly against the shipped sources — no
private registry, no `protoc` (the SDK ships pre-generated protobuf
bindings under `sdk/proto/`).

Supported order types in this distribution: `MARKET`, `LIMIT`.

## Package contents

- `examples/` — example **source files** (`quickstart/main.go`,
  `full_trader_example/main.go`, `internal/envloader/envloader.go`)
- `sdk/` — **vendored `godark` module** source (with pre-generated
  protobuf bindings under `sdk/proto/`); `sdk/UPSTREAM_REF` records the
  exact upstream commit this distribution was cut from
- `go.mod`, `go.sum` — workspace manifest wiring
  `replace github.com/gq-godark/gdx-go-sdk => ./sdk`, ready for
  `go build ./examples/...`
- `README.md`, `SDK_REFERENCE.md` — recipient docs
- `.env.example` — environment template

## 1) Prerequisites

| Item    | Requirement                                                                       |
|---------|-----------------------------------------------------------------------------------|
| OS / arch | any platform Go supports (Linux, macOS, Windows; amd64, arm64, …)                |
| Go      | stable ≥ 1.22 (`https://go.dev/dl/`)                                              |
| Network | a Go module proxy (default `proxy.golang.org`) for the third-party runtime modules (`coder/websocket`, `google/uuid`, `golang.org/x/crypto`, `google.golang.org/protobuf`); the `godark` module itself is bundled in `sdk/` |

## 2) Create testnet credentials

1. Open the testnet frontend: `https://app.godark-dex.com`
2. Create an account using email sign-up.
3. Fund the account using the faucet: `https://faucet.godark-dex.com`
4. In the frontend, go to **Settings → API Key Management** and click
   **Create API Key**.

## 3) Configure environment

Copy `.env.example` to `.env` and set:

- `GODARK_API_KEY_ID`
- `GODARK_API_SECRET`

```bash
cp .env.example .env
$EDITOR .env       # fill in your testnet creds
```

Optional override:

- `GODARK_EDGE_URL` — defaults to `wss://api.godark-dex.com` if unset.

The OS environment always wins over `.env`.

## 4) Build and run the examples

From inside the unzipped bundle:

```bash
go build ./examples/quickstart            # produces ./quickstart
go build ./examples/full_trader_example   # produces ./full_trader_example
```

Then run either binary:

```bash
./quickstart
./full_trader_example
```

The bundled `go.mod` wires `replace github.com/gq-godark/gdx-go-sdk =>
./sdk`, so the build resolves the `godark` module entirely from the
vendored copy. `go` only fetches the third-party runtime modules
(`coder/websocket`, `google/uuid`, `golang.org/x/crypto`,
`google.golang.org/protobuf`) from the configured GOPROXY.

## Go integration (your own bot)

The bundle includes a vendored `godark` module under `sdk/`. To build your
own bot against the same SDK revision, point your `go.mod` at the bundled
module via a path-based `replace` directive:

```go
// go.mod — your own bot
module github.com/your-org/your-mm-bot

go 1.22

require github.com/gq-godark/gdx-go-sdk v0.1.0

replace github.com/gq-godark/gdx-go-sdk => ./vendor/godark/sdk
```

(Or copy `sdk/` into your own project and reference it as
`replace ... => ./sdk`.)

Then in `cmd/bot/main.go`:

```go
package main

import (
    "context"
    "log"
    "os"

    "github.com/gq-godark/gdx-go-sdk"
)

func main() {
    client, err := godark.NewClient(godark.ClientConfig{
        APIKeyID:  os.Getenv("GODARK_API_KEY_ID"),
        APISecret: os.Getenv("GODARK_API_SECRET"),
    })
    if err != nil {
        log.Fatal(err)
    }

    ctx := context.Background()
    if err := client.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer client.Disconnect()

    ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
        Symbol:    "BTC-USDC-PERP",
        Side:      godark.SideSell,
        OrderType: godark.OrderTypeLimit,
        Price:     999_999,
        Quantity:  0.01,
    })
    if err != nil {
        log.Fatal(err)
    }

    if _, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP"); err != nil {
        log.Fatal(err)
    }
}
```

If you'd rather pin against the upstream `gdx-go-sdk` repository directly
(useful if you're tracking a moving branch rather than a release pin), the
bundled `sdk/UPSTREAM_REF` file records the exact commit this distribution
was built from:

```go
replace github.com/gq-godark/gdx-go-sdk => github.com/gq-godark/gdx-go-sdk <contents of sdk/UPSTREAM_REF>
```

See `SDK_REFERENCE.md` for the full client API.
