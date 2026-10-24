#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
out=${1:-"$here/../$(date +%Y-%m-%d)-stream"}
mkdir -p "$out"
out=$(cd "$out" && pwd)
if test -e "$out/baseline.json" || test -e "$out/buffer4k.json"; then echo "fresh results directory required" >&2; exit 1; fi
work=$(mktemp -d /tmp/trie-stream-bench.XXXXXX)
port=${PGPORT:-15439}
sync_method=${PG_SYNC_METHOD:-open_datasync}
case "$sync_method" in open_datasync|fsync_writethrough) ;; *) echo "unsupported benchmark sync method" >&2; exit 1;; esac
cleanup(){ pg_ctl -D "$work/pgdata" -m fast stop >/dev/null 2>&1 || true; rm -rf -- "$work"; }
trap cleanup EXIT
mkdir "$work/socket"
initdb -D "$work/pgdata" -A trust --no-locale -E UTF8 > "$out/initdb.log"
pg_ctl -D "$work/pgdata" -l "$work/postgres.log" -o "-p $port -k $work/socket -h 127.0.0.1 -c shared_buffers=256MB -c wal_sync_method=$sync_method" start
(cd "$here" && go test ./...)
python3 - "$repo" "$work" <<'PY'
import pathlib,json,sys
repo,work=map(pathlib.Path,sys.argv[1:]);source=repo/'go/server/cache/http.go'
raw=source.read_text()
choices=['bufio.NewReaderSize(r.Body, 128<<10)','bufio.NewReaderSize(r.Body, 4<<10)']
found=[x for x in choices if raw.count(x)==1]
assert len(found)==1,'review ingress buffer probe after production changes'
for mode,choice in zip(['baseline','buffer4k'],choices):
 target=work/(mode+'_http.go');target.write_text(raw.replace(found[0],choice))
 (work/(mode+'.json')).write_text(json.dumps({'Replace':{str(source):str(target)}}))
PY
(cd "$here" && go build -overlay "$work/baseline.json" -o "$work/baseline" . && go build -overlay "$work/buffer4k.json" -o "$work/buffer4k" .)
shasum -a 256 "$here/main.go" "$here/stream.go" "$repo/go/server/cache/http.go" > "$out/sources.sha256"
shasum -a 256 "$work/baseline" "$work/buffer4k" > "$out/binaries.sha256"
for mode in baseline buffer4k; do
 createdb -h 127.0.0.1 -p "$port" "stream_$mode"
 "$work/$mode" -subscriptions -durable-before-publish=true -stream-variants pg-json,pg-pil,trunk-lookup,trunk-direct,trunk-batch8 -dsn "postgres://$(id -un)@127.0.0.1:$port/stream_$mode?sslmode=disable" -root "$work/$mode-fixture" -output "$out/$mode.json" -samples "${STREAM_SAMPLES:-128}" -rounds "${STREAM_ROUNDS:-3}" -fanouts "${STREAM_FANOUTS:-1,10,100}" -stream-sizes "${STREAM_SIZES:-256,4096}" 2> "$out/$mode.log"
 echo "Completed $mode"
done
cp "$work/postgres.log" "$out/postgres.log"
