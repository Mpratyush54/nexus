using System.Globalization;
using System.Text.Json;
using System.Text.RegularExpressions;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

// The work log under an assistant reply (reference: T3 Code). One quiet
// summary line per turn ("Worked for 12s · Read 3 files · Ran 4 commands");
// expanded, one line per step with an icon, a verb and a target, and the
// step's output or diff one click further down. Live streams, saved Nexus
// replies and captured transcripts all render through this one view.

internal enum StepKind { Read, Edit, Run, Search, List, Web, Agent, Memory, Todo, Tool }

internal sealed class WorkStep
{
    public string Id { get; set; } = "";
    public string Parent { get; set; } = "";
    public string Tool { get; set; } = "";
    public StepKind Kind { get; set; } = StepKind.Tool;
    public string Verb { get; set; } = "";
    public string Target { get; set; } = "";
    public bool TargetIsCode { get; set; }
    public List<string> Files { get; } = new();
    public string Status { get; set; } = "completed";
    public long DurationMS { get; set; }
    public int? ExitCode { get; set; }
    public string Command { get; set; } = "";
    public string Prompt { get; set; } = "";
    public string Output { get; set; } = "";
    public string Error { get; set; } = "";
    public string Diff { get; set; } = "";
    public IReadOnlyList<(string Label, string Text)> Fields { get; set; } = Array.Empty<(string, string)>();
    public Func<UIElement?>? Extra { get; set; }
    public bool AwaitingResult { get; set; }
    public bool IsRunning => Status is "running" or "pending";
    public bool IsError => Status == "error" || ExitCode is not null and not 0;
}

internal static class WorkLogText
{
    public static string Plural(int n, string one, string? many = null) => n + " " + (n == 1 ? one : many ?? one + "s");

    public static string Duration(long ms)
    {
        if (ms < 1000) return Math.Max(ms, 1) + "ms";
        if (ms < 60_000) return (ms / 1000d).ToString(ms < 10_000 ? "0.0" : "0", CultureInfo.InvariantCulture) + "s";
        return $"{ms / 60_000}m {ms / 1000 % 60}s";
    }

    // "Read 3 files · Edited 2 files · Ran 4 commands · 1 sub-agent"
    public static List<string> Counts(IReadOnlyCollection<WorkStep> steps)
    {
        var parts = new List<string>();
        int Files(StepKind kind) => steps.Where(s => s.Kind == kind)
            .SelectMany(s => s.Files.Count > 0 ? s.Files : new List<string> { s.Target.Length > 0 ? s.Target : s.Id })
            .Distinct(StringComparer.OrdinalIgnoreCase).Count();
        int Count(params StepKind[] kinds) => steps.Count(s => kinds.Contains(s.Kind));
        if (Files(StepKind.Read) is var read and > 0) parts.Add("Read " + Plural(read, "file"));
        if (Files(StepKind.Edit) is var edited and > 0) parts.Add("Edited " + Plural(edited, "file"));
        if (Count(StepKind.Run) is var ran and > 0) parts.Add("Ran " + Plural(ran, "command"));
        if (Count(StepKind.Search, StepKind.List) is var searched and > 0) parts.Add(Plural(searched, "search", "searches"));
        if (Count(StepKind.Web) is var web and > 0) parts.Add(Plural(web, "web lookup"));
        if (Count(StepKind.Memory) is var memory and > 0) parts.Add(Plural(memory, "memory lookup"));
        if (Count(StepKind.Agent) is var agents and > 0) parts.Add(Plural(agents, "sub-agent"));
        if (Count(StepKind.Todo, StepKind.Tool) is var other and > 0) parts.Add(Plural(other, "tool call"));
        if (steps.Count(s => s.IsError) is var failed and > 0) parts.Add(failed + " failed");
        return parts;
    }

    public static string Glyph(StepKind kind) => kind switch
    {
        StepKind.Read => "",
        StepKind.Edit => "",
        StepKind.Run => "",
        StepKind.Search => "",
        StepKind.List => "",
        StepKind.Web => "",
        StepKind.Agent => "",
        StepKind.Memory => "",
        StepKind.Todo => "",
        _ => ""
    };

