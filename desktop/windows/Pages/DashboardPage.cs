using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Home: one job, start a chat. The composer is the page's single primary
/// action; activity sits below it, open, and setup appears only until
/// capture is configured.
/// </summary>
public sealed class DashboardPage : NexusPage
{
    private readonly TextBlock _title = NexusTokens.PageTitle("Home");
    private readonly TextBlock _subtitle = NexusTokens.Caption("");
    // Chat-first start, in the style of T3 Code: type what you want, pick an
    // agent, and the chat opens with the message already sent.
    private readonly TextBox _startPrompt = new()
    {
        AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, MinHeight = 84, MaxHeight = 220,
        FontSize = 14, PlaceholderText = "Ask an agent to build, fix, or explain something…"
    };
    private readonly ComboBox _startAgent = new() { MinWidth = 132 };
    // Where the chat starts. With "All projects" it is a picker; inside one
    // project it just names it.
    private readonly DropDownButton _startProject = new();
    private readonly TextBlock _startProjectText = new() { FontSize = 12.5, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 200, VerticalAlignment = VerticalAlignment.Center };
    private readonly Button _startSend = new() { Content = new FontIcon { Glyph = "", FontSize = 14 }, MinWidth = 40, MinHeight = 34 };
    private readonly TextBlock _startStatus = new() { FontSize = 12, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
    private static string _lastStartAgent = AppPrefs.Data.LastStartAgent is { Length: > 0 } saved ? saved : "claude";
    private readonly Grid _metrics = new() { ColumnSpacing = 12 };
    private readonly StackPanel _capture = new() { Spacing = 8 };
    private readonly Border _captureCard;
    private readonly ActivityCalendarView _calendar = new();
    private readonly TextBlock _notice = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.75, Visibility = Visibility.Collapsed };
    // Signed out: one clear way back in, above everything else.
    private readonly ContentControl _signedOut = new() { HorizontalContentAlignment = HorizontalAlignment.Stretch, Visibility = Visibility.Collapsed, IsTabStop = false };
    private readonly List<string> _approved = new();
    private readonly List<string> _suggested = new();
    private int _version;
    private UIElement _composerFrame = null!;
    private UIElement _activitySection = null!;

    public DashboardPage()
    {
        var content = new StackPanel { Spacing = NexusTokens.Space6, MaxWidth = NexusTokens.ContentMaxWidth, HorizontalAlignment = HorizontalAlignment.Stretch };
        var head = new StackPanel { Spacing = NexusTokens.Space1 };
        head.Children.Add(_title);
        head.Children.Add(_subtitle);
        content.Children.Add(head);
        content.Children.Add(_signedOut);
        _composerFrame = BuildStartComposer();
        content.Children.Add(_composerFrame);
        content.Children.Add(_notice);
        _captureCard = Card(_capture);
        _captureCard.MinHeight = 0;
        content.Children.Add(_captureCard);

        // Activity is open by default: collapsed, it looked like nothing was there.
        var activity = new StackPanel { Spacing = NexusTokens.Space3 };
        activity.Children.Add(NexusTokens.SectionTitle("Activity"));
        activity.Children.Add(_metrics);
        activity.Children.Add(_calendar);
        content.Children.Add(activity);
        _activitySection = activity;
        for (var i = 0; i < 3; i++) _metrics.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        ShowMetricsLoading();

        Content = new ScrollViewer
        {
            Content = new Border { Padding = NexusTokens.PagePadding, Child = content },
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled
        };
        WatchShellProject(() => LoadAsync());
        void FilterChanged(object? _, EventArgs __) { ApplyHeader(); _ = LoadActivityAsync(_version, ShellProjectId(), null); }
        void ShellChanged(object? _, EventArgs __) => DispatcherQueue.TryEnqueue(() => { ApplyHeader(); ApplyOffline(); });
        Loaded += (_, _) => { ShellState.ChatFilterChanged += FilterChanged; ShellState.Changed += ShellChanged; ApplyHeader(); ApplyOffline(); };
        Unloaded += (_, _) => { ShellState.ChatFilterChanged -= FilterChanged; ShellState.Changed -= ShellChanged; };
    }

