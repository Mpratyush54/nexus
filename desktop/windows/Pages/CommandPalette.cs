using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Automation.Peers;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.System;

namespace Nexus.Pages;

/// <summary>
/// Ctrl/Cmd+K: one box for chats, projects, memories and commands. Results
/// come from memory as you type (the sidebar's chats, the project list, the
/// command list), ranked by <see cref="PaletteRanking"/>; the server's chat
/// and memory search fill in a moment later. ↑/↓ move, Enter opens, Esc
/// closes. With nothing typed it shows what you picked recently. Mirrors the
/// web palette (frontend/src/components/CommandPalette.tsx).
/// </summary>
internal sealed class CommandPalette
{
    internal sealed record Item(string Id, string Group, string Title, string Subtitle, string Glyph, Action Run, string Keywords = "", string Keys = "", int GroupOrder = 0);

    public const string GroupChats = "Chats";
    public const string GroupProjects = "Projects";
    public const string GroupMemories = "Memories";
    public const string GroupActions = "Actions";
    public const string GroupSettings = "Settings";
    public const string GroupRecent = "Recent";

    private static CommandPalette? s_open;

    private readonly MainWindow _window;
    private readonly Grid _root;
    private readonly Grid _overlay = new();
    private readonly TextBox _input;
    private readonly StackPanel _results = new() { Spacing = 1 };
    private readonly ScrollViewer _scroll;
    private readonly TextBlock _empty;
    private readonly UIElement? _restoreFocus;
    private readonly List<Item> _remote = new();
    private readonly List<(Item Item, Button Row)> _rows = new();
    private int _selected;
    private int _searchVersion;
    private Microsoft.UI.Dispatching.DispatcherQueueTimer? _remoteTimer;

    public static bool IsOpen => s_open is not null;

    /// <summary>Opens the palette (or focuses it when already open).</summary>
    public static void Show(MainWindow window, string query = "")
    {
        if (s_open is { } open)
        {
            open._input.Focus(FocusState.Programmatic);
            return;
        }
        if (window.Content is not Grid root) return;
        s_open = new CommandPalette(window, root, query);
    }

    public static void CloseIfOpen() => s_open?.Close();

    private CommandPalette(MainWindow window, Grid root, string query)
    {
        _window = window;
        _root = root;
        _restoreFocus = root.XamlRoot is { } xr ? FocusManager.GetFocusedElement(xr) as UIElement : null;

        var dim = new Microsoft.UI.Xaml.Shapes.Rectangle
        {
            Fill = new SolidColorBrush(ThemeService.EffectiveIsDark ? Windows.UI.Color.FromArgb(0x66, 0, 0, 0) : Windows.UI.Color.FromArgb(0x33, 0, 0, 0))
        };
        AutomationProperties.SetAccessibilityView(dim, AccessibilityView.Raw);
        dim.PointerPressed += (_, _) => Close();
        _overlay.Children.Add(dim);

        _input = new TextBox
        {
            PlaceholderText = "Search chats, projects, memories and commands",
            FontSize = 15, BorderThickness = new Thickness(0), Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            Padding = new Thickness(4, 8, 4, 8), Text = query, IsSpellCheckEnabled = false
        };
        AutomationProperties.SetName(_input, "Search chats, projects, memories and commands");
        AutomationProperties.SetHelpText(_input, "Up and down arrows move through the results; Enter opens; Escape closes.");
        _input.TextChanged += (_, _) => { Render(); QueueRemoteSearch(); };
        _input.AddHandler(UIElement.KeyDownEvent, new KeyEventHandler(OnKey), handledEventsToo: true);

        var searchRow = new Grid { ColumnSpacing = 10, Padding = new Thickness(14, 6, 14, 6) };
        searchRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        searchRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        searchRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        searchRow.Children.Add(new FontIcon { Glyph = "", FontSize = 15, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center });
        Grid.SetColumn(_input, 1);
        searchRow.Children.Add(_input);
        var escHint = Keycap("Esc");
        Grid.SetColumn(escHint, 2);
        searchRow.Children.Add(escHint);

        _scroll = new ScrollViewer
        {
            Content = _results, MaxHeight = 420, Padding = new Thickness(6, 4, 6, 6),
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Auto
        };
        AutomationProperties.SetName(_scroll, "Results");
        _empty = new TextBlock { Opacity = 0.6, FontSize = NexusTokens.FontBody, Margin = new Thickness(16, 14, 16, 16), TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };

        var footer = new TextBlock
        {
            Text = "↑↓ move  ·  Enter open  ·  Esc close  ·  " + ShortcutRegistry.Label(ShortcutRegistry.Find(ShortcutRegistry.ShortcutSheet)!, Shortcuts.UseCommand) + " all shortcuts",
            FontSize = NexusTokens.FontMicro, Opacity = 0.5, Margin = new Thickness(16, 6, 16, 8)
        };

        var body = new StackPanel();
        body.Children.Add(searchRow);
        body.Children.Add(new Border { Height = 1, Background = NexusPalette.Hairline });
        body.Children.Add(_scroll);
        body.Children.Add(_empty);
        body.Children.Add(new Border { Height = 1, Background = NexusPalette.Hairline });
        body.Children.Add(footer);

        var card = new Border
        {
            Child = body,
            Width = 560, MaxWidth = 560,
            Margin = new Thickness(16, 64, 16, 16),
            HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Top,
            CornerRadius = NexusTokens.RadiusLarge,
            Background = ThemeService.Brush("NexusSurfaceBrush", NexusTokens.BrandInk),
            BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
        };
#if !HAS_UNO
        card.Translation = new System.Numerics.Vector3(0, 0, 48);
        card.Shadow = new ThemeShadow();
#endif
        AutomationProperties.SetName(card, "Command palette");
        _overlay.Children.Add(card);
        Grid.SetRowSpan(_overlay, Math.Max(1, root.RowDefinitions.Count));
        root.Children.Add(_overlay);
        Motion.FadeIn(dim, 0, 0, TimeSpan.FromMilliseconds(120));
        // Appears in 120 ms: fades in while dropping 4 px into place.
        Motion.FadeIn(card, rise: -4, duration: TimeSpan.FromMilliseconds(120));

        Render();
        _input.Loaded += (_, _) =>
        {
            _input.Focus(FocusState.Programmatic);
            _input.SelectAll();
        };
        _input.Focus(FocusState.Programmatic);
        if (query.Length > 0) QueueRemoteSearch();
    }

