using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using System.Text.Json;
using System.Text.RegularExpressions;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

public sealed class SessionPage : NexusPage
{
    private string _id = "";
    private readonly TextBlock _header = new() { TextWrapping = TextWrapping.Wrap, FontSize = 16 };
    private readonly StackPanel _chat = new() { Spacing = 18, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly Button _chatMore = new() { Content = "Load more turns" };
    private string _nextTurnCursor = "";
    private readonly StackPanel _files = new();
    private readonly StackPanel _commands = new();
    private readonly StackPanel _memory = new();
    private readonly StackPanel _summary = new();
    private readonly StackPanel _context = new() { Spacing = 8 };
    private readonly TextBlock _notice = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.8 };
    private string _agent = "";
    private string _nativeID = "";
    private bool _sessionLoaded;
    private bool _legacy;
    private bool _hasCompleteVersion;
    private Button _share = null!;
    private Button _teleport = null!;
    private Button _resume = null!;

    public SessionPage()
    {
        _chatMore.Click += async (_, _) => await LoadMoreTurnsAsync();
        var back = new Button { Content = "← Timeline" };
        back.Click += (_, _) => MainWindow.Current?.ShowTimeline();
        _resume = new Button { Content = "Copy native resume command" };
        _resume.Click += async (_, _) => await CopyResumeAsync();
        _share = new Button { Content = "Share" };
        _share.Click += async (_, _) => await ShareAsync();
        _teleport = new Button { Content = "Send teleport" };
        _teleport.Click += async (_, _) => await SendTeleportAsync();

        var tabs = new Pivot();
        tabs.Items.Add(Tab("Chat", _chat));
        tabs.Items.Add(Tab("Files", _files));
        tabs.Items.Add(Tab("Commands", _commands));
        tabs.Items.Add(Tab("Memory", _memory));
        tabs.Items.Add(Tab("Summary", _summary));

        var body = new Grid { ColumnSpacing = 16 };
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(280) });
        Grid.SetColumn(tabs, 0);
        var context = new ScrollViewer { Content = _context };
        Grid.SetColumn(context, 1);
        body.Children.Add(tabs);
        body.Children.Add(context);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        var actions = Row(back, _resume, _teleport, _share);
        Grid.SetRow(actions, 1);
        Grid.SetRow(body, 2);
        root.Children.Add(_header);
        root.Children.Add(actions);
        root.Children.Add(body);
        root.Children.Add(_notice);
        Grid.SetRow(_notice, 3);
        Content = root;
        Loaded += (_, _) => { ShellState.Changed += HandleShellChanged; UpdateActions(); };
        Unloaded += (_, _) => ShellState.Changed -= HandleShellChanged;
        UpdateActions();
    }

    protected override void OnEnter(object? parameter)
    {
        _id = parameter as string ?? "";
        _header.Text = "Loading session…";
        _notice.Text = "";
        _sessionLoaded = false;
        _legacy = false;
        _hasCompleteVersion = false;
        _nativeID = "";
        _agent = "";
        UpdateActions();
        _ = LoadAsync();
    }

    private static PivotItem Tab(string title, StackPanel body)
    {
        return new PivotItem
        {
            Header = title,
            Content = new ScrollViewer { Content = body }
        };
    }

    private async Task LoadAsync()
    {
        if (_id.Length == 0)
        {
            _header.Text = "Session";
            return;
        }
        var sessionTask = Call(NxMethods.SessionsGet, new { session_id = _id });
        var turnsTask = Call(NxMethods.SessionsTurns, new { session_id = _id, limit = 100 });
        var filesTask = Call(NxMethods.SessionsFiles, new { session_id = _id });
        var opsTask = Call(NxMethods.SessionsOperations, new { session_id = _id });
        var memoriesTask = Call(NxMethods.SessionsMemories, new { session_id = _id });
        var summaryTask = Call(NxMethods.SessionsSummary, new { session_id = _id });
        var versionsTask = Call(NxMethods.SessionsVersions, new { session_id = _id });
        var projectsTask = Call(NxMethods.ProjectsList);
        await Task.WhenAll(sessionTask, turnsTask, filesTask, opsTask, memoriesTask, summaryTask, versionsTask, projectsTask);
        var session = await sessionTask;
        var turns = await turnsTask;
        var files = await filesTask;
        var ops = await opsTask;
        var memories = await memoriesTask;
        var summary = await summaryTask;
        var versions = await versionsTask;
        var projects = await projectsTask;
        _nextTurnCursor = turns.Result is { } turnResult ? NxJson.Text(turnResult, "next_cursor") : "";
        FillLines(_files, files, "path", "file_path", "name");
        FillLines(_commands, ops, "command", "command_line", "cmd", "text");
        FillLines(_memory, memories, "content", "text", "title", "key");
        _summary.Children.Clear();
        _summary.Children.Add(Card(Line(summary.Ok && summary.Result is { } summaryBody
            ? Nonempty(NxJson.Text(summaryBody, "summary", "text"), "No summary has been captured for this session.")
            : summary.Error ?? "Summary unavailable.")));
        _context.Children.Clear();
        _context.Children.Add(Line("Session details", title: true));
        if (session.Ok && session.Result is { } body)
        {
            var title = NxJson.Text(body, "title", "summary", "preview");
            _agent = NxJson.Text(body, "agent", "harness");
            _nativeID = NxJson.Text(body, "native_id");
            _header.Text = Nonempty(title, "Captured " + Nonempty(_agent, "agent") + " session");
            AddDetail("Agent", _agent);
            AddDetail("Project", ProjectLabels.ForSession(NxJson.Text(body, "project_id", "project"), NxJson.Text(body, "workspace_root_hint"), projects.Result));
            AddDetail("Workspace", NxJson.Text(body, "workspace_root_hint"));
            AddDetail("Last activity", NxJson.Text(body, "last_active_at"));
            ToolTipService.SetToolTip(_header, "Session ID: " + _id);
            _legacy = NxJson.Text(body, "lineage_kind") == "legacy_snapshot";
            _hasCompleteVersion = NxJson.Items(versions.Result).Any(v => NxJson.Text(v, "state").Equals("complete", StringComparison.OrdinalIgnoreCase));
            _sessionLoaded = true;
            UpdateActions();
            if (_legacy)
            {
                _notice.Text = "Archived snapshot · read-only";
            }
        }
        else if (!session.Ok)
        {
            _header.Text = "Session unavailable";
            _notice.Text = session.Error ?? "Could not load this session.";
            UpdateActions();
        }
        FillChat(turns, false);
        if (_nextTurnCursor.Length > 0) _chat.Children.Add(_chatMore);
    }

    private async Task LoadMoreTurnsAsync()
    {
        if (_nextTurnCursor.Length == 0) return;
        _chatMore.IsEnabled = false;
        var response = await Call(NxMethods.SessionsTurns, new { session_id = _id, id = _id, limit = 100, cursor = _nextTurnCursor });
        _chat.Children.Remove(_chatMore);
        if (!response.Ok)
        {
            _chat.Children.Add(Line(response.Error ?? "Could not load more turns."));
        }
        else
        {
            FillChat(response, true);
            _nextTurnCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        }
        _chatMore.IsEnabled = true;
        if (_nextTurnCursor.Length > 0) _chat.Children.Add(_chatMore);
    }

    private void FillChat(NxResponse response, bool append)
    {
        if (!append) _chat.Children.Clear();
        if (!response.Ok)
        {
            _chat.Children.Add(Card(Line(response.Error ?? "Could not load this conversation.")));
            return;
        }
        var shown = 0;
        foreach (var item in NxJson.Items(response.Result))
        {
            var role = NxJson.Text(item, "role");
            if (role.Equals("system", StringComparison.OrdinalIgnoreCase) ||
                role.Equals("developer", StringComparison.OrdinalIgnoreCase)) continue;
            var text = ReadableTurn(NxJson.Text(item, "text_preview", "text", "content", "message"));
            if (text.Length == 0) continue;
            _chat.Children.Add(ChatMessage(role, text));
            shown++;
        }
        if (shown == 0 && !append) _chat.Children.Add(Card(Line("No readable chat turns have been captured yet.")));
    }

    private UIElement ChatMessage(string role, string content)
    {
        var fromUser = role.Equals("user", StringComparison.OrdinalIgnoreCase);
        var label = fromUser ? "YOU" : Nonempty(_agent, "AGENT").ToUpperInvariant();
        var message = new StackPanel { Spacing = 10 };
        message.Children.Add(new TextBlock
        {
            Text = label,
            FontSize = 11,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            Opacity = 0.72
        });
        message.Children.Add(FormattedMessage(content));
        var bubble = new Border
        {
            Child = message,
            Padding = new Thickness(18, 14, 18, 16),
            CornerRadius = new CornerRadius(14),
            MaxWidth = 820,
            HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Left,
            Background = new SolidColorBrush(fromUser
                ? Windows.UI.Color.FromArgb(255, 29, 69, 92)
                : Windows.UI.Color.FromArgb(255, 48, 48, 48)),
            BorderBrush = new SolidColorBrush(Windows.UI.Color.FromArgb(45, 255, 255, 255)),
            BorderThickness = new Thickness(1)
        };
        return bubble;
    }

    private static StackPanel FormattedMessage(string content)
    {
        var blocks = new StackPanel { Spacing = 7 };
        var lines = content.Replace("\r\n", "\n").Split('\n');
        var code = new List<string>();
        var inCode = false;
        foreach (var rawLine in lines)
        {
            var line = rawLine.TrimEnd();
            if (line.TrimStart().StartsWith("```", StringComparison.Ordinal))
            {
                if (inCode) AddCode();
                inCode = !inCode;
                continue;
            }
            if (inCode) { code.Add(line); continue; }
            if (line.Length == 0)
            {
                blocks.Children.Add(new Border { Height = 5 });
                continue;
            }
            var trimmed = line.TrimStart();
            var heading = Regex.Match(trimmed, "^#{1,6}\\s+(.+)$");
            var bullet = Regex.Match(trimmed, "^(?:[-*+]\\s+|\\d+[.)]\\s+)(.+)$");
            var quote = trimmed.StartsWith("> ", StringComparison.Ordinal);
            var display = heading.Success ? heading.Groups[1].Value : bullet.Success ? "•  " + bullet.Groups[1].Value : quote ? trimmed[2..] : line;
            var text = new TextBlock
            {
                TextWrapping = TextWrapping.Wrap,
                FontSize = heading.Success ? 17 : 14,
                FontWeight = heading.Success ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal,
                Opacity = quote ? 0.8 : 1,
                Margin = bullet.Success || quote ? new Thickness(12, 0, 0, 0) : new Thickness(0)
            };
            AddInlineFormatting(text, display);
            blocks.Children.Add(text);
        }
        if (inCode) AddCode();
        return blocks;

        void AddCode()
        {
            blocks.Children.Add(new Border
            {
                Background = new SolidColorBrush(Windows.UI.Color.FromArgb(100, 0, 0, 0)),
                CornerRadius = new CornerRadius(8),
                Padding = new Thickness(12),
                Child = new ScrollViewer
                {
                    HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
                    Content = new TextBlock
                    {
                        Text = string.Join("\n", code),
                        FontFamily = new FontFamily("Consolas"),
                        FontSize = 12.5,
                        TextWrapping = TextWrapping.NoWrap
                    }
                }
            });
            code.Clear();
        }
    }

    private static void AddInlineFormatting(TextBlock block, string value)
    {
        var pattern = new Regex(@"(\*\*(.+?)\*\*|`(.+?)`|\[([^\]]+)\]\([^)]+\))");
        var start = 0;
        foreach (Match match in pattern.Matches(value))
        {
            if (match.Index > start) block.Inlines.Add(new Run { Text = value[start..match.Index] });
            if (match.Groups[2].Success)
            {
                var bold = new Bold();
                bold.Inlines.Add(new Run { Text = match.Groups[2].Value });
                block.Inlines.Add(bold);
            }
            else block.Inlines.Add(new Run { Text = match.Groups[3].Success ? match.Groups[3].Value : match.Groups[4].Value });
            start = match.Index + match.Length;
        }
        if (start < value.Length) block.Inlines.Add(new Run { Text = value[start..] });
    }

    private static void FillLines(StackPanel panel, NxResponse response, params string[] keys) => FillLines(panel, response, false, keys);

    private static void FillLines(StackPanel panel, NxResponse response, bool append, params string[] keys)
    {
        if (!append) panel.Children.Clear();
        if (!response.Ok)
        {
            panel.Children.Add(Card(Line(response.Error ?? "Could not load this content.")));
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        if (items.Count == 0)
        {
            if (!append || panel.Children.Count == 0)
            {
                var message = keys.Contains("text_preview") ? "No readable chat turns have been captured yet." :
                    keys.Contains("path") ? "No files were captured for this session." :
                    keys.Contains("command") ? "No commands were captured for this session." :
                    "No memory is linked to this session. Project knowledge is on the Memory page.";
                panel.Children.Add(Card(Line(message)));
            }
            return;
        }
        var shown = 0;
        foreach (var item in items)
        {
            if (keys.Contains("text_preview") &&
                NxJson.Text(item, "role") is { } turnRole &&
                (turnRole.Equals("system", StringComparison.OrdinalIgnoreCase) ||
                 turnRole.Equals("developer", StringComparison.OrdinalIgnoreCase)))
            {
                continue;
            }
            var text = NxJson.Text(item, keys);
            if (keys.Contains("text_preview")) text = ReadableTurn(text);
            if (text.Length == 0) continue;
            if (keys.Contains("text_preview") && NxJson.Text(item, "role") is { Length: > 0 } role)
            {
                panel.Children.Add(Card(Line(role.Equals("user", StringComparison.OrdinalIgnoreCase) ? "You" : "Agent", title: true), Line(text)));
                shown++;
                continue;
            }
            var detail = keys.Contains("path") ? JoinArray(item, "operations") :
                keys.Contains("command") ? NxJson.Text(item, "tool", "tool_name", "directory") :
                NxJson.Text(item, "scope", "level");
            panel.Children.Add(string.IsNullOrWhiteSpace(detail) ? Card(Line(text)) : Card(Line(text, title: true), Line(detail)));
            shown++;
        }
        if (shown == 0 && !append) panel.Children.Add(Card(Line("No readable chat turns have been captured yet.")));
    }

    private static string ReadableTurn(string raw)
    {
        raw = raw.Trim();
        if (raw.StartsWith("{", StringComparison.Ordinal))
        {
            try
            {
                using var doc = JsonDocument.Parse(raw);
                raw = ExtractText(doc.RootElement);
                if (raw.Length == 0) return "";
            }
            catch (JsonException) { }
        }
        var query = Regex.Match(raw, "<user_query>(.*?)</user_query>", RegexOptions.Singleline | RegexOptions.IgnoreCase);
        if (query.Success) raw = query.Groups[1].Value;
        raw = Regex.Replace(raw, "<timestamp>.*?</timestamp>", "", RegexOptions.Singleline | RegexOptions.IgnoreCase).Trim();
        if (raw.StartsWith("# AGENTS.md instructions", StringComparison.OrdinalIgnoreCase) ||
            raw.StartsWith("<environment_context>", StringComparison.OrdinalIgnoreCase) ||
            raw.StartsWith("<external_codex_apps_open_page>", StringComparison.OrdinalIgnoreCase) ||
            raw.StartsWith("<INSTRUCTIONS>", StringComparison.OrdinalIgnoreCase)) return "";
        return raw;
    }

    private void UpdateActions()
    {
        _share.IsEnabled = _sessionLoaded && !_legacy && !ShellState.Current.Offline;
        _teleport.IsEnabled = _sessionLoaded && !_legacy && _hasCompleteVersion && !ShellState.Current.Offline;
        _resume.IsEnabled = _sessionLoaded && !_legacy && _nativeID.Length > 0 && _agent.Length > 0;
        ToolTipService.SetToolTip(_resume, _legacy ? "Archived snapshots cannot resume natively" : _nativeID.Length == 0 ? "No native session ID was captured" : "Copies a command for an installed local agent; it does not open a terminal");
        ToolTipService.SetToolTip(_teleport, _legacy ? "Archived snapshots cannot be teleported" : !_hasCompleteVersion ? "A completed version is required" : null);
    }

    private void HandleShellChanged(object? sender, EventArgs e) => UpdateActions();

    private static string ExtractText(JsonElement value)
    {
        if (value.ValueKind == JsonValueKind.String) return value.GetString() ?? "";
        if (value.ValueKind == JsonValueKind.Array)
            return string.Join("\n", value.EnumerateArray().Select(ExtractText).Where(v => v.Length > 0));
        if (value.ValueKind != JsonValueKind.Object) return "";
        foreach (var key in new[] { "text", "content", "message", "user_query", "prompt" })
        {
            if (value.TryGetProperty(key, out var part))
            {
                var text = ExtractText(part);
                if (text.Length > 0) return text;
            }
        }
        return "";
    }

    private static string JoinArray(JsonElement item, string key)
    {
        if (item.ValueKind != JsonValueKind.Object || !item.TryGetProperty(key, out var values) || values.ValueKind != JsonValueKind.Array) return "";
        return string.Join(" · ", values.EnumerateArray().Select(v => v.ToString()).Where(v => v.Length > 0));
    }

    private static string Nonempty(string value, string fallback) => string.IsNullOrWhiteSpace(value) ? fallback : value;

    private void AddDetail(string label, string value)
    {
        if (!string.IsNullOrWhiteSpace(value)) _context.Children.Add(Card(Line(label, title: true), Line(value)));
    }

    private async Task CopyResumeAsync()
    {
        var response = await Call(NxMethods.ContinueStart, new { session_id = _id, mode = "open_in_agent", resume = "native", agent = _agent, native_id = _nativeID });
        if (!response.Ok || response.Result is not { } result || !result.TryGetProperty("command", out var command) || command.ValueKind != JsonValueKind.Array)
        {
            _notice.Text = response.Error ?? "A native resume command is not available for this session.";
            return;
        }
        var text = string.Join(" ", command.EnumerateArray().Select(v => QuoteArgument(v.ToString())));
        var data = new DataPackage();
        data.SetText(text);
        Clipboard.SetContent(data);
        _notice.Text = "Native resume command copied. The agent CLI and original local session must be available; Nexus will not open a terminal window.";
    }

    private static string QuoteArgument(string value) => value.Any(char.IsWhiteSpace) ? "\"" + value.Replace("\"", "\\\"") + "\"" : value;

    private async Task ShareAsync()
    {
        var recipient = new TextBox { PlaceholderText = "Recipient user ID" };
        var dialog = new ContentDialog { Title = "Share session", Content = recipient, PrimaryButtonText = "Share", CloseButtonText = "Cancel", XamlRoot = XamlRoot };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return;
        if (string.IsNullOrWhiteSpace(recipient.Text)) { _notice.Text = "Enter a recipient user ID."; return; }
        var response = await Call(NxMethods.SessionsShare, new { session_id = _id, user_id = recipient.Text.Trim() });
        _notice.Text = response.Ok ? "Session shared with " + recipient.Text.Trim() + "." : response.Error ?? "Sharing failed.";
    }

    private async Task SendTeleportAsync()
    {
        if (_id.Length == 0)
        {
            return;
        }
        var recipient = new TextBox { PlaceholderText = "Recipient user ID" };
        var dialog = new ContentDialog
        {
            Title = "Send a teleport",
            Content = new StackPanel { Spacing = 10, Children = { new TextBlock { Text = "The recipient receives the latest completed, redacted session version." }, recipient } },
            PrimaryButtonText = "Send",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary,
            XamlRoot = XamlRoot
        };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary)
        {
            return;
        }
        if (string.IsNullOrWhiteSpace(recipient.Text))
        {
            _notice.Text = "A recipient user ID is required.";
            return;
        }
        var response = await Call(NxMethods.TeleportSend, new { session_id = _id, to_user_id = recipient.Text.Trim() });
        _notice.Text = response.Ok ? "Teleport sent." : response.Error ?? "Teleport could not be sent.";
    }
}
