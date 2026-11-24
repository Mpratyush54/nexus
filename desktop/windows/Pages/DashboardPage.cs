using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class DashboardPage : NexusPage
{
    private readonly TextBlock _title = new() { Text = "Home", FontSize = 20, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap };
    // Chat-first start, in the style of T3 Code: type what you want, pick an
    // agent, and the chat opens with the message already sent.
    private readonly TextBox _startPrompt = new()
    {
        AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, MinHeight = 84, MaxHeight = 220,
        FontSize = 14, PlaceholderText = "What should we work on?"
    };
    private readonly ComboBox _startAgent = new() { MinWidth = 140 };
    private readonly Button _startSend = new() { Content = new FontIcon { Glyph = "\uE74A", FontSize = 14 }, MinWidth = 40, MinHeight = 34 };
    private readonly TextBlock _startStatus = new() { FontSize = 12, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
    private static string _lastStartAgent = AppPrefs.Data.LastStartAgent is { Length: > 0 } saved ? saved : "claude";
    private readonly StackPanel _metrics = new() { Spacing = 8 };
    private readonly StackPanel _capture = new() { Spacing = 10 };
    private readonly StackPanel _recent = new() { Spacing = 1 };
    private readonly ActivityCalendarView _calendar = new();
    private readonly TextBlock _notice = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.7 };
    private readonly List<string> _approved = new();
    private readonly List<string> _suggested = new();
    private int _version;

    public DashboardPage()
    {
        var refresh = new Button { Content = "Refresh" };
        refresh.Click += async (_, _) => await LoadAsync();
        var chats = new Button { Content = "Browse all chats" };
        chats.Click += (_, _) => MainWindow.Current?.ShowTimeline();
        var content = new StackPanel { Spacing = 22, MaxWidth = 860, HorizontalAlignment = HorizontalAlignment.Stretch };
        content.Children.Add(_title);
        content.Children.Add(BuildStartComposer());
        content.Children.Add(_notice);
        // The sidebar already lists every chat; Home stays a place to start.
        _ = chats; _ = refresh;
        content.Children.Add(Card(_capture));
        // Numbers and the calendar are secondary; keep them out of the way.
        var activity = new StackPanel { Spacing = 14 };
        activity.Children.Add(_metrics);
        activity.Children.Add(_calendar);
        content.Children.Add(new Expander
        {
            Header = new TextBlock { Text = "Activity", FontSize = 13, Opacity = 0.75 },
            Content = activity, IsExpanded = false,
            HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch
        });
        Content = new ScrollViewer
        {
            Content = new Border { Padding = new Thickness(32, 28, 32, 32), Child = content },
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled
        };
        WatchShellProject(() => LoadAsync());
        void FilterChanged(object? _, EventArgs __) { ApplyHeader(); _ = LoadActivityAsync(_version, ShellProjectId(), null); }
        Loaded += (_, _) => { ShellState.ChatFilterChanged += FilterChanged; ApplyHeader(); };
        Unloaded += (_, _) => ShellState.ChatFilterChanged -= FilterChanged;
    }

    // The header follows the chat list's project filter. With "All projects"
    // it says so; new chats still start in the shell project, named in the hint.
    private void ApplyHeader()
    {
        var label = ShellProjectLabel();
        var all = ShellState.Current.ChatFilter.Length == 0;
        _title.Text = all ? "All projects" : label.Length > 0 ? label : "Welcome to Nexus";
        _startPrompt.PlaceholderText = label.Length == 0
            ? "Choose a project in the sidebar, then tell an agent what to do."
            : all ? "What should we work on? New chats start in " + label + "."
            : "What should we work on in " + label + "?";
    }

    protected override void OnEnter(object? parameter) => _ = LoadAsync();

    private UIElement BuildStartComposer()
    {
        foreach (var (name, value) in new[] { ("Claude Code", "claude"), ("Codex", "codex"), ("Cursor", "cursor"), ("OpenCode", "opencode"), ("Gemini", "gemini") })
            _startAgent.Items.Add(new ComboBoxItem { Content = name, Tag = value });
        _startAgent.SelectedItem = _startAgent.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (string)i.Tag == _lastStartAgent) ?? _startAgent.Items[0];
        ToolTipService.SetToolTip(_startAgent, "Agent that runs this chat on your computer");
        // The choice shown here is what the next new chat uses (also from the sidebar's New chat).
        _startAgent.SelectionChanged += (_, _) =>
        {
            if ((_startAgent.SelectedItem as ComboBoxItem)?.Tag is string chosen && chosen != _lastStartAgent) RememberAgent(chosen);
        };
        ToolTipService.SetToolTip(_startSend, "Start chat · Enter (Shift+Enter for a new line)");
        _startSend.Click += async (_, _) => await StartChatAsync();
        _startPrompt.PreviewKeyDown += async (_, e) =>
        {
            if (e.Key != Windows.System.VirtualKey.Enter) return;
            var shift = Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(Windows.System.VirtualKey.Shift)
                .HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);
            if (shift) return;
            e.Handled = true;
            await StartChatAsync();
        };
        var bar = new Grid { ColumnSpacing = 8 };
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        bar.Children.Add(_startAgent);
        Grid.SetColumn(_startStatus, 1);
        _startStatus.VerticalAlignment = VerticalAlignment.Center;
        bar.Children.Add(_startStatus);
        Grid.SetColumn(_startSend, 2);
        bar.Children.Add(_startSend);
        _startAgent.Background = new Microsoft.UI.Xaml.Media.SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _startAgent.BorderThickness = new Thickness(0);
        _startAgent.FontSize = 12.5;
        _startPrompt.Background = new Microsoft.UI.Xaml.Media.SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _startPrompt.BorderThickness = new Thickness(0);
        ChatTitles.Bare(_startPrompt);
        _startPrompt.Padding = new Thickness(4, 2, 4, 2);
        _startSend.Background = new Microsoft.UI.Xaml.Media.SolidColorBrush(Windows.UI.Color.FromArgb(255, 0x33, 0x33, 0x33)); // T3 round grey send
        _startSend.Foreground = new Microsoft.UI.Xaml.Media.SolidColorBrush(Microsoft.UI.Colors.White);
        _startSend.BorderThickness = new Thickness(0);
        _startSend.MinWidth = 32; _startSend.MinHeight = 32; _startSend.Padding = new Thickness(7); _startSend.CornerRadius = new CornerRadius(16);
        var stack = new StackPanel { Spacing = 8 };
        stack.Children.Add(_startPrompt);
        stack.Children.Add(bar);
        return new Border
        {
            Child = stack, Padding = new Thickness(12, 12, 10, 10), CornerRadius = new CornerRadius(16), BorderThickness = new Thickness(1),
            BorderBrush = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["NexusHairlineBrush"],
            Background = (Microsoft.UI.Xaml.Media.Brush)Application.Current.Resources["NexusSurfaceBrush"]
        };
    }

    private static void RememberAgent(string agent)
    {
        _lastStartAgent = agent;
        AppPrefs.Data.LastStartAgent = agent;
        AppPrefs.Save();
    }

    /// <summary>New chat: a draft here, created only when the first message is sent.</summary>
    internal void FocusNewChat()
    {
        _startPrompt.Focus(FocusState.Programmatic);
        if (_startPrompt.XamlRoot is null)
            _startPrompt.Loaded += FocusOnce;
        void FocusOnce(object sender, RoutedEventArgs e)
        {
            _startPrompt.Loaded -= FocusOnce;
            _startPrompt.Focus(FocusState.Programmatic);
        }
    }

    private void StartStatus(string text)
    {
        _startStatus.Text = text;
        _startStatus.Visibility = text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private async Task StartChatAsync()
    {
        var prompt = _startPrompt.Text.Trim();
        if (prompt.Length == 0 || !_startSend.IsEnabled) return;
        var project = ShellProjectId();
        if (project.Length == 0) { StartStatus("Choose a project in the sidebar first."); return; }
        var agent = (_startAgent.SelectedItem as ComboBoxItem)?.Tag as string ?? "claude";
        RememberAgent(agent);
        _startSend.IsEnabled = false;
        StartStatus("Starting chat…");
        try
        {
            var workspace = await ProjectFolders.ResolveAsync(Core, project, ShellProjectLabel());
            await UiThread.Resume(DispatcherQueue);
            var firstLine = prompt.Split('\n', 2)[0].Trim();
            var title = firstLine.Length > 72 ? firstLine[..69] + "…" : firstLine;
            var created = await Core.CallAsync(NxMethods.SessionsCreate, new
            {
                project_id = project, harness = agent, native_id = "nexus-" + Guid.NewGuid().ToString("N"),
                title, visibility = "private", workspace_root_hint = workspace
            });
            await UiThread.Resume(DispatcherQueue);
            var id = created is { Ok: true, Result: { } body } ? NxJson.Text(body, "id", "session_id") : "";
            if (id.Length == 0) { StartStatus(created.Error ?? "Could not start a chat."); return; }
            _startPrompt.Text = "";
            StartStatus("");
            SessionPage.PendingStart = (id, prompt);
            MainWindow.Current?.OpenSession(id);
            MainWindow.Current?.RefreshChats();
        }
        finally { _startSend.IsEnabled = true; }
    }

    private async Task LoadAsync()
    {
        var version = ++_version;
        var project = ShellProjectId();
        ApplyHeader();
        _notice.Text = "Updating your overview…";
        var statusTask = Core.CallAsync(NxMethods.StatusGet);
        var timelineTask = project.Length > 0 ? Core.CallAsync(NxMethods.TimelineList, new { project_id = project, limit = 6 }) : null;
        var dashboardTask = project.Length > 0 ? Core.CallAsync(NxMethods.DashboardGet, new { project_id = project, utc_offset_minutes = (int)TimeZoneInfo.Local.GetUtcOffset(DateTime.Now).TotalMinutes }) : null;
        var status = await statusTask;
        await UiThread.Resume(DispatcherQueue);
        if (version != _version) return;
        RenderCapture(status);
        var timeline = timelineTask is null ? null : await timelineTask;
        var dashboard = dashboardTask is null ? null : await dashboardTask;
        await UiThread.Resume(DispatcherQueue);
        if (version != _version) return;
        _notice.Text = project.Length == 0 ? "Choose a project in the sidebar to see its chats and memories." : "";
        _metrics.Children.Clear();
        if (dashboard is not null) NoteMemoryCount(project, dashboard);
        _ = LoadActivityAsync(version, project, dashboard);
        if (dashboard is { Ok: true, Result: { } data } && data.TryGetProperty("metrics", out var metrics))
        {
            var grid = new Grid { ColumnSpacing = 12 };
            for (var i = 0; i < 3; i++) grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            var stats = new[] { ("memories", "Memories"), ("proposed", "To review"), ("online", "Online devices") };
            for (var i = 0; i < stats.Length; i++)
            {
                var tones = new[] { NexusPalette.Accent, NexusPalette.Amber, NexusPalette.Teal };
                var metric = Card(new TextBlock { Text = NxJson.Text(metrics, stats[i].Item1), Foreground = tones[i], FontSize = 30, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold }, Muted(stats[i].Item2));
                Grid.SetColumn(metric, i);
                grid.Children.Add(metric);
            }
            _metrics.Children.Add(grid);
        }
        else if (dashboard is { Ok: false }) _metrics.Children.Add(Muted("Project metrics are unavailable. " + dashboard.Error));

        _recent.Children.Clear();
        if (timeline is { Ok: false }) { _recent.Children.Add(Muted(timeline.Error ?? "Chats unavailable.")); return; }
        foreach (var item in NxJson.Items(timeline?.Result))
        {
            if (SessionLineage.IsSubagentSession(item)) continue;
            var id = NxJson.Text(item, "session_id", "id");
            var agent = NxJson.Text(item, "harness", "agent");
            var summary = ChatTitles.Clean(NxJson.Text(item, "summary", "preview"));
            var title = ChatTitles.Title(NxJson.Text(item, "title"), summary, agent);
            var date = DateTimeOffset.TryParse(NxJson.Text(item, "last_active_at"), out var when) ? when.ToLocalTime().ToString("MMM d · h:mm tt") : "";
            var text = new StackPanel { Spacing = 3 };
            text.Children.Add(new TextBlock { Text = NxJson.Scrub(title), FontSize = 13.5, TextTrimming = TextTrimming.CharacterEllipsis });
            text.Children.Add(new TextBlock { Text = ChatTitles.AgentName(agent) + (date.Length > 0 ? " · " + date : ""), FontSize = 11.5, Opacity = 0.45 });
            var open = new Button
            {
                Content = text, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
                Padding = new Thickness(10, 7, 10, 7), CornerRadius = new CornerRadius(8),
                Background = new Microsoft.UI.Xaml.Media.SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0)
            };
            open.Click += (_, _) => MainWindow.Current?.OpenSession(id);
            _recent.Children.Add(open);
        }
        if (_recent.Children.Count == 0) _recent.Children.Add(Muted("No chats in this project yet. Type above to start one, or set up capture below to bring in your agents' history."));
    }

    // Agent activity follows the chat list's project filter. With "All
    // projects" it adds up every project the person can see: the shell
    // project alone (often an old one restored from the last session) left
    // the calendar empty while the work happened in other projects.
    private static readonly Dictionary<string, (ActivitySummary? Summary, string? Error, DateTime At)> s_activity = new(StringComparer.Ordinal);
    private static readonly TimeSpan ActivityFresh = TimeSpan.FromMinutes(5);

    private async Task LoadActivityAsync(int version, string shellProject, NxResponse? shellDashboard)
    {
        var filter = ShellState.Current.ChatFilter;
        var all = filter.Length == 0;
        var scope = all ? "all your projects" : ShellProjectLabel() is { Length: > 0 } label ? label : "this project";
        var key = all ? "*" : filter;
        if (s_activity.TryGetValue(key, out var cached) && DateTime.UtcNow - cached.At < ActivityFresh)
        {
            _calendar.Render(cached.Summary, scope, cached.Error);
            return;
        }
        var offset = (int)TimeZoneInfo.Local.GetUtcOffset(DateTime.Now).TotalMinutes;
        ActivitySummary? summary;
        string? error = null;
        var watch = System.Diagnostics.Stopwatch.StartNew();
        if (!all)
        {
            var response = filter == shellProject && shellDashboard is not null
                ? shellDashboard
                : await Core.CallAsync(NxMethods.DashboardGet, new { project_id = filter, utc_offset_minutes = offset });
            summary = response.Ok ? ActivitySummaries.Parse(response.Result) : null;
            if (!response.Ok) error = response.Error;
        }
        else
        {
            _calendar.ShowLoading(scope);
            var projects = await Core.CallAsync(NxMethods.ProjectsList);
            var ids = NxJson.Items(projects.Result).Select(p => NxJson.Text(p, "id", "project_id")).Where(ProjectScope.LooksLikeUuid).Distinct().ToList();
            if (!projects.Ok || ids.Count == 0)
            {
                summary = null;
                error = projects.Ok ? "No projects yet." : projects.Error;
            }
            else
            {
                var calls = ids.Select(id => id == shellProject && shellDashboard is { Ok: true }
                    ? Task.FromResult(shellDashboard)
                    : Core.CallAsync(NxMethods.DashboardGet, new { project_id = id, utc_offset_minutes = offset })).ToList();
                var answers = await Task.WhenAll(calls);
                for (var i = 0; i < ids.Count; i++) NoteMemoryCount(ids[i], answers[i]);
                error = answers.FirstOrDefault(a => !a.Ok)?.Error;
                summary = answers.Any(a => a.Ok) ? ActivitySummaries.Merge(answers.Select(a => a.Ok ? ActivitySummaries.Parse(a.Result) : null)) : null;
            }
        }
        System.Diagnostics.Trace.WriteLine($"[perf] activity ({key}): {watch.ElapsedMilliseconds} ms");
        await UiThread.Resume(DispatcherQueue);
        if (version != _version) return;
        // The project choice moved on while this loaded (e.g. restored at
        // startup): load what is chosen now instead of leaving "Loading…".
        if (ShellState.Current.ChatFilter != filter) { _ = LoadActivityAsync(version, ShellProjectId(), null); return; }
        if (summary is not null || NxErrors.IsSignedOut(error)) s_activity[key] = (summary, error, DateTime.UtcNow);
        _calendar.Render(summary, scope, error);
    }

    // The project switcher shows memory counts for projects already loaded.
    private static void NoteMemoryCount(string projectId, NxResponse dashboard)
    {
        if (dashboard is { Ok: true, Result: { } data } && data.TryGetProperty("metrics", out var metrics) &&
            long.TryParse(NxJson.Text(metrics, "memories"), out var memories))
            ProjectStats.SetMemories(projectId, memories);
    }

    private void RenderCapture(NxResponse status)
    {
        _capture.Children.Clear();
        _approved.Clear(); _suggested.Clear();
        var capture = status.Result is { } state && state.TryGetProperty("capture", out var c) ? c : default;
        SettingsPage.ReadPaths(capture, "approved_workspaces", _approved);
        SettingsPage.ReadPaths(capture, "suggested_workspaces", _suggested);
        CaptureSetup.ReadDiscovered(capture);
        var setup = _approved.Count == 0;
        _capture.Children.Add(Line(setup ? "Set up Nexus" : "Workspace capture", true));
        _capture.Children.Add(Line(setup
            ? "Choose your workspaces → review what is captured → allow monitoring."
            : $"{_approved.Count} approved workspace{(_approved.Count == 1 ? "" : "s")} · {(NxJson.Text(capture, "paused") == "true" ? "Paused" : "Enabled")}"));
        _capture.Children.Add(Muted(setup ? "Your existing cloud history stays available. Local monitoring starts after you approve specific folders." : "Review monitored folders, agent sources, and sync health in Settings."));
        var configure = new Button { Content = setup ? "Choose what to capture" : "Manage capture" };
        configure.Click += async (_, _) =>
        {
            if (!setup) { MainWindow.Current?.ShowSettings(); return; }
            var result = await CaptureSetup.ShowAsync(this, _approved, _suggested);
            if (result is null) return;
            if (!result.Ok) { _notice.Text = result.Error ?? "Could not save monitoring choices."; return; }
            await LoadAsync();
        };
        _capture.Children.Add(configure);
    }
}
