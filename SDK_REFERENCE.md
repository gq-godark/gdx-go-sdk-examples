# SDK Reference (maintainer view)

This document is the maintainer-grade index of the `gdx-go-sdk` public
surface vendored under `sdk/`. It links every concept the examples
exercise back to the implementing Go file so a reviewer can verify the
distribution faithfully ships the upstream contract.

For the recipient-facing API tutorial, see `bundle/SDK_REFERENCE.md`
(which is what ends up in the released zip).

## Source layout (vendored under `sdk/`)

| File                                  | Surface                                       |
| ------------------------------------- | --------------------------------------------- |
| `sdk/client.go`                       | `GodarkClient`, `ClientConfig`, `TransportConfig` |
| `sdk/rest_client.go`                  | `GodarkRestClient`, `RestClientConfig` (legacy; **encrypted REST trading is unsupported** — the examples trade over the WebSocket `GodarkClient`) |
| `sdk/market_data.go`                  | `MarketDataClient`, `MarketDataConfig`, `MarketDataMessage` |
| `sdk/types.go`                        | `OrderAck`, `OrderUpdate`, `PositionUpdate`, etc. |
| `sdk/enums.go`                        | `Side`, `OrderType`, `OrderStatus`, `TimeInForce`, etc. |
| `sdk/errors.go`                       | `AuthenticationError`, `SessionError`, `OrderError`, `ConnectionError`, `EncryptionError`, `TimeoutError` |
| `sdk/order_error_code.go`             | Canonical 34-entry numeric -> symbolic reject reason map |
| `sdk/proto.go`                        | Hand-written wrappers around generated proto (AAD builders, parsers, encoders) |
| `sdk/symbols.go` + `sdk/shared/symbols.json` | Embedded symbol-id table |
| `sdk/internal/noise/noise.go`         | Noise_XK_25519_AESGCM_SHA256 initiator |
| `sdk/internal/bound/bound.go`         | SHA-256-bound AEAD framing helpers |
| `sdk/internal/crypto/crypto.go`       | X25519 + AES-GCM primitives used by Noise |
| `sdk/internal/session/session.go`     | Post-handshake `CryptoSession` (encrypt/decrypt) |
| `sdk/internal/identity/identity.go`   | UUID <-> 16-byte wire helpers |
| `sdk/internal/transport/transport.go` | WS transport + docs-wire envelope normalisation |
| `sdk/internal/rest/transport.go`      | HTTP wrapper + `{code, data, message?}` envelope unwrap |
| `sdk/proto/gdx/{common,edge,sequencer}/v1/*.pb.go` | Generated proto bindings (committed) |

`internal/` is not importable from outside the SDK; the public surface
above is enumerated in `bundle/SDK_REFERENCE.md`.

## Wire contract

  - **Trading WS endpoint**: `wss://api.godark-dex.com/ws/v1` (overridable
    via `GODARK_EDGE_URL` / `GDX_EDGE_URL`).
  - **REST root**: `https://api.godark-dex.com/api/v1` (auto-derived from
    the WS host). Not used by the examples — encrypted REST trading is
    unsupported; all order flow goes over the WebSocket client.
  - **Public market-data WS**: `wss://api.godark-dex.com/ws/gomarket`.
  - **Envelope**: docs-wire `{id, op, args}` out; `{id, op, code, data?,
    message?}` in. The transport normalises both legacy and docs envelopes
    transparently.
  - **Crypto**: after login the client runs `Noise_XK_25519_AESGCM_SHA256`
    (`noise.handshake`). Pin the sequencer static key via
    `ClientConfig.NoiseStaticPublicKeyHex` or `GDX_NOISE_STATIC_PUBLIC_KEY`.
    Order bodies use bound AES-GCM (`SHA256(OrderHeader) || plaintext`).
    The legacy ECDH `session.setup` handshake is retired; all encrypted
    order flow now uses the Noise XK WebSocket client. Encrypted REST
    trading is unsupported.

## Examples mapping

| Example                              | API touchpoints                                                                                                                   |
| ------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| `examples/quickstart/main.go`        | `NewClient` -> `Connect` -> `Subscribe("orders")` -> `PlaceOrder` (book) -> `CancelOrder` -> `Disconnect`                          |
| `examples/full_trader_example/main.go` | `NewClient` (with `TransportConfig`) -> `Connect` -> `Subscribe` -> `PlaceOrder` / `ModifyOrder` / `CancelOrder` / `MassQuote` / `BatchCancel` (mixed) -> drain push channels -> `Disconnect` |

Both examples share `examples/internal/envloader/envloader.go` for `.env`
loading and `OrderError` pretty-printing.

## Reproducibility

The release pipeline (`scripts/package.sh` + `.github/workflows/release.yml`)
guarantees:

  - Vendored `sdk/` is **bit-equal** to `gq-godark/gdx-go-sdk@<UPSTREAM_REF>`
    for every file the bundle ships, modulo the rsync excludes documented
    in `scripts/refresh_sdk.sh` (tests, repo docs, in-repo examples,
    `.env*`, VCS/CI metadata).
  - The bundle is source-only and platform-agnostic; recipients build the
    example binaries on their own platform with `go build
    ./examples/quickstart` (works on any OS / arch Go supports).
  - The zip's structure is asserted post-build: every required file must
    be present, no maintainer-only directories may leak, and the bundle
    must not contain any compiled artifacts or unexpected file types.

## Layer-2 automation

When the upstream SDK ships a new commit on `main`, its
`notify-examples.yml` (in `gdx-go-sdk`) dispatches a `gdx-sdk-changed`
event here. Our `auto-bump-sdk-pin.yml` listener:

  1. clones `gdx-go-sdk` at the dispatched SHA,
  2. runs `scripts/refresh_sdk.sh`,
  3. opens (or force-pushes) a rolling PR `auto/bump-sdk-pin` if anything
     changed under `sdk/`.

The PR is annotated with a checklist: review the descriptor diff under
`sdk/proto/`, confirm CI is green, and merge to cut a new Release zip.
