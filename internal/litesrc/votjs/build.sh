#!/usr/bin/env bash
# Bundles vot.src.mjs + @vot.js/node (and its deps) into a single self-contained vot.bundle.cjs,
# which the Go server embeds via go:embed and runs as `node vot.cjs`. No npm install on the server.
# CommonJS output (not ESM) so undici's dynamic require() of node: builtins keeps working.
#
# Requires Node 18+ and npm network access. Run from this directory:  ./build.sh
set -euo pipefail
cd "$(dirname "$0")"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cp vot.src.mjs "$tmp/entry.mjs"
(
  cd "$tmp"
  npm init -y >/dev/null 2>&1
  npm install @vot.js/node esbuild >/dev/null 2>&1
  npx esbuild entry.mjs --bundle --platform=node --format=cjs --target=node18 --outfile=vot.bundle.cjs
)
cp "$tmp/vot.bundle.cjs" ./vot.bundle.cjs
echo "wrote vot.bundle.cjs ($(wc -c < ./vot.bundle.cjs) bytes)"