    public static string Humanize(string key)
    {
        var text = Regex.Replace(key.Replace('_', ' ').Replace('-', ' '), "(?<=[a-z])(?=[A-Z])", " ").Trim();
        if (text.Length == 0) return key;
        return char.ToUpperInvariant(text[0]) + text[1..].ToLowerInvariant();
    }

    public static string OneLine(string text, int max = 160)
    {
        var lines = (text ?? "").Replace("\r\n", "\n").Trim().Split('\n');
        var first = Regex.Replace(lines[0], @"\s+", " ").Trim();
        if (lines.Length > 1) first += " …";
        return first.Length <= max ? first : first[..(max - 1)] + "…";
    }

    // src/very/long/…/folder/file.ts: keep the start and, above all, the file name.
    public static string MiddleEllipsis(string text, int max = 64)
    {
        text = (text ?? "").Trim();
        if (text.Length <= max || max < 8) return text;
        var tail = (int)(max * 0.6);
        var head = max - tail - 1;
        return text[..head] + "…" + text[^tail..];
    }

    // Tool output for people: JSON envelopes become their text, never braces.
    public static string Readable(string text)
    {
        // A sub-agent's hand-back framing (tools-used list, "[Subagent
        // hand-back] …") is for the model; people see the report.
        var t = (text ?? "").Replace("<tool_use_error>", "").Replace("</tool_use_error>", "").Trim();
        if (t.Length > 0 && t[0] is '{' or '[' && !t.StartsWith("[Subagent hand-back]", StringComparison.Ordinal))
        {
            try
            {
                using var doc = JsonDocument.Parse(t);
                var flat = Flatten(doc.RootElement, 0).Trim();
                if (flat.Length > 0) return HarnessEnvelope.CleanToolResult(flat);
            }
            catch (JsonException) { }
        }
        return HarnessEnvelope.CleanToolResult(t);
    }

    private static readonly string[] Preferred = { "output", "aggregated_output", "formatted_output", "content", "text", "result", "message", "error" };

    private static string Flatten(JsonElement value, int depth)
    {
        switch (value.ValueKind)
        {
            case JsonValueKind.String:
                var s = value.GetString() ?? "";
                return depth < 4 && s.TrimStart() is { Length: > 0 } trimmed && trimmed[0] is '{' or '[' ? Readable(s) : s;
            case JsonValueKind.Number:
            case JsonValueKind.True:
            case JsonValueKind.False:
                return value.GetRawText();
            case JsonValueKind.Array:
                return string.Join("\n", value.EnumerateArray().Take(200).Select(item => Flatten(item, depth + 1)).Where(item => item.Length > 0));
            case JsonValueKind.Object:
                if (depth < 6)
                {
                    var stdout = NxJson.Text(value, "stdout");
                    var stderr = NxJson.Text(value, "stderr");
                    if (stdout.Length + stderr.Length > 0) return (stdout + (stdout.Length > 0 && stderr.Length > 0 ? "\n" : "") + stderr).TrimEnd();
                    foreach (var key in Preferred)
                        if (value.TryGetProperty(key, out var inner) && Flatten(inner, depth + 1) is { Length: > 0 } found) return found;
                }
                var lines = new List<string>();
                foreach (var property in value.EnumerateObject().Take(40))
                {
                    var v = property.Value;
                    var text = v.ValueKind switch
                    {
                        JsonValueKind.Object => depth < 2 ? Flatten(v, depth + 1) : "…",
                        JsonValueKind.Array => v.GetArrayLength() == 0 ? "" : depth < 2 ? Flatten(v, depth + 1) : Plural(v.GetArrayLength(), "item"),
                        _ => Flatten(v, depth + 1)
                    };
                    if (text.Length == 0) continue;
                    var label = Humanize(property.Name);
                    lines.Add(text.Contains('\n') ? label + ":\n  " + text.Replace("\n", "\n  ") : label + ": " + text);
                }
                return string.Join("\n", lines);
            default:
                return "";
        }
    }
}

internal sealed class WorkLogView : UserControl
{
    private readonly bool _live;
    private readonly WorkStepList _root = new(capped: true);
    private readonly Dictionary<string, WorkStepRow> _rows = new(StringComparer.Ordinal);
    private readonly Button _header;
    private readonly FontIcon _chevron = WorkLogVisuals.Chevron();
    private readonly TextBlock _summary;
    private readonly Border _body;
    private readonly DateTimeOffset _started = DateTimeOffset.Now;
    private DateTimeOffset _lastChange = DateTimeOffset.Now;
    private DispatcherTimer? _tick;
    private bool _expanded, _settled, _solo;

