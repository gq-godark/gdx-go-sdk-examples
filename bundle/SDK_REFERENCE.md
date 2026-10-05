# GoDark Go SDK Reference (MM Distribution)

This reference covers the `godark` public surface shipped under `sdk/` in
this bundle.

## Module + import

In your own `go.mod`, depend on the SDK via a path-based `replace`
directive pointing at the bundled copy this bundle ships (or a copy you
keep in your own repository):

```go
// go.mod
module github.com/your-org/your-mm-bot

go 1.22

require github.com/gq-godark/gdx-go-sdk v0.1.0

replace github.com/gq-godark/gdx-go-sdk => ./vendor/godark/sdk
```

Then import as usual:

```go
import "github.com/gq-godark/gdx-go-sdk"
```

The package name in code is `godark` (Go's "last path segment is the
package name" rule).

## Constructors

Trading is available through the encrypted WebSocket `GodarkClient` and the
one-shot HPKE `GodarkRestClient` (pin the sequencer static public key — see
Configuration in `README.md` / `.env.example`). A read-only
`MarketDataClient` is also available for streaming public market data.

### Encrypted WebSocket trading -- `GodarkClient`

```go
client, err := godark.NewClient(godark.ClientConfig{
    APIKeyID:   os.Getenv("GODARK_API_KEY_ID"),
    APISecret:  os.Getenv("GODARK_API_SECRET"),
    Passphrase: os.Getenv("GODARK_PASSPHRASE"),
    // HpkeStaticPublicKeyHex: os.Getenv("GDX_HPKE_STATIC_PUBLIC_KEY"),
    // BaseURL defaults to wss://api.godark-dex.com; override via
    // GODARK_EDGE_URL/GDX_EDGE_URL env vars or this field.
    BaseURL: os.Getenv("GODARK_EDGE_URL"),
})
if err != nil { ... }
```

Lifecycle:

```go
ctx := context.Background()
if err := client.Connect(ctx); err != nil { ... }   // REST access_token, then WS login + HPKE
defer client.Disconnect()

account := client.Account() // authenticated Solana account, base58
```

### One-shot encrypted REST -- `GodarkRestClient`

`GodarkRestClient` authenticates with the same credentials and encrypts each
private request with a separate HPKE setup:

```go
restClient, err := godark.NewRestClient(godark.RestClientConfig{
    APIKeyID:   os.Getenv("GODARK_API_KEY_ID"),
    APISecret:  os.Getenv("GODARK_API_SECRET"),
    Passphrase: os.Getenv("GODARK_PASSPHRASE"),
    Account:    os.Getenv("GODARK_ACCOUNT"),
    BaseURL:    os.Getenv("GODARK_REST_URL"),
})
if err != nil { ... }
if err := restClient.Connect(ctx); err != nil { ... }
defer restClient.Disconnect(ctx)

fmt.Printf("connected account=%s\n", restClient.Account())
```

Key-pair `Connect` on `GodarkClient` calls `POST /api/v1/auth/token` and sends
the returned `access_token` as the WebSocket login token.

The REST client supports place / modify / cancel, mass-quote, batch cancel /
modify, leverage updates, encrypted open-order / position / account snapshots,
authenticated order / profile / balance / leverage reads, and public
funding-rate / open-interest / volume reads. REST place does not register a
client-order id: the edge arms that correlation only for a WebSocket place.
Setting `ClientOrderID` on `PlaceOrderRestRequest` returns an error. After a
successful WebSocket place, the SDK calls `POST /orders/_register_coid` and
treats the id as registered only when that call returns HTTP 200.

REST has no private push streams, subscriptions, automatic reconnect, or
persistent HPKE session. Use `GodarkClient` for live private updates and
subscription replay. `Account` / `GODARK_ACCOUNT` is a fallback for legacy
edges that omit the authenticated Solana account; `UserUUID` remains
compatibility metadata and is not used for encrypted request identity.

### Public market-data feed -- `MarketDataClient`

Hosted market data defaults to `/ws/v1`. Public channels there are `volume`,
`open_interest`, and `funding_rate`. There is no `trades` channel on
`/ws/v1`. `SubscribeOrderbook` is rejected on that path. An unknown channel
fails the subscribe call immediately.

```go
md := godark.NewMarketDataClient(godark.MarketDataConfig{
    BaseURL: os.Getenv("GODARK_EDGE_URL"), // default path /ws/v1
})
if err := md.Connect(ctx); err != nil { ... }
defer md.Disconnect()

_ = md.SubscribePublicChannel(ctx, "volume", nil)
_ = md.SubscribePublicChannel(ctx, "open_interest", nil)
_ = md.SubscribePublicChannel(ctx, "funding_rate", nil)
```

## Trading commands

`PlaceOrder`, `CancelOrder`, `ModifyOrder` are exposed by the WebSocket
`GodarkClient`. Their request structs:

```go
sellPx := markPlus500   // decimal string; post-only limit at least 500 above the live mark
newPrice := markPlus501 // still at least 500 from the live mark
ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
    Symbol:      "BTC-USDC-PERP",
    Side:        godark.SideSell,               // SideBuy | SideSell
    OrderType:   godark.OrderTypeLimit,
    Quantity:    "0.001",
    Price:       sellPx,                        // post-only limit, at least mark+500
    TimeInForce: godark.TimeInForceGTC,
    Options:     godark.PlaceOrderOptions{PostOnly: true},
})
// ack.OrderID -- decimal string, the assigned sequencer order id
// ack.Sequence -- decimal string, the sequencer sequence number
// ack.Success  -- always true on a non-error return

// Wait at least a second, then cancel this order id.
cancelAck, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP")

// newPrice stays a post-only limit at least 500 from the live mark.
modAck, err := client.ModifyOrder(ctx, ack.OrderID, "BTC-USDC-PERP",
    &newPrice, /*newQuantity*/ nil, /*newTriggerPrice*/ nil)
```

**Rule:** prices and sizes on place/modify/mass-quote/batch-modify/TP-SL
(including `QuoteNotional`, min fill, and trigger) are decimal `string` /
`*string` only — not `float64` / int. Pass literals (`"0.001"`) or
format locally; the SDK has no float→string helper on the trading path.
`PlaceOrderRequest.Options` (`PlaceOrderOptions`) includes `ReduceOnly`,
`PostOnly`, `StpMode`, `PegOffsetBps`, `TriggerPrice`, `TakeProfitPrice`,
`StopLossPrice`, `SlippageBps`, and `QuoteNotional`. `SlippageBps` is accepted
only on `MARKET` and `STOP_MARKET`. Omit it (nil) to use the venue max walk
cap. `PEG` (`PegOffsetBps`) is incompatible with post-only. Set
`ClientOrderID` only on the WebSocket place; registration runs after that
place succeeds and counts only on HTTP 200 from `POST /orders/_register_coid`.

Encrypted headers, HPKE info, command bodies, and private pushes use the
32-byte Solana account returned by login. `ClientConfig.Account` /
`GODARK_ACCOUNT` is only a fallback for older local edges that omit it.

## Push streams (encrypted WS only)

`GodarkClient` exposes one buffered channel per stream plus an
`On...(cb)` callback hook for each. Channels keep the latest
`StreamBufferSize` entries (default 256) and drop the oldest on overflow
so a slow consumer can never deadlock the recv loop.

```go
for upd := range client.OrderUpdates() {
    fmt.Printf("status=%s filled=%s\n", upd.Status, upd.FilledQty)
}

client.OnPositionUpdate(func(p *godark.PositionUpdate) { ... })
client.OnPositionsSnapshot(func(s *godark.PositionsSnapshot) { ... })
client.OnSystemHealth(func(h *godark.SystemHealthUpdate) { ... })
client.OnBalanceUpdate(func(b *godark.BalanceUpdate) { ... })
client.OnMarginAlert(func(a *godark.MarginAlert) { ... })
client.OnFundingRateUpdate(func(f *godark.FundingRateUpdate) { ... })
client.OnSettlementUpdate(func(s *godark.SettlementUpdate) { ... })

client.OnError(func(err error)          { ... }) // non-fatal session errors
client.OnDisconnect(func()              { ... }) // WS closed (any reason)
client.OnReconnect(func()               { ... }) // automatic reconnect succeeded
```

Subscribe with `client.Subscribe(ctx, "orders", "positions")`. The `/ws/v1`
channel set is `orders`, `positions`, `volume`, `open_interest`, and
`funding_rate`. An unknown name, including `trades`, returns an error on the
subscribe waiter. The SDK replays remembered subscriptions after an automatic
reconnect. Read positions from `PositionsSnapshots` after subscribing to
`positions`, or from `GodarkRestClient.GetPositions`.

Default transport heartbeat settings: ping every `30s`, absolute stale timeout
`120s`, missed-heartbeat limit `2` consecutive intervals without inbound traffic.
Stale disconnects surface through `OnError` before the socket closes.

## Error handling

`PlaceOrder` / `CancelOrder` / `ModifyOrder` return a Go error on a
sequencer reject. The SDK canonicalises the numeric error code into a
symbolic name (e.g. `PRICE_DEVIATION_TOO_LARGE`) and surfaces both:

```go
var oe *godark.OrderError
if errors.As(err, &oe) {
    fmt.Printf("reject reason: %s (code %s)\n", oe.Error(), oe.ErrorCode)
}
```

Other typed errors you may see:

  - `*godark.AuthenticationError` -- login failed (bad API key, expired token)
  - `*godark.SessionError`        -- HPKE setup handshake or rekey failed
  - `*godark.ConnectionError`     -- WS or HTTP layer failure
  - `*godark.EncryptionError`     -- crypto path returned an error
  - `*godark.TimeoutError`        -- command exceeded its timeout

All five implement the `error` interface; match with `errors.As` /
`errors.Is`.

## Concurrency

All client methods are safe to call from multiple goroutines. Internally
the trading client serialises commands so only one is in flight at a
time (matching the python / rust SDKs); push-stream channels and
callbacks fire concurrently with command issuance.

## Symbol map

The SDK ships a frozen `symbols.json` embedded via `go:embed`. To trade
against a non-prod edge with a custom symbol set, pass `SymbolMap` on
`ClientConfig` / `RestClientConfig`.

```go
client, _ := godark.NewClient(godark.ClientConfig{
    APIKeyID: ..., APISecret: ..., Passphrase: ...,
    SymbolMap: map[string]int64{
        "BTC-USDC-PERP": 1,
        "ETH-USDC-PERP": 2,
    },
})
```

## Versioning

Each release ships a frozen `sdk/` tree under this bundle. To upgrade,
download a newer release zip, replace your local copy of `sdk/`, and
re-run `go build ./...` plus your regression tests.
