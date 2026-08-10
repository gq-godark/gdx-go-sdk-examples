# GoDark Go SDK

This package provides the GoDark Go SDK and minimal examples for encrypted
darkpool trading.

Supported order types in this distribution: `MARKET`, `LIMIT`.

## Package contents

- `examples/` — `quickstart` and `full_trader_example` sources
- `sdk/` — bundled `godark` module
- `go.mod`, `go.sum` — workspace manifest for `go build ./examples/...`
- `README.md`, `SDK_REFERENCE.md` — recipient docs
- `.env.example` — environment template

## 1) Prerequisites

| Item    | Requirement                                                                       |
|---------|-----------------------------------------------------------------------------------|
| OS / arch | any platform Go supports (Linux, macOS, Windows; amd64, arm64, …)                |
| Go      | stable ≥ 1.22 (`https://go.dev/dl/`)                                              |
| Network | default Go module proxy (`proxy.golang.org`) for third-party deps; `godark` is bundled in `sdk/` |

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
- `GODARK_PASSPHRASE` — required for API key-pair auth.

```bash
cp .env.example .env
$EDITOR .env       # fill in your testnet creds
```

Optional override:

- `GODARK_EDGE_URL` — override the edge URL (default: public testnet `wss://api.godark-dex.com` via the SDK Testnet environment preset).
- `GDX_NOISE_STATIC_PUBLIC_KEY` — override the sequencer Noise pin. **Not required for public testnet** — the SDK Environment Testnet preset bakes it in. Aliases: `GDX_NOISE_STATIC_PUBKEY`, `GODARK_NOISE_STATIC_PUBLIC_KEY`.

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

The bundled `go.mod` resolves `godark` from `./sdk`.

## Go integration (your own bot)

Point your `go.mod` at the bundled module:

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
        APIKeyID:   os.Getenv("GODARK_API_KEY_ID"),
        APISecret:  os.Getenv("GODARK_API_SECRET"),
        Passphrase: os.Getenv("GODARK_PASSPHRASE"),
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

See `SDK_REFERENCE.md` for the full client API.
