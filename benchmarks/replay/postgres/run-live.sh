#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=${1:-"$here/../$(date +%Y-%m-%d)-live-replay"}
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d /tmp/trie-live-replay.XXXXXX)
port=${PGPORT:-15439}
cleanup() { pg_ctl -D "$work/pgdata" -m fast stop >/dev/null 2>&1 || true; rm -rf -- "$work"; }
trap cleanup EXIT
mkdir "$work/socket"
initdb -D "$work/pgdata" -A trust --no-locale -E UTF8 > "$out/initdb.log"
pg_ctl -D "$work/pgdata" -l "$work/postgres.log" -o "-p $port -k $work/socket -h 127.0.0.1 -c shared_buffers=256MB" start
(cd "$here" && go test ./... && go build -o "$work/bench" .)
if [ -n "${BASELINE_PATCH:-}" ]; then
 python3 - "$here/../../.." "$work" "$BASELINE_PATCH" <<'PY'
import pathlib,sys,re,json,subprocess
repo,work,patch=map(lambda p:pathlib.Path(p).resolve(),sys.argv[1:])
replacement={}
for name in re.findall(r'^--- a/(.*)$',patch.read_text(),re.M):
 source=repo/name;target=work/'before'/name
 target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(source.read_bytes())
 replacement[str(source)]=str(target)
subprocess.run(['patch','-s','-p1','-d',str(work/'before'),'-i',str(patch)],check=True)
empty=work/'empty.go';empty.write_text('package cache\n')
for name in ['hot.go','view_live.go']:replacement[str(repo/'go/server/cache'/name)]=str(empty)
(work/'before.json').write_text(json.dumps({'Replace':replacement}))
PY
 (cd "$here" && go build -overlay "$work/before.json" -o "$work/before-bench" .)
 BASELINE_BIN="$work/before-bench"
fi
shasum -a 256 "$work/bench" > "$out/binaries.sha256"
if [ -n "${BASELINE_BIN:-}" ]; then shasum -a 256 "$BASELINE_BIN" >> "$out/binaries.sha256"; fi
for spec in '10000 10 256' '10000 100 256' '1000 1000 256' '10000 100 4096'; do
 read -r objects revisions textbytes <<< "$spec"
 name="o${objects}_r${revisions}_b${textbytes}"
 createdb -h 127.0.0.1 -p "$port" "$name"
 args=(-dsn "postgres://$(id -un)@127.0.0.1:$port/$name?sslmode=disable" -root "$work/$name" -objects "$objects" -revisions "$revisions" -text-bytes "$textbytes" -samples 300 -rounds 3)
 # First verify the still-unfinalized tail, then promotion to resident storage,
 # then old records outside the resident window. Reuse identical fixture bytes.
 reuse=(-reuse=false)
 for mode in live resident cold; do
  case "$mode" in live) age=1m;; resident) age=1h;; cold) age=48h;; esac
  mkdir -p "$out/$mode"
  "$work/bench" "${args[@]}" "${reuse[@]}" -age "$age" -reader "$mode" -output "$out/$mode/$name.json" 2> "$out/$mode/$name.log"
  reuse=(-reuse)
  echo "Completed $mode $name"
 done
 if [ -n "${BASELINE_BIN:-}" ]; then
  mkdir -p "$out/before"
  "$BASELINE_BIN" "${args[@]}" -reuse -reader before -output "$out/before/$name.json" 2> "$out/before/$name.log"
  echo "Completed before $name"
 fi
done
cp "$work/postgres.log" "$out/postgres.log"
