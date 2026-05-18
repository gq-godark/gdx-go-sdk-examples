#!/usr/bin/env bash
# MM bundle packager -- Linux x86_64 zip distribution, built strictly from
# the pinned upstream gdx-go-sdk commit recorded in sdk/UPSTREAM_REF.
#
# What this script does:
#   1. Reads the pinned upstream ref from sdk/UPSTREAM_REF.
#   2. Resolves the upstream source tree:
#        - If $UPSTREAM_SRC is set, use that directory (CI / explicit local
#          checkout).
#        - Else if a sibling ../gdx-go-sdk exists, use that.
#        - Else clone gq-godark/gdx-go-sdk@<pinned-ref> into a temp dir.
#   3. Verifies the resolved upstream is at exactly the pinned ref.
#   4. Parity check: vendored sdk/ must match $UPSTREAM_SRC for every Go
#      source file (excluding *_test.go, .git/, .github/, scripts/,
#      gdx-proto/ -- the same exclusions refresh_sdk.sh enforces). Drift
#      here means somebody hand-edited the vendored copy or forgot to
#      bump UPSTREAM_REF after a refresh -- fail loudly.
#   5. Builds release binaries via `go build` against the vendored sdk/.
#      The parity check above guarantees vendored sdk/ is bit-equal to
#      upstream for every file actually compiled, so the resulting
#      binaries are reproducible from the public upstream pin.
#   6. Stages the binaries + example sources + vendored sdk/ + top-level
#      go.mod / go.sum + recipient docs from bundle/, then zips them.
#      Recipients can either run the prebuilt binaries directly or
#      `go build ./examples/...` from the unzipped bundle.
#
# Output layout:
#   <DIST_NAME>/
#   |-- quickstart                 (prebuilt static-ish ELF, x86_64 Linux)
#   |-- full_trader                (prebuilt)
#   |-- go.mod                     (workspace; godark = ./sdk)
#   |-- go.sum
#   |-- .env.example
#   |-- README.md                  (from bundle/README.md)
#   |-- SDK_REFERENCE.md           (from bundle/SDK_REFERENCE.md)
#   |-- examples/
#   |   |-- quickstart/main.go
#   |   |-- full_trader/main.go
#   |   `-- internal/envloader/envloader.go
#   `-- sdk/
#       |-- UPSTREAM_REF           (the upstream commit the bundle was cut from)
#       |-- go.mod                 (godark module manifest)
#       |-- go.sum
#       |-- shared/symbols.json
#       |-- proto/...              (pre-generated bindings)
#       `-- *.go                   (godark package source)
#
# Usage:
#   bash scripts/package.sh
#   bash scripts/package.sh my-release-name
#   UPSTREAM_SRC=/path/to/gdx-go-sdk bash scripts/package.sh
set -euo pipefail

UPSTREAM_REPO="gq-godark/gdx-go-sdk"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_NAME="${1:-gdx-go-sdk-linux-x86_64}"

cd "$REPO_ROOT"

# ---- pre-flight ------------------------------------------------------------
if [[ ! -f "${REPO_ROOT}/sdk/UPSTREAM_REF" ]]; then
  echo "error: sdk/UPSTREAM_REF missing - run scripts/refresh_sdk.sh first" >&2
  exit 1
fi
PINNED_REF="$(tr -d '[:space:]' < "${REPO_ROOT}/sdk/UPSTREAM_REF")"
if [[ -z "$PINNED_REF" ]]; then
  echo "error: sdk/UPSTREAM_REF is empty" >&2
  exit 1
fi

for required in bundle/README.md bundle/SDK_REFERENCE.md .env.example \
                examples/quickstart/main.go examples/full_trader/main.go \
                examples/internal/envloader/envloader.go; do
  if [[ ! -f "${REPO_ROOT}/${required}" ]]; then
    echo "error: required source file missing: ${required}" >&2
    exit 1
  fi
done
if ! command -v zip >/dev/null 2>&1; then
  echo "error: 'zip' not found in PATH (apt-get install zip)" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "error: 'go' not found in PATH (install Go 1.22+)" >&2
  exit 1
fi

# ---- resolve upstream source tree -----------------------------------------
CLEANUP_UPSTREAM=false