    public WorkLogView(bool live, bool expanded)
    {
        _live = live;
        _expanded = expanded;
        _summary = new TextBlock
        {
            Text = "Working…", FontSize = 12.5, Foreground = NexusPalette.Muted,
            TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap,
            VerticalAlignment = VerticalAlignment.Center
        };
        var line = new Grid { ColumnSpacing = 8 };
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(16) });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        line.Children.Add(_chevron);
        Grid.SetColumn(_summary, 1);
        line.Children.Add(_summary);
        _header = WorkLogVisuals.RowButton(line);
        _header.Click += (_, _) => { _expanded = !_expanded; ApplyLayout(); };
        _body = new Border { Child = _root.Panel, BorderBrush = NexusPalette.Hairline };
        var stack = new StackPanel { HorizontalAlignment = HorizontalAlignment.Stretch };
        stack.Children.Add(_header);
        stack.Children.Add(_body);
        Content = stack;
        HorizontalAlignment = HorizontalAlignment.Stretch;
        HorizontalContentAlignment = HorizontalAlignment.Stretch;
        Margin = new Thickness(0, 2, 0, 2);
        Visibility = Visibility.Collapsed;
        ApplyLayout();
        Loaded += (_, _) => UpdateTick();
        Unloaded += (_, _) => StopTick();
    }

    public int Count => _rows.Count;

    /// Set by the page once the turn is over: the log stops ticking and
    /// any step still marked running is shown as stopped.
    public bool IsExpanded
    {
        get => _expanded;
        set { _expanded = value; Settle(); }
    }

    public void Upsert(WorkStep step)
    {
        if (step.Id.Length == 0) step.Id = "step:" + _rows.Count;
        if (_rows.TryGetValue(step.Id, out var row)) row.Update(step);
        else
        {
            row = new WorkStepRow(step, _live && !_settled);
            _rows[step.Id] = row;
            if (step.Parent.Length > 0 && _rows.TryGetValue(step.Parent, out var parent)) parent.AddChild(row);
            else _root.Add(row);
        }
        _lastChange = DateTimeOffset.Now;
        Visibility = Visibility.Visible;
        Refresh();
        UpdateTick();
    }

    private void Settle()
    {
        if (!_settled)
        {
            _settled = true;
            foreach (var row in _rows.Values.Where(r => r.Step.IsRunning)) row.Stop();
            StopTick();
        }
        Refresh();
    }

    private bool Running => !_settled && _rows.Values.Any(r => r.Step.IsRunning);

    private void Refresh()
    {
        var top = _root.Rows.Select(r => r.Step).ToList();
        var running = Running;
        // A saved exchange is replayed in one burst; its wall time is unknown.
        var replayed = !running && _lastChange - _started < TimeSpan.FromMilliseconds(400);
        _solo = top.Count == 1 && (!_live || _settled && replayed);
        string? time = null;
        if (_live && !replayed)
            time = WorkLogText.Duration((long)((running ? DateTimeOffset.Now : _lastChange) - _started).TotalMilliseconds);
        else if (top.Sum(s => s.DurationMS) is var total and > 0)
            time = WorkLogText.Duration(total);
        var parts = new List<string>();
        if (time is not null) parts.Add((running ? "Working for " : "Worked for ") + time);
        parts.AddRange(WorkLogText.Counts(top));
        _summary.Text = parts.Count > 0 ? string.Join(" · ", parts) : running ? "Working…" : "No steps";
        ToolTipService.SetToolTip(_header, _summary.Text);
        ApplyLayout();
    }

    private void ApplyLayout()
    {
        _header.Visibility = _solo ? Visibility.Collapsed : Visibility.Visible;
        _body.Visibility = _solo || _expanded ? Visibility.Visible : Visibility.Collapsed;
        _body.Margin = _solo ? new Thickness(0) : new Thickness(13, 0, 0, 4);
        _body.Padding = _solo ? new Thickness(0) : new Thickness(4, 0, 0, 0);
        _body.BorderThickness = _solo ? new Thickness(0) : new Thickness(1, 0, 0, 0);
        _chevron.Glyph = _expanded ? WorkLogVisuals.ChevronDown : WorkLogVisuals.ChevronRight;
    }

    private void UpdateTick()
    {
        if (!_live || !IsLoaded || !Running) { StopTick(); return; }
        if (_tick is not null) return;
        _tick = new DispatcherTimer { Interval = TimeSpan.FromSeconds(1) };
        _tick.Tick += (_, _) => { Refresh(); if (!Running) StopTick(); };
        _tick.Start();
    }

    private void StopTick()
    {
        _tick?.Stop();
        _tick = null;
    }
}

