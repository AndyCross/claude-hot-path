// A Claude Messages API client in one C# file: HttpClient, System.Text.Json
// and a Server-Sent Events loop over a StreamReader. Streams the reply,
// completes one tool call, reports API errors. Published with Native AOT.
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;

var key = Environment.GetEnvironmentVariable("ANTHROPIC_API_KEY");
if (string.IsNullOrEmpty(key)) { Console.Error.WriteLine("ANTHROPIC_API_KEY is not set"); return 2; }
var baseUrl = Environment.GetEnvironmentVariable("ANTHROPIC_BASE_URL") ?? "https://api.anthropic.com";
var model = Environment.GetEnvironmentVariable("MODEL") ?? "claude-opus-5-5";
var prompt = await Console.In.ReadToEndAsync();

var tools = JsonNode.Parse("""
[{"name":"local_time","description":"The current local time in an IANA timezone.",
  "input_schema":{"type":"object","properties":{"timezone":{"type":"string","description":"IANA timezone, e.g. Europe/London"}},"required":["timezone"]}}]
""")!;
var messages = new JsonArray((JsonNode)new JsonObject { ["role"] = "user", ["content"] = prompt });

using var http = new HttpClient { BaseAddress = new Uri(baseUrl) };
http.DefaultRequestHeaders.Add("x-api-key", key);
http.DefaultRequestHeaders.Add("anthropic-version", "2023-06-01");

while (true)
{
    var body = new JsonObject
    {
        ["model"] = model, ["max_tokens"] = 1024, ["stream"] = true,
        ["tools"] = tools.DeepClone(), ["messages"] = messages.DeepClone(),
    };
    var (content, stop) = await StreamAsync(http, body);
    Console.WriteLine();
    if (stop != "tool_use") return 0;

    var results = new JsonArray();
    foreach (var block in content)
        if ((string?)block!["type"] == "tool_use")
            results.Add((JsonNode)new JsonObject
            {
                ["type"] = "tool_result", ["tool_use_id"] = (string?)block["id"], ["content"] = Run(block),
            });
    messages.Add((JsonNode)new JsonObject { ["role"] = "assistant", ["content"] = content.DeepClone() });
    messages.Add((JsonNode)new JsonObject { ["role"] = "user", ["content"] = results });
}

static string Run(JsonNode block)
{
    if ((string?)block["name"] != "local_time") return "unknown tool " + block["name"];
    var tz = (string?)block["input"]?["timezone"] ?? "UTC";
    try
    {
        var info = TimeZoneInfo.FindSystemTimeZoneById(tz);
        var now = TimeZoneInfo.ConvertTime(DateTimeOffset.UtcNow, info);
        return now.ToString("yyyy-MM-dd'T'HH:mm:sszz00");
    }
    catch (TimeZoneNotFoundException) { return "unknown timezone " + tz; }
}

// Posts the request, prints text deltas as they arrive, returns the assembled
// assistant content and the stop reason. Exits the process on an API error.
static async Task<(JsonArray content, string? stop)> StreamAsync(HttpClient http, JsonObject body)
{
    using var req = new HttpRequestMessage(HttpMethod.Post, "/v1/messages")
    {
        Content = new StringContent(body.ToJsonString(), Encoding.UTF8, "application/json"),
    };
    using var resp = await http.SendAsync(req, HttpCompletionOption.ResponseHeadersRead);
    if (!resp.IsSuccessStatusCode)
    {
        var raw = await resp.Content.ReadAsStringAsync();
        string message = raw.Trim();
        try { message = (string?)JsonNode.Parse(raw)?["error"]?["message"] ?? message; } catch (JsonException) { }
        Console.Error.WriteLine($"error {(int)resp.StatusCode}: {message}");
        Environment.Exit(1);
    }

    var content = new JsonArray();
    var partial = new List<StringBuilder>();
    string? stop = null;
    var stdout = Console.Out;
    using var reader = new StreamReader(await resp.Content.ReadAsStreamAsync());
    while (await reader.ReadLineAsync() is { } line)
    {
        if (!line.StartsWith("data: ", StringComparison.Ordinal)) continue;
        var ev = JsonNode.Parse(line.Substring(6))!;
        var index = (int?)ev["index"] ?? 0;
        switch ((string?)ev["type"])
        {
            case "content_block_start":
                content.Add(ev["content_block"]!.DeepClone());
                partial.Add(new StringBuilder());
                break;
            case "content_block_delta":
                var delta = ev["delta"]!;
                if ((string?)delta["type"] == "text_delta")
                {
                    var text = (string)delta["text"]!;
                    stdout.Write(text); stdout.Flush();
                    content[index]!["text"] = (string?)content[index]!["text"] + text;
                }
                else if ((string?)delta["type"] == "input_json_delta")
                    partial[index].Append((string?)delta["partial_json"]);
                break;
            case "content_block_stop":
                if ((string?)content[index]!["type"] == "tool_use" && partial[index].Length > 0)
                    content[index]!["input"] = JsonNode.Parse(partial[index].ToString());
                break;
            case "message_delta":
                stop = (string?)ev["delta"]?["stop_reason"];
                break;
        }
    }
    return (content, stop);
}