if [[ -n "${UPSTREAM_SRC:-}" ]]; then
  echo "Using UPSTREAM_SRC=${UPSTREAM_SRC}"
elif [[ -d "${REPO_ROOT}/../gdx-go-sdk/.git" ]]; then
  UPSTREAM_SRC="$(cd "${REPO_ROOT}/../gdx-go-sdk" && pwd)"
  echo "Using sibling upstream checkout: $UPSTREAM_SRC"
else
  CLEANUP_UPSTREAM=true
  UPSTREAM_SRC="$(mktemp -d)/gdx-go-sdk"
  echo "Cloning ${UPSTREAM_REPO}@${PINNED_REF} -> $UPSTREAM_SRC ..."
  if command -v gh >/dev/null 2>&1; then
    gh repo clone "${UPSTREAM_REPO}" "$UPSTREAM_SRC" -- --quiet --filter=blob:none
  else
    git clone --quiet --filter=blob:none "https://github.com/${UPSTREAM_REPO}.git" "$UPSTREAM_SRC"
  fi
  git -C "$UPSTREAM_SRC" checkout --quiet "$PINNED_REF"
fi

cleanup() {
  if [[ "$CLEANUP_UPSTREAM" == true && -n "${UPSTREAM_SRC:-}" ]]; then
    rm -rf "$(dirname "$UPSTREAM_SRC")"
  fi
}
trap cleanup EXIT

# ---- verify upstream is at the pinned ref ---------------------------------
if [[ ! -d "$UPSTREAM_SRC/.git" ]]; then
  echo "error: '$UPSTREAM_SRC' is not a git checkout - cannot verify pin" >&2
  exit 1
fi
upstream_head_sha="$(git -C "$UPSTREAM_SRC" rev-parse HEAD)"
upstream_pin_sha="$(git -C "$UPSTREAM_SRC" rev-parse "$PINNED_REF" 2>/dev/null || true)"
if [[ -z "$upstream_pin_sha" ]]; then
  echo "error: pinned ref '$PINNED_REF' does not resolve in $UPSTREAM_SRC" >&2
  echo "       (try: git -C $UPSTREAM_SRC fetch --tags origin)" >&2
  exit 1
fi
if [[ "$upstream_head_sha" != "$upstream_pin_sha" ]]; then
  echo "error: upstream HEAD ($upstream_head_sha) does not match pinned ref" >&2
  echo "       sdk/UPSTREAM_REF=$PINNED_REF -> $upstream_pin_sha" >&2
  echo "       checkout the pinned ref before packaging:" >&2
  echo "         git -C $UPSTREAM_SRC checkout $PINNED_REF" >&2
  exit 1
fi
echo "Upstream verified at pin: $PINNED_REF ($upstream_head_sha)"

# ---- parity check: vendored sdk/ must match upstream  ---------------------
# Excludes mirror the rsync drops in refresh_sdk.sh.
PARITY_EXCLUDES=(
  --exclude='.git'
  --exclude='.github'
  --exclude='scripts'
  --exclude='gdx-proto'
  --exclude='.gitmodules'
  --exclude='.gitignore'
  --exclude='*_test.go'
  --exclude='UPSTREAM_REF'
)
if ! diff -r --brief "${PARITY_EXCLUDES[@]}" \
       "$UPSTREAM_SRC" "$REPO_ROOT/sdk" >/dev/null; then
  echo
  echo "error: vendored sdk/ has drifted from upstream $PINNED_REF:" >&2
  diff -r --brief "${PARITY_EXCLUDES[@]}" \
       "$UPSTREAM_SRC" "$REPO_ROOT/sdk" >&2 || true
  echo >&2
  echo "  fix: bash scripts/refresh_sdk.sh $UPSTREAM_SRC && git add sdk/ && git commit" >&2
  exit 1
fi
echo "Parity check passed: sdk/ matches $UPSTREAM_SRC"

# ---- build release binaries ----------------------------------------------
echo "Building release binaries (quickstart + full_trader)..."
mkdir -p "$REPO_ROOT/build"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$REPO_ROOT/build/quickstart"   ./examples/quickstart
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$REPO_ROOT/build/full_trader"  ./examples/full_trader

