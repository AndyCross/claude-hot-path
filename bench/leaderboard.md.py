"""Render results/leaderboard.csv as a Markdown table (used in the posts)."""
import csv, sys
rows = list(csv.DictReader(open(sys.argv[1] if len(sys.argv) > 1 else "results/leaderboard.csv")))
def kb(b): return f"{int(b)/1024:.0f} KB" if int(b) < 10_000_000 else f"{int(b)/1048576:.1f} MB"
print("| Client | On the path | Cold start | Turnaround | Peak RSS | Ship | Artefact | Runtime | Lines | Toolchain |")
print("|---|---|---|---|---|---|---|---|---|---|")
for r in rows:
    ship = int(r['artefact_bytes']) + int(r['runtime_bytes'])
    print(f"| {r['client']} | **{r['client_ms_median']} ms** | {r['cold_start_ms_median']} ms | {r['turnaround_ms_median']} ms | {int(r['max_rss_kb_median'])//1024} MB | "
          f"{kb(ship)} | {kb(r['artefact_bytes'])} | {kb(r['runtime_bytes']) if int(r['runtime_bytes']) else '0'} | {r.get('source_lines','')} | {r['toolchain'].split(';')[0]} |")
