# GoDark Go SDK

Encrypted Go client for the GoDark DEX. End-to-end-encrypted order flow over
WebSocket, with channel-first push streams that play well with Go's
concurrency primitives.

## Status

Initial scaffold. Public surface, wire crypto, ECDH session lifecycle, proto
bridge, WebSocket transport, the trading client (`GodarkClient`), the REST
client (`GodarkRestClient`), and the public market-data client
(`MarketDataClient`) are all implemented, unit-tested, and exercised by
end-to-end mock integration tests.

## WebSocket endpoints

The SDK speaks WebSocket on the canonical `/ws/v1` path. Pass the host origin
as `BaseURL`; the client appends `/ws/v1` automatically.

| Network  | Canonical URL                              |
|----------|--------------------------------------------|
| Testnet  | `wss://api.godark-dex.com/ws/v1`           |
| Localnet | `ws://localhost:4000/ws/v1` (typical dev)  |

The SDK default host is `wss://api.godark-dex.com`.

## Quickstart

The minimal happy path is:

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
    client, err := godark.NewClient(godark.ClientConfig{
        APIKeyID:  os.Getenv("GODARK_API_KEY_ID"),
        APISecret: os.Getenv("GODARK_API_SECRET"),
        // BaseURL defaults to wss://api.godark-dex.com (override via
        // GODARK_EDGE_URL / GDX_EDGE_URL env vars or this field).
    })
    if err != nil {
        log.Fatal(err)
    }

    ctx := context.Background()
    if err := client.Connect(ctx); err != nil {
        log.Fatal(err)
    }
    defer client.Disconnect()

    fmt.Printf("Connected as user %s\n", client.UserUUID())

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
    fmt.Printf("Place OK -- order_id=%s\n", ack.OrderID)

    if _, err := client.CancelOrder(ctx, ack.OrderID, "BTC-USDC-PERP"); err != nil {
        log.Fatal(err)
    }
    fmt.Println("Cancel OK")
}
```

## Repo layout

```text
gdx-go-sdk/
├── go.mod                              module github.com/gq-godark/gdx-go-sdk
├── client.go                           GodarkClient (encrypted WS trading)
├── rest_client.go                      GodarkRestClient (encrypted REST trading)
├── market_data.go                      MarketDataClient (public gomarket WS feed)
├── proto.go                            hand-written wrappers around generated proto
├── enums.go, errors.go, types.go       public surface
├── order_error_code.go                 canonical 34-entry error registry
├── symbols.go + shared/symbols.json    embedded symbol-id table
├── internal/
│   ├── crypto/                         X25519 ECDH + HKDF + AES-256-GCM
│   ├── session/                        CryptoSession state machine
│   ├── identity/                       UUID <-> 16-byte wire helpers
│   ├── rest/                           HTTP transport + envelope unwrap
│   └── transport/                      WS transport + docs-wire normalisation
├── proto/gdx/                          generated protobuf bindings (committed)
├── gdx-proto/                          git submodule pinned to v1/devnet
├── scripts/proto_gen.sh                regenerate Go bindings from gdx-proto
└── .github/workflows/                  CI + Layer-1/2 auto-publish chain
```

## Building from source

Requirements:

- Go 1.22 or newer (developed against 1.25; CI tests 1.22 + 1.23).
- `protoc` 3.21+ and `protoc-gen-go` v1.36+ for regenerating proto bindings.

```bash
git clone --recurse-submodules https://github.com/gq-godark/gdx-go-sdk
cd gdx-go-sdk

# If you already cloned without --recurse-submodules:
git submodule update --init --recursive

go build ./...
go test ./... -count=1
```

To refresh the protobuf bindings against `gdx-proto@v1/devnet`:

```bash
# Install once:
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

# Each regen:
bash scripts/proto_gen.sh
```

## Wire protocol

End-to-end encryption mirrors the python / rust / js SDKs byte-for-byte:

1. **TLS WS upgrade** to `/ws/v1`.
2. **Login** with the API key (or key-pair token), receiving an auth_result
   with the user's canonical UUID + session metadata.
3. **ECDH session setup**: the SDK generates an ephemeral X25519 keypair,
   sends the base64 public key in a `session.setup` op, and on response
   derives a 32-byte AES key via HKDF-SHA256 with HKDF info
   `b"gdx-e2e-session-key-v1"` and salt
   `min(local, remote) || max(local, remote)` (byte-lex order).
4. **Encrypted trading**: each place / cancel / modify command serializes a
   protobuf body and AES-256-GCM-encrypts it under the session key with a
   96-bit nonce of `session_id(u64 BE) || nonce_counter(u32 BE)`. The
   protobuf `OrderHeader` is the AEAD AAD.
5. **Encrypted pushes**: the sequencer wraps every push frame in the same
   AES-GCM construction; the SDK decrypts using `ResponseHeader` as AAD,
   then dispatches to per-stream channels and callbacks.

## Auto-publish chain

This repo participates in a tokenless cross-repo automation chain:

```text
gdx-proto (proto schema)
       |   push to main
       v
  notify-sdks.yml
       |
       v   repository_dispatch (gdx-proto-changed)
+-----------------------------+
| gdx-go-sdk (this repo)      |
|   auto-regen-protos.yml     |
|     regen Go bindings,      |
|     bump submodule pin,     |
|     open / refresh PR       |
+-----------------------------+
       |   merge PR -> push to main
       v
  notify-examples.yml
       |
       v   repository_dispatch (gdx-sdk-changed)
+-----------------------------+
| gdx-go-sdk-examples         |
|   auto-bump-sdk-pin.yml     |
|     update vendored SDK,    |
|     open / refresh PR       |
+-----------------------------+
       |   merge PR -> push to main
       v
  release.yml -> tagged zip on Releases
```

The dispatcher uses the `godark-ci` GitHub App (organization-installed) for
tokenless cross-repo writes.

## Concurrency contract

- One trading command in flight at a time. PlaceOrder / CancelOrder /
  ModifyOrder serialize through the transport mutex.
- Per-stream channels (OrderUpdates / PositionUpdates / etc.) are bounded
  (default 256). When full, the oldest frame is dropped to make room.
- Callbacks (`OnOrderUpdate`, etc.) run from the WS recv goroutine; keep them
  fast and non-blocking, or hand the value to your own queue.
- The SDK does NOT auto-reconnect. Subscribe to `OnDisconnect` and re-call
  `Connect` if you need that behaviour.

## License

Internal -- this SDK is currently distributed only within the GoDark
organization. A public license will be applied before any external release.
