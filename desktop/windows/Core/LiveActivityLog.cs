#nullable enable
using System.Text.Json;

namespace Nexus.Core;

internal sealed record LiveToolActivity(string ID, string Tool, string Status, IReadOnlyList<string> Files,
    string Title = "", string Command = "", string Input = "", string Output = "", string Error = "", int? ExitCode = null, long DurationMS = 0, string Diff = "", string Parent = "")
{
    public bool HasDetails => Command.Length + Input.Length + Output.Length + Error.Length + Diff.Length > 0 || Files.Count > 0 || ExitCode.HasValue;
    public string Preview => Command.Length > 0 ? Command : Title.Length > 0 ? Title : Files.FirstOrDefault() ?? Tool;
    public string Label => Status switch
    {
        "pending" => "Queued",
        "running" => "Running",
        "completed" => "Completed",
        "error" => "Failed",
        _ => "Unknown"
    };
}

/// <summary>Tool progress is not assistant prose. Keep it separate and bounded.</summary>
internal sealed class LiveActivityLog
{
    private readonly Dictionary<string, LiveToolActivity> _items = new(StringComparer.Ordinal);
    private readonly List<string> _order = new();
    public int Count => _items.Count;
    public LiveToolActivity? Latest { get; private set; }
    public IEnumerable<LiveToolActivity> Recent => _order.TakeLast(40).Select(id => _items[id]);
    public IEnumerable<LiveToolActivity> All => _order.Select(id => _items[id]);
    public string Summary => $"Tool activity · {Count}" +
        (_items.Values.Count(item => item.Status == "running") is var running && running > 0 ? $" · {running} running" : "") +
        (_items.Values.Count(item => item.Status == "error") is var failed && failed > 0 ? $" · {failed} failed" : "");

    public bool Apply(string json)
    {
        try
        {
            using var doc = JsonDocument.Parse(json);
            if (doc.RootElement.ValueKind != JsonValueKind.Object) return false;
            var root = doc.RootElement;
            var id = NxJson.Text(root, "id");
            var tool = NxJson.Text(root, "tool");
            var status = NxJson.Text(root, "status");
            if (id.Length is 0 or > 256 || tool.Length is 0 or > 100 ||
                status is not ("pending" or "running" or "completed" or "error")) return false;
            var files = new List<string>();
            if (root.TryGetProperty("files", out var paths) && paths.ValueKind == JsonValueKind.Array)
                foreach (var path in paths.EnumerateArray().Take(100))
                    if (path.ValueKind == JsonValueKind.String && path.GetString() is { Length: > 0 and <= 512 } value) files.Add(value);
            string Detail(string key, int cap)
            {
                var value = NxJson.Text(root, key);
                return value.Length <= cap ? value : value[..cap] + "\n… Preview truncated.";
            }
            var exitCode = root.TryGetProperty("exit_code", out var exit) && exit.ValueKind == JsonValueKind.Number && exit.TryGetInt32(out var number) ? (int?)number : null;
            var duration = root.TryGetProperty("duration_ms", out var time) && time.ValueKind == JsonValueKind.Number && time.TryGetInt64(out var ms) && ms >= 0 ? ms : 0;
            var item = new LiveToolActivity(id, tool, status, files, Detail("title", 512), Detail("command", 8192),
                Detail("input", 16384), Detail("output", 32768), Detail("error", 4096), exitCode, duration, Detail("diff", 32768), NxJson.Text(root, "parent"));
            if (_items.TryGetValue(id, out var old) && (old with { Files = files }) == item && old.Files.SequenceEqual(files)) return false;
            if (!_items.ContainsKey(id))
            {
                if (_order.Count >= 2048) { _items.Remove(_order[0]); _order.RemoveAt(0); }
                _order.Add(id);
            }
            _items[id] = item;
            Latest = item;
            return true;
        }
        catch (JsonException) { return false; }
    }
}
