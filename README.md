# Claude Hot Path

The fastest way from your code to Claude. One file, no SDK.

The hot path to Claude is one HTTPS POST and a stream of six Server-Sent Event
types. Each client here writes it by hand in one source file, in one language,
and the harness measures what it adds on top of the API: cold start, the
turnaround between the two requests of a tool call, memory, and size. The
leaderboard sorts by the milliseconds added, fastest first.

Posts about each one are at <https://stepinto.dev/claude-hot-path/>.

## Results

Median of 100 cold starts for each client, under Docker, against the mock server. Client time is every millisecond the mock was not streaming: cold start, turnaround and exit added together.

| Client | Client time | Cold start | Turnaround | Peak memory | Ships | Lines | Post |
|---|---|---|---|---|---|---|---|
| [C](clients/c/) | **2.81 ms** | 1.3 ms | 0.67 ms | 6.1 MB | 2.1 MB | 271 |  |
| [Rust](clients/rust/) | **3.16 ms** | 1.14 ms | 0.73 ms | 3.0 MB | 3.3 MB | 184 | [read](https://stepinto.dev/hot-path/a-claude-client-in-one-rust-file-and-the-first-dependency/) |
| [C#, tuned](clients/csharp-tuned/) | **3.35 ms** | 1.67 ms | 0.73 ms | 4.3 MB | 10.4 MB | 344 | [read](https://stepinto.dev/hot-path/a-faster-c-claude-client-by-breaking-the-rules/) |
| [Zig](clients/zig/) | **3.67 ms** | 1.39 ms | 0.99 ms | 2.3 MB | 488 KB | 179 | [read](https://stepinto.dev/hot-path/a-claude-client-in-one-zig-file-under-half-a-megabyte/) |
| [Go](clients/go/) | **4.42 ms** | 1.73 ms | 1.1 ms | 6.4 MB | 5.8 MB | 156 | [read](https://stepinto.dev/hot-path/a-claude-client-in-one-go-file/) |
| [C#, ahead-of-time](clients/csharp/) | **7.09 ms** | 3.01 ms | 1.4 ms | 7.1 MB | 7.8 MB | 102 | [read](https://stepinto.dev/hot-path/a-claude-client-in-one-c-file-compiled-ahead-of-time/) |
| [bash and curl](clients/bash/) | **35.3 ms** | 6.98 ms | 19.09 ms | 11.8 MB | 23.7 MB | 75 | [read](https://stepinto.dev/hot-path/the-hot-path-to-claude-is-a-shell-script/) |
| [Python, standard library](clients/python/) | **140.37 ms** | 123.78 ms | 1.9 ms | 21.2 MB | 35.5 MB | 90 |  |
| [Python, official SDK](clients/python-sdk/) | **592.47 ms** | 464.62 ms | 8.65 ms | 84.1 MB | 71.9 MB | 55 |  |

"Ships" is the artefact plus any runtime it needs. The Python SDK row is the reference and is not an entry: it uses the official `anthropic` package the way its documentation says. The full board, with p95 figures, toolchain versions and the commit each row was measured at, is [`results/leaderboard.csv`](results/leaderboard.csv).

## Run it

```sh
./bench/bench.sh bash          # build the bash client, check it, measure 100 cold starts
./bench/bench.sh all           # every client, then results/leaderboard.csv
```

You need Docker and Python 3. You do not need an API key: the mock server in
`mock/` replays recorded exchanges with their original timing. The only step
that talks to Anthropic is recording, which proxies one real exchange through
the mock and saves it:

```sh
ANTHROPIC_API_KEY=... ./bench/bench.sh record bash
```

The key is read from your shell by the proxy. The client under test is given a
placeholder and never sees it, and nothing writes it to disk.

## The spec

Every client must do the following, and the bench checks each point rather
than trusting the code.

1. Read the key from `ANTHROPIC_API_KEY` and the base URL from
   `ANTHROPIC_BASE_URL` (default `https://api.anthropic.com`). Nothing else
   from the environment.
2. Read the prompt from stdin.
3. `POST /v1/messages` with headers `x-api-key`, `anthropic-version:
   2023-06-01` and `content-type: application/json`, and a body with `model`,
   `max_tokens`, `stream: true`, `messages` and `tools`.
4. Print each `text_delta` as it arrives. The bench checks that the first
   visible byte reached stdout before the mock finished sending the stream
   that carried it, so buffering the reply, or flushing only on newlines,
   fails.
5. Declare one tool, `local_time`, taking an IANA `timezone`. When the stream
   ends with `stop_reason: "tool_use"`, assemble the `input_json_delta`
   fragments, run the tool, send the `tool_result` in a second request with the
   assistant turn echoed back, and stream that reply too.
6. On a non-2xx response, print the body's `error.message` to stderr and exit
   non-zero.
7. One source file. HTTP and TLS from the platform where it has them. Where it
   does not, one named library, which counts towards size.

## The tuned C# row

`clients/csharp-tuned/` is the same spec written for speed rather than the
way the templates say: libc's socket calls and hand-written HTTP/1.1 in place
of `HttpClient`, `Utf8JsonReader` in place of `JsonNode`, bare file
descriptors in place of `Console`, `_exit` in place of runtime shutdown, and
a static musl build from `scratch`. It bends rule 7, so it sits on the board
as its own row next to the stock `csharp` client rather than replacing it.

## What is measured

| Column | Meaning |
|---|---|
| `artefact_bytes` | The stripped binary, or the source file for an interpreted language. |
| `runtime_bytes` | Zero for a static binary. The interpreter and any shared runtime otherwise, measured inside the image. |
| `cold_start_ms` | Process spawn to the mock receiving the first request. Median and p95. |
| `turnaround_ms` | First reply finished to second request received: parse the stream, run the tool, build and send the next request. |
| `tail_ms` | Second reply finished to exit. |
| `client_ms` | The sum of the three: every millisecond the mock was not streaming. Median and p95. |
| `max_rss_kb` | Peak resident memory, from the kernel's rusage for the child. |
| `source_lines` | Non-blank, non-comment lines in the one source file. Manifests (Cargo.toml, the csproj) are not counted. |
| `build_s` | Clean image build, reported and not ranked. |

The streaming check uses the same clocks: the runner notes when the first
non-whitespace byte reached stdout and the mock notes when it wrote each chunk,
and the first must come before the last. On the recorded fixture a streaming
client has about 11 ms in hand and a buffering one is 0.3 to 1 ms late, which
the check catches. All timings are differences between timestamps the mock recorded and the
runner's spawn and exit times, which share a clock because the containers share
a kernel. Nothing is subtracted, so the mock's own sleep overshoot never counts
against a client. The leaderboard sorts by `client_ms`, fastest first. Nothing is
measured over the network: the one real exchange is for conformance, and no
timing from it is published.

## Add a language

Make `clients/<name>/` with the source file, a `Dockerfile` whose `ENTRYPOINT`
runs it, and a `meta.env`:

```sh
NAME=zig
KIND=binary            # interpreted, or sdk for an official-SDK baseline row
ARTIFACT=/app/client   # path inside the image
RUNTIME=''             # interpreter binaries, for interpreted and sdk
RUNTIME_DIRS=''        # directories priced whole, e.g. a stdlib and site-packages
SOURCE=client.zig      # the one file, for the lines column
```

Write `/toolchain.txt` in the image during the build; the bench reads it.

Then `./bench/bench.sh <name>`. CI runs the mock bench on every pull request.

## Licence

[MIT](LICENSE).
