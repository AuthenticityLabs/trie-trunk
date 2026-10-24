#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
out=${1:?fresh output directory required}
mkdir -p "$out"
out=$(cd "$out" && pwd)
if test -e "$out/results.json"; then echo 'fresh results required' >&2;exit 1;fi
work=$(mktemp -d /tmp/trie-publication-bench.XXXXXX)
port=${PGPORT:-15439}
sync_method=${PG_SYNC_METHOD:-open_datasync}
case "$sync_method" in open_datasync|fsync_writethrough);;*)exit 1;;esac
cleanup(){ pg_ctl -D "$work/pgdata" -m fast stop >/dev/null 2>&1 || true; rm -rf -- "$work"; }
trap cleanup EXIT
python3 "$here/staged-overlay.py" "$repo" "$out"
(cd "$here" && go test -race -tags stagedobserver -overlay "$out/overlay.json" ./... -count=1)
mkdir "$work/socket"
initdb -D "$work/pgdata" -A trust --no-locale -E UTF8 > "$out/initdb.log"
pg_ctl -D "$work/pgdata" -l "$work/postgres.log" -o "-p $port -k $work/socket -h 127.0.0.1 -c shared_buffers=256MB -c wal_sync_method=$sync_method" start
createdb -h 127.0.0.1 -p "$port" publication
(cd "$here" && go build -tags stagedobserver -overlay "$out/overlay.json" -o "$work/benchmark" .)
shasum -a 256 "$here/main.go" "$here/stream.go" "$here/staged_observer.go" "$out/staged_store.go" "$repo/go/server/cache/http.go" > "$out/sources.sha256"
shasum -a 256 "$work/benchmark" > "$out/binary.sha256"
"$work/benchmark" -subscriptions -durable-before-publish=false -stream-variants pg-pil,trunk-durable,trunk-direct -dsn "postgres://$(id -un)@127.0.0.1:$port/publication?sslmode=disable" -root "$work/fixture" -output "$out/results.json" -samples "${STREAM_SAMPLES:-128}" -rounds "${STREAM_ROUNDS:-3}" -fanouts "${STREAM_FANOUTS:-1,10,100}" -stream-sizes "${STREAM_SIZES:-256,4096}" 2> "$out/run.log"
cp "$work/postgres.log" "$out/postgres.log"
echo 'Publication comparison completed'