internal sealed class WorkStepList
{
    private const int Cap = 30;
    private bool _capped;
    private HyperlinkButton? _more;
    public StackPanel Panel { get; } = new() { HorizontalAlignment = HorizontalAlignment.Stretch };
    public List<WorkStepRow> Rows { get; } = new();

    public WorkStepList(bool capped) => _capped = capped;

    public void Add(WorkStepRow row)
    {
        Rows.Add(row);
        Panel.Children.Add(row.View);
        if (!_capped || Rows.Count <= Cap) return;
        // Long runs keep the newest steps in view; older ones fold away.
        var hidden = Rows.Count - Cap;
        for (var i = 0; i < Rows.Count; i++) Rows[i].View.Visibility = i < hidden ? Visibility.Collapsed : Visibility.Visible;
        if (_more is null)
        {
            _more = WorkLogVisuals.Link("");
            _more.Margin = new Thickness(30, 2, 0, 2);
            _more.Click += (_, _) =>
            {
                _capped = false;
                foreach (var r in Rows) r.View.Visibility = Visibility.Visible;
                Panel.Children.Remove(_more);
            };
            Panel.Children.Insert(0, _more);
        }
        _more.Content = "Show " + WorkLogText.Plural(hidden, "earlier step");
    }
}

internal sealed class WorkStepRow
{
    private readonly bool _live;
    private readonly DateTimeOffset _seen = DateTimeOffset.Now;
    private readonly Button _button;
    private readonly Button _preview;
    private readonly FontIcon _icon;
    private readonly ProgressRing _spin = WorkLogVisuals.Spinner();
    private readonly TextBlock _text;
    private readonly TextBlock _status;
    private readonly FontIcon _chevron = WorkLogVisuals.Chevron();
    private readonly Border _detail = new() { Visibility = Visibility.Collapsed };
    private readonly Border _childHost = new()
    {
        Visibility = Visibility.Collapsed, Margin = new Thickness(13, 0, 0, 2),
        Padding = new Thickness(4, 0, 0, 0), BorderThickness = new Thickness(1, 0, 0, 0)
    };
    private readonly TextBlock _childSummary = new()
    {
        FontSize = 12, Margin = new Thickness(6, 2, 0, 2),
        TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap
    };
    private readonly Border _tail = new() { Visibility = Visibility.Collapsed };
    private WorkStepList? _children;
    private bool _open, _stopped;

    public WorkStep Step { get; private set; }
    public WorkStepRow? ParentRow { get; private set; }
    public StackPanel View { get; } = new() { HorizontalAlignment = HorizontalAlignment.Stretch };

