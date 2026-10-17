# Fixtures

One file per scenario, replayed by the mock with the recorded inter-chunk
delays. `synthetic: true` means I wrote it by hand from the documented event
shapes; the record step (`./bench record <client>`) replaces it with a real
exchange and sets the flag to false. The leaderboard page says which it was.

| File | Chosen when |
|---|---|
| `turn1.json` | The request has no `tool_result`. Streams a little text, then a `local_time` tool call. |
| `turn2.json` | The request contains a `tool_result`. Streams the answer. |
| `error-401.json` | The `x-api-key` header is `invalid` or starts with `sk-ant-invalid`. |
| `error-400.json` | The body is not JSON. |
