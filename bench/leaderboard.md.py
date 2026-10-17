"""Render results/leaderboard.csv as a Markdown table (used in the posts)."""
import csv, sys
rows = list(csv.DictReader(open(sys.argv[1] if len(sys.argv) > 1 else "results/leaderboard.csv")))
def kb(b): return f"{int(b)/1024:.0f} KB" if int(b) < 10_000_000 else f"{int(b)/1048576:.1f} MB"
print("| Client | Artefact | Runtime | Cold start (median) | Overhead (median) | Peak RSS | Toolchain |")
print("|---|---|---|---|---|---|---|")
for r in rows:
    print(f"| {r['client']} | {kb(r['artefact_bytes'])} | {kb(r['runtime_bytes']) if int(r['runtime_bytes']) else '0'} | "
          f"{r['cold_start_ms_median']} ms | {r['overhead_ms_median']} ms | {int(r['max_rss_kb_median'])//1024} MB | {r['toolchain']} |")
