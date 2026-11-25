using System.Text.Json;
using Microsoft.UI.Dispatching;
using CommunityToolkit.Mvvm.Input;
using H.NotifyIcon;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Nexus.Core.Diagnostics;
using Nexus.Pages;
using Windows.Graphics;

namespace Nexus;

public sealed partial class MainWindow : Window
{
    private bool _quitting;
    private ContentDialog? _desktopApproval;
    private string _desktopApprovalId = "";
    private readonly BoundedShutdown _shutdown = new();
    private bool _navLock;
    private bool _shellInitialized;
    private bool _shellInitializing;
    private bool _projectSync;
    private string _shellProjectId = "";
    private MenuFlyout? _projectCompactFlyout;
    private readonly List<ChatPaneEntry> _chatEntries = new();
    private string _chatStatus = "";
    // "" = chats from every project (reference: T3 Code); otherwise one project id.
    private string _projectFilter = "";
    private string _chatCursor = "";
    private int _chatLoadVersion;
    private bool _chatLoading;
    private bool _paneExpanded;
    private bool _paneLayoutApplied;
    private bool _applyingPendingLink;
    private DispatcherQueueTimer? _chatSearchTimer;
    // Last server answer per (project filter, search) for this app session,
    // so switching back to a filter paints at once and refreshes quietly.
    private sealed record ChatCacheEntry(List<ChatPaneEntry> Entries, string Cursor, DateTime At);
    private readonly Dictionary<string, ChatCacheEntry> _chatCache = new(StringComparer.Ordinal);
    private static readonly TimeSpan ChatCacheFresh = TimeSpan.FromSeconds(20);
    // Built rows are reused across renders; only selection colours change.
    private readonly Dictionary<(ChatPaneEntry Entry, bool ShowMatch), (Button Button, TextBlock Title)> _chatRows = new();
    private readonly Dictionary<string, (Button Button, TextBlock Title)> _shownChatRows = new(StringComparer.Ordinal);
    private string _chatInFlightKey = "";
    private bool _chatRefreshing;
    private CancellationTokenSource? _chatFetchCts;
    private sealed record ChatPaneEntry(string Id, string Title, string Agent, string Summary, string When, bool Forked, string ParentId, string ProjectId = "", string Match = "", long TurnCount = -1);

    public new static MainWindow? Current { get; private set; }

    public ShellState State => ShellState.Current;
    public IRelayCommand TrayOpenCommand { get; }
    public IAsyncRelayCommand TrayPauseCommand { get; }
    public IRelayCommand TrayTeleportCommand { get; }
    public IAsyncRelayCommand TrayQuitCommand { get; }
    public IRelayCommand TrayReportCommand { get; }

