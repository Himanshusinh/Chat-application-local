#!/bin/bash
# Local build of everything into ./dist (GitHub Actions builds releases
# automatically on every push to main; see .github/workflows/release.yml).
# Needs: brew install go makensis, plus Xcode command line tools.
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${1:-$(tr -d '[:space:]' < VERSION).0}"
rm -rf dist && mkdir -p dist
go run ./tools/mkicon web
scripts/build-windows.sh "$VERSION" dist
[[ "$(uname)" == "Darwin" ]] && scripts/build-mac.sh "$VERSION" dist
echo; ls -lh dist