    // The header follows the sidebar's project choice. With "All projects"
    // the composer's project chip says where a new chat starts.
    private void ApplyHeader()
    {
        var label = ShellProjectLabel();
        var all = ShellState.Current.ChatFilter.Length == 0;
        _title.Text = all ? "All projects" : label.Length > 0 ? label : "Welcome to Nexus";
        _subtitle.Text = all ? "Start a chat, or pick up a recent one from the sidebar." : "Start a chat in this project, or pick up a recent one from the sidebar.";
        _startProjectText.Text = label.Length > 0 ? label : "Choose a project";
        // Inside one project the title already names it; the chip is only
        // needed to choose where a chat starts from "All projects".
        _startProject.Visibility = all || label.Length == 0 ? Visibility.Visible : Visibility.Collapsed;
        ToolTipService.SetToolTip(_startProject, all ? "Project this chat starts in" : "This chat starts in " + label);
        AutomationProperties.SetName(_startProject, "Start in project: " + _startProjectText.Text);
    }

    protected override void OnEnter(object? parameter) => _ = LoadAsync();

    private UIElement BuildStartComposer()
    {
        FillStartAgents();
        AutomationProperties.SetName(_startAgent, "Agent");
        // The choice shown here is what the next new chat uses (also from the sidebar's New chat).
        _startAgent.SelectionChanged += (_, _) =>
        {
            if (_fillingAgents) return;
            if ((_startAgent.SelectedItem as ComboBoxItem)?.Tag is string chosen && chosen != _lastStartAgent) RememberAgent(chosen);
        };
        // Only agents that can run here are listed; the list follows installs
        // and sign-ins when the window regains focus.
        Loaded += (_, _) => { AgentAvailabilityService.Changed += AgentsChanged; AgentAvailabilityService.Refresh(); };
        Unloaded += (_, _) => AgentAvailabilityService.Changed -= AgentsChanged;
        AutomationProperties.SetName(_startPrompt, "Message for a new chat");
        AutomationProperties.SetName(_startSend, "Start chat");
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
        _startPrompt.TextChanged += (_, _) => StyleSend();

        // Project chip.
        var chip = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        chip.Children.Add(new FontIcon { Glyph = "", FontSize = 12, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center });
        chip.Children.Add(_startProjectText);
        _startProject.Content = chip;
        _startProject.Padding = new Thickness(8, 4, 8, 5);
        _startProject.MinHeight = 30;
        _startProject.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _startProject.BorderThickness = new Thickness(0);
        var projectMenu = new MenuFlyout { Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedLeft };
        projectMenu.Opening += (_, _) =>
        {
            projectMenu.Items.Clear();
            var current = ShellProjectId();
            foreach (var (id, name) in (MainWindow.Current?.Projects ?? []).OrderBy(p => p.Name, StringComparer.OrdinalIgnoreCase))
            {
                var item = new ToggleMenuFlyoutItem { Text = name, IsChecked = id == current };
                item.Click += (_, _) => MainWindow.Current?.ChooseTargetProject(id);
                projectMenu.Items.Add(item);
            }
            if (projectMenu.Items.Count == 0) projectMenu.Items.Add(new MenuFlyoutItem { Text = "No projects yet", IsEnabled = false });
        };
        _startProject.Flyout = projectMenu;

        var bar = new Grid { ColumnSpacing = 4 };
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        bar.Children.Add(_startAgent);
        Grid.SetColumn(_startProject, 1);
        bar.Children.Add(_startProject);
        Grid.SetColumn(_startStatus, 2);
        _startStatus.VerticalAlignment = VerticalAlignment.Center;
        _startStatus.Margin = new Thickness(8, 0, 8, 0);
        bar.Children.Add(_startStatus);
        Grid.SetColumn(_startSend, 3);
        bar.Children.Add(_startSend);
        _startAgent.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _startAgent.BorderThickness = new Thickness(0);
        _startAgent.FontSize = 12.5;
        _startPrompt.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _startPrompt.BorderThickness = new Thickness(0);
        ChatTitles.Bare(_startPrompt);
        _startPrompt.Padding = new Thickness(4, 2, 4, 2);
        _startSend.BorderThickness = new Thickness(0);
        _startSend.MinWidth = 32; _startSend.MinHeight = 32; _startSend.Padding = new Thickness(7); _startSend.CornerRadius = new CornerRadius(16);
        StyleSend();
        var stack = new StackPanel { Spacing = 8 };
        stack.Children.Add(_startPrompt);
        stack.Children.Add(bar);
        var frame = new Border
        {
            Child = stack, Padding = new Thickness(12, 12, 10, 10), CornerRadius = new CornerRadius(16), BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Hairline, Background = NexusPalette.Surface
        };
        // The frame lights up while you type in it.
        _startPrompt.GotFocus += (_, _) => frame.BorderBrush = new SolidColorBrush(ThemeService.EffectiveIsDark
            ? Windows.UI.Color.FromArgb(56, 255, 255, 255) : Windows.UI.Color.FromArgb(48, 0, 0, 0));
        _startPrompt.LostFocus += (_, _) => frame.BorderBrush = NexusPalette.Hairline;
        return frame;
    }