    public MainWindow()
    {
        TrayOpenCommand = new RelayCommand(RestoreWindow);
        TrayPauseCommand = new AsyncRelayCommand(PauseCaptureAsync);
        TrayTeleportCommand = new RelayCommand(() => { RestoreWindow(); OpenTeleport(); });
        TrayQuitCommand = new AsyncRelayCommand(QuitAsync);
        TrayReportCommand = new RelayCommand(() => { RestoreWindow(); ShowReportProblem(); });
        InitializeComponent();
        // One memory line a minute in the desktop log (Core/Diagnostics/DesktopMemoryLog.cs).
        Nexus.Core.Diagnostics.UiStats.Sidebar = () => (_chatEntries.Count, _chatRows.Count);
        Nexus.Core.Diagnostics.DesktopMemoryLog.Start(DispatcherQueue);
        ProjectSelector.SelectionChanged += ProjectSelector_SelectionChanged;
        _projectCompactFlyout = new MenuFlyout();
        _projectCompactFlyout.Opening += (_, _) => RebuildCompactProjectFlyout();
        // The collapsed rail opens the same project switcher as the sidebar.
        ProjectCompactButton.Click += (_, _) => _projectSwitcher.Open(ProjectCompactButton);
        DecorateFooter();
        _projectSwitcher = new ProjectSwitcher(SwitcherProjects, () => _projectFilter, SetProjectFilter, ChatCountFor);
        ProjectSwitcherHost.Content = _projectSwitcher;
        ShellState.Changed += (_, _) => DispatcherQueue.TryEnqueue(_projectSwitcher.Refresh);
        ApplyPaneProjectLayout();
        Nav.SizeChanged += (_, _) => ResizeChatPane();
        Nav.Loaded += (_, _) => ResizeChatPane();
        PaneHeaderRoot.SizeChanged += (_, _) => ResizeChatPane();
        FooterIcons.SizeChanged += (_, _) => ResizeChatPane();
        ChatTitles.Bare(ChatSearch);
        var filterMenu = new MenuFlyout { Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedRight };
        filterMenu.Opening += (_, _) => BuildProjectFilterMenu(filterMenu);
        ProjectFilterButton.Flyout = filterMenu;
        ChatTags.Changed += (_, _) => { _chatRows.Clear(); RenderChats(); };
        Tray.ForceCreate();
        Current = this;
        ResizeForDisplay(1240, 820);
        AppWindow.SetIcon(Path.Combine(AppContext.BaseDirectory, "Assets", "Nexus.ico"));
        ExtendsContentIntoTitleBar = true;
        // Mica only behind the sidebar (its tint is translucent); the reading
        // canvas stays solid so content never sits on a grey wash.
        if (Microsoft.UI.Composition.SystemBackdrops.MicaController.IsSupported())
            SystemBackdrop = new Microsoft.UI.Xaml.Media.MicaBackdrop { Kind = Microsoft.UI.Composition.SystemBackdrops.MicaKind.Base };
        else
            RootGrid.Background = ThemeService.Brush("NexusCanvasBrush", NexusTokens.BrandInk);
        ThemeService.Apply(this);
        ThemeService.Watch(this);
        // After the handler that changed the theme has returned (Settings'
        // picker), so the page it lives on can be rebuilt.
        ThemeService.Changed += (_, _) => DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, RebuildForTheme);
        Motion.ApplyPageTransitions(ContentFrame);
        AppWindow.Closing += OnClosing;
        TeleportBadge.Visibility = Visibility.Collapsed;
        ContentFrame.Loaded += OnContentFrameLoaded;
        App.Core.Subscribe(DispatcherQueue, OnCoreEvent);
        _ = App.Core.CallAsync(NxMethods.EventsSubscribe);
        EnsureLoginStartDefault();
        // Names for any control built without one (icon buttons, rows with
        // panel content), once layout settles after a change.
        _a11yTimer = DispatcherQueue.CreateTimer();
        _a11yTimer.Interval = TimeSpan.FromMilliseconds(900);
        _a11yTimer.IsRepeating = false;
        _a11yTimer.Tick += (_, _) => AccessibleNames.Fill(Content);
        RootGrid.LayoutUpdated += (_, _) => { if (!_a11yTimer.IsRunning) _a11yTimer.Start(); };
        Connectivity.Changed += () => DispatcherQueue.TryEnqueue(ApplyConnectivity);
        Connectivity.Start();
        _ = RefreshBannerAsync();
        _ = RefreshInboxBadgeAsync();
        Activated += async (_, _) => await EnsureShellInitializedAsync();
        Nav.Loaded += (_, _) => { if (_setup is null && ContentFrame.Content is DashboardPage home) home.FocusNewChat(); };
        UpdateService.Shared.CheckInBackground(TimeSpan.FromSeconds(30));
        _ = Task.Run(UpdateService.Shared.SyncDisplayVersion);
        if (!AppPrefs.SetupCompleted) ShowSetup();
        else DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, ShowUpdatedToast);
        // A crash in an earlier run left a report: ask once the window is up
        // (not over the setup guide, not while hidden in the tray).
        Activated += (_, _) => DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, OfferCrashReports);
    }

    private bool _crashOfferDone;

    /// <summary>A dim layer over the whole window (sidebar and title bar too) while a dialog is open.</summary>
    internal UIElement? ShowDim()
    {
        if (Content is not Grid root) return null;
        var dim = new Microsoft.UI.Xaml.Shapes.Rectangle
        {
            Fill = new SolidColorBrush(ThemeService.EffectiveIsDark ? Windows.UI.Color.FromArgb(0x8C, 0, 0, 0) : Windows.UI.Color.FromArgb(0x4D, 0, 0, 0)),
            IsHitTestVisible = true
        };
        Grid.SetRowSpan(dim, 2);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetAccessibilityView(dim, Microsoft.UI.Xaml.Automation.Peers.AccessibilityView.Raw);
        root.Children.Add(dim);
        Nexus.Pages.Motion.FadeIn(dim, 0, 0, Nexus.Pages.Motion.Fast);
        return dim;
    }

    internal void HideDim(UIElement dim)
    {
        if (Content is Grid root) root.Children.Remove(dim);
    }
    private readonly DispatcherQueueTimer _a11yTimer;

    // A live theme switch: XAML-bound resources follow the root's theme by
    // themselves; everything built in code is rebuilt with brushes of the
    // new theme (ThemeService.Lookup). The open page is recreated in place.
    private void RebuildForTheme()
    {
        if (SystemBackdrop is null && _setup is null) RootGrid.Background = ThemeService.Brush("NexusCanvasBrush", NexusTokens.BrandInk);
        DiagLog.Info("theme", $"rebuilding for {(ThemeService.EffectiveIsDark ? "dark" : "light")}");
        // Rows are rebuilt from scratch: the repeater recycles the old ones
        // (through ChatRowFactory) and asks for new ones, which the empty
        // row cache builds with the new theme's brushes.
        _chatRows.Clear();
        RenderChats();
        _projectSwitcher.Refresh();
        _setup?.ThemeChanged();
        var current = ContentFrame.Content;
        PageCache.Reset();
        if (_setup is not null) return;
        ContentFrame.Content = null;
        switch (current)
        {
            case SessionPage session when session.SessionId.Length > 0: OpenSession(session.SessionId); break;
            case MemoryPage: ShowPage(PageCache.Memory); break;
            case SettingsPage: ShowPage(PageCache.Settings); break;
            case AgentSetupPage: ShowPage(PageCache.AgentSetup); break;
            case TeleportPage: OpenTeleport(); break;
            case HomeHubPage: ShowTimeline(); break;
            default: ShowDashboard(); break;
        }
        UpdateFooterSelection();
    }

    private void OfferCrashReports()
    {
        if (_crashOfferDone || _setup is not null || !AppWindow.IsVisible || Content?.XamlRoot is null) return;
        _crashOfferDone = true;
        _ = ReportDialogs.OfferHeldReportsAsync(Content.XamlRoot);
    }

    /// Settings and the tray menu: "Report a problem".
    public void ShowReportProblem()
    {
        if (Content?.XamlRoot is { } root) _ = ReportDialogs.ShowReportProblemAsync(root);
    }

    /// Opens the sidebar's project switcher from a page (Memory's project chip).
    public void OpenProjectSwitcher(FrameworkElement? anchor) => _projectSwitcher.Open(anchor);

    // After a background update is applied: a quiet toast, once per version.
    private void ShowUpdatedToast()
    {
        var version = UpdateService.Shared.CurrentVersion;
        if (version.Length == 0) return;
        var previous = AppPrefs.Data.LastVersion;
        AppPrefs.Data.LastVersion = version;
        AppPrefs.Save();
        if (previous.Length == 0 || previous == version || Content is not Grid root) return;
        var whatsNew = new HyperlinkButton { Content = "What's new", NavigateUri = new Uri($"https://github.com/Mpratyush54/nexus/releases/tag/desktop-v{version}") };
        var toast = new InfoBar
        {
            IsOpen = true, Severity = InfoBarSeverity.Success, Title = $"Updated to Nexus {version}",
            ActionButton = whatsNew, HorizontalAlignment = HorizontalAlignment.Right, VerticalAlignment = VerticalAlignment.Bottom,
            Margin = new Thickness(0, 0, 20, 20), MaxWidth = 420
        };
        Grid.SetRowSpan(toast, 2);
        root.Children.Add(toast);
        Nexus.Pages.Motion.FadeSlideIn(toast, 400, 0, 16, 520);
        var timer = DispatcherQueue.CreateTimer();
        timer.Interval = TimeSpan.FromSeconds(12);
        timer.IsRepeating = false;
        timer.Tick += (_, _) => toast.IsOpen = false;
        toast.Closed += (_, _) => root.Children.Remove(toast);
        timer.Start();
    }

    // ---- First-run setup (also reachable from Settings > General)

    private SetupWizardPage? _setup;

    // The guide covers the whole window on the app's own canvas.
    public void ShowSetup()
    {
        if (_setup is null && Content is Grid root)
        {
            // The guide paints the same ink canvas as the app (its own
            // background), so finishing it doesn't jump from a tinted
            // backdrop to the shell's colours.
            _setup = new SetupWizardPage(FinishSetup);
            Grid.SetRowSpan(_setup, 2);
            Nav.Visibility = Visibility.Collapsed;
            OfflineBar.Visibility = Visibility.Collapsed;
            root.Children.Add(_setup);
        }
        if (AppWindow.IsVisible) Activate();
    }


    private void FinishSetup()
    {
        if (Content is Grid root && _setup is not null) root.Children.Remove(_setup);
        _setup = null;
        Nav.Visibility = Visibility.Visible;
        OfflineBar.Visibility = Visibility.Visible;
        ApplyConnectivity();
        // The shell rises in as the guide fades out.
        ContentFrame.Transitions = new Microsoft.UI.Xaml.Media.Animation.TransitionCollection
        {
            new Microsoft.UI.Xaml.Media.Animation.EntranceThemeTransition { FromVerticalOffset = 24 }
        };
        Nexus.Pages.Motion.FadeSlideIn(Nav, 0, 0, 16, 520);
        ShowDashboard();
        PageCache.Dashboard.FocusNewChat();
        _ = RefreshBannerAsync();
        PageCache.Settings.RefreshAccount();
        _chatCache.Clear();
        _ = ReloadAfterSetupAsync();
        ApplyPendingLink();
    }

    private async Task ReloadAfterSetupAsync()
    {
        await BootstrapProjectsAsync();
        await LoadChatsAsync(force: true);
    }

    // The first size is in layout pixels (DIPs): on a 150% display a raw
    // 1240x820 window was only 827x547 DIPs, too small for the sidebar plus a
    // page. Capped to 90% of the work area.
    private void ResizeForDisplay(int width, int height)
    {
        try
        {
            var scale = GetDpiForWindow(WinRT.Interop.WindowNative.GetWindowHandle(this)) / 96.0;
            if (scale <= 0) scale = 1;
            var area = DisplayArea.GetFromWindowId(AppWindow.Id, DisplayAreaFallback.Primary).WorkArea;
            var w = (int)Math.Min(width * scale, area.Width * 0.9);
            var h = (int)Math.Min(height * scale, area.Height * 0.9);
            AppWindow.Resize(new SizeInt32(w, h));
        }
        catch
        {
            AppWindow.Resize(new SizeInt32(width, height));
        }
    }

    [System.Runtime.InteropServices.DllImport("user32.dll")]
    private static extern uint GetDpiForWindow(IntPtr hwnd);

    private async Task EnsureShellInitializedAsync()
    {
        if (_shellInitialized || _shellInitializing)
        {
            return;
        }
        _shellInitializing = true;
        try
        {
            await InitializeShellAsync();
            _shellInitialized = true;
        }
        catch (Exception ex)
        {
            _shellInitialized = false;
            DiagLog.Error("startup", "shell initialisation failed", ex);
            if (_chatEntries.Count == 0)
            {
                _chatStatus = "Could not finish loading the shell. Retrying next time the window is activated. " + ex.Message;
                RenderChats();
            }
        }
        finally
        {
            _shellInitializing = false;
        }
    }

    private async Task InitializeShellAsync()
    {
        // Show Home immediately; project discovery can involve a network
        // request and must not hold the entire first screen blank.
        if (ContentFrame.Content is null)
        {
            RestoreNavPage("home");
            ShowPage(PageCache.Dashboard);
        }
        await BootstrapProjectsAsync();
        _ = LoadChatsAsync();
    }

    private static string NormalizeNavTag(string tag)
    {
        var normalized = string.IsNullOrWhiteSpace(tag) ? "timeline" : tag.Trim().ToLowerInvariant();
        return normalized switch
        {
            "teleport" or "agents" => "timeline",
            _ => normalized
        };
    }

    private static NexusPage CreatePageForTag(string tag) => tag switch
    {
        "home" => PageCache.Dashboard,
        "memory" => PageCache.Memory,
        "settings" => PageCache.Settings,
        "agentsetup" => PageCache.AgentSetup,
        _ => PageCache.HomeHub
    };

    private void RestoreNavPage(string tag)
    {
        var normalized = NormalizeNavTag(tag);
        NavigationViewItem? item = normalized switch
        {
            "home" => HomeItem,
            "memory" => MemoryItem,
            "settings" => SettingsItem,
            _ => TimelineItem
        };
        _navLock = true;
        Nav.SelectedItem = item ?? TimelineItem;
        _navLock = false;
    }

    private async Task BootstrapProjectsAsync()
    {
        _projectSync = true;
        var loaded = false;
        try
        {
            for (var attempt = 0; attempt < 3 && !loaded; attempt++)
            {
                loaded = await ShellProjects.RefreshSelectorAsync(ProjectSelector, App.Core, DispatcherQueue);
                if (!loaded && attempt < 2)
                {
                    await Task.Delay(500);
                }
            }
        }
        finally
        {
            _projectSync = false;
        }
        _shellProjectId = ShellState.Current.ProjectId;
        RebuildCompactProjectFlyout();
        UpdateCompactProjectTooltip();
        _chatRows.Clear();
        RestoreProjectChoice();
        RenderChats();
        ToolTipService.SetToolTip(ProjectSelector,
            loaded ? null : "Projects could not be refreshed. Keeping the last known list.");
        if (!loaded && ProjectSelector.Items.Count == 0 && _chatEntries.Count == 0)
        {
            _chatStatus = "Projects are unavailable. Check your connection, then reopen the window to retry.";
            RenderChats();
        }
    }

    private void ProjectSelector_SelectionChanged(object sender, SelectionChangedEventArgs e)
    {
        if (_projectSync)
        {
            return;
        }
        var previous = _shellProjectId;
        ShellProjects.SyncStateFromSelector(ProjectSelector);
        _shellProjectId = ShellState.Current.ProjectId;
        PageCache.InvalidateProject(previous, _shellProjectId);
        UpdateCompactProjectTooltip();
        RebuildCompactProjectFlyout();
        _ = LoadChatsAsync();
        if (ContentFrame.Content is SessionPage) ShowTimeline();
    }

    // Minimal (< 640): the closed pane is hidden and opens as an overlay, so
    // the rail is never on screen and the open layout applies when shown.
    // Compact (640-960): closed = icon rail, open = overlay with the list.
    // Expanded (>= 960): inline; the user can still close it to the rail.
    private void Nav_DisplayModeChanged(NavigationView sender, NavigationViewDisplayModeChangedEventArgs args)
    {
        System.Diagnostics.Trace.WriteLine($"[perf] sidebar: display mode {args.DisplayMode}, pane open={Nav.IsPaneOpen}");
        ApplyPaneProjectLayout(Nav.IsPaneOpen);
        // A display-mode switch can close the pane without PaneClosed; settle
        // on the final state once the control has applied it.
        DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () => ApplyPaneProjectLayout(Nav.IsPaneOpen));
    }

    // Both directions switch content at the START of the animation, so the
    // slide only ever moves light content: opening shows the (virtualized)
    // chat list at once; closing swaps to the icon rail at once instead of
    // sliding the full list shut and swapping on PaneClosed (the ~1 s lag).
    // PaneOpened/PaneClosed only reconcile with the final state.
    private void Nav_PaneOpening(NavigationView sender, object args) => ApplyPaneProjectLayout(true);

    private void Nav_PaneOpened(NavigationView sender, object args)
    {
        ApplyPaneProjectLayout(true);
        RelayoutOpenChat("sidebar opened");
    }

    private void Nav_PaneClosing(NavigationView sender, NavigationViewPaneClosingEventArgs args)
    {
        if (args.Cancel) return;
        ApplyPaneProjectLayout(false);
    }

    private void Nav_PaneClosed(NavigationView sender, object args)
    {
        ApplyPaneProjectLayout(Nav.IsPaneOpen);
        RelayoutOpenChat("sidebar closed");
    }

    // The content width changed with the pane: the open chat re-fits its
    // transcript once the slide has finished.
    private void RelayoutOpenChat(string reason)
    {
        if (ContentFrame.Content is not SessionPage page) return;
        DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () => page.RelayoutTranscript(reason));
    }

    private void ApplyPaneProjectLayout() => ApplyPaneProjectLayout(Nav.IsPaneOpen);

    private void ApplyPaneProjectLayout(bool expanded)
    {
        // DisplayMode stays Expanded when the user closes the pane manually.
        // Visibility must track the actual pane state or the full chat list is
        // clipped into the 52px icon rail. Only visibility and the footer
        // layout change here: chat rows are never re-created.
        if (_paneLayoutApplied && _paneExpanded == expanded) return;
        var watch = System.Diagnostics.Stopwatch.StartNew();
        _paneLayoutApplied = true;
        _paneExpanded = expanded;
        var show = expanded ? Visibility.Visible : Visibility.Collapsed;
        var hide = expanded ? Visibility.Collapsed : Visibility.Visible;
        ProjectPaneExpanded.Visibility = show;
        PaneHeaderRoot.MinHeight = expanded ? 48 : 0;
        ProjectPaneCompact.Visibility = hide;
        ChatPane.Visibility = show;
        ApplyRailLayout(expanded);
        ResizeChatPane();
        UpdateCompactProjectTooltip();
        System.Diagnostics.Trace.WriteLine($"[perf] sidebar: {(expanded ? "expand" : "collapse")} layout switch {watch.Elapsed.TotalMilliseconds:0.0} ms (mode={Nav.DisplayMode})");
    }

    // Rail geometry. The SplitView pane keeps its open width and is only
    // clipped when closed, so anything centred in the pane lands outside the
    // visible rail. Rail content is therefore left-aligned with the width of
    // the compact pane; 36px buttons centre inside it, 8px apart. The +1 left
    // margin cancels the template's -1 PaneContentGrid margin, so the icons
    // line up with the toggle button (centred at CompactPaneLength / 2).
    private void ApplyRailLayout(bool expanded)
    {
        var railWidth = Nav.CompactPaneLength > 0 ? Nav.CompactPaneLength : 52;
        ProjectPaneCompact.Width = railWidth;
        // Footer: a horizontal row when open (unchanged), a vertical column
        // of 36px buttons in the rail so every destination stays reachable.
        FooterIcons.Orientation = expanded ? Orientation.Horizontal : Orientation.Vertical;
        FooterIcons.HorizontalAlignment = HorizontalAlignment.Left;
        FooterIcons.Width = expanded ? double.NaN : railWidth;
        FooterIcons.Spacing = expanded ? 0 : 8;
        FooterIcons.Margin = expanded ? new Thickness(8, 4, 8, 8) : new Thickness(1, 6, 0, 10);
        foreach (var child in FooterIcons.Children)
        {
            if (child is not Button button) continue;
            if (_footerParts.TryGetValue(button, out var parts)) parts.Label.Visibility = expanded ? Visibility.Visible : Visibility.Collapsed;
            button.Width = expanded ? 51 : 36;
            button.Height = expanded ? double.NaN : 40;
            button.Padding = expanded ? new Thickness(2, 6, 2, 2) : new Thickness(0, 4, 0, 0);
            if (expanded) button.ClearValue(Control.CornerRadiusProperty);
            else button.CornerRadius = new CornerRadius(6);
            button.HorizontalAlignment = HorizontalAlignment.Center;
            button.HorizontalContentAlignment = HorizontalAlignment.Center;
            button.VerticalContentAlignment = VerticalAlignment.Center;
        }
    }

    // Rail search: open the pane and put the caret in the chat search box.
    private void RailSearch_Click(object sender, RoutedEventArgs e)
    {
        Nav.IsPaneOpen = true;
        ApplyPaneProjectLayout(true);
        DispatcherQueue.TryEnqueue(() => ChatSearch.Focus(FocusState.Programmatic));
    }

    // Footer destinations: icon over a short label while the sidebar is
    // open (icons only in the rail, where the tooltip names them), and a
    // gold bar under the page you are on.
    private readonly Dictionary<Button, (TextBlock Label, Border Marker, FrameworkElement Icon)> _footerParts = new();

    private static readonly Dictionary<string, string> FooterLabels = new()
    {
        ["home"] = "Home", ["teleport"] = "Inbox", ["agentsetup"] = "Agents", ["memory"] = "Memory", ["settings"] = "Settings"
    };

    private void DecorateFooter()
    {
        foreach (var child in FooterIcons.Children)
        {
            if (child is not Button { Tag: string tag, Content: FrameworkElement icon } button || _footerParts.ContainsKey(button)) continue;
            var label = new TextBlock { Text = FooterLabels.GetValueOrDefault(tag, tag), FontSize = 10.5, Opacity = 0.75, HorizontalAlignment = HorizontalAlignment.Center, Margin = new Thickness(0, 3, 0, 0) };
            var marker = new Border { Height = 2, Width = 18, CornerRadius = new CornerRadius(1), Margin = new Thickness(0, 3, 0, 0), HorizontalAlignment = HorizontalAlignment.Center, Background = NexusPalette.Accent, Opacity = 0 };
            button.Content = null;
            var stack = new StackPanel { HorizontalAlignment = HorizontalAlignment.Center };
            icon.HorizontalAlignment = HorizontalAlignment.Center;
            stack.Children.Add(icon);
            stack.Children.Add(label);
            stack.Children.Add(marker);
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetAccessibilityView(label, Microsoft.UI.Xaml.Automation.Peers.AccessibilityView.Raw);
            button.Content = stack;
            _footerParts[button] = (label, marker, icon);
        }
        FooterIcons.Spacing = 0;
    }

    // Selected indicator for the footer / rail destinations.
    private void UpdateFooterSelection()
    {
        // Type checks, not PageCache getters: those construct pages lazily.
        var tag = ContentFrame.Content switch
        {
            TeleportPage => "teleport",
            DashboardPage => "home",
            MemoryPage => "memory",
            SettingsPage => "settings",
            AgentSetupPage => "agentsetup",
            _ => ""
        };
        foreach (var child in FooterIcons.Children)
        {
            if (child is not Button { Tag: string buttonTag } button) continue;
            var selected = buttonTag == tag;
            button.Background = TransparentRow;
            if (_footerParts.TryGetValue(button, out var parts))
            {
                parts.Marker.Background = NexusPalette.Accent;
                parts.Marker.Opacity = selected ? 1 : 0;
                parts.Label.Opacity = selected ? 1 : 0.7;
                parts.Label.FontWeight = selected ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
            }
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetItemStatus(button, selected ? "Current page" : "");
        }
    }

    private void RebuildCompactProjectFlyout()
    {
        if (_projectCompactFlyout is null)
        {
            return;
        }
        _projectCompactFlyout.Items.Clear();
        foreach (var item in ProjectSelector.Items.OfType<ComboBoxItem>())
        {
            var id = item.Tag as string ?? "";
            var name = item.Content?.ToString() ?? id;
            var menuItem = new MenuFlyoutItem { Text = name, Tag = id };
            if (ReferenceEquals(ProjectSelector.SelectedItem, item))
            {
                menuItem.Icon = new FontIcon { Glyph = "\uE73E" };
            }
            menuItem.Click += CompactProjectItem_Click;
            _projectCompactFlyout.Items.Add(menuItem);
        }
        if (_projectCompactFlyout.Items.Count == 0)
        {
            _projectCompactFlyout.Items.Add(new MenuFlyoutItem { Text = "No projects", IsEnabled = false });
        }
    }

    private void CompactProjectItem_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not MenuFlyoutItem menu || menu.Tag is not string id || id.Length == 0)
        {
            return;
        }
        var previous = _shellProjectId;
        ShellProjects.SelectProjectId(ProjectSelector, id, suppressEvents: false);
        _shellProjectId = ShellState.Current.ProjectId;
        PageCache.InvalidateProject(previous, _shellProjectId);
        UpdateCompactProjectTooltip();
        _ = LoadChatsAsync();
        if (ContentFrame.Content is SessionPage) ShowTimeline();
    }

    private void UpdateCompactProjectTooltip()
    {
        var label = ShellState.Current.ProjectName;
        if (label.Length == 0)
        {
            label = "Choose project";
        }
        ToolTipService.SetToolTip(ProjectCompactButton, "Project: " + label);
    }

    private void ResizeChatPane()
    {
        if (Nav.ActualHeight <= 0 || ChatPane.Visibility != Visibility.Visible) return;
        var footer = Math.Max(48, FooterIcons.ActualHeight + FooterIcons.Margin.Top + FooterIcons.Margin.Bottom);
        var top = ChatPane.TransformToVisual(Nav).TransformPoint(new Windows.Foundation.Point()).Y;
        var height = SidebarLayout.ChatHeight(Nav.ActualHeight, top, footer);
        if (double.IsNaN(ChatPane.Height) || Math.Abs(ChatPane.Height - height) > 0.5)
        {
            ChatPane.Height = height;
            ChatPane.MaxHeight = height;
        }
    }

    private void ChatSearch_TextChanged(object sender, TextChangedEventArgs e)
    {
        // Narrow what is already loaded on every keystroke; the server search
        // (which also matches message text) follows after a short pause.
        _chatRefreshing = true;
        ShowLocalChats(_projectFilter, ChatQuery);
        _chatSearchTimer ??= DispatcherQueue.CreateTimer();
        _chatSearchTimer.Interval = TimeSpan.FromMilliseconds(250);
        _chatSearchTimer.IsRepeating = false;
        _chatSearchTimer.Tick -= ChatSearchTimer_Tick;
        _chatSearchTimer.Tick += ChatSearchTimer_Tick;
        _chatSearchTimer.Stop();
        _chatSearchTimer.Start();
    }

    private void ChatSearchTimer_Tick(DispatcherQueueTimer sender, object args)
    {
        sender.Stop();
        _ = LoadChatsAsync();
    }

    private async void MoreChatsButton_Click(object sender, RoutedEventArgs e) => await LoadMoreChatsAsync();

    public void RefreshChats() => _ = LoadChatsAsync(force: true);

    /// Readable title the sidebar computed for a chat ("" when not listed).
    public string ChatTitleFor(string sessionId) =>
        _chatEntries.FirstOrDefault(entry => entry.Id == sessionId)?.Title ?? "";

    private string ChatQuery => ChatSearch.Text.Trim();

    private ProjectSwitcher _projectSwitcher = null!;

    private IReadOnlyList<(string Id, string Name)> SwitcherProjects() =>
        ProjectSelector.Items.OfType<ComboBoxItem>().Select(i => (i.Tag as string ?? "", i.Content?.ToString() ?? "")).Where(p => p.Item1.Length > 0).ToList();

    // Chats in the sidebar per project, from the unfiltered list when it is loaded.
    private int ChatCountFor(string projectId)
    {
        var source = _chatCache.TryGetValue(ChatCacheKey("", ""), out var all) ? all.Entries : _chatEntries;
        return projectId.Length == 0 ? source.Count : source.Count(e => string.Equals(e.ProjectId, projectId, StringComparison.OrdinalIgnoreCase));
    }

    // The switcher's choice from the last run ("" = all projects).
    private void RestoreProjectChoice()
    {
        if (ProjectSwitcher.SavedFilter is { } saved && (saved.Length == 0 || ProjectName(saved).Length > 0) && saved != _projectFilter)
            SetProjectFilter(saved);
        _projectSwitcher.Refresh();
    }

    private void NewChat_Invoked(KeyboardAccelerator sender, KeyboardAcceleratorInvokedEventArgs args)
    {
        args.Handled = true;
        NewChatButton_Click(NewChatButton, new RoutedEventArgs());
    }

    /// Projects in the account (id, name), for pickers outside the sidebar.
    public IReadOnlyList<(string Id, string Name)> Projects => SwitcherProjects();

    private bool _targetChosen;

    /// Sets where new chats start (Home's project chip) without changing the
    /// sidebar's "All projects" view.
    public void ChooseTargetProject(string projectId)
    {
        _targetChosen = true;
        SelectTargetProject(projectId);
    }

    private void SelectTargetProject(string projectId)
    {
        if (projectId.Length == 0 || projectId == ShellState.Current.ProjectId) return;
        var previous = _shellProjectId;
        _projectSync = true;
        try { ShellProjects.SelectProjectId(ProjectSelector, projectId); }
        finally { _projectSync = false; }
        _shellProjectId = ShellState.Current.ProjectId;
        PageCache.InvalidateProject(previous, _shellProjectId);
        RebuildCompactProjectFlyout();
        UpdateCompactProjectTooltip();
        _projectSwitcher.Refresh();
    }

    // With "All projects", new chats start in the project being worked in
    // now, not in whatever project last session's prefs restored.
    // Not while the first-run guide is up: the account (and so the chats the
    // pick is made from) only exists once setup finishes.
    private void ApplyDefaultTarget(List<ChatPaneEntry> entries)
    {
        if (_targetChosen || _setup is not null) return;
        _targetChosen = true;
        var known = SwitcherProjects().Select(p => p.Id).ToHashSet(StringComparer.OrdinalIgnoreCase);
        var pick = DefaultProject.Pick(entries.Select(e => e.ProjectId), AppPrefs.Data.ProjectFolders, App.CaptureWorkspaceRoot(), known);
        SelectTargetProject(pick);
    }

    private void ProjectSwitch_Invoked(KeyboardAccelerator sender, KeyboardAcceleratorInvokedEventArgs args)
    {
        args.Handled = true;
        _projectSwitcher.Open(Nav.IsPaneOpen ? null : ProjectCompactButton);
    }

    /// Turn count the chat list reported for a chat (-1 when unknown).
    public long ChatTurnCountFor(string sessionId) =>
        _chatEntries.FirstOrDefault(entry => entry.Id == sessionId)?.TurnCount ?? -1;

    // Chats shown first and per "More chats": most timeline rows are often
    // sub-agent runs (shown inside their parent chat, not here), so pages are
    // fetched until this many chats are visible.
    private const int ChatPageTarget = 15;
    private const int ChatFetchLimit = 100;
    private const int MaxChatPagesPerFill = 8;
    private string _tagFilter = "";

    private bool PassesTagFilter(ChatPaneEntry entry) => _tagFilter.Length == 0 || ChatTags.Has(entry.Id, _tagFilter);

    private int VisibleChatCount(IEnumerable<ChatPaneEntry> entries) => entries.Count(PassesTagFilter);

    // Fetches further pages into `entries` until `target` chats are visible
    // or the timeline ends. Returns the cursor after the last page read.
    private async Task<string> FillChatsAsync(string filter, string query, List<ChatPaneEntry> entries, string cursor, int target, Func<bool> stale, Action? progress = null)
    {
        for (var page = 0; page < MaxChatPagesPerFill && cursor.Length > 0 && VisibleChatCount(entries) < target; page++)
        {
            var response = await FetchChatsAsync(filter, query, cursor);
            await UiThread.Resume(DispatcherQueue);
            if (stale() || !response.Ok) break;
            cursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
            AppendChats(response, filter, entries);
            progress?.Invoke();
        }
        return cursor;
    }

    private void ChatScroll_ViewChanged(object? sender, ScrollViewerViewChangedEventArgs e)
    {
        // Infinite scroll: the next chats load as the end of the list comes into view.
        if (ChatScroll.ScrollableHeight <= 0 || ChatScroll.VerticalOffset < ChatScroll.ScrollableHeight - 160) return;
        if (!_chatLoading && _chatCursor.Length > 0) _ = LoadMoreChatsAsync();
    }

    private static string ChatCacheKey(string filter, string query) => filter + "\n" + query;

    private static bool ChatMatches(ChatPaneEntry entry, string filter, string query) =>
        (filter.Length == 0 || string.Equals(entry.ProjectId, filter, StringComparison.OrdinalIgnoreCase)) &&
        (query.Length == 0 ||
         entry.Title.Contains(query, StringComparison.OrdinalIgnoreCase) ||
         entry.Summary.Contains(query, StringComparison.OrdinalIgnoreCase) ||
         entry.Match.Contains(query, StringComparison.OrdinalIgnoreCase) ||
         ChatTitles.AgentName(entry.Agent).Contains(query, StringComparison.OrdinalIgnoreCase));

    // Paints the list for (filter, query) from memory, never waiting on the
    // network: the cached answer for exactly that key, else the chats already
    // loaded narrowed on the client. The server answer reconciles it later.
    private void ShowLocalChats(string filter, string query)
    {
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var key = ChatCacheKey(filter, query);
        var exact = _chatCache.TryGetValue(key, out var hit);
        List<ChatPaneEntry> shown;
        if (exact)
        {
            shown = hit!.Entries;
            _chatCursor = hit.Cursor;
        }
        else
        {
            var source = _chatCache.TryGetValue(ChatCacheKey(filter, ""), out var unsearched) ? unsearched.Entries
                : _chatCache.TryGetValue(ChatCacheKey("", ""), out var all) ? all.Entries
                : _chatEntries.ToList();
            shown = source.Where(entry => ChatMatches(entry, filter, query)).ToList();
            _chatCursor = "";
        }
        _chatEntries.Clear();
        _chatEntries.AddRange(shown);
        if (exact || shown.Count > 0) _chatStatus = "";
        UpdateProjectFilterLabel();
        RenderChats();
        System.Diagnostics.Trace.WriteLine($"[perf] chats: filter-to-first-render ({(exact ? "cached" : "client-filtered")}, {shown.Count} rows) {watch.ElapsedMilliseconds} ms");
    }

    private async Task LoadChatsAsync(bool force = false)
    {
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var filter = _projectFilter;
        var query = ChatQuery;
        var key = ChatCacheKey(filter, query);
        if (force)
        {
            // Something changed on the server: every cached list may be stale,
            // but each still paints instantly until its own refresh lands.
            foreach (var cachedKey in _chatCache.Keys.ToList())
                _chatCache[cachedKey] = _chatCache[cachedKey] with { At = DateTime.MinValue };
        }
        var fresh = _chatCache.TryGetValue(key, out var hit) && DateTime.UtcNow - hit.At < ChatCacheFresh;
        if (!force && !fresh && key == _chatInFlightKey)
        {
            // The same request is already running; just repaint from memory.
            ShowLocalChats(filter, query);
            return;
        }
        var version = ++_chatLoadVersion;
        _chatRefreshing = !fresh;
        ShowLocalChats(filter, query);
        if (fresh)
        {
            _chatInFlightKey = "";
            FinishChatLoading(version);
            return;
        }
        // A stale request cannot be stopped once nx_call runs, but one that has
        // not started yet is dropped, and any late answer is ignored below.
        _chatFetchCts?.Cancel();
        var cts = _chatFetchCts = new CancellationTokenSource();
        _chatInFlightKey = key;
        _chatLoading = true;
        MoreChatsButton.IsEnabled = true;
        try
        {
            NxResponse response;
            try { response = await FetchChatsAsync(filter, query, "", cts.Token); }
            catch (OperationCanceledException) { return; }
            await UiThread.Resume(DispatcherQueue);
            if (version != _chatLoadVersion) return;
            _chatRefreshing = false;
            System.Diagnostics.Trace.WriteLine($"[perf] chats: server answer {watch.ElapsedMilliseconds} ms (ok={response.Ok})");
            if (!response.Ok)
            {
                // Unreachable: the banner says so once; the list keeps what it has.
                _chatStatus = NxErrors.IsUnreachable(response.RawError)
                    ? (_chatEntries.Count > 0 ? "" : "Your chats appear here when Nexus reconnects.")
                    : response.Error ?? "Could not load chats.";
                RenderChats();
                return;
            }
            var entries = new List<ChatPaneEntry>();
            AppendChats(response, filter, entries);
            var cursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
            // The search box may have moved on; its own request reconciles.
            if (filter != _projectFilter || query != ChatQuery) { StoreChatCache(key, entries, cursor); return; }
            _chatStatus = "";
            _chatCursor = cursor;
            _chatEntries.Clear();
            _chatEntries.AddRange(entries);
            RenderChats();
            if (filter.Length == 0 && query.Length == 0) ApplyDefaultTarget(entries);
            System.Diagnostics.Trace.WriteLine($"[perf] chats: first page {watch.ElapsedMilliseconds} ms, {VisibleChatCount(entries)} visible");
            if (VisibleChatCount(entries) < ChatPageTarget && cursor.Length > 0)
            {
                bool Stale() => version != _chatLoadVersion || filter != _projectFilter || query != ChatQuery;
                cursor = await FillChatsAsync(filter, query, entries, cursor, ChatPageTarget, Stale, () =>
                {
                    _chatEntries.Clear();
                    _chatEntries.AddRange(entries);
                    RenderChats();
                });
                if (Stale()) return;
                _chatCursor = cursor;
                RenderChats();
                System.Diagnostics.Trace.WriteLine($"[perf] chats: {VisibleChatCount(entries)} visible after fill {watch.ElapsedMilliseconds} ms");
            }
            StoreChatCache(key, entries, cursor);
        }
        finally
        {
            if (version == _chatLoadVersion)
            {
                _chatInFlightKey = "";
                _chatRefreshing = false;
            }
            FinishChatLoading(version);
        }
    }

    private void StoreChatCache(string key, List<ChatPaneEntry> entries, string cursor)
    {
        _chatCache[key] = new ChatCacheEntry(entries.ToList(), cursor, DateTime.UtcNow);
        if (_chatCache.Count <= 40) return;
        var oldest = _chatCache.Where(pair => pair.Key != key).OrderBy(pair => pair.Value.At).First().Key;
        _chatCache.Remove(oldest);
    }

    // All projects use the cross-project timeline; if the server insists on a
    // project, fall back to the selected one so the list is never empty.
    private static async Task<NxResponse> FetchChatsAsync(string filter, string q, string cursor, CancellationToken ct = default)
    {
        if (filter.Length > 0)
            return await App.Core.CallAsync(NxMethods.TimelineList, cursor.Length > 0
                ? new { project_id = filter, q, limit = ChatFetchLimit, cursor } : (object)new { project_id = filter, q, limit = ChatFetchLimit }, ct);
        var all = await App.Core.CallAsync(NxMethods.TimelineList, cursor.Length > 0 ? new { q, limit = ChatFetchLimit, cursor } : (object)new { q, limit = ChatFetchLimit }, ct);
        var selected = ShellState.Current.ProjectId;
        if (all.Ok || !ProjectScope.LooksLikeUuid(selected) || ct.IsCancellationRequested) return all;
        return await App.Core.CallAsync(NxMethods.TimelineList, cursor.Length > 0
            ? new { project_id = selected, q, limit = ChatFetchLimit, cursor } : (object)new { project_id = selected, q, limit = ChatFetchLimit }, ct);
    }

    private void FinishChatLoading(int version)
    {
        if (version != _chatLoadVersion) return;
        _chatLoading = false;
        MoreChatsButton.IsEnabled = true;
    }

    private async Task LoadMoreChatsAsync()
    {
        if (_chatLoading || _chatCursor.Length == 0) return;
        var version = _chatLoadVersion;
        var filter = _projectFilter;
        var query = ChatQuery;
        _chatLoading = true;
        MoreChatsButton.IsEnabled = false;
        try
        {
            var response = await FetchChatsAsync(filter, query, _chatCursor);
            await UiThread.Resume(DispatcherQueue);
            if (version != _chatLoadVersion || filter != _projectFilter || query != ChatQuery) return;
            if (!response.Ok)
            {
                ToolTipService.SetToolTip(MoreChatsButton, response.Error ?? "Could not load more chats.");
                return;
            }
            _chatCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
            var before = VisibleChatCount(_chatEntries);
            AppendChats(response, filter, _chatEntries);
            RenderChats();
            _chatCursor = await FillChatsAsync(filter, query, _chatEntries, _chatCursor, before + ChatPageTarget,
                () => version != _chatLoadVersion || filter != _projectFilter || query != ChatQuery, RenderChats);
            StoreChatCache(ChatCacheKey(filter, query), _chatEntries, _chatCursor);
            RenderChats();
        }
        finally
        {
            FinishChatLoading(version);
        }
    }

    // Capture once used a transcript file's name as the chat id for stores
    // that hold many chats (state.vscdb, opencode.db, ...) or keep the id in
    // the folder (brain transcript*.jsonl). Those rows are never one chat.
    private static readonly HashSet<string> FileNameChatIds = new(StringComparer.OrdinalIgnoreCase)
    {
        "state", "opencode", "ai-code-tracking", "transcript", "transcript_full", "harvest-smoke-test"
    };

    // A chat with no turns is hidden, but only when the server marks its
    // counts exact (older servers sent 0 for every chat), and never a chat
    // started in Nexus (native id "nexus-…"), which is empty until the first
    // message is sent.
    private static bool IsEmptyCapturedChat(JsonElement item, bool exactCounts)
    {
        if (item.ValueKind != JsonValueKind.Object) return false;
        var native = NxJson.Text(item, "native_id", "nativeId").Trim();
        if (FileNameChatIds.Contains(native)) return true;
        if (!exactCounts || native.StartsWith("nexus-", StringComparison.Ordinal)) return false;
        return item.TryGetProperty("turn_count", out var count)
            && count.ValueKind == JsonValueKind.Number && count.TryGetInt64(out var n) && n == 0;
    }

    private static void AppendChats(NxResponse response, string filter, List<ChatPaneEntry> into)
    {
        var seen = new HashSet<string>(into.Select(entry => entry.Id), StringComparer.Ordinal);
        var exactCounts = response.Result is { ValueKind: JsonValueKind.Object } result
            && NxJson.Text(result, "turn_counts") == "exact";
        foreach (var item in NxJson.Items(response.Result))
        {
            if (filter.Length > 0 && !ProjectScope.TimelineItemMatches(item, filter)) continue;
            if (SessionLineage.IsSubagentSession(item)) continue;
            if (IsEmptyCapturedChat(item, exactCounts)) continue;
            var id = NxJson.Text(item, "id", "session_id", "sessionId");
            if (id.Length == 0 || !seen.Add(id)) continue;
            var agent = NxJson.Text(item, "agent", "harness");
            // The title names the chat; a search match is shown separately so
            // a result keeps its name instead of a fragment of some message.
            var summary = ChatTitles.Clean(NxJson.Text(item, "summary", "preview"));
            var match = ChatTitles.Clean(NxJson.Text(item, "match_preview"));
            // The server cuts a window around the hit; drop the partial word
            // at a cut edge ("…arate driver" -> "driver").
            if (match.StartsWith('…') || match.StartsWith("...", StringComparison.Ordinal))
            {
                match = match.TrimStart('…', '.');
                var space = match.IndexOf(' ');
                if (space > 0 && space < 20) match = match[(space + 1)..];
            }
            match = match.TrimEnd('…', '.', ' ').Trim();
            var rawTitle = NxJson.Text(item, "title");
            if (rawTitle == NxJson.Text(item, "native_id")) rawTitle = "";
            var title = ChatTitles.Title(rawTitle, summary.Length > 0 ? summary : match, agent);
            into.Add(new ChatPaneEntry(id, title, agent, summary,
                NxJson.Text(item, "last_active_at", "updated_at", "captured_at"),
                SessionLineage.IsForkSession(item), NxJson.Text(item, "parent_session_id", "parentSessionId"),
                NxJson.Text(item, "project_id", "projectId"), match,
                long.TryParse(NxJson.Text(item, "turn_count"), out var turns) ? turns : -1));
        }
    }

    private static readonly Brush TransparentRow = new SolidColorBrush(Microsoft.UI.Colors.Transparent);

    // Sidebar in the style of T3 Code: one flat list of chats across projects.
    // Each row: project badge + name + time / title / agent. Rows are built
    // once per entry and reused, so filtering re-parents existing rows instead
    // of building every control again.
    //
    // The list is an ItemsRepeater: RenderChats only hands it the item list
    // (entries plus status lines); ChatRowFactory builds/reuses a row when
    // it scrolls into view, so a render or a pane width/visibility change
    // only measures the rows on screen, not every chat.
    private void RenderChats()
    {
        var watch = System.Diagnostics.Stopwatch.StartNew();
        _shownChatRows.Clear();
        _renderShowMatch = ChatQuery.Length > 0;
        var items = new List<object>(_chatEntries.Count + 2);
        if (_chatRefreshing && _chatEntries.Count > 0)
            items.Add(new ChatListNotice("Updating…"));
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (var entry in _chatEntries)
        {
            if (PassesTagFilter(entry) && seen.Add(entry.Id)) items.Add(entry);
        }
        if (_chatStatus.Length > 0)
            items.Add(new ChatListNotice(NxErrors.IsSignedOut(_chatStatus) ? NxErrors.SignedOutMessage : _chatStatus));
        else if (seen.Count == 0)
            items.Add(new ChatListNotice(_chatRefreshing ? LoadingNotice : _tagFilter.Length > 0 ? $"No loaded chats are tagged #{_tagFilter}." : ChatQuery.Length > 0 ? "No chats match your search." : "No chats yet. Start one with the pencil above, or press Ctrl+N."));
        _chatItems = items;
        ChatList.ItemTemplate ??= new ChatRowFactory(this);
        ChatList.ItemsSource = items;
        MoreChatsButton.Visibility = _chatCursor.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        System.Diagnostics.Trace.WriteLine($"[perf] chats: render {seen.Count} rows (virtualized) {watch.Elapsed.TotalMilliseconds:0.0} ms");
    }

    private List<object> _chatItems = new();
    private bool _renderShowMatch;

    // A status line in the chat list ("Updating…", errors, empty states).
    private sealed record ChatListNotice(string Text);

    // Row for one item, reusing the cached row for (entry, showMatch). A
    // cached row still live in the repeater (e.g. kept realized because it
    // has focus) is never handed out twice; a fresh one is built instead.
    private const string LoadingNotice = "\u0001loading";
    // The first rows shown after launch rise in, once.
    private int _entranceBudget = 12;

    private UIElement ChatListElement(object? data)
    {
        if (data is not ChatPaneEntry entry)
        {
            var text = (data as ChatListNotice)?.Text ?? data?.ToString() ?? "";
            return text == LoadingNotice ? Skeleton.ChatRows() : ChatPaneMessage(text);
        }
        var key = (entry, _renderShowMatch);
        if (!_chatRows.TryGetValue(key, out var row) || _liveChatRows.Contains(row.Button))
        {
            if (_chatRows.Count > 300) _chatRows.Clear();
            row = ChatRow(entry, _renderShowMatch);
            _chatRows[key] = row;
            if (_entranceBudget > 0) Motion.FadeIn(row.Button, 6, (12 - _entranceBudget--) * 22, TimeSpan.FromMilliseconds(200));
        }
        if (row.Button.Parent is Panel stale) stale.Children.Remove(row.Button);
        _liveChatRows.Add(row.Button);
        var currentId = (ContentFrame.Content as SessionPage)?.SessionId ?? "";
        ApplyChatRowSelection(row, entry.Id == currentId);
        _shownChatRows[entry.Id] = row;
        return row.Button;
    }

    private void ChatListRecycle(UIElement element, UIElement? parent)
    {
        _liveChatRows.Remove(element);
        // Detach so a cached row can be re-parented later; the repeater
        // re-adds whatever GetElement returns.
        if (parent is Panel panel)
        {
            var index = panel.Children.IndexOf(element);
            if (index >= 0) panel.Children.RemoveAt(index);
        }
    }

    private readonly HashSet<UIElement> _liveChatRows = new(ReferenceEqualityComparer.Instance);

    private sealed partial class ChatRowFactory : IElementFactory
    {
        private readonly MainWindow _owner;
        public ChatRowFactory(MainWindow owner) => _owner = owner;
        public UIElement GetElement(ElementFactoryGetArgs args) => _owner.ChatListElement(args.Data);
        public void RecycleElement(ElementFactoryRecycleArgs args) => _owner.ChatListRecycle(args.Element, args.Parent);
    }

    private static void ApplyChatRowSelection((Button Button, TextBlock Title) row, bool selected)
    {
        row.Button.Background = selected ? NexusPalette.UserFill : TransparentRow;
        row.Title.Opacity = selected ? 1 : 0.88;
    }

    // Opening a chat only recolours rows; rebuilding the whole list on every
    // click held the UI thread before the chat could start loading.
    private void UpdateChatSelection()
    {
        var currentId = (ContentFrame.Content as SessionPage)?.SessionId ?? "";
        foreach (var (id, row) in _shownChatRows) ApplyChatRowSelection(row, id == currentId);
    }

    private string ProjectName(string projectId) =>
        ProjectSelector.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (i.Tag as string) == projectId)?.Content?.ToString() ?? "";

    private void BuildProjectFilterMenu(MenuFlyout menu)
    {
        menu.Items.Clear();
        // Projects are chosen in the project switcher above the list; this
        // menu filters by tag (chat tags are kept on this device; see ChatTags).
        var tags = ChatTags.All();
        if (tags.Count == 0 && _tagFilter.Length == 0)
        {
            menu.Items.Add(new MenuFlyoutItem { Text = "No tags yet. Right-click a chat to tag it.", IsEnabled = false });
            return;
        }
        void AddTag(string label, string tag)
        {
            var item = new ToggleMenuFlyoutItem { Text = label, IsChecked = _tagFilter == tag };
            item.Click += (_, _) => { _tagFilter = tag; UpdateProjectFilterLabel(); RenderChats(); };
            menu.Items.Add(item);
        }
        AddTag("Any tag", "");
        foreach (var tag in tags.Take(20)) AddTag("#" + tag, tag);
    }

    private void SetProjectFilter(string projectId)
    {
        _projectFilter = projectId;
        if (projectId.Length > 0) _targetChosen = true;
        if (projectId.Length > 0 && projectId != ShellState.Current.ProjectId)
        {
            // New chats and Home follow the filtered project, without leaving
            // the chat that is open.
            var previous = _shellProjectId;
            _projectSync = true;
            try { ShellProjects.SelectProjectId(ProjectSelector, projectId); }
            finally { _projectSync = false; }
            _shellProjectId = ShellState.Current.ProjectId;
            PageCache.InvalidateProject(previous, _shellProjectId);
            RebuildCompactProjectFlyout();
            UpdateCompactProjectTooltip();
        }
        // Home's header follows the filter ("All projects" included).
        ShellState.Current.SetChatFilter(projectId);
        _ = LoadChatsAsync();
    }

    private void UpdateProjectFilterLabel()
    {
        // The project shows in the switcher; only the tag filter is named here.
        var name = _tagFilter.Length > 0 ? "Tagged #" + _tagFilter : "";
        _projectSwitcher?.Refresh();
        ProjectFilterLabel.Text = name.Length > 0 ? name : "";
        ProjectFilterLabel.Visibility = name.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private void FooterNav_Click(object sender, RoutedEventArgs e)
    {
        if (sender is not Button { Tag: string tag }) return;
        if (tag == "teleport") { OpenTeleport(); return; }
        AppPrefs.LastNavTag = tag;
        _navLock = true;
        Nav.SelectedItem = tag switch { "home" => HomeItem, "memory" => MemoryItem, "settings" => SettingsItem, _ => TimelineItem };
        _navLock = false;
        ShowPage(CreatePageForTag(tag));
    }

    private (Button Button, TextBlock Title) ChatRow(ChatPaneEntry entry, bool showMatch)
    {
        var projectName = ProjectName(entry.ProjectId);
        var lines = new Grid { RowSpacing = 2 };
        lines.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        lines.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        lines.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });

        // Line 1: project badge, project name, time
        var top = new Grid { ColumnSpacing = 6 };
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        if (projectName.Length > 0)
        {
            top.Children.Add(ProjectBadge(projectName));
            var name = new TextBlock { Text = projectName, FontSize = 12, Opacity = 0.5, TextTrimming = TextTrimming.CharacterEllipsis, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(name, 1);
            top.Children.Add(name);
        }
        var when = ChatDate(entry.When);
        if (when.Length > 0)
        {
            var time = new TextBlock { Text = when, FontSize = 12, Opacity = 0.45, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(time, 2);
            top.Children.Add(time);
        }
        lines.Children.Add(top);

        // Line 2: title
        var title = new TextBlock
        {
            Text = NxJson.Scrub(entry.Title), FontSize = 13.5, Opacity = 0.88,
            TextTrimming = TextTrimming.CharacterEllipsis, TextWrapping = TextWrapping.NoWrap
        };
        Grid.SetRow(title, 1);
        lines.Children.Add(title);

        // Line 3: agent
        var bottom = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        bottom.Children.Add(new Microsoft.UI.Xaml.Shapes.Ellipse { Width = 6, Height = 6, Fill = AgentBrush(entry.Agent), VerticalAlignment = VerticalAlignment.Center });
        bottom.Children.Add(new TextBlock { Text = ChatTitles.AgentName(entry.Agent), FontSize = 12, Opacity = 0.45 });
        if (entry.Forked) bottom.Children.Add(new TextBlock { Text = "· fork", FontSize = 12, Opacity = 0.4 });
        if (ChatTags.For(entry.Id) is { Count: > 0 } chatTags)
            bottom.Children.Add(new TextBlock { Text = "· " + string.Join(" ", chatTags.Select(t => "#" + t)), FontSize = 12, Opacity = 0.75, Foreground = NexusPalette.Accent, MaxWidth = 170, TextTrimming = TextTrimming.CharacterEllipsis });
        if (entry.Match.Length > 0 && showMatch)
            bottom.Children.Add(new TextBlock { Text = "· " + entry.Match, FontSize = 12, Opacity = 0.6, MaxWidth = 230, TextTrimming = TextTrimming.CharacterEllipsis });
        Grid.SetRow(bottom, 2);
        lines.Children.Add(bottom);

        var button = new Button
        {
            Content = lines, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
            Padding = new Thickness(10, 8, 10, 8), CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(0),
            Background = TransparentRow
        };
        var summary = entry.Summary.Length > 240 ? entry.Summary[..240] + "…" : entry.Summary;
        ToolTipService.SetToolTip(button, entry.Title + (summary.Length > 0 && summary != entry.Title ? "\n\n" + summary : ""));
        button.Click += (_, _) =>
        {
            Motion.PrepareChatTitle(title);
            OpenSession(entry.Id, ChatSearch.Text.Trim());
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, entry.Title + (projectName.Length > 0 ? ", " + projectName : ""));
        Motion.AddPressFeedback(button);
        var rowMenu = new MenuFlyout();
        var editTags = new MenuFlyoutItem { Text = "Tags…", Icon = new FontIcon { Glyph = "\uE8EC" } };
        editTags.Click += async (_, _) => await ChatTags.EditAsync(button.XamlRoot, entry.Id, entry.Title);
        rowMenu.Items.Add(editTags);
        button.ContextFlyout = rowMenu;
        return (button, title);
    }

    // Two-letter project tag in a stable colour, like T3 Code's "HR"/"HA".
    private static Border ProjectBadge(string name)
    {
        var letters = new string(name.Where(char.IsLetterOrDigit).Take(1).ToArray());
        var words = name.Split(new[] { ' ', '-', '_', '.', '·' }, StringSplitOptions.RemoveEmptyEntries);
        letters += words.Length > 1 ? char.ToUpperInvariant(words[1][0]).ToString() : name.Skip(1).FirstOrDefault(char.IsLetterOrDigit).ToString();
        letters = letters.Trim('\0').ToUpperInvariant();
        var palette = new[]
        {
            Windows.UI.Color.FromArgb(255, 0x34, 0xD3, 0x99), Windows.UI.Color.FromArgb(255, 0x8E, 0xA2, 0xFF),
            Windows.UI.Color.FromArgb(255, 0xF4, 0xA2, 0x61), Windows.UI.Color.FromArgb(255, 0xC0, 0x84, 0xFC),
            Windows.UI.Color.FromArgb(255, 0x5E, 0xC8, 0xE5), Windows.UI.Color.FromArgb(255, 0xF8, 0x71, 0x71)
        };
        var hash = 0;
        foreach (var c in name) hash = (hash * 31 + c) & 0x7fffffff;
        var color = NexusPalette.BadgeInk(palette[hash % palette.Length]);
        return new Border
        {
            Background = new SolidColorBrush(Windows.UI.Color.FromArgb(38, color.R, color.G, color.B)),
            CornerRadius = new CornerRadius(3), Padding = new Thickness(3, 0, 3, 1), VerticalAlignment = VerticalAlignment.Center,
            Child = new TextBlock
            {
                Text = letters.Length > 0 ? letters : "?", FontSize = 8.5, FontWeight = Microsoft.UI.Text.FontWeights.Bold,
                FontFamily = new FontFamily("Cascadia Mono, Consolas"), Foreground = new SolidColorBrush(color)
            }
        };
    }

    private static Button SidebarButton(UIElement content, bool selected, double minHeight, double leftPad) => new()
    {
        Content = content, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
        Padding = new Thickness(leftPad, 4, 8, 4), CornerRadius = new CornerRadius(6), MinHeight = minHeight,
        Background = selected ? NexusPalette.AccentFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent),
        BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0)
    };

    internal static Brush AgentBrush(string agent) => new SolidColorBrush((agent ?? "").Trim().ToLowerInvariant() switch
    {
        "claude" or "claude-code" => Windows.UI.Color.FromArgb(255, 0xD9, 0x77, 0x57),
        "codex" => Windows.UI.Color.FromArgb(255, 0x6E, 0xC1, 0x8F),
        "cursor" or "agent" => Windows.UI.Color.FromArgb(255, 0x9A, 0xA4, 0xB5),
        "opencode" => Windows.UI.Color.FromArgb(255, 0xE8, 0xBB, 0x67),
        "gemini" => Windows.UI.Color.FromArgb(255, 0x83, 0xB6, 0xFF),
        _ => Windows.UI.Color.FromArgb(255, 0x8A, 0x8F, 0x98)
    });

    private static string ChatDate(string raw)
    {
        if (!DateTimeOffset.TryParse(raw, out var date)) return "";
        var local = date.ToLocalTime();
        return local.Date == DateTime.Today ? local.ToString("h:mm tt") :
            local.Year == DateTime.Today.Year ? local.ToString("MMM d") : local.ToString("MMM d, yy");
    }

    private static TextBlock ChatPaneMessage(string message, bool indent = false) => new()
    {
        Text = message, TextWrapping = TextWrapping.Wrap, Opacity = 0.62,
        FontSize = 12, Margin = new Thickness(indent ? 32 : 8, 6, 8, 4)
    };

    // New chat opens a draft (Home's start composer) with the agent last
    // chosen there. The session is created only when the first message is
    // sent, so an empty chat never appears in the list.
    private void NewChatButton_Click(object sender, RoutedEventArgs e)
    {
        if (!ProjectScope.LooksLikeUuid(ShellState.Current.ProjectId))
        {
            _chatStatus = "Choose a project before starting a chat.";
            RenderChats();
            return;
        }
        ShowDashboard();
        PageCache.Dashboard.FocusNewChat();
        UpdateChatSelection();
    }

    private void OnContentFrameLoaded(object sender, RoutedEventArgs e)
    {
        ContentFrame.Loaded -= OnContentFrameLoaded;
        ApplyCaptionClearance();
        if (ContentFrame.XamlRoot is { } root) root.Changed += (_, _) => ApplyCaptionClearance();
        ApplyPendingLink();
    }

    // The content is extended into the title bar, so the top caption band
    // (drag area plus min/max/close) is not clickable. Every page starts
    // below it; pages keep their own header padding on top of this.
    private void ApplyCaptionClearance()
    {
        var scale = ContentFrame.XamlRoot?.RasterizationScale is double s && s > 0 ? s : 1.0;
        var height = AppWindow.TitleBar.Height > 0 ? AppWindow.TitleBar.Height / scale : 32;
        // Page headers already pad ~14px at the top; trim the overlap so the
        // first control lands just under the caption buttons, not far below.
        var top = Math.Max(0, Math.Round(height) - 8);
        if (Math.Abs(ContentFrame.Margin.Top - top) > 0.5)
            ContentFrame.Margin = new Thickness(0, top, 0, 0);
    }

    private void ShowPage(NexusPage page, object? parameter = null)
    {
        // Setup owns the window until it is finished or skipped.
        if (_setup is not null) return;
        if (ReferenceEquals(ContentFrame.Content, page))
        {
            page.Enter(parameter);
            return;
        }
        page.Enter(parameter);
        ContentFrame.Content = page;
        AppDiagnostics.CurrentView = page.GetType().Name.Replace("Page", "").ToLowerInvariant();
        UpdateFooterSelection();
    }

    public void OpenSession(string sessionId, string? findQuery = null)
    {
        // In-app navigation must not call WindowExtensions.Show: it restores
        // a maximized window to its smaller normal bounds. Deep-link and tray
        // entry points already restore the hidden window explicitly.
        var page = PageCache.GetSession(sessionId);
        ShowPage(page, sessionId);
        if (!string.IsNullOrWhiteSpace(findQuery)) page.FindText(findQuery);
        else page.ClearFindForPlainOpen();
        UpdateChatSelection();
    }

    public void ShowTimeline(string? query = null)
    {
        _navLock = true;
        Nav.SelectedItem = TimelineItem;
        _navLock = false;
        AppPrefs.LastNavTag = "timeline";
        ShowPage(PageCache.HomeHub, new HubNavigation(HomeHubPage.SegmentSessions, query ?? ""));
    }

    public void ShowDashboard()
    {
        _navLock = true;
        Nav.SelectedItem = HomeItem;
        _navLock = false;
        ShowPage(PageCache.Dashboard);
    }

    public void ShowAgentSessions(string agent, string projectId = "")
    {
        if (projectId.Length > 0)
        {
            ShellProjects.SelectProjectId(ProjectSelector, projectId);
        }
        _navLock = true;
        Nav.SelectedItem = TimelineItem;
        _navLock = false;
        AppPrefs.LastNavTag = "timeline";
        var filter = new TimelineAgentFilter(agent);
        if (ContentFrame.Content is HomeHubPage hub)
        {
            hub.NavigateToSegment(HomeHubPage.SegmentSessions, filter);
            return;
        }
        ShowPage(PageCache.HomeHub, new HubNavigation(HomeHubPage.SegmentSessions, filter));
    }

    public void ShowSettings()
    {
        _navLock = true;
        Nav.SelectedItem = SettingsItem;
        _navLock = false;
        ShowPage(PageCache.Settings);
    }

    public void OpenTeleport(string? teleportId = null)
    {
        _navLock = true;
        Nav.SelectedItem = SharedItem;
        _navLock = false;
        AppPrefs.LastNavTag = "timeline";
        ShowPage(new TeleportPage(), teleportId ?? "");
    }

    public void HandleRemote(string payload)
    {
        var lines = payload.Split('\n', StringSplitOptions.RemoveEmptyEntries);
        if (lines.Any(line => line.Equals("--quit", StringComparison.OrdinalIgnoreCase)))
        { _ = QuitAsync(); return; }
        App.ReadDeepLink(lines);
        ApplyPendingLink();
        RestoreWindow();
    }

    private async void ApplyPendingLink()
    {
        if (_applyingPendingLink)
        {
            return;
        }
        _applyingPendingLink = true;
        try
        {
            var authUrl = App.PendingAuthUrl;
            if (_setup is not null)
            {
                // Links to chats wait until setup is done; sign-in does not.
                App.ClearPendingAuth();
            }
            else
            {
                if (App.PendingSessionId is { Length: > 0 } sessionId)
                {
                    OpenSession(sessionId);
                }
                else if (App.PendingTeleportId is { Length: > 0 } teleportId)
                {
                    OpenTeleport(teleportId);
                }
                App.ClearPending();
            }
            if (authUrl is not { Length: > 0 })
            {
                return;
            }
            var response = await App.Core.CallAsync(NxMethods.AuthCallback, new { url = authUrl });
            if (!response.Ok)
            {
                var dialog = new ContentDialog
                {
                    Title = "Sign-in failed",
                    Content = new TextBlock
                    {
                        Text = response.Error ?? "Could not finish sign-in.",
                        TextWrapping = TextWrapping.Wrap
                    },
                    CloseButtonText = "Close",
                    XamlRoot = Content.XamlRoot
                };
                _ = dialog.ShowThemedAsync();
                return;
            }
            await Task.Run(() => App.InitializeCore());
            _setup?.AccountChanged();
            await RefreshBannerAsync();
            PageCache.Settings.RefreshAccount();
            _chatCache.Clear();
            _ = LoadChatsAsync(force: true);
        }
        finally
        {
            _applyingPendingLink = false;
        }
    }

    private void OnClosing(AppWindow sender, AppWindowClosingEventArgs args)
    {
        if (_quitting)
        {
            return;
        }
        args.Cancel = true;
        WindowExtensions.Hide(this);
    }

    private void Nav_ItemInvoked(NavigationView sender, NavigationViewItemInvokedEventArgs args)
    {
        if (_navLock || args.InvokedItemContainer is not NavigationViewItem item || item.Tag is not string tag)
        {
            return;
        }
        if (tag == "teleport")
        {
            OpenTeleport();
            return;
        }
        AppPrefs.LastNavTag = tag;
        ShowPage(CreatePageForTag(tag));
    }

    private void RestoreWindow()
    {
        if (_quitting) return;
        WindowExtensions.Show(this);
        Activate();
    }

    private async Task PauseCaptureAsync()
    {
        var method = State.CapturePaused ? NxMethods.CaptureResume : NxMethods.CapturePause;
        var response = await App.Core.CallAsync(method);
        await UiThread.Resume(DispatcherQueue);
        if (!response.Ok)
        {
            PauseItem.Text = response.Error ?? "Pause capture";
            return;
        }
        State.CapturePaused = !State.CapturePaused;
        PauseItem.Text = State.CapturePaused ? "Resume capture" : "Pause capture";
    }

    private async Task QuitAsync()
    {
        if (_quitting) return;
        _quitting = true;
        DiagLog.Info("shutdown", "quit requested");
        // Never block WinUI's dispatcher in native shutdown. The tray creates
        // its own hidden windows; destroying the visible AppWindow alone is
        // not a process-exit guarantee, and exceptions used to be swallowed.
        var uiCleaned = new TaskCompletionSource(TaskCreationOptions.RunContinuationsAsynchronously);
        // Start the deadline before touching tray resources: even a blocked
        // shell/Explorer disposal must not defeat the user's explicit Quit.
        var shutdown = _shutdown.RunAsync(async () =>
        {
            App.Core.Shutdown();
            await uiCleaned.Task.ConfigureAwait(false);
        }, () => Environment.Exit(0), TimeSpan.FromSeconds(6));
        try
        {
            _chatSearchTimer?.Stop();
            try { Tray.Dispose(); } catch (Exception ex) { DiagLog.Warn("shutdown", "tray dispose failed", ex); }
            try { SingleInstance.Stop(); } catch (Exception ex) { DiagLog.Warn("shutdown", "single-instance stop failed", ex); }
        }
        finally { uiCleaned.TrySetResult(); }
        await shutdown;
    }

    private async void Search_Invoked(KeyboardAccelerator sender, KeyboardAcceleratorInvokedEventArgs args)
    {
        args.Handled = true;
        var box = new TextBox { PlaceholderText = "Sessions, memories, files, projects" };
        var dialog = new ContentDialog
        {
            Title = "Search",
            Content = box,
            PrimaryButtonText = "Search",
            CloseButtonText = "Close",
            XamlRoot = Content.XamlRoot,
            DefaultButton = ContentDialogButton.Primary
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary)
        {
            return;
        }
        ShowPage(new SearchPage(), box.Text);
    }

    private void OnCoreEvent(string json)
    {
        if (json.Contains("\"type\":\"desktop.request", StringComparison.Ordinal))
        {
            _ = HandleDesktopRequestAsync(json);
            return;
        }
        // Streaming chat deltas are consumed by SessionPage; refreshing the
        // global status banner for every token would flood the core bridge.
        if (json.Contains("\"type\":\"continue.progress\"", StringComparison.Ordinal))
        {
            return;
        }
        if (json.Contains("teleport", StringComparison.OrdinalIgnoreCase))
        {
            _ = RefreshInboxBadgeAsync();
        }
        _ = RefreshBannerAsync();
    }

    private async Task HandleDesktopRequestAsync(string json)
    {
        var ownsDialog = false;
        var requestId = "";
        try
        {
            using var doc = JsonDocument.Parse(json);
            var body = doc.RootElement;
            var id = NxJson.Text(body, "request_id");
            requestId = id;
            if (NxJson.Text(body, "type") == "desktop.request.closed")
            {
                if (_desktopApprovalId == id) _desktopApproval?.Hide();
                return;
            }
            if (_quitting || _desktopApproval is not null)
            {
                await App.Core.CallAsync("desktop.execution.approve", new { request_id = id, approved = false });
                return;
            }
            RestoreWindow();
            ownsDialog = true;
            _desktopApprovalId = id;
            var prompt = NxJson.Text(body, "prompt");
            var content = new StackPanel { Spacing = 12 };
            content.Children.Add(new TextBlock { Text = $"A browser signed in to your Nexus account requested {NxJson.Text(body, "agent")} on this device.", TextWrapping = TextWrapping.Wrap });
            content.Children.Add(new TextBlock { Text = NxJson.Text(body, "workspace"), TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
            var model = NxJson.Text(body, "model");
            content.Children.Add(new TextBlock { Text = "Model: " + (model.Length > 0 ? model : "the agent's default"), TextWrapping = TextWrapping.Wrap });
            content.Children.Add(new ScrollViewer { MaxHeight = 240, Content = new TextBlock { Text = prompt, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true } });
            // Images sent from the browser, already checked and saved here:
            // shown before anything runs so the person sees what goes in.
            if (body.TryGetProperty("images", out var images) && images.ValueKind == JsonValueKind.Array && images.GetArrayLength() > 0)
            {
                var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
                foreach (var image in images.EnumerateArray().Take(4))
                {
                    var path = image.GetString() ?? "";
                    if (path.Length == 0 || !System.IO.File.Exists(path)) continue;
                    var bitmap = new Microsoft.UI.Xaml.Media.Imaging.BitmapImage { DecodePixelWidth = 144, UriSource = new Uri(path) };
                    row.Children.Add(new Border { Width = 72, Height = 72, CornerRadius = new CornerRadius(8), Child = new Image { Source = bitmap, Stretch = Microsoft.UI.Xaml.Media.Stretch.UniformToFill } });
                }
                content.Children.Add(new TextBlock { Text = images.GetArrayLength() == 1 ? "With 1 image:" : $"With {images.GetArrayLength()} images:", Opacity = 0.75 });
                content.Children.Add(row);
            }
            content.Children.Add(new TextBlock { Text = "Allowing this turn lets the agent read and modify this workspace using its installed permissions. Approval applies only to this message.", TextWrapping = TextWrapping.Wrap });
            _desktopApproval = new ContentDialog { Title = "Allow browser request?", Content = content, PrimaryButtonText = "Allow this turn", CloseButtonText = "Decline", DefaultButton = ContentDialogButton.Close, XamlRoot = Content.XamlRoot };
            var answer = await _desktopApproval.ShowThemedAsync();
            await App.Core.CallAsync("desktop.execution.approve", new { request_id = id, approved = answer == ContentDialogResult.Primary });
        }
        catch (Exception ex)
        {
            DiagLog.Warn("execution", "browser approval dialog failed; declining", ex);
            // A competing native dialog must decline promptly, not leave a
            // hidden browser request waiting for the full approval timeout.
            if (requestId.Length > 0)
                try { await App.Core.CallAsync("desktop.execution.approve", new { request_id = requestId, approved = false }); } catch { }
        }
        finally { if (ownsDialog) { _desktopApproval = null; _desktopApprovalId = ""; } }
    }

    public void SetInboxCount(int count)
    {
        TeleportBadge.Value = count;
        TeleportBadge.Visibility = count > 0 ? Visibility.Visible : Visibility.Collapsed;
        FooterSharedDot.Visibility = count > 0 ? Visibility.Visible : Visibility.Collapsed;
        ToolTipService.SetToolTip(FooterShared, count > 0 ? $"Chats sent to you · {count} new" : "Chats sent to you");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(FooterShared, count > 0 ? $"Chats sent to you, {count} new" : "Chats sent to you");
    }

    private async Task RefreshInboxBadgeAsync()
    {
        var response = await App.Core.CallAsync(NxMethods.TeleportInbox);
        await UiThread.Resume(DispatcherQueue);
        if (response.Ok)
        {
            var items = NxJson.Items(response.Result).ToList();
            var pendingText = response.Result is { } result ? NxJson.Text(result, "pending") : "";
            var pending = int.TryParse(pendingText, out var parsed)
                ? parsed
                : items.Count(item => IsPendingTeleport(NxJson.Text(item, "status")));
            SetInboxCount(pending);
        }
    }

    private static bool IsPendingTeleport(string status) =>
        !status.Equals("accepted", StringComparison.OrdinalIgnoreCase) &&
        !status.Equals("revoked", StringComparison.OrdinalIgnoreCase) &&
        !status.Equals("expired", StringComparison.OrdinalIgnoreCase);

    public async Task RefreshBannerAsync()
    {
        var netTask = App.Core.CallAsync(NxMethods.NetStatus);
        var uploadsTask = App.Core.CallAsync(NxMethods.UploadsStatus);
        await Task.WhenAll(netTask, uploadsTask);
        var net = netTask.Result; var uploads = uploadsTask.Result;
        await UiThread.Resume(DispatcherQueue);
        _networkDown = net.Ok && net.Result is { } netBody && netBody.TryGetProperty("online", out var flag) && flag.ValueKind == JsonValueKind.False;
        var pending = 0;
        if (uploads.Ok && uploads.Result is { } uploadBody &&
            uploadBody.TryGetProperty("pending", out var pendingEl) &&
            pendingEl.TryGetInt32(out var parsed))
        {
            pending = parsed;
        }
        State.PendingUploads = pending;
        ApplyConnectivity();
    }

    private bool _networkDown;
    private bool _wasOffline;
    private Button? _retryNow;

    // One offline presentation for the whole app: the banner on top, plus
    // pages that disable what needs the server. It comes from failed calls
    // and a /healthz probe (Connectivity), not just Windows' network state,
    // and clears by itself when the server answers again.
    private void ApplyConnectivity()
    {
        var offline = _networkDown || Connectivity.Offline;
        var pending = State.PendingUploads;
        var waiting = pending > 0 ? $" {pending} item{(pending == 1 ? "" : "s")} will upload then." : "";
        State.Banner = !offline ? ""
            : _networkDown ? "You're offline. Your chats on this computer are still here; Nexus reconnects when you're back." + waiting
            : $"Can't reach the Nexus server{(Connectivity.Host.Length > 0 ? " (" + Connectivity.Host + ")" : "")}. Nexus keeps trying and reconnects by itself." + waiting;
        State.Offline = offline;
        OfflineBar.Severity = InfoBarSeverity.Informational;
        OfflineBar.Title = offline ? "Offline" : "";
        OfflineBar.Message = State.Banner;
        if (_retryNow is null)
        {
            _retryNow = new Button { Content = "Try now" };
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_retryNow, "Try to reconnect now");
            _retryNow.Click += (_, _) => { Connectivity.ProbeNow(); _ = RefreshBannerAsync(); };
            OfflineBar.ActionButton = _retryNow;
        }
        OfflineBar.IsOpen = offline && _setup is null;
        if (_wasOffline && !offline) OnReconnected();
        _wasOffline = offline;
    }

    // Back online: reload what is on screen so nothing keeps showing the
    // "can't reach" state.
    private void OnReconnected()
    {
        DiagLog.Info("net", "reconnected; refreshing the visible page");
        _chatStatus = "";
        _chatCache.Clear();
        if (ProjectSelector.Items.Count == 0) _ = BootstrapProjectsAsync();
        _ = LoadChatsAsync(force: true);
        _ = RefreshInboxBadgeAsync();
        if (ContentFrame.Content is NexusPage page)
        {
            var parameter = page is SessionPage session ? session.SessionId : null;
            page.Enter(parameter);
        }
    }

    private static void EnsureLoginStartDefault()
    {
        if (!AppPrefs.IsFirstRun)
        {
            return;
        }
        // Starting at sign-in is an explicit Settings choice, not a side
        // effect of trying the beta. Preserve existing users' preference.
        AppPrefs.Save();
    }
}
