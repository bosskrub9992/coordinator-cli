#!/usr/bin/env bash
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
REV=${1:-HEAD}
DENY=${COORD_EXPORT_DENY:-$ROOT/.export-deny}
SHORT=$(git -C "$ROOT" rev-parse --short "$REV")
OUT=${2:-$ROOT/dist/coordinator-cli-$SHORT.zip}

if [ ! -s "$DENY" ]; then
  echo "export: the deny-pattern file $DENY is missing or empty (one extended regex per line)" >&2
  exit 1
fi

TMP=$(mktemp -d "${TMPDIR:-/tmp}/coord-export.XXXXXX")
trap 'rm -rf "$TMP"' EXIT
git -C "$ROOT" archive --prefix=coordinator-cli/ "$REV" | tar -x -C "$TMP"

found=0
if grep -rnIiE -f "$DENY" "$TMP"; then
  found=1
fi
if (cd "$TMP" && find . | grep -iE -f "$DENY"); then
  found=1
fi
if [ "$found" -ne 0 ]; then
  echo "export: refused; scrub the matches above and commit, then retry" >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT")"
git -C "$ROOT" archive --format=zip --prefix=coordinator-cli/ -o "$OUT" "$REV"
echo "$OUT"