    public WorkStepRow(WorkStep step, bool live)
    {
        _live = live;
        Step = step;
        _icon = new FontIcon { FontFamily = WorkLogVisuals.IconFont, FontSize = 12, VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center };
        _text = new TextBlock
        {
            FontSize = 12.5, TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap,
            MaxLines = 1, VerticalAlignment = VerticalAlignment.Center
        };
        _status = new TextBlock { FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Right };
        var line = new Grid { ColumnSpacing = 8 };
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(16) });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(10) });
        // Spinner and icon share one fixed cell: finishing never moves the line.
        var iconCell = new Grid { Width = 16, Height = 16, VerticalAlignment = VerticalAlignment.Center };
        iconCell.Children.Add(_icon);
        iconCell.Children.Add(_spin);
        line.Children.Add(iconCell);
        Grid.SetColumn(_text, 1); line.Children.Add(_text);
        Grid.SetColumn(_status, 2); line.Children.Add(_status);
        Grid.SetColumn(_chevron, 3); line.Children.Add(_chevron);
        _button = WorkLogVisuals.RowButton(line);
        _button.Click += (_, _) => Toggle();
        _childSummary.Foreground = NexusPalette.Muted;
        _childHost.BorderBrush = NexusPalette.Hairline;
        // Edited/read files open the chat's file preview; the eye button sits
        // beside the row (not inside it) so the row still expands its diff.
        var head = new Grid();
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.Children.Add(_button);
        _preview = new Button
        {
            Content = new FontIcon { Glyph = "", FontFamily = WorkLogVisuals.IconFont, FontSize = 12 },
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
            Padding = new Thickness(6, 0, 6, 0), MinWidth = 26, Height = SessionChatRender.AuxRowHeight, MinHeight = 0,
            Foreground = NexusPalette.Muted, Opacity = 0, Visibility = Visibility.Collapsed, VerticalAlignment = VerticalAlignment.Top
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_preview, "Preview file");
        _preview.Click += (_, _) => PreviewFiles();
        Grid.SetColumn(_preview, 1);
        head.Children.Add(_preview);
        head.PointerEntered += (_, _) => _preview.Opacity = 0.8;
        head.PointerExited += (_, _) => _preview.Opacity = _preview.FocusState == FocusState.Unfocused ? 0 : 0.8;
        _preview.GotFocus += (_, _) => _preview.Opacity = 0.8;
        _preview.LostFocus += (_, _) => _preview.Opacity = 0;
        View.Children.Add(head);
        View.Children.Add(_detail);
        View.Children.Add(_childHost);
        View.Children.Add(_tail);
        Render();
    }

    public void Update(WorkStep step)
    {
        if (step.DurationMS == 0)
        {
            if (Step.DurationMS > 0) step.DurationMS = Step.DurationMS;
            else if (_live && Step.IsRunning && !step.IsRunning) step.DurationMS = (long)(DateTimeOffset.Now - _seen).TotalMilliseconds;
        }
        step.Extra ??= Step.Extra;
        Step = step;
        Render();
        if (_open) BuildDetail();
        ParentRow?.Render();
    }

    public void AddChild(WorkStepRow row)
    {
        if (_children is null)
        {
            _children = new WorkStepList(capped: false);
            var host = new StackPanel();
            host.Children.Add(_childSummary);
            host.Children.Add(_children.Panel);
            _childHost.Child = host;
        }
        row.ParentRow = this;
        _children.Add(row);
        _childHost.Visibility = _open ? Visibility.Visible : Visibility.Collapsed;
        Render();
    }

    public void Stop()
    {
        _stopped = true;
        Render();
    }

    private bool HasDetail =>
        Step.Output.Length + Step.Error.Length + Step.Diff.Length + Step.Command.Length + Step.Prompt.Length > 0 ||
        Step.Fields.Count > 0 || Step.Extra is not null || Step.Files.Count > 1 || _children is not null;

    internal void Render()
    {
        var s = Step;
        var running = s.IsRunning && !_stopped;
        _spin.IsActive = running;
        _spin.Visibility = running ? Visibility.Visible : Visibility.Collapsed;
        _icon.Visibility = running ? Visibility.Collapsed : Visibility.Visible;
        _icon.Glyph = WorkLogText.Glyph(s.Kind);
        _icon.Foreground = s.IsError ? NexusPalette.Danger : NexusPalette.Muted;
        _text.Inlines.Clear();
        _text.Inlines.Add(new Run { Text = s.Verb.Length > 0 ? s.Verb : s.Tool });
        if (s.Target.Length > 0)
        {
            var target = new Run { Text = " " + s.Target, Foreground = NexusPalette.Muted };
            if (s.TargetIsCode) { target.FontFamily = WorkLogVisuals.MonoFont; target.FontSize = 12; }
            _text.Inlines.Add(target);
        }
        var full = s.Kind == StepKind.Run && s.Command.Length > 0 ? s.Command : s.Files.Count > 0 ? string.Join("\n", s.Files) : s.Target;
        ToolTipService.SetToolTip(_button, (s.Verb + " " + full).Trim());
        string status;
        if (running) status = "";
        else if (s.ExitCode is int code && code != 0) status = "exit " + code;
        else if (s.Status == "error") status = "failed";
        else if (s.IsRunning) status = "stopped";
        else status = s.DurationMS > 0 ? WorkLogText.Duration(s.DurationMS) : "";
        if (_children is { } kids)
        {
            var steps = WorkLogText.Plural(kids.Rows.Count, "step");
            status = status.Length > 0 ? steps + " · " + status : steps;
            var parts = WorkLogText.Counts(kids.Rows.Select(r => r.Step).ToList());
            _childSummary.Text = parts.Count > 0 ? string.Join(" · ", parts) : steps;
        }
        _status.Text = status;
        _status.Foreground = s.IsError ? NexusPalette.Danger : NexusPalette.Muted;
        _chevron.Opacity = HasDetail ? 0.7 : 0;
        _chevron.Glyph = _open ? WorkLogVisuals.ChevronDown : WorkLogVisuals.ChevronRight;
        var previewable = PreviewPaths();
        _preview.Visibility = previewable.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        ToolTipService.SetToolTip(_preview, previewable.Count == 1 ? "Preview " + previewable[0] : "Preview a file");
    }

    // Files a read/edit step touched, as full or workspace-relative paths.
    private List<string> PreviewPaths()
    {
        if (Step.Kind is not (StepKind.Edit or StepKind.Read)) return new List<string>();
        if (Step.Files.Count > 0) return Step.Files.Where(f => f.Trim().Length > 0).Distinct(StringComparer.OrdinalIgnoreCase).ToList();
        var target = Step.Target.Trim();
        return Step.TargetIsCode && target.Length > 0 && !target.Contains('…') ? new List<string> { target } : new List<string>();
    }

    private void PreviewFiles()
    {
        var paths = PreviewPaths();
        if (paths.Count == 1) { FileLinks.Open(_preview, _preview.XamlRoot, new FileRef(paths[0])); return; }
        if (paths.Count == 0) return;
        var menu = new MenuFlyout();
        foreach (var path in paths)
        {
            var item = new MenuFlyoutItem { Text = WorkLogText.MiddleEllipsis(path, 80) };
            ToolTipService.SetToolTip(item, path);
            item.Click += (_, _) => FileLinks.Open(_preview, _preview.XamlRoot, new FileRef(path));
            menu.Items.Add(item);
        }
        menu.ShowAt(_preview);
    }

    private void Toggle()
    {
        if (!HasDetail)
        {
            // A plain "Edited src/x.ts" row has nothing to expand: preview it.
            if (PreviewPaths().Count > 0) PreviewFiles();
            return;
        }
        _open = !_open;
        if (_open) BuildDetail();
        _detail.Visibility = _open && _detail.Child is not null ? Visibility.Visible : Visibility.Collapsed;
        _childHost.Visibility = _open && _children is not null ? Visibility.Visible : Visibility.Collapsed;
        _tail.Visibility = _open && _tail.Child is not null ? Visibility.Visible : Visibility.Collapsed;
        Render();
    }

    private void BuildDetail()
    {
        _detail.Child = WorkLogVisuals.Detail(Step);
        var result = Step.Kind == StepKind.Agent ? WorkLogText.Readable(Step.Output) : "";
        _tail.Child = result.Length > 0
            ? new Border { Margin = new Thickness(30, 2, 6, 8), Child = WorkLogVisuals.OutputBlock(result, 8, mono: false, error: Step.IsError) }
            : null;
        _detail.Visibility = _open && _detail.Child is not null ? Visibility.Visible : Visibility.Collapsed;
        _tail.Visibility = _open && _tail.Child is not null ? Visibility.Visible : Visibility.Collapsed;
    }
}

