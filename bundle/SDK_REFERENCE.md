# GoDark Go SDK Reference (MM Distribution)

This reference covers the `godark` public surface shipped under `sdk/` in
this bundle. It is curated for MM integrators and shows the smallest set
of calls you need to place, modify, cancel, subscribe, and decode order
and position pushes from the encrypted edge.

Everything required to build is included in this bundle: SDK source under
`sdk/`, pre-generated protobuf bindings under `sdk/proto/`, and a
top-level `go.mod` wired so a local `go build` resolves the SDK from the
bundled copy.

## Module + import

In your own `go.mod`, depend on the SDK via a path-based `replace`
directive pointing at the vendored copy this bundle ships (or a copy you
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

The SDK ships three concrete clients. All three share the same wire
crypto (X25519 ECDH + HKDF-SHA256 + AES-256-GCM under a per-session
key) and the same protobuf wire bindings.

### Encrypted WebSocket trading -- `GodarkClient`

```go
client, err := godark.NewClient(godark.ClientConfig{
    APIKeyID:  os.Getenv("GODARK_API_KEY_ID"),
    APISecret: os.Getenv("GODARK_API_SECRET"),
    // BaseURL defaults to wss://api.godark-dex.com; override via
    // GODARK_EDGE_URL/GDX_EDGE_URL env vars or this field.
    BaseURL: os.Getenv("GODARK_EDGE_URL"),
})
if err != nil { ... }
```

Lifecycle:

```go
ctx := context.Background()
if err := client.Connect(ctx); err != nil { ... }   // login + ECDH session.setup
defer client.Disconnect()

uid := client.UserUUID()
```

### Encrypted REST trading -- `GodarkRestClient`

Same crypto and protobuf builders as `GodarkClient`, but the wire is HTTP
(`POST /api/v1/orders`, etc.). Useful for stateless integrations that
don't need push streams.

```go
rest, err := godark.NewRestClient(godark.RestClientConfig{
    APIKeyID:  os.Getenv("GODARK_API_KEY_ID"),
    APISecret: os.Getenv("GODARK_API_SECRET"),
    // BaseURL defaults to https://api.godark-dex.com (derived from the
    // edge WS URL if GODARK_REST_URL/GDX_REST_URL is unset).
})
_ = rest.Connect(ctx)
defer rest.Disconnect(ctx)
```

### Public market-data feed -- `MarketDataClient`

```go
md := godark.NewMarketDataClient(godark.MarketDataConfig{
    BaseURL: os.Getenv("GODARK_EDGE_URL"), // same host; appends /ws/gomarket
})
if err := md.Connect(ctx); err != nil { ... }
defer md.Disconnect()

_ = md.SubscribeOrderbook(ctx, "BTC-USDC-PERP", func(m godark.MarketDataMessage) {
    // m.Channel == "orderbook", m.Raw["bids"] / m.Raw["asks"]
})
_ = md.SubscribeTrades(ctx, "BTC-USDC-PERP", func(m godark.MarketDataMessage) {
    // m.Channel == "trades", m.Raw["price"] / m.Raw["qty"]
})
```

Channel-first delivery is also supported:

```go
for msg := range md.OrderbookEvents() { ... }
for msg := range md.TradesEvents()    { ... }
```

## Trading commands

`PlaceOrder`, `CancelOrder`, `ModifyOrder` work the same on both
`GodarkClient` and `GodarkRestClient`. Their request structs:

```go
ack, err := client.PlaceOrder(ctx, godark.PlaceOrderRequest{
    Symbol:      "BTC-USDC-PERP",
    Side:        godark.SideBuy,                // SideBuy | SideSell
    OrderType:   godark.OrderTypeLimit,         // OrderTypeMarket | OrderTypeLimit
    Quantity:    0.1,
    Price:       67_500,                        // required for LIMIT
    TimeInForce: godark.TimeInForceGTC,
})
// ack.OrderID -- decimal string, the assigned sequencer order id
// ack.Sequence -- decimal string, the sequencer sequence number
// ack.Success  -- always true on a non-error return

cancelAck, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP")

newPrice := 68_000.0
modAck, err := client.ModifyOrder(ctx, ack.OrderID, "BTC-USDC-PERP",
    &newPrice, /*newQuantity*/ nil)
```

On the REST client, `PlaceOrder` takes `PlaceOrderRestRequest` (an
embed of `PlaceOrderRequest` plus an optional `ClientOrderID` field):

```go
ack, err := rest.PlaceOrder(ctx, godark.PlaceOrderRestRequest{
    PlaceOrderRequest: godark.PlaceOrderRequest{...},
    ClientOrderID: "my-uuid-here",
})
// Subsequent cancel by client-order-id:
_, _ = rest.CancelOrderByClientID(ctx, "my-uuid-here", "BTC-USDC-PERP")
```

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
```

Subscribe to channels with `client.Subscribe(ctx, "orders", "positions")`.
The same subscriptions are replayed after a `Disconnect` + `Connect`
cycle (the SDK does not auto-reconnect).

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
  - `*godark.SessionError`        -- ECDH session setup failed
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
    APIKeyID: ..., APISecret: ...,
    SymbolMap: map[string]int64{
        "BTC-USDC-PERP": 1,
        "ETH-USDC-PERP": 2,
    },
})
```

## Versioning

This bundle was cut from a specific upstream SDK commit; the SHA is
recorded in `sdk/UPSTREAM_REF`. Newer bundles bump that pin and ship
under a new release tag. Wire compatibility with the production
sequencer is the contract `gdx-proto@v1/devnet` (and later revisions)
maintains.

To upgrade your project to a newer SDK revision:

1. Download the new release zip.
2. Replace your local copy of the vendored `sdk/` (wherever your
   `replace` directive points) with the `sdk/` directory from the new
   zip.
3. `go build ./...` and re-run your regression tests.
