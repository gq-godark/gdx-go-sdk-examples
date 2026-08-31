# Changelog

All notable changes to this project will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Breaking: Noise XK replaces ECDH session setup.** Encrypted WebSocket
  trading now runs `Noise_XK_25519_AESGCM_SHA256` after login
  (`noise.handshake`). Pin the sequencer static public key via
  `ClientConfig.NoiseStaticPublicKeyHex` or `GDX_NOISE_STATIC_PUBLIC_KEY`.
  Encrypted REST order flow is unsupported (use `GodarkClient` over WS).
- **PlaceOrder confirmation:** `PlaceOrderRequest.Confirmation` replaces the
  temporary `WaitForOutcome *bool` escape hatch. Default is `book` (wait for
  OPEN / REJECTED / FILLED / PARTIALLY_FILLED / CANCELLED after the fast ack).
  Use `Confirmation: PlaceOrderConfirmationAck` to return on sequencer ack and
  consume `OrderUpdates` yourself. Book rejection returns `*OrderError` with
  symbolic code and update `msg` / ack `reject_text` when present.

### Added

- `Environment` (`testnet`, `devnet`, `localnet`) and
  `ClientConfig.Environment`. Testnet and Devnet each bake in their own
  sequencer Noise XK pin and edge URL so integrators no longer need
  `GDX_NOISE_STATIC_PUBLIC_KEY` for those deployments. Explicit config /
  env values still override the preset.
- `PlaceOrderConfirmation` (`ack` | `book`) and `ClientConfig.PlaceOrderTerminalTimeout`
  for the book-confirmation deadline (starts after the fast ack).
- `internal/noise` initiator and `internal/bound` AEAD framing helpers.

## [0.2.0] - 2026-05-22

### Changed

- **Breaking:** API key pair auth now requires a user-chosen passphrase. WebSocket
  login uses a 3-part token (`key_id:secret:passphrase`); REST sends `passphrase`
  on `POST /api/v1/auth/token`. Legacy opaque `APIKey` (e.g. localnet
  `test-key-1`) is unchanged.

### Added

- `GodarkRestClient.GetBalance(ctx, owner) (*Balance, error)`: REST
  snapshot of the on-chain USDT balance for the Solana base58 wallet
  `owner`, returning the wallet (SPL ATA), pending-shield-deposit, and
  sequencer-tracked shielded breakdowns as u64 raw units (decimal-encoded
  on the wire). Backed by the existing `GET /api/v1/shielded-pool/balances/{owner}`
  edge endpoint. Calling this also nudges the edge's BalanceWatchService
  to start streaming shielded-balance pushes for (user, owner).
- `GodarkRestClient.GetMe(ctx) (*MeProfile, error)`: fetches the
  authenticated user's profile via `GET /api/v1/auth/me`, including the
  `WalletAddress` needed for `GetBalance`. The result is cached on the
  client for the lifetime of the connected session.
- `GodarkRestClient.GetMyBalance(ctx) (*Balance, error)`: convenience
  pairing of `GetMe` + `GetBalance` -- resolves the user's owner pubkey
  via the cached `/auth/me` lookup, then fetches the shielded-pool
  balance snapshot in a single call.
- `Balance` and `MeProfile` types in `types.go`.
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
