# GoDark Go SDK

This package provides the GoDark Go SDK and minimal examples for encrypted
darkpool trading.

Order types: `MARKET`, `LIMIT`, `PEG`, `STOP_MARKET`, `STOP_LIMIT`.
`SlippageBps` is valid only on `MARKET` and `STOP_MARKET`. `PEG` cannot be
combined with post-only. Prices and sizes are decimal Go strings.

## Package contents

- `examples/` — `quickstart`, `full_trader_example`, and `rest_client_example` sources
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

Copy `.env.example` to `.env`. Set values only in that file or the process
environment. This README names the variables and does not include secrets.

Required for hosted key-pair auth:

- `GODARK_API_KEY_ID`
- `GODARK_API_SECRET`
- `GODARK_PASSPHRASE`

Optional:

- `GODARK_EDGE_URL` — WebSocket origin. Empty uses the testnet preset.
- `GODARK_REST_URL` — REST origin. Empty is derived from the edge host.
- `GODARK_ACCOUNT` — Solana account fallback when an older edge omits it.
- `GDX_HPKE_STATIC_PUBLIC_KEY` — sequencer HPKE pin. Required on localnet;
  testnet and devnet pins are baked in. Aliases: `GDX_HPKE_STATIC_PUBKEY`,
  `GODARK_HPKE_STATIC_PUBLIC_KEY`, `VITE_GDX_HPKE_STATIC_PUBKEY`.

The OS environment always wins over `.env`.

## 4) Build and run the examples

From inside the unzipped bundle:

```bash
go build ./examples/quickstart            # produces ./quickstart
go build ./examples/full_trader_example   # produces ./full_trader_example
go build ./examples/rest_client_example   # produces ./rest_client_example
```

Then run either binary:

```bash
./quickstart
./full_trader_example
./rest_client_example
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

Key-pair clients mint `POST /api/v1/auth/token` and log in on `/ws/v1` with
that REST `access_token`. The key id, secret, and passphrase are not the
WebSocket login token.

`/ws/v1` subscribe channels are `orders`, `positions`, `volume`,
`open_interest`, and `funding_rate`. An unknown channel (including `trades`)
returns an error on the subscribe waiter instead of waiting out the command
timeout. There is no trades channel on `/ws/v1`.

`Price`, `Quantity`, and the other price/size fields are Go `string` values.
`ClientOrderID` is sent to `POST /orders/_register_coid` only after a
successful WebSocket place, and only a HTTP 200 response counts as
registered. A REST place does not register a client-order id.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/gq-godark/gdx-go-sdk"
)

func main() {
    ctx := context.Background()

    rest, err := godark.NewRestClient(godark.RestClientConfig{
        APIKeyID:   os.Getenv("GODARK_API_KEY_ID"),
        APISecret:  os.Getenv("GODARK_API_SECRET"),
        Passphrase: os.Getenv("GODARK_PASSPHRASE"),
        BaseURL:    os.Getenv("GODARK_REST_URL"),
    })
    if err != nil {
        log.Fatal(err)
    }
    if err := rest.Connect(ctx); err != nil { // REST auth/token
        log.Fatal(err)
    }
    defer rest.Disconnect(ctx)

    pos, err := rest.GetPositions(ctx)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("positions=%d\n", len(pos.Rows))

    client, err := godark.NewClient(godark.ClientConfig{
        APIKeyID:   os.Getenv("GODARK_API_KEY_ID"),
        APISecret:  os.Getenv("GODARK_API_SECRET"),
        Passphrase: os.Getenv("GODARK_PASSPHRASE"),
        BaseURL:    os.Getenv("GODARK_EDGE_URL"),
    })
    if err != nil {
        log.Fatal(err)
    }
    if err := client.Connect(ctx); err != nil { // WS login uses the REST access token
        log.Fatal(err)
    }
    defer client.Disconnect()

    if err := client.Subscribe(ctx, "orders", "positions"); err != nil {
        log.Fatal(err)
    }

    ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
        Symbol:        "BTC-USDC-PERP",
        Side:          godark.SideSell,
        OrderType:     godark.OrderTypeLimit,
        Price:         "999999",
        Quantity:      "0.01",
        ClientOrderID: "demo-coid-1",
    })
    if err != nil {
        log.Fatal(err)
    }

    if _, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP"); err != nil {
        log.Fatal(err)
    }
}
```

`SlippageBps` belongs only on `MARKET` and `STOP_MARKET`. Do not set post-only
on a `PEG` order.

See `SDK_REFERENCE.md` for the full client API.
