#!/usr/bin/env bash
set -euo pipefail
# Requires Go and PostgreSQL binaries in PATH. Uses only a fresh temporary cluster.
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
out=${1:-"$here/../$(date +%Y-%m-%d-%H%M%S)"}
mkdir -p "$out/results" "$out/exact-read-probe"
out=$(cd "$out" && pwd)
work=$(mktemp -d /tmp/trie-replay.XXXXXX)
port=${PGPORT:-15439}
cleanup() {
  pg_ctl -D "$work/pgdata" -m fast stop >/dev/null 2>&1 || true
  rm -rf -- "$work"
}
trap cleanup EXIT
mkdir "$work/socket"
initdb -D "$work/pgdata" -A trust --no-locale -E UTF8 > "$out/initdb.log"
pg_ctl -D "$work/pgdata" -l "$work/postgres.log" \
  -o "-p $port -k $work/socket -h 127.0.0.1 -c shared_buffers=256MB" start
(cd "$here" && go test ./... && go build -o "$work/baseline" .)
python3 - "$repo" "$work" <<'PY'
import sys,pathlib,json
repo,work=map(pathlib.Path,sys.argv[1:])
source=repo/'go/server/cache/population.go'
raw=source.read_text();old='through := start + 1<<20'
assert raw.count(old)==1, 'production reader changed; review the probe first'
probe=work/'population_exact.go'
probe.write_text(raw.replace(old,'through := end // Isolated benchmark probe only.'))
(work/'overlay.json').write_text(json.dumps({'Replace':{str(source):str(probe)}}))
PY
(cd "$here" && go build -overlay "$work/overlay.json" -o "$work/exact" .)
for spec in '10000 1 256' '10000 10 256' '10000 100 256' '10000 100 4096' '100000 10 256' '1000 1000 256'; do
  read -r objects revisions textbytes <<< "$spec"
  name="o${objects}_r${revisions}_b${textbytes}"
  createdb -h 127.0.0.1 -p "$port" "$name"
  args=(-dsn "postgres://$(id -un)@127.0.0.1:$port/$name?sslmode=disable"
        -root "$work/$name" -objects "$objects" -revisions "$revisions"
        -text-bytes "$textbytes" -samples 300 -rounds 3)
  "$work/baseline" "${args[@]}" -output "$out/results/$name.json" 2> "$out/results/$name.log"
  "$work/exact" "${args[@]}" -reuse -reader exact-read-probe \
    -output "$out/exact-read-probe/$name.json" 2> "$out/exact-read-probe/$name.log"
  echo "Completed $name"
done
cp "$work/postgres.log" "$out/postgres.log"
echo "Results saved in $out"