internal static class WorkLogVisuals
{
    public const string ChevronRight = "";
    public const string ChevronDown = "";
    public static FontFamily IconFont => new("Segoe Fluent Icons,Segoe MDL2 Assets");
    public static FontFamily MonoFont => new("Cascadia Mono,Consolas");

    public static Button RowButton(UIElement content) => new()
    {
        Content = content,
        Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
        BorderThickness = new Thickness(0),
        Padding = new Thickness(6, 0, 6, 0),
        Height = SessionChatRender.AuxRowHeight,
        MinHeight = SessionChatRender.AuxRowHeight,
        CornerRadius = new CornerRadius(4),
        HorizontalAlignment = HorizontalAlignment.Stretch,
        HorizontalContentAlignment = HorizontalAlignment.Stretch,
        VerticalContentAlignment = VerticalAlignment.Center
    };

    public static FontIcon Chevron() => new()
    {
        Glyph = ChevronRight, FontFamily = IconFont, FontSize = 9, Foreground = NexusPalette.Muted,
        VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center
    };

    public static ProgressRing Spinner() => new()
    {
        Width = 12, Height = 12, MinWidth = 12, MinHeight = 12, IsActive = false,
        Foreground = NexusPalette.Muted, Visibility = Visibility.Collapsed,
        VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center
    };