    private void Close()
    {
        if (!ReferenceEquals(s_open, this)) return;
        s_open = null;
        _searchVersion++;
        _remoteTimer?.Stop();
        _root.Children.Remove(_overlay);
        if (_restoreFocus is Control control && control.XamlRoot is not null) control.Focus(FocusState.Programmatic);
    }

    private void Run(Item item)
    {
        ShellPrefs.Update(p => p.PaletteRecent = PaletteRanking.Remember(p.PaletteRecent, item.Id));
        Close();
        try { item.Run(); }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Warn("palette", "command failed: " + item.Id, ex); }
    }

    // ---- Items

    private IEnumerable<Item> LocalItems()
    {
        var w = _window;
        var mac = Shortcuts.UseCommand;
        var registry = ShortcutRegistry.All(ShellPrefs.QuickAskKeys, mac);
        string Keys(string id) => ShortcutRegistry.Find(id, registry) is { } s ? ShortcutRegistry.Label(s, mac) : "";
        var sessionId = w.OpenChatId;

        yield return new Item("action:new-chat", GroupActions, "New chat", "Start a conversation with an agent", "", w.StartNewChat, "start create compose", Keys(ShortcutRegistry.NewChat));
        yield return new Item("action:quick-ask", GroupActions, "Quick ask", "A small prompt window over any app", "", () => w.ShowQuickAsk(), "launcher hotkey floating", Keys(ShortcutRegistry.QuickAsk));
        yield return new Item("action:switch-project", GroupActions, "Switch project", "Choose which project's chats to show", "", w.OpenProjectSwitcher, "project change", Keys(ShortcutRegistry.SwitchProject));
        if (sessionId.Length > 0)
        {
            var pinned = w.IsChatPinned(sessionId);
            yield return new Item("action:find", GroupActions, "Find in this chat", "Search every message in the open chat", "", () => w.FindInOpenChat(), "search", Keys("find"));
            yield return new Item("action:pin", GroupActions, pinned ? "Unpin this chat" : "Pin this chat", "Pinned chats stay at the top of the sidebar", pinned ? "" : "", () => w.TogglePin(sessionId), "favourite star", Keys(ShortcutRegistry.PinChat));
            yield return new Item("action:rename", GroupActions, "Rename this chat", "Give it a name you'll recognise", "", () => w.BeginRename(sessionId), "title", Keys(ShortcutRegistry.RenameChat));
            yield return new Item("action:archive", GroupActions, "Archive this chat", "Hide it from the list; find it under Archived", "", () => w.ArchiveChat(sessionId), "hide", Keys(ShortcutRegistry.ArchiveChat));
            yield return new Item("action:copy-link", GroupActions, "Copy link to this chat", "A nexus:// link that opens it", "", () => w.CopyChatLink(sessionId), "share url");
        }
        yield return new Item("action:archived", GroupActions, "Show archived chats", "Chats you archived", "", w.ShowArchivedChats, "archive");
        yield return new Item("action:sidebar", GroupActions, "Show or hide the sidebar", "More room for the chat", "", w.ToggleSidebar, "pane panel collapse", Keys(ShortcutRegistry.ToggleSidebar));
        var dark = ThemeService.EffectiveIsDark;
        yield return new Item("action:theme", GroupActions, dark ? "Switch to light theme" : "Switch to dark theme", "Appearance", dark ? "" : "", w.ToggleTheme, "theme dark light mode appearance");
        yield return new Item("action:zoom-in", GroupActions, "Bigger text", $"Chat text is {ShellPrefs.Current.TextScale}%", "", () => w.ZoomText(+1), "zoom font size larger", Keys(ShortcutRegistry.ZoomIn));
        yield return new Item("action:zoom-out", GroupActions, "Smaller text", $"Chat text is {ShellPrefs.Current.TextScale}%", "", () => w.ZoomText(-1), "zoom font size", Keys(ShortcutRegistry.ZoomOut));
        yield return new Item("action:zoom-reset", GroupActions, "Default text size", "Back to 100%", "", () => w.ZoomText(0), "zoom reset font", Keys(ShortcutRegistry.ZoomReset));
        yield return new Item("action:density", GroupActions, ShellPrefs.Compact ? "Roomier chat list" : "Compact chat list", "Sidebar density", "", w.ToggleDensity, "density compact comfortable rows");
        yield return new Item("action:shortcuts", GroupActions, "Keyboard shortcuts", "Every shortcut in one place", "", w.ShowShortcutSheet, "keys help", Keys(ShortcutRegistry.ShortcutSheet));
        yield return new Item("action:home", GroupActions, "Home", "Recent chats and activity", "", w.ShowDashboard, "dashboard");
        yield return new Item("action:memory", GroupActions, "Memory", "What your agents learned", "", () => w.ShowNav("memory"), "memories knowledge");
        yield return new Item("action:agents", GroupActions, "Agents", "Skills, sub-agents and tools", "", () => w.ShowNav("agentsetup"), "mcp tools skills");
        yield return new Item("action:teleport", GroupActions, "Chats sent to you", "Chats people shared with you", "", () => w.OpenTeleport(), "inbox shared teleport");
        yield return new Item("action:logs", GroupActions, "Open logs folder", "Nexus's log files", "", AppDiagnostics.OpenLogsFolder, "diagnostics debug files");
        yield return new Item("action:report", GroupActions, "Report a problem", "Send the team what went wrong", "", w.ShowReportProblem, "bug feedback issue support");

        foreach (var (key, title, about) in SettingsSections)
            yield return new Item("settings:" + key, GroupSettings, "Settings: " + title, about, "", () => w.ShowSettings(key), "preferences options " + key, key == "general" ? Keys(ShortcutRegistry.Settings) : "", GroupOrder: 2);

        foreach (var (id, name) in w.Projects)
            yield return new Item("project:" + id, GroupProjects, name, "Switch to this project", "", () => w.SwitchToProject(id), "project", GroupOrder: 1);

        foreach (var chat in w.PaletteChats())
        {
            var subtitle = string.Join(" · ", new[] { chat.Agent, chat.Project, chat.Archived ? "Archived" : chat.Pinned ? "Pinned" : "" }.Where(s => s.Length > 0));
            yield return new Item("chat:" + chat.Id, GroupChats, chat.Title, subtitle, chat.Pinned ? "" : "", () => w.OpenSession(chat.Id), chat.Agent + " " + chat.Project);
        }
    }

    internal static readonly (string Key, string Title, string About)[] SettingsSections =
    {
        ("general", "General", "Appearance, text size, quick ask and startup"),
        ("account", "Account", "Your Nexus identity and agent tokens"),
        ("workspaces", "Workspaces & capture", "Folders Nexus monitors"),
        ("agents", "Agents", "What agents may do without asking"),
        ("memory", "Memory", "Project briefs and who sees what"),
        ("organizations", "Organizations", "Teams, invitations and roles"),
        ("usage", "Usage", "How much Nexus you use"),
        ("privacy", "Privacy & diagnostics", "Crash reports and log files"),
        ("advanced", "Advanced", "Sync, cache and version details")
    };

    // ---- Render

    private void Render()
    {
        var query = _input.Text.Trim();
        var all = LocalItems().Concat(_remote).ToList();
        var byId = new Dictionary<string, Item>(StringComparer.Ordinal);
        foreach (var item in all) byId.TryAdd(item.Id, item);
        var entries = byId.Values.Select(i => new PaletteRanking.Entry(i.Id, i.Group, i.Title, i.Keywords + " " + i.Subtitle, i.GroupOrder));
        var recent = ShellPrefs.Current.PaletteRecent;
        var ranked = PaletteRanking.Rank(entries, query, recent, limit: query.Length == 0 ? 24 : 60);
        if (query.Length == 0)
        {
            // Nothing typed: what you picked recently, then a few chats and the main commands.
            var recentSet = new HashSet<string>(recent, StringComparer.Ordinal);
            ranked = ranked
                .Select(r => recentSet.Contains(r.Entry.Id) ? r with { Entry = r.Entry with { Group = GroupRecent } } : r)
                .Where(r => r.Entry.Group is GroupRecent or GroupActions or GroupChats)
                .ToList();
        }
        var groups = PaletteRanking.Grouped(ranked, perGroup: query.Length == 0 ? 6 : 8);
        _results.Children.Clear();
        _rows.Clear();
        foreach (var (group, items) in groups)
        {
            _results.Children.Add(new TextBlock
            {
                Text = group, FontSize = NexusTokens.FontMicro, Opacity = 0.55, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                Margin = new Thickness(10, _results.Children.Count == 0 ? 4 : 10, 10, 4)
            });
            foreach (var ranked1 in items)
            {
                if (!byId.TryGetValue(ranked1.Entry.Id, out var item)) continue;
                var row = Row(item, _rows.Count);
                _rows.Add((item, row));
                _results.Children.Add(row);
            }
        }
        _empty.Visibility = _rows.Count == 0 ? Visibility.Visible : Visibility.Collapsed;
        _empty.Text = query.Length == 0 ? "Type to search." : $"Nothing matches “{query}” yet." + (query.Length >= 2 ? " Still searching your chats and memories…" : "");
        _scroll.Visibility = _rows.Count == 0 ? Visibility.Collapsed : Visibility.Visible;
        Select(0, announce: false);
    }

    private Button Row(Item item, int index)
    {
        var grid = new Grid { ColumnSpacing = 10 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(20) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.Children.Add(new FontIcon { Glyph = item.Glyph, FontSize = 14, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center });
        var text = new StackPanel { VerticalAlignment = VerticalAlignment.Center };
        text.Children.Add(new TextBlock { Text = item.Title, FontSize = NexusTokens.FontBody, TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap });
        if (item.Subtitle.Length > 0)
            text.Children.Add(new TextBlock { Text = item.Subtitle, FontSize = NexusTokens.FontCaption, Opacity = 0.55, TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap });
        Grid.SetColumn(text, 1);
        grid.Children.Add(text);
        if (item.Keys.Length > 0)
        {
            var keys = Keycap(item.Keys);
            Grid.SetColumn(keys, 2);
            grid.Children.Add(keys);
        }
        var button = new Button
        {
            Content = grid, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
            Padding = new Thickness(10, 7, 10, 7), CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(0),
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), IsTabStop = false
        };
        AutomationProperties.SetName(button, item.Title + (item.Subtitle.Length > 0 ? ", " + item.Subtitle : "") + (item.Keys.Length > 0 ? ", " + item.Keys : ""));
        button.Click += (_, _) => Run(item);
        button.PointerMoved += (_, _) => { if (_selected != index) Select(index, announce: false); };
        return button;
    }

    private static Border Keycap(string text) => new()
    {
        Child = new TextBlock { Text = text, FontSize = NexusTokens.FontMicro, Opacity = 0.7 },
        Padding = new Thickness(6, 1, 6, 2), CornerRadius = new CornerRadius(4),
        BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1),
        VerticalAlignment = VerticalAlignment.Center
    };

    private void Select(int index, bool announce = true)
    {
        if (_rows.Count == 0) { _selected = 0; return; }
        _selected = Math.Clamp(index, 0, _rows.Count - 1);
        for (var i = 0; i < _rows.Count; i++)
            _rows[i].Row.Background = i == _selected ? NexusPalette.AccentFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        var row = _rows[_selected].Row;
        row.StartBringIntoView(new BringIntoViewOptions { AnimationDesired = false, VerticalAlignmentRatio = 0.5 });
        if (!announce) return;
        try
        {
            var peer = FrameworkElementAutomationPeer.FromElement(_input) ?? FrameworkElementAutomationPeer.CreatePeerForElement(_input);
            peer?.RaiseNotificationEvent(AutomationNotificationKind.Other, AutomationNotificationProcessing.MostRecent,
                AutomationProperties.GetName(row), "nexus-palette-selection");
        }
        catch { /* announcements are a courtesy */ }
    }

    private void OnKey(object sender, KeyRoutedEventArgs e)
    {
        switch (e.Key)
        {
            case VirtualKey.Down:
                Select(_selected + 1); e.Handled = true; break;
            case VirtualKey.Up:
                Select(_selected - 1); e.Handled = true; break;
            case VirtualKey.PageDown:
                Select(_selected + 6); e.Handled = true; break;
            case VirtualKey.PageUp:
                Select(_selected - 6); e.Handled = true; break;
            case VirtualKey.Enter:
                e.Handled = true;
                if (_rows.Count > 0) Run(_rows[_selected].Item);
                break;
            case VirtualKey.Escape:
                e.Handled = true;
                Close();
                break;
            case VirtualKey.Tab:
                // Keyboard-first: Tab moves through results instead of leaving the box.
                Select(_selected + (IsShiftDown() ? -1 : 1)); e.Handled = true; break;
        }
    }

    private static bool IsShiftDown() =>
        Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(VirtualKey.Shift).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);

    // ---- Server search (chats by content, memories), a moment after typing stops

    private void QueueRemoteSearch()
    {
        _remoteTimer ??= _root.DispatcherQueue.CreateTimer();
        _remoteTimer.Stop();
        _remoteTimer.Interval = TimeSpan.FromMilliseconds(180);
        _remoteTimer.IsRepeating = false;
        _remoteTimer.Tick -= OnRemoteTick;
        _remoteTimer.Tick += OnRemoteTick;
        _remoteTimer.Start();
    }

    private void OnRemoteTick(Microsoft.UI.Dispatching.DispatcherQueueTimer sender, object args) => _ = SearchServerAsync();

    private async Task SearchServerAsync()
    {
        var query = _input.Text.Trim();
        var version = ++_searchVersion;
        if (query.Length < 2)
        {
            if (_remote.Count > 0) { _remote.Clear(); Render(); }
            return;
        }
        var project = ShellState.Current.ProjectId;
        var chats = App.Core.CallAsync(NxMethods.TimelineList, new { q = query, limit = 8 });
        var memories = ProjectScope.LooksLikeUuid(project)
            ? App.Core.CallAsync(NxMethods.MemorySearch, new { browse = true, q = query, project_id = project, limit = 5, offset = 0 })
            : Task.FromResult<NxResponse>(new NxResponse(false, null, null));
        await Task.WhenAll(chats, memories);
        await UiThread.Resume(_root.DispatcherQueue);
        if (version != _searchVersion || !ReferenceEquals(s_open, this)) return;
        _remote.Clear();
        if (chats.Result is { Ok: true } c)
        {
            foreach (var item in NxJson.Items(c.Result))
            {
                var id = NxJson.Text(item, "id", "session_id");
                if (id.Length == 0 || SessionLineage.IsSubagentSession(item)) continue;
                var agent = NxJson.Text(item, "agent", "harness");
                var title = ChatTitles.Title(NxJson.Text(item, "title"), NxJson.Text(item, "summary"), agent);
                var match = ChatTitles.Clean(NxJson.Text(item, "match_preview"));
                var subtitle = match.Length > 0 ? "“" + match.Trim('…', '.', ' ') + "”" : ChatTitles.AgentName(agent);
                var sessionId = id;
                _remote.Add(new Item("chat:" + id, GroupChats, title, subtitle, "", () => _window.OpenSession(sessionId, query), match + " " + query));
            }
        }
        if (memories.Result is { Ok: true } m)
        {
            foreach (var item in NxJson.Items(m.Result))
            {
                var id = NxJson.Text(item, "id", "memory_id");
                var text = ChatTitles.Clean(NxJson.Text(item, "content", "text", "title", "key"));
                if (id.Length == 0 || text.Length == 0) continue;
                var shown = text.Length > 90 ? text[..90] + "…" : text;
                _remote.Add(new Item("memory:" + id, GroupMemories, shown, "Memory · " + NxJson.PublicStatus(NxJson.Text(item, "status")), "", () => _window.ShowSearch(query), text + " " + query, GroupOrder: 3));
            }
        }
        var keep = _rows.Count > 0 ? _rows[_selected].Item.Id : "";
        Render();
        // Results arriving must not move the selection out from under the keyboard.
        var again = _rows.FindIndex(r => r.Item.Id == keep);
        if (again > 0) Select(again, announce: false);
    }
}
