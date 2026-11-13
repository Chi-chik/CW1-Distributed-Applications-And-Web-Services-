#!/usr/bin/env bash
# Package the CW1 submission: builds 100872005_6CC545_CW1.zip from the repository.
# Usage: scripts/package-cw1.sh   (from the repository root or anywhere)
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="$(cd .. && pwd)/100872005_6CC545_CW1.zip"
rm -f "$OUT"
zip -rq "$OUT" . -x "*.DS_Store"
echo "wrote $OUT"