    public static HyperlinkButton Link(string text) => new()
    {
        Content = text, FontSize = 11.5, Padding = new Thickness(0), MinHeight = 0,
        Foreground = NexusPalette.Muted
    };

    private static void Copy(string text)
    {
        var data = new DataPackage();
        data.SetText(text);
        Clipboard.SetContent(data);
    }

    private static Border Panel(UIElement child) => new()
    {
        Child = child,
        Background = NexusPalette.Surface,
        BorderBrush = NexusPalette.Hairline,
        BorderThickness = new Thickness(1),
        CornerRadius = new CornerRadius(6),
        Padding = new Thickness(10, 7, 10, 6),
        HorizontalAlignment = HorizontalAlignment.Stretch
    };

    public static UIElement? Detail(WorkStep s)
    {
        var panel = new StackPanel { Spacing = 6, Margin = new Thickness(30, 2, 6, 8), HorizontalAlignment = HorizontalAlignment.Stretch };
        if (s.Kind == StepKind.Agent && s.Prompt.Length > 0)
            panel.Children.Add(new TextBlock
            {
                Text = s.Prompt.Trim(), FontSize = 12.5, LineHeight = 18, Foreground = NexusPalette.Muted,
                TextWrapping = TextWrapping.Wrap, MaxLines = 4, TextTrimming = TextTrimming.CharacterEllipsis,
                IsTextSelectionEnabled = true
            });
        if (s.Kind == StepKind.Run && s.Command.Length > 0 && (s.Command.Contains('\n') || s.Command.Length > 90))
            panel.Children.Add(OutputBlock("$ " + s.Command.Trim(), 6));
        if (s.Files.Count > 1)
            panel.Children.Add(new TextBlock
            {
                Text = string.Join("\n", s.Files.Select(f => WorkLogText.MiddleEllipsis(f, 96))),
                FontFamily = MonoFont, FontSize = 12, LineHeight = 18, Foreground = NexusPalette.Muted,
                IsTextSelectionEnabled = true
            });
        foreach (var (label, text) in s.Fields)
            panel.Children.Add(Field(label, text));
        if (s.Diff.Length > 0 && DiffBlock(s.Diff) is { } diff) panel.Children.Add(diff);
        var output = WorkLogText.Readable(s.Output);
        var error = WorkLogText.Readable(s.Error);
        var showOutput = output.Length > 0 && s.Kind != StepKind.Agent && !(s.Kind == StepKind.Edit && s.Diff.Length > 0 && !s.IsError);
        if (showOutput) panel.Children.Add(OutputBlock(output, 12, error: s.IsError && error.Length == 0));
        if (error.Length > 0 && error != output) panel.Children.Add(OutputBlock(error, 12, error: true));
        if (s.Extra?.Invoke() is { } extra) panel.Children.Add(extra);
        if (panel.Children.Count == 0)
            panel.Children.Add(new TextBlock { Text = s.IsRunning ? "Running…" : "No output.", FontSize = 12, Foreground = NexusPalette.Muted });
        return panel;
    }

