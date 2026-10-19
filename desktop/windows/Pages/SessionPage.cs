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
    private readonly ScrollViewer _chatScroll = new();
    private readonly Button _chatMore = new() { Content = "Load more turns" };
    private string _nextTurnCursor = "";
    private readonly StackPanel _activity = new() { Spacing = 8 };
    private readonly StackPanel _memoryTab = new() { Spacing = 8 };
    private readonly StackPanel _meta = new() { Spacing = 6, Visibility = Visibility.Collapsed };
    private readonly TextBox _railSearch = new() { PlaceholderText = "Search chats" };
    private readonly StackPanel _railList = new() { Spacing = 2 };
    private readonly Button _railMore = new() { Content = "Load more chats", Visibility = Visibility.Collapsed, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly List<RailEntry> _railEntries = new();
    private sealed record RailEntry(string Id, string Title, string Meta);
    private string _railCursor = "";
    private string _projectID = "";
    private bool _railLoading;
    private readonly TextBlock _notice = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.8 };
    private string _agent = "";
    private string _nativeID = "";
    private string _workspaceHint = "";
    private bool _sessionLoaded;
    private bool _legacy;
    private bool _hasCompleteVersion;
    private Button _teleport = null!;
    private Button _resume = null!;
    private Button _continueHere = null!;
    private readonly StackPanel _composer = new() { Spacing = 8, Visibility = Visibility.Collapsed };
    private readonly TextBox _prompt = new() { PlaceholderText = "Ask a follow-up in this session…", AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, MinHeight = 72, MaxHeight = 140 };
    private readonly Button _sendHere = new() { Content = "Send to agent", HorizontalAlignment = HorizontalAlignment.Right };
    private readonly TextBlock _composerStatus = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.75 };

    public SessionPage()
    {
        _chatMore.Click += async (_, _) => await LoadMoreTurnsAsync();
        _railMore.Click += async (_, _) => await LoadMoreRailAsync();
        _railSearch.TextChanged += (_, _) => RenderRail();
        var back = new Button { Content = "← Timeline" };
        back.Click += (_, _) => MainWindow.Current?.ShowTimeline();
        _continueHere = new Button { Content = "Continue here" };
        _continueHere.Click += (_, _) => { _composer.Visibility = Visibility.Visible; _prompt.Focus(FocusState.Programmatic); };
        _resume = new Button { Content = "Resume in agent · copy command" };
        _resume.Click += async (_, _) => await CopyResumeAsync();
        _teleport = new Button { Content = "Share chat + code" };
        _teleport.Click += async (_, _) => await SendTeleportAsync();
        _sendHere.Click += async (_, _) => await SendHereAsync();
        _composer.Children.Add(Line("Continue this conversation in Nexus", title: true));
        _composer.Children.Add(_prompt);
        _composer.Children.Add(_sendHere);
        _composer.Children.Add(_composerStatus);

        var tabs = new Pivot();
        _chatScroll.Content = _chat;
        tabs.Items.Add(new PivotItem { Header = "Chat", Content = _chatScroll });
        tabs.Items.Add(Tab("Activity", _activity));
        tabs.Items.Add(Tab("Memory", _memoryTab));

        var railScroll = new ScrollViewer { Content = _railList, VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
        var railGrid = new Grid { RowSpacing = 10 };
        railGrid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        railGrid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        railGrid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        Grid.SetRow(_railSearch, 0);
        Grid.SetRow(railScroll, 1);
        Grid.SetRow(_railMore, 2);
        railGrid.Children.Add(_railSearch);
        railGrid.Children.Add(railScroll);
        railGrid.Children.Add(_railMore);
        var rail = new Border
        {
            Padding = new Thickness(12, 12, 12, 10),
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            Background = Surface("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = Surface("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255)),
            Child = railGrid
        };

        var body = new Grid { ColumnSpacing = 14 };
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(300) });
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        Grid.SetColumn(rail, 0);
        Grid.SetColumn(tabs, 1);
        body.Children.Add(rail);
        body.Children.Add(tabs);

        var head = new StackPanel { Spacing = 8 };
        head.Children.Add(Row(back, _continueHere, _resume));
        head.Children.Add(Row(_teleport));
        head.Children.Add(_meta);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        Grid.SetRow(head, 1);
        Grid.SetRow(body, 2);
        root.Children.Add(_header);
        root.Children.Add(head);
        root.Children.Add(body);
        root.Children.Add(_composer);
        Grid.SetRow(_composer, 3);
        root.Children.Add(_notice);
        Grid.SetRow(_notice, 4);
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
        _workspaceHint = "";
        _composer.Visibility = Visibility.Collapsed;
        _composerStatus.Text = "";
        _prompt.Text = "";
        _agent = "";
        _projectID = "";
        _railCursor = "";
        _meta.Children.Clear();
        _meta.Visibility = Visibility.Collapsed;
        _railEntries.Clear();
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
        FillActivity(files, ops);
        FillMemoryTab(memories, summary);
        _meta.Children.Clear();
        _meta.Visibility = Visibility.Collapsed;
        if (session.Ok && session.Result is { } body)
        {
            var title = NxJson.Text(body, "title", "summary", "preview");
            _agent = NxJson.Text(body, "agent", "harness");
            _nativeID = NxJson.Text(body, "native_id");
            _workspaceHint = NxJson.Text(body, "workspace_root_hint");
            _projectID = NxJson.Text(body, "project_id", "project");
            _header.Text = Nonempty(title, "Captured " + Nonempty(_agent, "agent") + " session");
            var chips = Row();
            if (_agent.Length > 0) chips.Children.Add(Chip(_agent));
            var workspace = NxJson.Text(body, "workspace_root_hint");
            var projectLabel = ProjectLabels.ForSession(_projectID, workspace, projects.Result);
            if (projectLabel.Length > 0)
            {
                var projectChip = Chip(projectLabel);
                if (workspace.Length > 0) ToolTipService.SetToolTip(projectChip, workspace);
                chips.Children.Add(projectChip);
            }
            var last = Humanize(NxJson.Text(body, "last_active_at"));
            if (last.Length > 0) chips.Children.Add(Muted("Last activity " + last));
            if (chips.Children.Count > 0)
            {
                _meta.Children.Add(chips);
                _meta.Visibility = Visibility.Visible;
            }
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
        _ = LoadRailAsync();
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

    private object RailArgs(string cursor)
    {
        return _projectID.Length > 0
            ? new { project_id = _projectID, cursor, limit = 50 }
            : (object)new { cursor, limit = 50 };
    }

    private async Task LoadRailAsync()
    {
        if (_railLoading) return;
        _railLoading = true;
        _railEntries.Clear();
        _railCursor = "";
        _railList.Children.Clear();
        _railList.Children.Add(Muted("Loading chats…"));
        var response = await Call(NxMethods.TimelineList, RailArgs(""));
        _railLoading = false;
        _railList.Children.Clear();
        if (!response.Ok)
        {
            _railList.Children.Add(Muted(response.Error ?? "Could not load chats."));
            return;
        }
        _railCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        AppendRail(response);
        RenderRail();
    }

    private async Task LoadMoreRailAsync()
    {
        if (_railCursor.Length == 0 || _railLoading) return;
        _railLoading = true;
        _railMore.IsEnabled = false;
        var response = await Call(NxMethods.TimelineList, RailArgs(_railCursor));
        _railLoading = false;
        _railMore.IsEnabled = true;
        if (!response.Ok)
        {
            _notice.Text = response.Error ?? "Could not load more chats.";
            return;
        }
        _railCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        AppendRail(response);
        RenderRail();
    }

    private void AppendRail(NxResponse response)
    {
        foreach (var item in NxJson.Items(response.Result))
        {
            var id = NxJson.Text(item, "id", "session_id", "sessionId");
            if (id.Length == 0) continue;
            var title = NxJson.Text(item, "title", "summary", "preview", "kind");
            var agent = NxJson.Text(item, "agent", "harness");
            var meta = string.Join(" · ", new[]
            {
                agent,
                Humanize(NxJson.Text(item, "last_active_at", "updated_at", "captured_at"))
            }.Where(part => part.Length > 0));
            _railEntries.Add(new RailEntry(id, title.Length > 0 ? title : id, meta));
        }
    }

    private void RenderRail()
    {
        var query = _railSearch.Text.Trim();
        _railList.Children.Clear();
        var shown = 0;
        foreach (var entry in _railEntries)
        {
            if (query.Length > 0 &&
                entry.Title.IndexOf(query, StringComparison.OrdinalIgnoreCase) < 0 &&
                entry.Meta.IndexOf(query, StringComparison.OrdinalIgnoreCase) < 0) continue;
            _railList.Children.Add(RailItem(entry));
            shown++;
        }
        if (shown == 0 && _railEntries.Count > 0)
        {
            _railList.Children.Add(Muted("No chats match \u201c" + query + "\u201d"));
        }
        if (shown == 0 && _railEntries.Count == 0)
        {
            _railList.Children.Add(Muted("No captured chats in this project yet."));
        }
        _railMore.Visibility = _railCursor.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private Button RailItem(RailEntry entry)
    {
        var current = entry.Id == _id;
        var stack = new StackPanel { Spacing = 2 };
        stack.Children.Add(new TextBlock
        {
            Text = NxJson.Scrub(entry.Title),
            TextWrapping = TextWrapping.NoWrap,
            TextTrimming = TextTrimming.CharacterEllipsis,
            FontSize = 13.5,
            FontWeight = current ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal
        });
        if (entry.Meta.Length > 0)
        {
            stack.Children.Add(new TextBlock
            {
                Text = NxJson.Scrub(entry.Meta),
                TextWrapping = TextWrapping.NoWrap,
                TextTrimming = TextTrimming.CharacterEllipsis,
                FontSize = 11,
                Opacity = 0.6
            });
        }
        var item = new Button
        {
            Content = stack,
            HorizontalAlignment = HorizontalAlignment.Stretch,
            HorizontalContentAlignment = HorizontalAlignment.Left,
            Padding = new Thickness(10, 8, 10, 8),
            CornerRadius = new CornerRadius(8),
            BorderThickness = new Thickness(1),
            Background = current
                ? new SolidColorBrush(Windows.UI.Color.FromArgb(40, 255, 255, 255))
                : new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            BorderBrush = current
                ? new SolidColorBrush(Windows.UI.Color.FromArgb(70, 255, 255, 255))
                : new SolidColorBrush(Microsoft.UI.Colors.Transparent)
        };
        ToolTipService.SetToolTip(item, entry.Title);
        item.Click += (_, _) => { if (!current) MainWindow.Current?.OpenSession(entry.Id); };
        return item;
    }

    private static Brush Surface(string key, Windows.UI.Color fallback)
    {
        return Application.Current?.Resources.TryGetValue(key, out var value) == true && value is Brush brush
            ? brush
            : new SolidColorBrush(fallback);
    }

    private static string Humanize(string iso)
    {
        if (!DateTimeOffset.TryParse(iso, out var when)) return "";
        var local = when.ToLocalTime();
        var now = DateTimeOffset.Now;
        var span = now - local;
        if (span.TotalMinutes < 1) return "just now";
        if (span.TotalMinutes < 60) return $"{(int)span.TotalMinutes}m ago";
        if (span.TotalHours < 24 && local.Date == now.Date) return local.ToString("HH:mm");
        if (local.Year == now.Year) return local.ToString("MMM d");
        return local.ToString("MMM d, yyyy");
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
                IsTextSelectionEnabled = true,
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
                        IsTextSelectionEnabled = true,
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

    private void FillActivity(NxResponse files, NxResponse ops)
    {
        _activity.Children.Clear();
        FillFiles(files);
        FillCommands(ops);
    }

    private void FillFiles(NxResponse response)
    {
        var files = NxJson.Items(response.Result).ToList();
        _activity.Children.Add(SectionHeader(response.Ok ? "Files · " + files.Count : "Files"));
        if (!response.Ok)
        {
            _activity.Children.Add(Card(Line(response.Error ?? "Could not load this session's files.")));
            return;
        }
        if (files.Count == 0)
        {
            _activity.Children.Add(Card(Line("No files were captured for this session.")));
            return;
        }
        foreach (var item in files)
        {
            var path = NxJson.Text(item, "path", "file_path", "name");
            if (path.Length == 0) continue;
            var row = new StackPanel { Spacing = 6 };
            row.Children.Add(Monospace(path));
            var operations = StringArray(item, "operations");
            if (operations.Count > 0)
            {
                var chips = Row();
                foreach (var operation in operations) chips.Children.Add(Chip(operation));
                row.Children.Add(chips);
            }
            _activity.Children.Add(ListItem(row));
        }
    }

    private void FillCommands(NxResponse response)
    {
        var commands = NxJson.Items(response.Result).ToList();
        _activity.Children.Add(SectionHeader(response.Ok ? "Commands · " + commands.Count : "Commands"));
        if (!response.Ok)
        {
            _activity.Children.Add(Card(Line(response.Error ?? "Could not load this session's commands.")));
            return;
        }
        if (commands.Count == 0)
        {
            _activity.Children.Add(Card(Line("No commands were captured for this session.")));
            return;
        }
        foreach (var item in commands)
        {
            var command = NxJson.Text(item, "command", "command_line", "cmd", "text");
            if (command.Length == 0) continue;
            var card = new StackPanel { Spacing = 6 };
            card.Children.Add(Monospace(command));
            var meta = Row();
            var tool = NxJson.Text(item, "tool", "tool_name");
            if (tool.Length > 0) meta.Children.Add(Chip(tool));
            var directory = NxJson.Text(item, "directory");
            if (directory.Length > 0) meta.Children.Add(Muted(directory));
            if (meta.Children.Count > 0) card.Children.Add(meta);
            _activity.Children.Add(ListItem(card));
        }
    }

    private void FillMemoryTab(NxResponse memories, NxResponse summary)
    {
        _memoryTab.Children.Clear();
        _memoryTab.Children.Add(SectionHeader("Summary"));
        _memoryTab.Children.Add(OverviewCards(summary));
        var items = NxJson.Items(memories.Result).ToList();
        _memoryTab.Children.Add(SectionHeader(memories.Ok ? "Linked memories · " + items.Count : "Linked memories"));
        if (!memories.Ok)
        {
            _memoryTab.Children.Add(Card(Line(memories.Error ?? "Could not load session memory.")));
            return;
        }
        if (items.Count == 0)
        {
            _memoryTab.Children.Add(Card(Line("No memory is linked to this session yet. Project knowledge is on the Memory page.")));
            return;
        }
        foreach (var item in items)
        {
            var text = NxJson.Text(item, "content", "text", "title", "key");
            if (text.Length == 0) continue;
            var meta = Row();
            var scope = NxJson.Text(item, "scope", "level");
            if (scope.Length > 0) meta.Children.Add(Chip(scope));
            var status = NxJson.Text(item, "status");
            if (status.Length > 0) meta.Children.Add(Chip(NxJson.PublicStatus(status)));
            var key = NxJson.Text(item, "key");
            if (key.Length > 0) meta.Children.Add(Muted(key));
            _memoryTab.Children.Add(ListItem(Line(text), meta));
        }
    }

    private static UIElement OverviewCards(NxResponse response)
    {
        if (!response.Ok)
        {
            return Card(Line(response.Error ?? "Summary unavailable."));
        }
        var text = response.Result is { } body ? NxJson.Text(body, "summary", "text").Trim() : "";
        if (text.Length == 0)
        {
            return Card(Line("No summary has been captured for this session yet."));
        }
        var requestIndex = text.IndexOf("Request:", StringComparison.Ordinal);
        var responseIndex = text.IndexOf("Latest agent response:", StringComparison.Ordinal);
        if (requestIndex >= 0 && responseIndex > requestIndex)
        {
            var stack = new StackPanel { Spacing = 8 };
            var caption = text[..requestIndex].Trim();
            if (caption.Length > 0 && caption.StartsWith("Conversation overview", StringComparison.OrdinalIgnoreCase))
            {
                stack.Children.Add(Muted(caption));
            }
            var request = text[(requestIndex + "Request:".Length)..responseIndex].Trim();
            var latest = text[(responseIndex + "Latest agent response:".Length)..].Trim();
            if (request.Length > 0) stack.Children.Add(Card(Muted("Request"), FormattedMessage(request)));
            if (latest.Length > 0) stack.Children.Add(Card(Muted("Latest agent response"), FormattedMessage(latest)));
            return stack;
        }
        return Card(FormattedMessage(text));
    }

    private static List<string> StringArray(JsonElement item, string key)
    {
        var values = new List<string>();
        if (item.ValueKind == JsonValueKind.Object && item.TryGetProperty(key, out var array) && array.ValueKind == JsonValueKind.Array)
        {
            foreach (var element in array.EnumerateArray())
            {
                var text = element.ValueKind == JsonValueKind.String ? element.GetString() ?? "" : element.ToString();
                if (text.Length > 0) values.Add(text);
            }
        }
        return values;
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
        _teleport.IsEnabled = _sessionLoaded && !_legacy && _hasCompleteVersion && _workspaceHint.Length > 0 && !ShellState.Current.Offline;
        _resume.IsEnabled = _sessionLoaded && !_legacy && _nativeID.Length > 0 && _agent.Length > 0;
        _continueHere.IsEnabled = _sessionLoaded && !_legacy && _nativeID.Length > 0 && _workspaceHint.Length > 0 && SupportsInAppReply(_agent);
        ToolTipService.SetToolTip(_continueHere, _legacy ? "Archived snapshots are read-only" : _nativeID.Length == 0 ? "No native agent session was captured" : _workspaceHint.Length == 0 ? "This chat has no mapped workspace" : SupportsInAppReply(_agent) ? "Runs the installed agent invisibly in the selected workspace and shows its reply here" : "This agent has no supported headless reply mode yet");
        ToolTipService.SetToolTip(_resume, _legacy ? "Archived snapshots cannot resume natively" : _nativeID.Length == 0 ? "No native session ID was captured" : "Copies a command for an installed local agent; it does not open a terminal");
        ToolTipService.SetToolTip(_teleport, _legacy ? "Archived snapshots cannot be teleported" : !_hasCompleteVersion ? "A completed version is required" : _workspaceHint.Length == 0 ? "This chat has no mapped workspace for its code package" : null);
    }

    private static bool SupportsInAppReply(string agent) => agent.Trim().ToLowerInvariant() is "claude" or "claude-code" or "codex" or "gemini" or "opencode" or "kimi";

    private async Task SendHereAsync()
    {
        var prompt = _prompt.Text.Trim();
        if (prompt.Length == 0) { _composerStatus.Text = "Write a follow-up first."; return; }
        _sendHere.IsEnabled = false;
        _composerStatus.Text = "Waiting for " + _agent + "… no terminal window will open.";
        var response = await Call(NxMethods.ContinueHere, new { session_id = _id, native_id = _nativeID, agent = _agent, prompt, workspace_root = _workspaceHint });
        _sendHere.IsEnabled = true;
        if (!response.Ok || response.Result is not { } result)
        {
            _composerStatus.Text = response.Error ?? "The agent did not return a reply.";
            return;
        }
        var answer = NxJson.Text(result, "text");
        if (answer.Length == 0) { _composerStatus.Text = "The agent returned no readable reply."; return; }
        _chat.Children.Add(ChatMessage("user", prompt));
        _chat.Children.Add(ChatMessage("assistant", answer));
        _prompt.Text = "";
        _composerStatus.Text = "Reply received. Nexus will capture the native session on its next scan.";
        _chatScroll.ChangeView(null, _chatScroll.ScrollableHeight, null);
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
            Content = new StackPanel { Spacing = 10, Children = { new TextBlock { Text = "Send this chat and a verified copy of the mapped workspace (up to 128 MB). Known credential paths and generated dependencies are excluded, but secrets inside ordinary source files are not detected. Review your code before sending. The recipient restores to a new folder; existing files are never overwritten.", TextWrapping = TextWrapping.Wrap }, recipient } },
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
        _notice.Text = "Packaging code and sending teleport…";
        var response = await Call(NxMethods.TeleportSend, new { session_id = _id, to_user_id = recipient.Text.Trim(), include_code = true, workspace_root = _workspaceHint });
        _notice.Text = response.Ok && response.Result is { } sent
            ? "Teleport sent with " + NxJson.Text(sent, "code_files") + " code files."
            : response.Error ?? "Teleport could not be sent.";
    }
}
