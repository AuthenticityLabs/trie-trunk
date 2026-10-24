#!/bin/sh
set -eu
cd "$(dirname "$0")"
mkdir -p build
rustc --edition=2024 --crate-type staticlib -C opt-level=3 buffers.rs -o build/libboundary_probe.a
CGO_ENABLED=1 go build -o build/boundary-probe .