    private static UIElement Field(string label, string text)
    {
        var grid = new Grid { ColumnSpacing = 10 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto, MaxWidth = 140 });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(new TextBlock { Text = label, FontSize = 12, Foreground = NexusPalette.Muted, TextTrimming = TextTrimming.CharacterEllipsis });
        var value = new TextBlock
        {
            Text = text, FontSize = 12, LineHeight = 18, TextWrapping = TextWrapping.Wrap,
            MaxLines = 8, TextTrimming = TextTrimming.CharacterEllipsis, IsTextSelectionEnabled = true
        };
        Grid.SetColumn(value, 1);
        grid.Children.Add(value);
        return grid;
    }

    // Output capped at a few lines, with "Show all" and "Copy": monospace,
    // or markdown when mono is false (sub-agent reports).
    public static UIElement OutputBlock(string text, int cap, bool mono = true, bool error = false)
    {
        const int hardCap = 2000;
        var normalized = text.Replace("\r\n", "\n").TrimEnd();
        var lines = normalized.Split('\n');
        var block = new TextBlock
        {
            FontSize = mono ? 12 : 12.5, LineHeight = 18, IsTextSelectionEnabled = true,
            TextWrapping = TextWrapping.Wrap, Opacity = 0.9
        };
        if (mono) block.FontFamily = MonoFont;
        if (error) block.Foreground = NexusPalette.Danger;
        // Prose (a sub-agent's report) is markdown: rendered as chat bubbles
        // render it. Command output stays plain monospace text.
        var prose = mono ? null : new Border();
        var all = false;
        void Show()
        {
            var take = Math.Min(lines.Length, all ? hardCap : cap);
            var shown = string.Join("\n", lines.Take(take));
            if (all && lines.Length > hardCap) shown += $"\n… {lines.Length - hardCap} more lines";
            if (prose is null)
            {
                block.Text = shown;
                return;
            }
            var document = SessionPage.FormattedMessage(shown);
            document.FontSize = 12.5;
            document.LineHeight = 18;
            document.Opacity = 0.9;
            if (error) document.Foreground = NexusPalette.Danger;
            prose.Child = document;
        }
        Show();
        var stack = new StackPanel { Spacing = 4 };
        stack.Children.Add(prose is null ? block : prose);
        var footer = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 14 };
        if (lines.Length > cap)
        {
            var more = Link($"Show all {lines.Length} lines");
            more.Click += (_, _) =>
            {
                all = !all;
                Show();
                more.Content = all ? "Show less" : $"Show all {lines.Length} lines";
            };
            footer.Children.Add(more);
        }
        var copy = Link("Copy");
        copy.Click += (_, _) => Copy(normalized);
        footer.Children.Add(copy);
        stack.Children.Add(footer);
        return Panel(stack);
    }

    private static readonly Regex HunkHeader = new(@"^@@ -\d", RegexOptions.Compiled);

    // Compact unified diff: +/- lines tinted, file and hunk headers muted.
    public static UIElement? DiffBlock(string diff, int cap = 24)
    {
        const int hardCap = 2000;
        var rows = FileDiffPreview.ParseLines(diff).Where(row => row.Kind is "added" or "removed" or "context" ||
            HunkHeader.IsMatch(row.Text) || row.Text.StartsWith("*** Add File:") || row.Text.StartsWith("*** Update File:") ||
            row.Text.StartsWith("*** Delete File:")).ToList();
        if (rows.Count == 0) return null;
        var added = rows.Count(r => r.Kind == "added");
        var removed = rows.Count(r => r.Kind == "removed");
        var block = new TextBlock { FontFamily = MonoFont, FontSize = 12, LineHeight = 18, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true };
        var all = false;
        void Show()
        {
            block.Inlines.Clear();
            var take = Math.Min(rows.Count, all ? hardCap : cap);
            for (var i = 0; i < take; i++)
            {
                var row = rows[i];
                var run = new Run { Text = row.Text.Length > 0 ? row.Text : " " };
                if (row.Kind == "added") run.Foreground = NexusPalette.DiffAdded;
                else if (row.Kind == "removed") run.Foreground = NexusPalette.DiffRemoved;
                else if (row.Kind == "header") run.Foreground = NexusPalette.Muted;
                block.Inlines.Add(run);
                if (i < take - 1) block.Inlines.Add(new LineBreak());
            }
        }
        Show();
        var stack = new StackPanel { Spacing = 4 };
        stack.Children.Add(block);
        var footer = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 14 };
        var stats = new TextBlock { FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center };
        stats.Inlines.Add(new Run { Text = "+" + added, Foreground = NexusPalette.DiffAdded });
        stats.Inlines.Add(new Run { Text = "  −" + removed, Foreground = NexusPalette.DiffRemoved });
        footer.Children.Add(stats);
        if (rows.Count > cap)
        {
            var more = Link($"Show all {rows.Count} lines");
            more.Click += (_, _) =>
            {
                all = !all;
                Show();
                more.Content = all ? "Show less" : $"Show all {rows.Count} lines";
            };
            footer.Children.Add(more);
        }
        var copy = Link("Copy diff");
        copy.Click += (_, _) => Copy(diff);
        footer.Children.Add(copy);
        stack.Children.Add(footer);
        return Panel(stack);
    }
}