QUICKSTART_BIN="$REPO_ROOT/build/quickstart"
FULL_TRADER_BIN="$REPO_ROOT/build/full_trader"

for bin in "$QUICKSTART_BIN" "$FULL_TRADER_BIN"; do
  if [[ ! -x "$bin" ]]; then
    echo "error: expected binary missing or non-executable: $bin" >&2
    exit 1
  fi
done

# ---- stage ----------------------------------------------------------------
STAGING_DIR="$(mktemp -d)"
DEST="$STAGING_DIR/$DIST_NAME"
mkdir -p "$DEST"

echo "Staging distribution at $DEST ..."
mkdir -p "$DEST/examples/quickstart" "$DEST/examples/full_trader" \
         "$DEST/examples/internal/envloader" "$DEST/sdk"

# Prebuilt binaries.
cp "$QUICKSTART_BIN"                          "$DEST/quickstart"
cp "$FULL_TRADER_BIN"                         "$DEST/full_trader"

# Recipient docs come from bundle/, never from the repo-root copies.
cp "${REPO_ROOT}/.env.example"                "$DEST/.env.example"
cp "${REPO_ROOT}/bundle/README.md"            "$DEST/README.md"
cp "${REPO_ROOT}/bundle/SDK_REFERENCE.md"     "$DEST/SDK_REFERENCE.md"

# Top-level go.mod / go.sum so `cd <bundle> && go build ./examples/...`
# resolves the vendored godark via the `replace` directive.
cp "${REPO_ROOT}/go.mod"                      "$DEST/go.mod"
cp "${REPO_ROOT}/go.sum"                      "$DEST/go.sum"

# Example sources.
cp "${REPO_ROOT}/examples/quickstart/main.go"            "$DEST/examples/quickstart/main.go"
cp "${REPO_ROOT}/examples/full_trader/main.go"           "$DEST/examples/full_trader/main.go"
cp "${REPO_ROOT}/examples/internal/envloader/envloader.go" "$DEST/examples/internal/envloader/envloader.go"

# Vendored godark module -- mirror of $REPO_ROOT/sdk/ minus the parity-
# checked drops. Use cp -a to keep .go files and the proto bindings tree.
cp -a "${REPO_ROOT}/sdk/." "$DEST/sdk/"

# ---- zip ------------------------------------------------------------------
ARCHIVE="$REPO_ROOT/${DIST_NAME}.zip"
rm -f "$ARCHIVE"
( cd "$STAGING_DIR" && zip -qr "$ARCHIVE" "$DIST_NAME" )
rm -rf "$STAGING_DIR"

# ---- post-flight assertions ----------------------------------------------
echo
echo "Package created: $ARCHIVE"
LISTING="$(unzip -l "$ARCHIVE")"
echo "$LISTING"

# Recipient contract: no maintainer-only directories must leak.
if echo "$LISTING" | grep -E "${DIST_NAME}/(scripts|build|bundle|\.git)/" >/dev/null; then
  echo "error: bundle contains forbidden internal directory" >&2
  exit 1
fi
# Every required path must be present.
for required in \
  "${DIST_NAME}/quickstart" \
  "${DIST_NAME}/full_trader" \
  "${DIST_NAME}/README\\.md" \
  "${DIST_NAME}/SDK_REFERENCE\\.md" \
  "${DIST_NAME}/go\\.mod" \
  "${DIST_NAME}/\\.env\\.example" \
  "${DIST_NAME}/examples/quickstart/main\\.go" \
  "${DIST_NAME}/examples/full_trader/main\\.go" \
  "${DIST_NAME}/sdk/UPSTREAM_REF" \
  "${DIST_NAME}/sdk/go\\.mod" \
  "${DIST_NAME}/sdk/client\\.go"; do
  if ! echo "$LISTING" | grep -E "${required}" >/dev/null; then
    echo "error: bundle missing required entry: ${required}" >&2
    exit 1
  fi
done

echo
echo "bundle-shape assertion: PASSED"
echo "built from upstream:    ${UPSTREAM_REPO}@${PINNED_REF} (${upstream_head_sha})"