    // Send is the page's primary action: accent when there is something to send.
    private void StyleSend()
    {
        var ready = _startPrompt.Text.Trim().Length > 0;
        _startSend.Background = ready
            ? NexusTokens.Brush("AccentFillColorDefaultBrush", NexusTokens.BrandGold)
            : NexusTokens.Brush("NexusUserFillBrush", Windows.UI.Color.FromArgb(255, 0x33, 0x33, 0x33));
        _startSend.Foreground = ready
            ? NexusTokens.Brush("TextOnAccentFillColorPrimaryBrush", NexusTokens.BrandInk)
            : NexusTokens.Brush("TextFillColorSecondaryBrush", Windows.UI.Color.FromArgb(255, 0xA3, 0xA3, 0xA3));
    }

    private bool _fillingAgents;

    private void AgentsChanged() => DispatcherQueue.TryEnqueue(FillStartAgents);

    private void FillStartAgents()
    {
        var choices = AgentAvailabilityService.PickerChoices();
        var shown = _startAgent.Items.OfType<ComboBoxItem>().Select(i => i.Tag as string ?? "").ToList();
        var current = (_startAgent.SelectedItem as ComboBoxItem)?.Tag as string;
        var pick = AgentAvailability.Choose(choices, current ?? _lastStartAgent);
        _fillingAgents = true;
        try
        {
            if (!shown.SequenceEqual(choices.Select(c => c.Value)))
            {
                _startAgent.Items.Clear();
                foreach (var (value, name) in choices) _startAgent.Items.Add(new ComboBoxItem { Content = name, Tag = value });
            }
            _startAgent.SelectedItem = _startAgent.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (string)i.Tag == pick);
        }
        finally { _fillingAgents = false; }
        _startAgent.PlaceholderText = choices.Count == 0 ? "No agent ready" : "Agent";
        ToolTipService.SetToolTip(_startAgent, choices.Count == 0 ? AgentAvailability.NoneReady : "Agent that runs this chat on your computer");
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

    private bool _offlineShown;

    // Offline: starting a chat needs the server, so the composer waits with
    // one calm line (the typed prompt is kept) and comes back by itself.
    private void ApplyOffline()
    {
        var offline = ShellState.Current.Offline;
        _startSend.IsEnabled = !offline;
        _startPrompt.IsEnabled = !offline;
        _startAgent.IsEnabled = !offline;
        if (offline) { StartStatus("Starting chats is paused until Nexus reconnects. What you typed is kept."); _offlineShown = true; }
        else if (_offlineShown) { _offlineShown = false; StartStatus(""); }
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
        if (project.Length == 0) { StartStatus("Choose a project first."); _startProject.Focus(FocusState.Programmatic); return; }
        var agent = (_startAgent.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        if (agent.Length == 0) { StartStatus(AgentAvailability.NoneReady); return; }
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
            if (id.Length == 0) { StartStatus(StateView.Friendly(created.Error) is { Length: > 0 } why ? why : "Couldn't start the chat. Try again."); return; }
            _startPrompt.Text = "";
            StartStatus("");
            SessionPage.PendingStart = (id, prompt);
            MainWindow.Current?.OpenSession(id);
            MainWindow.Current?.RefreshChats();
        }
        finally { _startSend.IsEnabled = !ShellState.Current.Offline; }
    }

    private void ShowMetricsLoading()
    {
        _metrics.Children.Clear();
        for (var i = 0; i < 3; i++)
        {
            var tile = Tile(Skeleton.Block(48, 26, 6), Skeleton.Block(84, 10, 5));
            Grid.SetColumn(tile, i);
            _metrics.Children.Add(tile);
        }
    }

