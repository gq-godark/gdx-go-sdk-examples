#!/usr/bin/env bash
# Refresh the vendored SDK source under `sdk/` from a sibling `gdx-go-sdk`
# checkout AND record the upstream commit in `sdk/UPSTREAM_REF` so the
# release pipeline (scripts/package.sh + .github/workflows/release.yml) can
# verify the vendored copy hasn't drifted from upstream.
#
# Pre-generated protobuf bindings under `proto/gdx/**/*.pb.go` are included
# so the distribution does not require `protoc` or `protoc-gen-go`.
#
# Usage:
#   ./scripts/refresh_sdk.sh /path/to/gdx-go-sdk
#
# The source checkout MUST:
#   1. be a git checkout (`.git/` present) so the pin can be recorded
#   2. have a clean worktree (no uncommitted changes); otherwise the recorded
#      SHA wouldn't faithfully describe what was vendored
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /path/to/gdx-go-sdk" >&2
  exit 1
fi

SRC="$1"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEST="$REPO_ROOT/sdk"

if [[ ! -d "$SRC" ]]; then
  echo "error: source directory '$SRC' does not exist" >&2
  exit 1
fi
if [[ ! -d "$SRC/.git" ]]; then
  echo "error: '$SRC' is not a git checkout - pin cannot be recorded" >&2
  exit 1
fi
if [[ ! -f "$SRC/go.mod" ]]; then
  echo "error: '$SRC/go.mod' missing - this is not a gdx-go-sdk checkout" >&2
  exit 1
fi

# Refuse to refresh from a dirty upstream worktree. The pin would not be
# reproducible and the CI parity check would fail in confusing ways.
if ! git -C "$SRC" diff --quiet || ! git -C "$SRC" diff --cached --quiet; then
  echo "error: upstream '$SRC' has uncommitted changes; commit or stash first" >&2
  exit 1
fi

UPSTREAM_SHA="$(git -C "$SRC" rev-parse HEAD)"
UPSTREAM_TAG="$(git -C "$SRC" describe --tags --exact-match HEAD 2>/dev/null || true)"

echo "Refreshing $DEST from $SRC ..."
echo "  upstream HEAD: $UPSTREAM_SHA${UPSTREAM_TAG:+ (tag $UPSTREAM_TAG)}"

rm -rf "$DEST"
mkdir -p "$DEST"

# Copy SDK source. Deliberately drop:
#   - .git/, .github/                            (VCS + CI)
#   - scripts/                                   (SDK maintainer tooling)
#   - gdx-proto/                                 (submodule; pre-generated
#                                                 bindings under proto/ are
#                                                 committed, so consumers
#                                                 don't need protoc)
#   - *_test.go                                  (huge size win; recipient
#                                                 doesn't run upstream tests)
#   - mock_integration_test.go / rest/transport_test.go (same as above)
rsync -a \
  --exclude='.git/' \
  --exclude='.github/' \
  --exclude='scripts/' \
  --exclude='gdx-proto/' \
  --exclude='.gitmodules' \
  --exclude='.gitignore' \
  --exclude='*_test.go' \
  "$SRC/" "$DEST/"

# Pin the commit (prefer tag for human readability if HEAD is on one).
if [[ -n "$UPSTREAM_TAG" ]]; then
  echo "$UPSTREAM_TAG" > "$DEST/UPSTREAM_REF"
else
  echo "$UPSTREAM_SHA" > "$DEST/UPSTREAM_REF"
fi
echo "  wrote pin: $(cat "$DEST/UPSTREAM_REF")  -> sdk/UPSTREAM_REF"

echo "Vendored size: $(du -sh "$DEST" | cut -f1)"
echo "Done. Review with: cd '$REPO_ROOT' && git status sdk/"
