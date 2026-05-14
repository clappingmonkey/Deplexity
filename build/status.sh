#!/usr/bin/env bash
# Generates workspace status for Bazel stamping.
# Used with: build:release --stamp

set -euo pipefail

echo "STABLE_VERSION $(git describe --tags --always --dirty 2>/dev/null || echo dev)"
echo "BUILD_TIMESTAMP $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
