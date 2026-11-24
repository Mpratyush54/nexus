using System.Globalization;
using System.Text;
using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// One usage total as the core reports it (internal/usage.Total): tokens,
/// cost (agent-reported plus estimated), and tokens of models with no
/// known price, whose cost is not included.
/// </summary>
internal sealed class UsageTotal
{
    public string Key = "";
    public string Model = "";
    public long Input, Output, CacheRead, CacheWrite, UnpricedTokens, Turns;
    public double CostUsd;
    public bool Estimated, HasCost;

    public long InTokens => Input + CacheRead + CacheWrite;
    public long AllTokens => InTokens + Output;

    public static UsageTotal Parse(JsonElement el)
    {
        static long L(JsonElement e, string n) => e.TryGetProperty(n, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetInt64(out var x) ? x : 0;
        static bool B(JsonElement e, string n) => e.TryGetProperty(n, out var v) && v.ValueKind == JsonValueKind.True;
        var t = new UsageTotal
        {
            Key = NxJson.Text(el, "key"),
            Model = NxJson.Text(el, "model"),
            Input = L(el, "input"), Output = L(el, "output"), CacheRead = L(el, "cache_read"), CacheWrite = L(el, "cache_write"),
            UnpricedTokens = L(el, "unpriced_tokens"), Turns = L(el, "turns"),
            Estimated = B(el, "estimated"), HasCost = B(el, "has_cost"),
        };
        if (el.TryGetProperty("cost_usd", out var c) && c.ValueKind == JsonValueKind.Number) t.CostUsd = c.GetDouble();
        return t;
    }

    public void Add(UsageTotal o)
    {
        Input += o.Input; Output += o.Output; CacheRead += o.CacheRead; CacheWrite += o.CacheWrite;
        UnpricedTokens += o.UnpricedTokens; Turns += o.Turns; CostUsd += o.CostUsd;
        Estimated |= o.Estimated; HasCost |= o.HasCost;
    }
}

internal static class UsageFormat
{
    public static string Tokens(long n) => n switch
    {
        >= 1_000_000_000 => (n / 1e9).ToString("0.#", CultureInfo.InvariantCulture) + "B",
        >= 1_000_000 => (n / 1e6).ToString("0.#", CultureInfo.InvariantCulture) + "M",
        >= 1_000 => (n / 1e3).ToString("0.#", CultureInfo.InvariantCulture) + "k",
        _ => n.ToString(CultureInfo.InvariantCulture),
    };

    public static string Money(double usd) => usd switch
    {
        0 => "$0",
        < 0.01 => "<$0.01",
        < 100 => "$" + usd.ToString("0.00", CultureInfo.InvariantCulture),
        _ => "$" + usd.ToString("#,0", CultureInfo.InvariantCulture),
    };

    /// <summary>"$0.21 (est.)", "$0.21", or "" when nothing could be priced.</summary>
    public static string Cost(UsageTotal t) => !t.HasCost ? "" : Money(t.CostUsd) + (t.Estimated ? " (est.)" : "");

    /// <summary>"12.4k in · 3.1k out · $0.21 (est.)"</summary>
    public static string Line(UsageTotal t)
    {
        var parts = new List<string> { Tokens(t.InTokens) + " in", Tokens(t.Output) + " out" };
        var cost = Cost(t);
        if (cost.Length > 0) parts.Add(cost);
        return string.Join(" · ", parts);
    }

