//! A Claude Messages API client in one Zig file. std.http.Client for the
//! transport (TLS included), std.json for parsing and building, and the
//! 0.17 Io interface for everything that touches the outside world.
//! Streams the reply, completes one tool call, reports API errors.
const std = @import("std");
const Io = std.Io;
const json = std.json;

const version = "2023-06-01";
const tools_json =
    \\[{"name":"local_time","description":"The current local time in an IANA timezone.","input_schema":{"type":"object","properties":{"timezone":{"type":"string","description":"IANA timezone, e.g. Europe/London"}},"required":["timezone"]}}]
;

const Ctx = struct {
    gpa: std.mem.Allocator,
    io: Io,
    env: *std.process.Environ.Map,
    client: *std.http.Client,
    base: []const u8,
    key: []const u8,
    model: []const u8,
    out: *Io.Writer,
};

pub fn main(init: std.process.Init) !void {
    const gpa = init.gpa;
    const io = init.io;
    const env = init.environ_map;

    const key = env.get("ANTHROPIC_API_KEY") orelse "";
    if (key.len == 0) {
        try fail(io, "ANTHROPIC_API_KEY is not set\n", 2);
    }
    const base = env.get("ANTHROPIC_BASE_URL") orelse "https://api.anthropic.com";
    const model = env.get("MODEL") orelse "claude-opus-5-5";

    var stdin_buf: [4096]u8 = undefined;
    var stdin = Io.File.stdin().reader(io, &stdin_buf);
    const prompt = try stdin.interface.allocRemaining(gpa, .unlimited);

    var stdout_buf: [4096]u8 = undefined;
    var stdout = Io.File.stdout().writer(io, &stdout_buf);

    var client: std.http.Client = .{ .allocator = gpa, .io = io };
    defer client.deinit();

    const ctx: Ctx = .{
        .gpa = gpa, .io = io, .env = env, .client = &client,
        .base = base, .key = key, .model = model, .out = &stdout.interface,
    };

    // The conversation so far, as JSON values we append to between turns.
    const tools = try json.parseFromSliceLeaky(json.Value, gpa, tools_json, .{});
    var messages: json.Array = .init(gpa);
    try messages.append(try object(gpa, &.{ .{ "role", .{ .string = "user" } }, .{ "content", .{ .string = prompt } } }));

    while (true) {
        var body: json.ObjectMap = .empty;
        try body.put(gpa, "model", .{ .string = model });
        try body.put(gpa, "max_tokens", .{ .integer = 1024 });
        try body.put(gpa, "stream", .{ .bool = true });
        try body.put(gpa, "tools", tools);
        try body.put(gpa, "messages", .{ .array = messages });

        const turn = try stream(ctx, .{ .object = body });
        try ctx.out.writeAll("\n");
        try ctx.out.flush();
        if (!std.mem.eql(u8, turn.stop, "tool_use")) return;

        var results: json.Array = .init(gpa);
        for (turn.content.items) |block| {
            if (!std.mem.eql(u8, str(block, "type"), "tool_use")) continue;
            try results.append(try object(gpa, &.{
                .{ "type", .{ .string = "tool_result" } },
                .{ "tool_use_id", .{ .string = str(block, "id") } },
                .{ "content", .{ .string = try run(ctx, block) } },
            }));
        }
        try messages.append(try object(gpa, &.{ .{ "role", .{ .string = "assistant" } }, .{ "content", .{ .array = turn.content } } }));
        try messages.append(try object(gpa, &.{ .{ "role", .{ .string = "user" } }, .{ "content", .{ .array = results } } }));
    }
}

const Turn = struct { content: json.Array, stop: []const u8 };

