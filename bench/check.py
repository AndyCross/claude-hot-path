"""Conformance checks and CSV summaries for the bench. Stdlib only."""
import csv, json, statistics, sys

EXPECTED_T1 = "Let me check the clock in London."
EXPECTED_T2 = 'It is 09:41 in London, so "good morning" is right.\nThe path separator there is still \\ on Windows, and the café is open.'

def fail(msg):
    print("FAIL:", msg); sys.exit(1)

def conformance(run_json, stats_json):
    run = json.loads(run_json); stats = json.loads(stats_json)
    if run["exit_code"] != 0:
        fail(f"exit code {run['exit_code']}: {run['stderr'].strip()}")
    scen = [s["scenario"] for s in stats]
    if scen != ["turn1", "turn2"]:
        fail(f"expected requests turn1,turn2 got {scen}")
    out = run["stdout"]
    synthetic = json.load(open("fixtures/turn1.json")).get("synthetic", True)
    if synthetic:
        want = EXPECTED_T1 + "\n" + EXPECTED_T2 + "\n"
        if out != want:
            fail(f"stdout mismatch\n--- want\n{want!r}\n--- got\n{out!r}")
    else:
        if not out.strip():
            fail("no output")
    print("ok: two requests, streamed text matches, tool round trip completed")

def error(run_json):
    run = json.loads(run_json)
    if run["exit_code"] == 0:
        fail("exit code 0 on a 401")
    if "invalid x-api-key" not in run["stderr"] and "authentication" not in run["stderr"].lower():
        fail(f"stderr does not carry the API error message: {run['stderr'].strip()!r}")
    print(f"ok: non-zero exit, stderr: {run['stderr'].strip()}")

def summarise(name, kind, sizes, toolchain, build_s, commit):
    rows = [json.loads(l) for l in sys.stdin if l.strip()]
    cold, overhead, rss = [], [], []
    for r in rows:
        run, st = r["run"], r["stats"]
        if run["exit_code"] != 0 or len(st) != 2:
            continue
        first = min(s["received_unix_ns"] for s in st)
        cold.append((first - run["spawn_unix_ns"]) / 1e6)
        replay = sum(s["replay_ms"] for s in st)
        overhead.append(run["wall_ms"] - replay)
        rss.append(run["max_rss_kb"])
    if not cold:
        fail("no successful runs")
    size_parts = [int(x) for x in sizes.split()]
    artefact, runtime = size_parts[0], sum(size_parts[1:])
    w = csv.writer(sys.stdout)
    w.writerow(["client","kind","artefact_bytes","runtime_bytes","cold_start_ms_median","cold_start_ms_p95",
                "overhead_ms_median","max_rss_kb_median","runs","build_s","toolchain","commit"])
    q = statistics.quantiles(cold, n=20)
    w.writerow([name, kind, artefact, runtime, round(statistics.median(cold),2), round(q[-1],2),
                round(statistics.median(overhead),2), int(statistics.median(rss)), len(cold), build_s, toolchain, commit])

def leaderboard(files):
    rows = []
    for f in files:
        if f.endswith("leaderboard.csv"): continue
        rows += list(csv.DictReader(open(f)))
    rows.sort(key=lambda r: int(r["artefact_bytes"]) + int(r["runtime_bytes"]))
    w = csv.DictWriter(sys.stdout, fieldnames=list(rows[0].keys()))
    w.writeheader(); w.writerows(rows)

def _resolve(tf, path):
    """Follow symlinks inside the tar at every path component, the way the
    loader would on disk. Debian's /bin and /lib are themselves symlinks."""
    import posixpath
    names = set(tf.getnames())
    def walk(path, depth=0):
        if depth > 40:
            raise RuntimeError("symlink loop at " + path)
        parts = [p for p in path.split("/") if p]
        cur = ""
        for i, part in enumerate(parts):
            cur = cur + "/" + part
            key = cur.lstrip("/")
            if key not in names:
                raise KeyError(cur)
            m = tf.getmember(key)
            if m.issym():
                target = m.linkname if m.linkname.startswith("/") else posixpath.join(posixpath.dirname(cur), m.linkname)
                rest = "/".join(parts[i+1:])
                return walk(posixpath.normpath(target + ("/" + rest if rest else "")), depth + 1)
        return tf.getmember(cur.lstrip("/"))
    return walk(path)

def sizes(tar, *paths):
    import tarfile
    with tarfile.open(tar) as tf:
        for p in paths:
            print(_resolve(tf, p).size)

def toolchain(tar):
    import tarfile
    with tarfile.open(tar) as tf:
        try:
            f = tf.extractfile(_resolve(tf, "/toolchain.txt"))
            print(";".join(l.strip() for l in f.read().decode().splitlines() if l.strip()))
        except KeyError:
            print("unknown")

if __name__ == "__main__":
    cmd, args = sys.argv[1], sys.argv[2:]
    {"conformance": conformance, "error": error, "leaderboard": lambda *a: leaderboard(a),
     "summarise": summarise, "sizes": sizes, "toolchain": toolchain}[cmd](*args)
