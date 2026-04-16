#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")/.."
rm -rf bin/
echo "✓ quickwit provider cleaned up"