    private static Border Tile(UIElement value, UIElement label)
    {
        var stack = new StackPanel { Spacing = 6 };
        stack.Children.Add(value);
        stack.Children.Add(label);
        return new Border
        {
            Padding = new Thickness(16, 14, 16, 14), CornerRadius = NexusTokens.RadiusLarge, BorderThickness = new Thickness(1),
            Background = NexusTokens.Brush("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = NexusTokens.Brush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255)),
            Child = stack
        };
    }

    // Memories, items to review and devices online, for one project or
    // added up over every project.
    private void RenderMetrics(IReadOnlyList<JsonElement> metrics, string? error)
    {
        _metrics.Children.Clear();
        if (metrics.Count == 0)
        {
            if (error is { Length: > 0 }) { var note = NexusTokens.Caption("Numbers aren't available right now."); Grid.SetColumnSpan(note, 3); _metrics.Children.Add(note); }
            return;
        }
        long Sum(string key) => metrics.Sum(m => long.TryParse(NxJson.Text(m, key), out var n) ? n : 0);
        long Max(string key) => metrics.Max(m => long.TryParse(NxJson.Text(m, key), out var n) ? n : 0);
        var memories = Sum("memories"); var online = Max("online");
        var stats = new (long Value, string Label, string Tip, Brush Tone)[]
        {
            (memories, memories == 1 ? "Memory" : "Memories", "Decisions and facts Nexus keeps for your agents", NexusPalette.Accent),
            (Sum("proposed"), "To review", "Suggested memories waiting for your OK (Memory page)", NexusPalette.Amber),
            (online, online == 1 ? "Device online" : "Devices online", "Computers signed in to Nexus right now", NexusTokens.Brush("TextFillColorPrimaryBrush", Microsoft.UI.Colors.White))
        };
        for (var i = 0; i < stats.Length; i++)
        {
            var value = new TextBlock { Text = stats[i].Value.ToString("N0"), Foreground = stats[i].Tone, FontSize = 26, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold };
            var tile = Tile(value, NexusTokens.Caption(stats[i].Label));
            ToolTipService.SetToolTip(tile, stats[i].Tip);
            AutomationProperties.SetName(tile, stats[i].Label + ": " + stats[i].Value);
            Grid.SetColumn(tile, i);
            _metrics.Children.Add(tile);
            Motion.FadeIn(tile, 6, i * 40);
        }
    }

    private async Task LoadAsync()
    {
        var version = ++_version;
        var project = ShellProjectId();
        ApplyHeader();
        var statusTask = Core.CallAsync(NxMethods.StatusGet);
        var dashboardTask = project.Length > 0 ? Core.CallAsync(NxMethods.DashboardGet, new { project_id = project, utc_offset_minutes = (int)TimeZoneInfo.Local.GetUtcOffset(DateTime.Now).TotalMinutes }) : null;
        var status = await statusTask;
        await UiThread.Resume(DispatcherQueue);
        if (version != _version) return;
        RenderCapture(status);
        var dashboard = dashboardTask is null ? null : await dashboardTask;
        await UiThread.Resume(DispatcherQueue);
        if (version != _version) return;
        var signedOut = NxErrors.IsSignedOut(dashboard?.Error) || NxErrors.IsSignedOut(status.Error);
        _notice.Visibility = Visibility.Collapsed;
        if (signedOut && _signedOut.Content is null)
        {
            var card = StateView.Empty("", "You're signed out",
                "Your sign-in on this computer expired or was removed. Sign in again to see your chats and memories.",
                "Sign in", () => MainWindow.Current?.ShowSettings(), primary: true);
            _signedOut.Content = card;
        }
        _signedOut.Visibility = signedOut ? Visibility.Visible : Visibility.Collapsed;
        // Signed out, nothing else on Home can work: show only the way back in.
        var rest = signedOut ? Visibility.Collapsed : Visibility.Visible;
        _composerFrame.Visibility = rest;
        _activitySection.Visibility = rest;
        if (signedOut) _captureCard.Visibility = Visibility.Collapsed;
        if (dashboard is not null) NoteMemoryCount(project, dashboard);
        _ = LoadActivityAsync(version, project, dashboard);
    }

    // Agent activity follows the chat list's project filter. With "All
    // projects" it adds up every project the person can see: the shell
    // project alone (often an old one restored from the last session) left
    // the calendar empty while the work happened in other projects.
    private static readonly Dictionary<string, (ActivitySummary? Summary, List<JsonElement> Metrics, string? Error, DateTime At)> s_activity = new(StringComparer.Ordinal);
    private static readonly TimeSpan ActivityFresh = TimeSpan.FromMinutes(5);

    private async Task LoadActivityAsync(int version, string shellProject, NxResponse? shellDashboard)
    {
        var filter = ShellState.Current.ChatFilter;
        var all = filter.Length == 0;
        var scope = all ? "across all your projects" : ShellProjectLabel() is { Length: > 0 } label ? "in " + label : "in this project";
        var key = all ? "*" : filter;
        if (s_activity.TryGetValue(key, out var cached) && DateTime.UtcNow - cached.At < ActivityFresh)
        {
            _calendar.Render(cached.Summary, scope, cached.Error);
            RenderMetrics(cached.Metrics, cached.Error);
            return;
        }
        var offset = (int)TimeZoneInfo.Local.GetUtcOffset(DateTime.Now).TotalMinutes;
        ActivitySummary? summary;
        string? error = null;
        var metrics = new List<JsonElement>();
        var watch = System.Diagnostics.Stopwatch.StartNew();
        _calendar.ShowLoading(scope);
        ShowMetricsLoading();
        if (!all)
        {
            var response = filter == shellProject && shellDashboard is not null
                ? shellDashboard
                : await Core.CallAsync(NxMethods.DashboardGet, new { project_id = filter, utc_offset_minutes = offset });
            summary = response.Ok ? ActivitySummaries.Parse(response.Result) : null;
            if (!response.Ok) error = response.Error;
            if (response is { Ok: true, Result: { } data } && data.TryGetProperty("metrics", out var m)) metrics.Add(m.Clone());
        }
        else
        {
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
                foreach (var answer in answers)
                    if (answer is { Ok: true, Result: { } data } && data.TryGetProperty("metrics", out var m)) metrics.Add(m.Clone());
            }
        }
        System.Diagnostics.Trace.WriteLine($"[perf] activity ({key}): {watch.ElapsedMilliseconds} ms");
        await UiThread.Resume(DispatcherQueue);
        if (version != _version) return;
        // The project choice moved on while this loaded (e.g. restored at
        // startup): load what is chosen now instead of leaving a skeleton.
        if (ShellState.Current.ChatFilter != filter) { _ = LoadActivityAsync(version, ShellProjectId(), null); return; }
        if (summary is not null || NxErrors.IsSignedOut(error)) s_activity[key] = (summary, metrics, error, DateTime.UtcNow);
        _calendar.Render(summary, scope, error);
        RenderMetrics(metrics, error);
    }