    /// <summary>One line per model for tooltips and lists.</summary>
    public static string Detail(UsageTotal t)
    {
        var name = t.Model.Length > 0 ? t.Model : t.Key.Length > 0 ? t.Key : "Unknown model";
        var sb = new StringBuilder(name).Append(": ").Append(Tokens(t.InTokens)).Append(" in");
        if (t.CacheRead > 0) sb.Append(" (").Append(Tokens(t.CacheRead)).Append(" cached)");
        sb.Append(" · ").Append(Tokens(t.Output)).Append(" out");
        if (t.HasCost) sb.Append(" · ").Append(Cost(t));
        if (t.UnpricedTokens > 0) sb.Append(t.HasCost ? " + tokens with no known price" : " · no known price");
        return sb.ToString();
    }
}

/// <summary>
/// Compact usage line for a chat: "12.4k in · 3.1k out · $0.21 (est.)",
/// with a per-model tooltip. It loads sessions.usage for the page's chat and
/// adds the live turn's usage as it streams (live.event kind "usage" and
/// turn_done), then reloads once the turn is recorded. Self-contained: the
/// page only places it and hands it a session id getter.
/// </summary>
public sealed class ChatUsageBar : UserControl
{
    private readonly Func<string> _sessionId;
    private readonly TextBlock _line = new() { FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
    private readonly DispatcherTimer _watch = new() { Interval = TimeSpan.FromSeconds(1) };
    private List<UsageTotal> _base = new();
    private List<UsageTotal> _live = new();
    private string _loadedId = "";
    private int _generation;

    public ChatUsageBar(Func<string> sessionId)
    {
        _sessionId = sessionId;
        _line.Foreground = NexusPalette.Muted;
        Content = _line;
        Visibility = Visibility.Collapsed;
        VerticalAlignment = VerticalAlignment.Center;
        // The page swaps chats without recreating controls; a string compare
        // per second notices that without any core call.
        _watch.Tick += (_, _) => { if (_sessionId() != _loadedId) _ = RefreshAsync(); };
        Loaded += (_, _) => { App.Core.EventReceived += OnCoreEvent; _watch.Start(); _ = RefreshAsync(); };
        Unloaded += (_, _) => { App.Core.EventReceived -= OnCoreEvent; _watch.Stop(); };
    }

    public async Task RefreshAsync()
    {
        var id = _sessionId();
        var generation = ++_generation;
        _loadedId = id;
        if (id.Length == 0) { _base = new(); _live = new(); Render(); return; }
        var response = await App.Core.CallAsync(NxMethods.SessionsUsage, new { session_id = id });
        await UiThread.Resume(DispatcherQueue);
        if (generation != _generation) return;
        _base = new();
        if (response.Ok && response.Result is { } result && result.TryGetProperty("by_model", out var models) && models.ValueKind == JsonValueKind.Array)
            foreach (var m in models.EnumerateArray()) _base.Add(UsageTotal.Parse(m));
        _live = new();
        Render();
    }

    private void OnCoreEvent(string json)
    {
        if (!json.Contains("\"type\":\"live.", StringComparison.Ordinal) || _loadedId.Length == 0) return;
        try
        {
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            if (NxJson.Text(root, "session_id") != _loadedId) return;
            if (NxJson.Text(root, "type") == "live.recorded") { _ = RefreshAsync(); return; }
            var kind = NxJson.Text(root, "kind");
            if (kind != "usage" && kind != "turn_done") return;
            if (!root.TryGetProperty("usage", out var usage) || usage.ValueKind != JsonValueKind.Array) return;
            // Each event carries the whole turn so far, so it replaces the last.
            var live = new List<UsageTotal>();
            foreach (var e in usage.EnumerateArray())
            {
                var t = UsageTotal.Parse(e);
                if (e.TryGetProperty("cost_usd", out var c) && c.ValueKind == JsonValueKind.Number) t.HasCost = true;
                live.Add(t);
            }
            _live = live;
            Render();
        }
        catch (JsonException) { }
    }

    private void Render()
    {
        var byModel = new Dictionary<string, UsageTotal>(StringComparer.Ordinal);
        foreach (var t in _base.Concat(_live))
        {
            if (!byModel.TryGetValue(t.Model, out var sum)) byModel[t.Model] = sum = new UsageTotal { Model = t.Model };
            sum.Add(t);
        }
        var total = new UsageTotal();
        foreach (var t in byModel.Values) total.Add(t);
        if (total.AllTokens == 0 && !total.HasCost)
        {
            Visibility = Visibility.Collapsed;
            return;
        }
        Visibility = Visibility.Visible;
        _line.Text = UsageFormat.Line(total);
        var tip = new StringBuilder();
        foreach (var t in byModel.Values.OrderByDescending(t => t.AllTokens)) tip.AppendLine(UsageFormat.Detail(t));
        if (_live.Count > 0) tip.AppendLine("Includes the reply in progress.");
        if (total.Estimated) tip.AppendLine("(est.) = estimated from list prices where the agent didn't report cost.");
        ToolTipService.SetToolTip(this, tip.ToString().TrimEnd());
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(this, "Chat usage: " + _line.Text);
    }
}
