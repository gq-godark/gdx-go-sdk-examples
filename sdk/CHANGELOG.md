# Changelog

All notable changes to this project will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial Go SDK scaffold.
  - `internal/crypto`: X25519 ECDH + HKDF-SHA256 + AES-256-GCM + replay-aware
    NonceTracker, byte-for-byte compatible with the python / rust / js
    reference implementations.
  - `internal/identity`: UUID <-> 16-byte wire encoding helpers.
  - `internal/session`: CryptoSession state machine for the ECDH lifecycle.
  - `internal/transport`: docs-wire WebSocket transport (`/ws/v1`) with
    heartbeat, command serialization, subscription ack collation, and the
    docs-reply normalisation pass.
  - Public surface: `Side`, `OrderType`, `TimeInForce`, `OrderStatus`,
    `OrderUpdateType`, `PositionUpdateType`, `CancelReason`,
    `PositionsSnapshotSource`, `SettlementBatchStatus` enums; typed errors
    (`AuthenticationError`, `SessionError`, `OrderError`, `ConnectionError`,
    `EncryptionError`, `TimeoutError`); push types (`OrderAck`, `OrderUpdate`,
    `PositionUpdate`, `PositionsSnapshot`, `SystemHealthUpdate`,
    `BalanceUpdate`, `MarginAlert`, `FundingRateUpdate`, `SettlementUpdate`,
    `UnknownSequencerPush`).
  - `OrderErrorCodes`: canonical 34-entry order-error registry with
    `MakeOrderErrorFromCode` / `MakeOrderErrorFromJSON` helpers.
  - `DefaultSymbolMap` from go:embed of `shared/symbols.json`.
  - `proto.go`: hand-written builders for `PlaceOrder` / `CancelOrder` /
    `ModifyOrder` and parsers for every wire-mapped push variant; unknown
    push variants surface as `UnknownSequencerPush` (forward-compat).
  - `GodarkClient`: full encrypted trading client with channel-first push
    streams (`OrderUpdates() <-chan *OrderUpdate`, etc.) and callback
    alternatives (`OnOrderUpdate`, etc.). Single-command-in-flight
    semantics gated through the transport mutex.
  - GitHub Actions: `ci.yml` (build + vet + test + proto-drift across Go
    1.22 + 1.23), `auto-regen-protos.yml` (Layer 1 listener for
    gdx-proto-changed dispatches), `notify-examples.yml` (Layer 2
    dispatcher to gdx-go-sdk-examples).

### Added (REST + market-data + mock integration)

- `GodarkRestClient` (in `rest_client.go`) -- REST trading client on
  `/api/v1/{auth,session,orders}`. Reuses the same crypto session +
  AES-256-GCM construction as `GodarkClient`, so the edge stays stateless
  and order contents never leave the SDK in cleartext. Supports
  `PlaceOrder` / `CancelOrder` / `CancelOrderByClientID` / `ModifyOrder` /
  `GetOrder` / `GetOrderByClientID` / `AwaitTerminalStatus`, plus a
  best-effort `(client_order_id -> order_id)` registration post-decrypt.
- `internal/rest` -- thin `net/http` wrapper that unwraps the docs
  envelope `{code, data, message?, request_id?}` and surfaces non-zero
  codes as `*EnvelopeError`.
- `MarketDataClient` (in `market_data.go`) -- public gomarket WebSocket
  feed on `/ws/gomarket`. Subscribe to `orderbook` / `trades` per symbol
  with channel-first + callback delivery; URL normalisation accepts host
  origins ending in `/ws/v1`, `/ws`, or bare.
- `mock_integration_test.go` + `market_data_test.go` + `rest_client_test.go`
  -- end-to-end behavioral coverage via in-process `httptest.Server`
  harnesses that drive the real crypto handshake. Every push / ack now
  goes through AES-GCM with the session key derived via X25519 ECDH +
  HKDF-SHA256, byte-for-byte against the production rules.

### Deferred

- Auto-reconnect for both WS clients (caller-controlled today).
