#!/usr/bin/env bash
# ./bench/bench.sh <client> [iterations]     measure one client against the mock
# ./bench/bench.sh record <client>           one real exchange, fixtures replaced
# ./bench/bench.sh all                       every client, then the leaderboard
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
N=${2:-100}
ARCH=$(docker version --format '{{.Server.Arch}}')
NET=claude-hot-path
PROMPT="What time is it in London right now?"

need() { command -v "$1" >/dev/null || { echo "need $1" >&2; exit 1; }; }
need docker; need python3

build_runner() {
  mkdir -p bin
  (cd runner && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -ldflags="-s -w" -o "../bin/runner-linux-$ARCH" .)
}

start_mock() { # $1 = replay|record
  docker network inspect $NET >/dev/null 2>&1 || docker network create $NET >/dev/null
  docker rm -f sc-mock >/dev/null 2>&1 || true
  docker build -q -t sc-mock ./mock >/dev/null
  local extra=()
  if [[ $1 == record ]]; then
    extra=(-e UPSTREAM=https://api.anthropic.com -e ANTHROPIC_API_KEY -e ANTHROPIC_AUTH_TOKEN)
  fi
  docker run -d --name sc-mock --network $NET -v "$ROOT/fixtures:/fixtures" ${extra[@]+"${extra[@]}"} sc-mock >/dev/null
  for _ in $(seq 50); do
    docker exec sc-mock wget -qO- http://localhost:8080/healthz >/dev/null 2>&1 && return
    sleep 0.1
  done
  echo "mock did not start" >&2; exit 1
}

stats() { docker exec sc-mock wget -qO- http://localhost:8080/stats; }
reset_stats() { docker exec sc-mock wget -qO- --method=DELETE http://localhost:8080/stats >/dev/null 2>&1 || true; }

run_once() { # $1 image, $2 key; prints runner JSON
  printf '%s' "$PROMPT" | docker run --rm -i --network $NET \
    -v "$ROOT/bin/runner-linux-$ARCH:/runner:ro" \
    -e ANTHROPIC_API_KEY="$2" -e ANTHROPIC_BASE_URL=http://sc-mock:8080 \
    --entrypoint /runner "$1" "${ENTRY[@]}"
}

load_meta() { # $1 client dir
  # shellcheck disable=SC1090
  set -a; source "$1/meta.env"; set +a
  ENTRY=()
  while IFS= read -r e; do ENTRY+=("$e"); done < <(docker image inspect "sc-$NAME" --format '{{join .Config.Entrypoint "\n"}}')
}

measure() { # $1 client name
  local dir=clients/$1 img=sc-$1
  [[ -f $dir/meta.env ]] || { echo "no clients/$1/meta.env" >&2; exit 1; }
  echo "== $1: building"
  local t0 t1
  t0=$(date +%s); docker build -q -t "$img" "$dir" >/dev/null; t1=$(date +%s)
  load_meta "$dir"
  local sizes toolchain libs="" tar
  # Sizes come from the exported image, so the host's binaries never count and
  # a scratch image with no shell still measures. For an interpreted client the
  # shared libraries its runtime links are added too (ldd, deduplicated), so a
  # dynamically linked curl pays for libcurl and OpenSSL just as a static
  # binary pays for them inside itself.
  # Scratch images have no shell; ldd then finds nothing, which is correct.
  if docker run --rm --entrypoint sh "$img" -c true >/dev/null 2>&1; then
    libs=$(docker run --rm --entrypoint sh "$img" -c '
      for f in "$@"; do ldd "$f" 2>/dev/null | awk "/=> \\// {print \$3} /^\\t\\// {print \$1}"; done | sort -u' sh "$ARTIFACT" $RUNTIME | tr '\n' ' ')
  fi
  tar=$(mktemp); local cid
  cid=$(docker create "$img"); docker export "$cid" > "$tar"; docker rm "$cid" >/dev/null
  sizes=$(python3 bench/check.py sizes "$tar" "$ARTIFACT" $RUNTIME ${RUNTIME_DIRS:-} $libs | tr "\n" " ")
  toolchain=$(python3 bench/check.py toolchain "$tar")
  rm -f "$tar"
  echo "== $1: conformance"
  reset_stats
  local out
  out=$(run_once "$img" test)
  python3 bench/check.py conformance "$out" "$(stats)"
  echo "== $1: error handling"
  out=$(run_once "$img" invalid)
  python3 bench/check.py error "$out"
  echo "== $1: $N cold starts"
  mkdir -p results/raw
  : > "results/raw/$1.jsonl"
  for i in $(seq "$N"); do
    reset_stats
    run_once "$img" test > "results/raw/$1.run"
    printf '{"run":%s,"stats":%s}\n' "$(cat results/raw/$1.run)" "$(stats)" >> "results/raw/$1.jsonl"
  done
  python3 bench/check.py summarise "$1" "$KIND" "$sizes" "$toolchain" "$((t1-t0))" "$(git rev-parse --short HEAD 2>/dev/null || echo uncommitted)" "$dir/$SOURCE" \
    < "results/raw/$1.jsonl" > "results/$1.csv"
  cat "results/$1.csv"
}

record() { # $1 client
  local dir=clients/$1 img=sc-$1
  docker build -q -t "$img" "$dir" >/dev/null
  load_meta "$dir"
  start_mock record
  echo "== recording through the real API (key read from your shell, never written)"
  run_once "$img" placeholder | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r["stdout"]); sys.exit(r["exit_code"])'
  run_once "$img" sk-ant-invalid-record | python3 -c 'import json,sys; r=json.load(sys.stdin); print(r["stderr"].strip()); sys.exit(0 if r["exit_code"]!=0 else 1)'
  python3 - <<'PY'
import json,glob
for f in glob.glob("fixtures/*.json"):
    d=json.load(open(f)); d["synthetic"]=False; json.dump(d,open(f,"w"),indent=2)
PY
  echo "fixtures replaced; synthetic flags cleared"
}

case ${1:-} in
  record) build_runner; record "$2"; docker rm -f sc-mock >/dev/null ;;
  all) build_runner; start_mock replay
       for d in clients/*/; do measure "$(basename "$d")"; done
       python3 bench/check.py leaderboard results/*.csv > results/leaderboard.csv; cat results/leaderboard.csv
       docker rm -f sc-mock >/dev/null ;;
  "") sed -n 2,4p "$0"; exit 2 ;;
  *) build_runner; start_mock replay; measure "$1"
     python3 bench/check.py leaderboard results/*.csv > results/leaderboard.csv
     docker rm -f sc-mock >/dev/null ;;
esac
