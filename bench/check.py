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
    # Streaming, not just output: the first visible byte has to reach stdout
    # before the mock has finished sending the stream that carries it. A client
    # that buffers the body, or flushes only on newlines, prints after `done`.
    first = run.get("first_text_unix_ns", 0)
    carrier = next((s for s in stats if s["scenario"] == "turn2"), stats[-1])
    if not first:
        fail("no visible output")
    margin_ms = (carrier["done_unix_ns"] - first) / 1e6
    if margin_ms <= 0:
        fail(f"not streaming: first text reached stdout {-margin_ms:.1f} ms after the mock finished sending")
    chunks = carrier.get("chunk_unix_ns") or []
    before = sum(1 for c in chunks if c > first)
    print(f"ok: two requests, streamed text matches, tool round trip completed, "
          f"first text {margin_ms:.1f} ms before the stream ended ({before} of {len(chunks)} chunks still to come)")

def error(run_json):
    run = json.loads(run_json)
    if run["exit_code"] == 0:
        fail("exit code 0 on a 401")
    if "invalid x-api-key" not in run["stderr"] and "authentication" not in run["stderr"].lower():
        fail(f"stderr does not carry the API error message: {run['stderr'].strip()!r}")
    print(f"ok: non-zero exit, stderr: {run['stderr'].strip()}")

COMMENT_PREFIXES = ("//", "#", "--", "/*", "*", "*/", "///", "//!")

def source_lines(path):
    """Non-blank lines that are not whole-line comments. A shebang counts as a
    comment. Inline comments after code are left alone, as are block
    comments' bodies when they do not start with '*'."""
    n = 0
    for line in open(path, encoding="utf-8"):
        t = line.strip()
        if not t or t.startswith(COMMENT_PREFIXES):
            continue
        n += 1
    return n

def summarise(name, kind, sizes, toolchain, build_s, commit, source=None):
    """Timings come from timestamps the mock recorded, not from subtraction,
    so the mock's own sleep overshoot never lands on the client:
      cold       spawn -> first request received
      turnaround first reply finished -> second request received
                 (parse the stream, run the tool, build and send the next request)
      tail       second reply finished -> exit
      client     the sum: every millisecond the mock was not streaming
    """
    rows = [json.loads(l) for l in sys.stdin if l.strip()]
    cold, turn, tail, client, rss = [], [], [], [], []
    for r in rows:
        run, st = r["run"], r["stats"]
        if run["exit_code"] != 0 or len(st) != 2:
            continue
        t1, t2 = sorted(st, key=lambda s: s["received_unix_ns"])
        c = (t1["received_unix_ns"] - run["spawn_unix_ns"]) / 1e6
        t = (t2["received_unix_ns"] - t1["done_unix_ns"]) / 1e6
        e = (run["exit_unix_ns"] - t2["done_unix_ns"]) / 1e6
        cold.append(c); turn.append(t); tail.append(e); client.append(c + t + e)
        rss.append(run["max_rss_kb"])
    if not cold:
        fail("no successful runs")
    size_parts = [int(x) for x in sizes.split()]
    artefact, runtime = size_parts[0], sum(size_parts[1:])
    med = lambda xs: round(statistics.median(xs), 2)
    p95 = lambda xs: round(statistics.quantiles(xs, n=20)[-1], 2)
    w = csv.writer(sys.stdout)
    loc = source_lines(source) if source else ""
    w.writerow(["client","kind","artefact_bytes","runtime_bytes",
                "cold_start_ms_median","cold_start_ms_p95","turnaround_ms_median","tail_ms_median",
                "client_ms_median","client_ms_p95","max_rss_kb_median","source_lines","runs","build_s","toolchain","commit"])
    w.writerow([name, kind, artefact, runtime, med(cold), p95(cold), med(turn), med(tail),
                med(client), p95(client), int(statistics.median(rss)), loc, len(cold), build_s, toolchain, commit])

def leaderboard(files):
    rows = []
    for f in files:
        if f.endswith("leaderboard.csv"): continue
        rows += list(csv.DictReader(open(f)))
    # Fastest first: the milliseconds a client adds on top of the API.
    rows.sort(key=lambda r: float(r["client_ms_median"]))
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
        parts = [p for p in posixpath.normpath(path).split("/") if p]  # ldd prints ../lib/... paths
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
    """One size per path. A directory counts every regular file under it,
    which is how an interpreter's standard library and packages are priced."""
    import tarfile
    with tarfile.open(tar) as tf:
        members = tf.getmembers()
        for p in paths:
            m = _resolve(tf, p)
            if m.isdir():
                prefix = m.name.rstrip("/") + "/"
                print(sum(x.size for x in members if x.isfile() and x.name.startswith(prefix)))
            else:
                print(m.size)

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