    // The project switcher shows memory counts for projects already loaded.
    private static void NoteMemoryCount(string projectId, NxResponse dashboard)
    {
        if (dashboard is { Ok: true, Result: { } data } && data.TryGetProperty("metrics", out var metrics) &&
            long.TryParse(NxJson.Text(metrics, "memories"), out var memories))
            ProjectStats.SetMemories(projectId, memories);
    }

    // Setup shows until capture has a folder; after that Home stays about chats.
    private void RenderCapture(NxResponse status)
    {
        _capture.Children.Clear();
        _approved.Clear(); _suggested.Clear();
        var capture = status.Result is { } state && state.TryGetProperty("capture", out var c) ? c : default;
        SettingsPage.ReadPaths(capture, "approved_workspaces", _approved);
        SettingsPage.ReadPaths(capture, "suggested_workspaces", _suggested);
        CaptureSetup.ReadDiscovered(capture);
        var setup = _approved.Count == 0 && status.Ok;
        _captureCard.Visibility = setup ? Visibility.Visible : Visibility.Collapsed;
        if (!setup) return;
        var head = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        head.Children.Add(new FontIcon { Glyph = "", FontSize = 16, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center });
        head.Children.Add(NexusTokens.SectionTitle("Bring in your agents' chats"));
        _capture.Children.Add(head);
        _capture.Children.Add(new TextBlock
        {
            Text = "Pick the folders where you use coding agents. Nexus saves those chats so you can search them and continue them anywhere. Nothing outside the folders you pick is read.",
            TextWrapping = TextWrapping.Wrap, Opacity = 0.75, FontSize = NexusTokens.FontBody, LineHeight = 20
        });
        var configure = new Button { Content = "Choose folders", Margin = new Thickness(0, 4, 0, 0) };
        configure.Click += async (_, _) =>
        {
            var result = await CaptureSetup.ShowAsync(this, _approved, _suggested);
            if (result is null) return;
            if (!result.Ok)
            {
                _notice.Text = StateView.Friendly(result.Error) is { Length: > 0 } why ? why : "Couldn't save your folder choices. Try again.";
                _notice.Visibility = Visibility.Visible;
                return;
            }
            await LoadAsync();
        };
        _capture.Children.Add(configure);
    }
}