/// Posts the request, prints text deltas as they arrive, and returns the
/// assembled assistant content and the stop reason.
fn stream(ctx: Ctx, body: json.Value) !Turn {
    const gpa = ctx.gpa;
    const payload = try json.Stringify.valueAlloc(gpa, body, .{});

    const url = try std.mem.concat(gpa, u8, &.{ ctx.base, "/v1/messages" });
    const uri = try std.Uri.parse(url);
    const headers = [_]std.http.Header{
        .{ .name = "x-api-key", .value = ctx.key },
        .{ .name = "anthropic-version", .value = version },
        .{ .name = "content-type", .value = "application/json" },
    };
    var req = try ctx.client.request(.POST, uri, .{ .extra_headers = &headers, .keep_alive = false });
    defer req.deinit();
    try req.sendBodyComplete(payload);

    var redirect_buf: [1024]u8 = undefined;
    var response = try req.receiveHead(&redirect_buf);
    // One SSE line has to fit in this buffer. Tool inputs can be long.
    const transfer_buf = try gpa.alloc(u8, 1 << 20);
    const reader = response.reader(transfer_buf);

    if (response.head.status.class() != .success) {
        const raw = try reader.allocRemaining(gpa, .unlimited);
        var message = std.mem.trim(u8, raw, " \r\n\t");
        if (json.parseFromSliceLeaky(json.Value, gpa, raw, .{})) |v| {
            if (v == .object) if (v.object.get("error")) |e| if (e == .object) if (e.object.get("message")) |m| if (m == .string) {
                message = m.string;
            };
        } else |_| {}
        var buf: [256]u8 = undefined;
        var w = Io.File.stderr().writer(ctx.io, &buf);
        try w.interface.print("error {d}: {s}\n", .{ @intFromEnum(response.head.status), message });
        try w.interface.flush();
        std.process.exit(1);
    }

    var content: json.Array = .init(gpa);
    var partial: std.ArrayList(std.ArrayList(u8)) = .empty;
    var stop: []const u8 = "";

    while (try reader.takeDelimiter('\n')) |line| {
        if (!std.mem.startsWith(u8, line, "data: ")) continue; // event:, ping, blank
        const ev = try json.parseFromSliceLeaky(json.Value, gpa, try gpa.dupe(u8, line[6..]), .{});
        const kind = str(ev, "type");
        const index: usize = if (ev.object.get("index")) |i| @intCast(i.integer) else 0;
        if (std.mem.eql(u8, kind, "content_block_start")) {
            try content.append(ev.object.get("content_block").?);
            try partial.append(gpa, .empty);
        } else if (std.mem.eql(u8, kind, "content_block_delta")) {
            const delta = ev.object.get("delta").?;
            const dkind = str(delta, "type");
            if (std.mem.eql(u8, dkind, "text_delta")) {
                const text = str(delta, "text");
                try ctx.out.writeAll(text);
                try ctx.out.flush();
                const existing = str(content.items[index], "text");
                try content.items[index].object.put(gpa, "text", .{ .string = try std.mem.concat(gpa, u8, &.{ existing, text }) });
            } else if (std.mem.eql(u8, dkind, "input_json_delta")) {
                try partial.items[index].appendSlice(gpa, str(delta, "partial_json"));
            }
        } else if (std.mem.eql(u8, kind, "content_block_stop")) {
            if (std.mem.eql(u8, str(content.items[index], "type"), "tool_use") and partial.items[index].items.len > 0) {
                const input = try json.parseFromSliceLeaky(json.Value, gpa, partial.items[index].items, .{});
                try content.items[index].object.put(gpa, "input", input);
            }
        } else if (std.mem.eql(u8, kind, "message_delta")) {
            stop = str(ev.object.get("delta").?, "stop_reason");
        }
    }
    return .{ .content = content, .stop = stop };
}

/// std has no timezone database; the platform does. Run `date` with TZ set.
fn run(ctx: Ctx, block: json.Value) ![]const u8 {
    if (!std.mem.eql(u8, str(block, "name"), "local_time")) return "unknown tool";
    const tz = if (block.object.get("input")) |in| str(in, "timezone") else "UTC";
    var env = try ctx.env.clone(ctx.gpa);
    defer env.deinit();
    try env.put("TZ", tz);
    const result = std.process.run(ctx.gpa, ctx.io, .{
        .argv = &.{ "date", "+%Y-%m-%dT%H:%M:%S%z" },
        .environ_map = &env,
    }) catch return try std.mem.concat(ctx.gpa, u8, &.{ "unknown timezone ", tz });
    return std.mem.trim(u8, result.stdout, " \r\n");
}

/// Field lookup that reads a missing or non-string field as "".
fn str(v: json.Value, field: []const u8) []const u8 {
    if (v != .object) return "";
    const f = v.object.get(field) orelse return "";
    return if (f == .string) f.string else "";
}

fn object(gpa: std.mem.Allocator, fields: []const struct { []const u8, json.Value }) !json.Value {
    var map: json.ObjectMap = .empty;
    for (fields) |f| try map.put(gpa, f[0], f[1]);
    return .{ .object = map };
}

fn fail(io: Io, msg: []const u8, code: u8) !noreturn {
    var buf: [256]u8 = undefined;
    var w = Io.File.stderr().writer(io, &buf);
    try w.interface.writeAll(msg);
    try w.interface.flush();
    std.process.exit(code);
}
