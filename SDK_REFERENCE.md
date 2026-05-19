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
| `sdk/rest_client.go`                  | `GodarkRestClient`, `RestClientConfig`, `PlaceOrderRestRequest` |
| `sdk/market_data.go`                  | `MarketDataClient`, `MarketDataConfig`, `MarketDataMessage` |
| `sdk/types.go`                        | `OrderAck`, `OrderUpdate`, `PositionUpdate`, etc. |
| `sdk/enums.go`                        | `Side`, `OrderType`, `OrderStatus`, `TimeInForce`, etc. |
| `sdk/errors.go`                       | `AuthenticationError`, `SessionError`, `OrderError`, `ConnectionError`, `EncryptionError`, `TimeoutError` |
| `sdk/order_error_code.go`             | Canonical 34-entry numeric -> symbolic reject reason map |
| `sdk/proto.go`                        | Hand-written wrappers around generated proto (AAD builders, parsers, encoders) |
| `sdk/symbols.go` + `sdk/shared/symbols.json` | Embedded symbol-id table |
| `sdk/internal/crypto/crypto.go`       | X25519 ECDH + HKDF-SHA256 + AES-256-GCM primitives |
| `sdk/internal/session/session.go`     | `CryptoSession` lifecycle (Establish / EncryptOrder / DecryptPush / Reset) |
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
    the WS host; overridable via `GODARK_REST_URL` / `GDX_REST_URL`).
  - **Public market-data WS**: `wss://api.godark-dex.com/ws/gomarket`.
  - **Envelope**: docs-wire `{id, op, args}` out; `{id, op, code, data?,
    message?}` in. The transport normalises both legacy and docs envelopes
    transparently.
  - **Crypto**: every order body is AES-256-GCM-encrypted under a per-
    session key derived via X25519 ECDH + HKDF-SHA256. AAD is the
    serialised `OrderHeader` proto. Pushes carry a `ResponseHeader` AAD.
    Both the WS and REST clients reuse the same `CryptoSession` state
    machine (`sdk/internal/session/`).

## Examples mapping

| Example                              | API touchpoints                                                                                                                   |
| ------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| `examples/quickstart/main.go`        | `NewClient` -> `Connect` -> `PlaceOrder` -> `CancelOrder` -> `Disconnect`                                                          |
| `examples/full_trader_example/main.go` | `NewClient` (with `TransportConfig`) -> `Connect` -> `Subscribe` -> `PlaceOrder` / `ModifyOrder` / `CancelOrder` (mixed) -> drain push channels -> `Disconnect` |

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
