#!/bin/bash
# Copyright (C) 2026 Alex Kunich
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

cd "$(dirname "$0")/.."
rm -rf bin/
echo "✓ quickwit provider cleaned up"
