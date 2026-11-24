using System.Diagnostics;
using System.Text.Json;
using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Automation.Peers;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Imaging;
using Nexus.Core;
using Nexus.Platform;

namespace Nexus.Pages;

/// <summary>
/// First-run setup: welcome, connect to a server, detect agents, choose
/// workspaces, privacy, startup, done. Shown in place of the shell on the
/// first launch and again from Settings > General. Every step except the
/// summary can be skipped; nothing here is required to use Nexus.
/// </summary>
public sealed class SetupWizardPage : NexusPage
{
    public const string ProductionServer = "https://api-nexus.pratyushes.dev";

    private enum Step { Welcome, Connect, Agents, Workspaces, Privacy, Startup, Done }

    private static readonly (Step Step, string Name)[] Steps =
    [
        (Step.Welcome, "Welcome"),
        (Step.Connect, "Connect"),
        (Step.Agents, "Agents"),
        (Step.Workspaces, "Workspaces"),
        (Step.Privacy, "Privacy"),
        (Step.Startup, "Startup"),
        (Step.Done, "Done")
    ];

    private readonly Action _finished;
    private int _index;

    // chrome
    private readonly TextBlock _counter = new() { FontSize = 12, Foreground = NexusPalette.Muted };
    // Step indicator: one segment + label per step; the current segment fills.
    private readonly Grid _rail = new() { ColumnSpacing = 6, Margin = new Thickness(0, 12, 0, 0) };
    private readonly List<Border> _railFill = new();
    private readonly List<TextBlock> _railLabels = new();
    private int _railShown = -1;
    private readonly StackPanel _titles = new() { Spacing = 8, Margin = new Thickness(0, 28, 0, 22) };
    private readonly TextBlock _title = NexusTokens.PageTitle("");
    private readonly TextBlock _subtitle = new() { FontSize = NexusTokens.FontBody, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, LineHeight = 20 };
    // The app's own palette (App.xaml): the brand gold accent, per theme, so the
    // guide looks like the shell it opens into.
    private static Brush Brand => NexusPalette.Accent;
    private static Brush BrandFill => (Brush)Application.Current.Resources["AccentFillColorDefaultBrush"];
    private static Brush OnBrand => (Brush)Application.Current.Resources["TextOnAccentFillColorPrimaryBrush"];

    // The shell's accent button (App.xaml: gold with ink text, per theme).
    private static void BrandPrimary(Button button)
    {
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accent) && accent is Style style) button.Style = style;
    }
    private Action? _playDone;
    private readonly ContentControl _body = new() { HorizontalContentAlignment = HorizontalAlignment.Stretch };
    private readonly ScrollViewer _scroller = new() { HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
    private readonly Button _back = new() { Content = "Back", MinWidth = 88, AccessKey = "B" };
    private readonly Button _skip = new() { Content = "Skip for now", MinWidth = 88, AccessKey = "K" };
    private readonly Button _next = new() { Content = "Next", MinWidth = 120, AccessKey = "N" };
    private readonly Button _skipAll = new() { Content = "Skip setup", AccessKey = "S", Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0) };
    private readonly Dictionary<Step, UIElement> _views = new();

    // account
    private bool _signedIn;
    private string _accountName = "";
    private string _serverUrl = "";
    private readonly TextBlock _accountState = new() { TextWrapping = TextWrapping.Wrap, FontWeight = Microsoft.UI.Text.FontWeights.Medium };
    private readonly TextBlock _connectResult = new() { TextWrapping = TextWrapping.Wrap, FontSize = NexusTokens.FontCaption };
    private readonly TextBox _server = new() { Header = "Server URL", InputScope = UrlScope() };
    private readonly PasswordBox _token = new() { Header = "API token", PlaceholderText = "Paste a token from your Nexus account", PasswordRevealMode = PasswordRevealMode.Peek };
    private DispatcherQueueTimer? _authPoll;
    private DateTime _authPollUntil;

    // agents
    private readonly StackPanel _agentRows = new() { Spacing = 0 };
    private readonly StackPanel _agentReport = new() { Spacing = 6 };
    private readonly TextBlock _agentState = new() { TextWrapping = TextWrapping.Wrap, FontSize = NexusTokens.FontCaption, Foreground = NexusPalette.Muted };
    private readonly Button _agentConnect = new() { Content = "Connect Nexus to these agents" };
    private bool _agentPreviewed;
    private int _agentsInstalled;
    private int _agentsConnected;

    // workspaces
    private readonly StackPanel _workspaceHost = new() { Spacing = 12 };
    private CaptureSetup.Chooser? _chooser;
    private CheckBox? _consent;
    private readonly List<string> _approved = new();
    private bool _workspacesDirty;
    private bool _workspacesLoaded;
    private readonly TextBlock _workspaceState = new() { TextWrapping = TextWrapping.Wrap, FontSize = NexusTokens.FontCaption };

    // startup
    private readonly ToggleSwitch _loginStart = new() { OnContent = "", OffContent = "", MinWidth = 0 };
    private readonly ToggleSwitch _desktopShortcut = new() { OnContent = "", OffContent = "", MinWidth = 0 };
    private readonly ToggleSwitch _autoReports = new() { OnContent = "", OffContent = "", MinWidth = 0 };
    private bool _startupSync;

    public SetupWizardPage(Action finished)
    {
        _finished = finished;
        // Transparent: the window shows Mica behind the setup guide.
        Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        BrandPrimary(_next);
        foreach (var live in new[] { _connectResult, _accountState, _agentState, _workspaceState })
            AutomationProperties.SetLiveSetting(live, AutomationLiveSetting.Polite);
        AutomationProperties.SetHeadingLevel(_title, AutomationHeadingLevel.Level1);
        AutomationProperties.SetName(_rail, "Setup progress");
        AutomationProperties.SetName(_skipAll, "Skip setup and open Nexus");
        _server.Text = ProductionServer;
        _back.Click += (_, _) => Go(_index - 1);
        _skip.Click += (_, _) => Go(_index + 1);
        _next.Click += async (_, _) => await NextAsync();
        _skipAll.Click += (_, _) => Finish();
        _agentConnect.Click += async (_, _) => await ConnectAgentsAsync();
        _token.KeyDown += async (_, e) =>
        {
            if (e.Key != Windows.System.VirtualKey.Enter) return;
            e.Handled = true;
            await TokenConnectAsync(true);
        };
        // Alt+Left / Alt+Right move between steps, like a browser.
        AddAccelerator(Windows.System.VirtualKey.Left, () => { if (_back.Visibility == Visibility.Visible && _back.IsEnabled) Go(_index - 1); });
        AddAccelerator(Windows.System.VirtualKey.Right, () => { if (_next.IsEnabled) _ = NextAsync(); });
        Content = BuildChrome();
        Unloaded += (_, _) => StopAuthPoll();
    }

    protected override void OnEnter(object? parameter)
    {
        _ = RefreshAccountAsync();
        Go(_index);
    }

    /// <summary>Called by the shell after a nexus://auth callback completes.</summary>
    public void AccountChanged() => _ = RefreshAccountAsync();

    // ---- chrome & navigation

    private UIElement BuildChrome()
    {
        var header = new StackPanel { Spacing = 2 };
        var brand = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8, Margin = new Thickness(0, 0, 0, 14) };
        brand.Children.Add(new Image { Source = new BitmapImage(new Uri("ms-appx:///Assets/Nexus.png")), Width = 22, Height = 22 });
        brand.Children.Add(new TextBlock { Text = "Nexus setup", FontSize = 13, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, VerticalAlignment = VerticalAlignment.Center });
        header.Children.Add(brand);
        header.Children.Add(_counter);
        header.Children.Add(BuildRail());

        var titles = _titles;
        titles.Children.Add(_title);
        titles.Children.Add(_subtitle);

        _scroller.Content = new Border { Padding = new Thickness(0, 0, 12, 16), Child = _body };

        var footer = new Grid { ColumnSpacing = 8, Padding = new Thickness(0, 14, 0, 0), BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 1, 0, 0) };
        footer.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        footer.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        footer.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        footer.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        footer.Children.Add(_skipAll);
        Grid.SetColumn(_back, 1); footer.Children.Add(_back);
        Grid.SetColumn(_skip, 2); footer.Children.Add(_skip);
        Grid.SetColumn(_next, 3); footer.Children.Add(_next);

        var column = new Grid { MaxWidth = 720, Padding = new Thickness(40, 36, 40, 28), HorizontalAlignment = HorizontalAlignment.Stretch };
        // Window entrance: the whole guide rises in once (theme animation,
        // so it follows the system animation setting by itself).
        column.Transitions = new Microsoft.UI.Xaml.Media.Animation.TransitionCollection
        {
            new Microsoft.UI.Xaml.Media.Animation.EntranceThemeTransition { FromVerticalOffset = 48 }
        };
        column.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        column.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        column.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        column.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        column.Children.Add(header);
        Grid.SetRow(titles, 1); column.Children.Add(titles);
        Grid.SetRow(_scroller, 2); column.Children.Add(_scroller);
        Grid.SetRow(footer, 3); column.Children.Add(footer);
        return column;
    }

    private UIElement BuildRail()
    {
        for (var i = 0; i < Steps.Length; i++)
        {
            _rail.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            var fill = new Border { Background = Brand, CornerRadius = new CornerRadius(2), Height = 4 };
            var track = new Border { Background = NexusPalette.Hairline, CornerRadius = new CornerRadius(2), Height = 4, Child = fill };
            var label = new TextBlock { Text = Steps[i].Name, FontSize = NexusTokens.FontMicro, Foreground = NexusPalette.Muted, Margin = new Thickness(0, 7, 0, 0), TextTrimming = TextTrimming.CharacterEllipsis };
            var cell = new StackPanel();
            cell.Children.Add(track);
            cell.Children.Add(label);
            Grid.SetColumn(cell, i);
            _rail.Children.Add(cell);
            _railFill.Add(fill);
            _railLabels.Add(label);
            Motion.FillX(fill, 0, 0);
        }
        return _rail;
    }

    private void UpdateRail()
    {
        for (var i = 0; i < Steps.Length; i++)
        {
            var target = i <= _index ? 1f : 0f;
            var was = i <= _railShown ? 1f : 0f;
            // Only the newly reached (or left) segment animates.
            Motion.FillX(_railFill[i], i == _index && i > _railShown ? 0f : was, target);
            _railLabels[i].FontWeight = i == _index ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
            _railLabels[i].Foreground = i == _index ? (Brush)Application.Current.Resources["TextFillColorPrimaryBrush"] : NexusPalette.Muted;
        }
        _railShown = _index;
        AutomationProperties.SetName(_rail, $"Setup progress, step {_index + 1} of {Steps.Length}, {Steps[_index].Name}");
    }

    // Each step's content cascades in from the direction of travel.
    private static void Cascade(UIElement view, bool forward)
    {
        if (view is not Panel panel) { Motion.FadeSlideIn(view, 0, forward ? 28 : -28, 0); return; }
        var delay = 0;
        foreach (var child in panel.Children.Take(10))
        {
            Motion.FadeSlideIn(child, delay, forward ? 28 : -28, 0, 460);
            delay += 45;
        }
    }

    private void AddAccelerator(Windows.System.VirtualKey key, Action action)
    {
        var accelerator = new KeyboardAccelerator { Key = key, Modifiers = Windows.System.VirtualKeyModifiers.Menu };
        accelerator.Invoked += (_, args) => { args.Handled = true; action(); };
        KeyboardAccelerators.Add(accelerator);
    }

    private Step Current => Steps[_index].Step;

    private void Go(int index)
    {
        var forward = index >= _index;
        _index = Math.Clamp(index, 0, Steps.Length - 1);
        var step = Current;
        if (step != Step.Connect) StopAuthPoll();
        _counter.Text = $"Step {_index + 1} of {Steps.Length} · {Steps[_index].Name}";
        UpdateRail();
        (_title.Text, _subtitle.Text) = step switch
        {
            Step.Welcome => ("Welcome to Nexus", "Nexus keeps a shared memory of your coding agents' work, so any agent can pick up where another left off."),
            Step.Connect => ("Connect to your Nexus account", "Sign in so captured sessions and memories sync to your account."),
            Step.Agents => ("Your coding agents", "Nexus works with the agents you already use. Here is what it found on this computer."),
            Step.Workspaces => ("Choose what to capture", CaptureSetup.FoldersExplained),
            Step.Privacy => ("What Nexus captures and syncs", "A short summary before anything is monitored."),
            Step.Startup => ("Startup", "Capture only runs while Nexus is running."),
            _ => ("You're all set", "Here is how Nexus is set up. You can change any of it in Settings.")
        };
        // The summary reflects the latest choices, so it is rebuilt each time.
        if (step == Step.Done || !_views.TryGetValue(step, out var view))
        {
            view = BuildStep(step);
            _views[step] = view;
        }
        _body.Content = view;
        Motion.FadeSlideIn(_titles, 0, forward ? 20 : -20, 0, 380);
        Cascade(view, forward);
        _scroller.ChangeView(null, 0, null, true);
        UpdateButtons();
        _ = OnStepShownAsync(step);
        DispatcherQueue?.TryEnqueue(DispatcherQueuePriority.Low, () => FocusFirst(step));
    }

    private void FocusFirst(Step step)
    {
        Control? target = step switch
        {
            Step.Connect when !_signedIn => _views.TryGetValue(step, out var v) ? FirstButton(v) : null,
            _ => null
        };
        (target ?? _next).Focus(FocusState.Programmatic);
    }

    private static Control? FirstButton(UIElement root) =>
        root is Panel panel ? panel.Children.Select(c => c is Button b ? b : FirstButton(c)).FirstOrDefault(c => c is not null)
        : root is Border { Child: { } child } ? FirstButton(child) : null;

    private void UpdateButtons()
    {
        var step = Current;
        _back.Visibility = _index == 0 ? Visibility.Collapsed : Visibility.Visible;
        _skipAll.Visibility = step == Step.Done ? Visibility.Collapsed : Visibility.Visible;
        _skip.Visibility = step switch
        {
            Step.Connect => _signedIn ? Visibility.Collapsed : Visibility.Visible,
            Step.Workspaces => Visibility.Visible,
            _ => Visibility.Collapsed
        };
        _next.IsEnabled = true;
        _next.Content = step switch
        {
            Step.Welcome => "Get started",
            Step.Connect => "Next",
            Step.Workspaces when WorkspaceSaveNeeded => "Save and continue",
            Step.Done => "Open Nexus",
            _ => "Next"
        };
        if (step == Step.Connect) _next.IsEnabled = _signedIn;
        if (step == Step.Workspaces) _next.IsEnabled = !WorkspaceSaveNeeded || (_consent?.IsChecked == true && _chooser?.Selected.Length > 0);
        AutomationProperties.SetName(_next, step == Step.Done ? "Finish setup and open Nexus" : (_next.Content as string ?? "Next"));
    }

    private async Task NextAsync()
    {
        if (!_next.IsEnabled) return;
        if (Current == Step.Done) { Finish(); return; }
        if (Current == Step.Workspaces && WorkspaceSaveNeeded && !await SaveWorkspacesAsync()) return;
        Go(_index + 1);
    }

    private async void Finish()
    {
        StopAuthPoll();
        AppPrefs.SetupCompleted = true;
        _next.IsEnabled = false;
        await Motion.FadeOutAsync(this, 280);
        _finished();
    }

    private Task OnStepShownAsync(Step step) => step switch
    {
        Step.Connect => RefreshAccountAsync(),
        Step.Agents => LoadAgentsAsync(),
        Step.Workspaces => LoadWorkspacesAsync(),
        Step.Startup => Task.Run(SyncStartup),
        Step.Done => PlayDoneAsync(),
        Step.Welcome => PlayWelcomeAsync(),
        _ => Task.CompletedTask
    };

    private Task PlayDoneAsync()
    {
        _playDone?.Invoke();
        return Task.CompletedTask;
    }

    private Task PlayWelcomeAsync()
    {
        _playWelcome?.Invoke();
        return Task.CompletedTask;
    }

    private UIElement BuildStep(Step step) => step switch
    {
        Step.Welcome => BuildWelcome(),
        Step.Connect => BuildConnect(),
        Step.Agents => BuildAgents(),
        Step.Workspaces => _workspaceHost,
        Step.Privacy => BuildPrivacy(),
        Step.Startup => BuildStartup(),
        _ => BuildDone()
    };

    // ---- Welcome

    private Action? _playWelcome;

    // The Nexus mark inside a slowly orbiting ring of memory-graph nodes
    // (same motif as the installer splash).
    private UIElement Hero()
    {
        const double box = 168, radius = 70;
        var host = new Grid { Width = box, Height = box, HorizontalAlignment = HorizontalAlignment.Left, Margin = new Thickness(-14, -10, 0, 4) };
        AutomationProperties.SetAccessibilityView(host, AccessibilityView.Raw);
        var orbit = new Canvas { Width = box, Height = box };
        orbit.Children.Add(new Microsoft.UI.Xaml.Shapes.Ellipse
        {
            Width = radius * 2, Height = radius * 2, Stroke = NexusPalette.Hairline, StrokeThickness = 1,
            Margin = new Thickness(box / 2 - radius, box / 2 - radius, 0, 0)
        });
        var dots = new List<UIElement>();
        for (var i = 0; i < 7; i++)
        {
            var a = i * Math.PI * 2 / 7 + 0.4;
            var size = i % 3 == 0 ? 9.0 : 6.0;
            var dot = new Microsoft.UI.Xaml.Shapes.Ellipse
            {
                Width = size, Height = size,
                Fill = i % 3 == 0 ? Brand : (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"]
            };
            Canvas.SetLeft(dot, box / 2 + Math.Cos(a) * radius - size / 2);
            Canvas.SetTop(dot, box / 2 + Math.Sin(a) * radius - size / 2);
            orbit.Children.Add(dot);
            dots.Add(dot);
        }
        host.Children.Add(orbit);
        var glow = new Microsoft.UI.Xaml.Shapes.Ellipse
        {
            Width = 92, Height = 92, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center,
            Fill = new RadialGradientBrush
            {
                GradientStops =
                {
                    new GradientStop { Color = Windows.UI.Color.FromArgb(90, NexusTokens.BrandGold.R, NexusTokens.BrandGold.G, NexusTokens.BrandGold.B), Offset = 0 },
                    new GradientStop { Color = Windows.UI.Color.FromArgb(0, NexusTokens.BrandGold.R, NexusTokens.BrandGold.G, NexusTokens.BrandGold.B), Offset = 1 }
                }
            }
        };
        host.Children.Add(glow);
        var logo = new Image { Source = new BitmapImage(new Uri("ms-appx:///Assets/Nexus.png")), Width = 76, Height = 76 };
        var logoBox = new Border { Child = logo, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center };
        host.Children.Add(logoBox);
        _playWelcome = () =>
        {
            Motion.PopIn(logoBox, 120, 0.55f, 640);
            Motion.Breathe(logo, 0.04f, 3200, 900);
            Motion.PopIn(glow, 260, 0.3f, 900);
            Motion.Spin(orbit, 36000);
            var delay = 300;
            foreach (var dot in dots)
            {
                Motion.PopIn(dot, delay, 0f, 420);
                delay += 70;
            }
        };
        return host;
    }

    private UIElement BuildWelcome()
    {
        var panel = new StackPanel { Spacing = 18 };
        panel.Children.Add(Hero());
        panel.Children.Add(Point("", "Shared memory", "Decisions, fixes and context from every session become memory your agents can search."));
        panel.Children.Add(Point("", "All your chats in one place", "Claude Code, Codex, Cursor and other agents' sessions on one timeline. Continue any of them in another agent."));
        panel.Children.Add(Point("", "You choose what is captured", "Nothing is monitored until you pick folders and allow it. You can pause any time."));
        panel.Children.Add(Para("Setup takes about two minutes. Every step can be skipped and changed later in Settings.", 0.7, NexusTokens.FontCaption));
        return panel;
    }

    // ---- Connect

    private UIElement BuildConnect()
    {
        var panel = new StackPanel { Spacing = 14 };
        panel.Children.Add(Card(_accountState));

        var browser = new Button { Content = "Sign in with browser", Padding = new Thickness(18, 8, 18, 9) };
        BrandPrimary(browser);
        browser.Click += async (_, _) => await BrowserSignInAsync();
        var browserBlock = new StackPanel { Spacing = 6 };
        browserBlock.Children.Add(Label("Recommended"));
        browserBlock.Children.Add(browser);
        browserBlock.Children.Add(Para("Opens nexus.pratyushes.dev in your browser. When you finish there, Nexus signs in by itself.", 0.65, 12));
        panel.Children.Add(browserBlock);

        panel.Children.Add(Label("Or connect with an API token", 10));
        AutomationProperties.SetName(_server, "Server URL");
        AutomationProperties.SetName(_token, "API token");
        AutomationProperties.SetHelpText(_server, "The Nexus server address. The default is the hosted Nexus server.");
        panel.Children.Add(_server);
        panel.Children.Add(_token);
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        var test = new Button { Content = "Test connection" };
        var connect = new Button { Content = "Connect" };
        test.Click += async (_, _) => await TokenConnectAsync(false);
        connect.Click += async (_, _) => await TokenConnectAsync(true);
        actions.Children.Add(test);
        actions.Children.Add(connect);
        panel.Children.Add(actions);
        panel.Children.Add(_connectResult);
        var envServer = Environment.GetEnvironmentVariable("NEXUS_SERVER") ?? Environment.GetEnvironmentVariable("CENTRAL_SERVER_URL");
        var envToken = Environment.GetEnvironmentVariable("NEXUS_TOKEN") ?? Environment.GetEnvironmentVariable("CENTRAL_MEMORY_TOKEN");
        if (!string.IsNullOrWhiteSpace(envServer) || !string.IsNullOrWhiteSpace(envToken))
        {
            panel.Children.Add(new InfoBar
            {
                IsOpen = true, IsClosable = false, Severity = InfoBarSeverity.Informational,
                Title = "Environment variables in use",
                Message = "NEXUS_SERVER or NEXUS_TOKEN is set for your Windows account. Those take precedence over what you save here."
            });
        }
        return panel;
    }

    private async Task RefreshAccountAsync()
    {
        var response = await Call(NxMethods.AuthStatus);
        if (response.Ok && response.Result is { } status)
        {
            _signedIn = NxJson.Text(status, "signed_in") == "true";
            _accountName = NxJson.Text(status, "username");
            if (_accountName.Length == 0) _accountName = NxJson.Text(status, "user_id");
            _serverUrl = NxJson.Text(status, "server_url");
            if (_serverUrl.Length > 0 && _server.Text == ProductionServer) _server.Text = _serverUrl;
        }
        _accountState.Text = _signedIn
            ? (_accountName.Length > 0 ? $"Signed in as {_accountName}" : "Signed in") + (_serverUrl.Length > 0 ? $" on {HostOf(_serverUrl)}." : ".")
            : "Not signed in. Nexus still captures on this computer, and uploads once you sign in.";
        if (_signedIn) StopAuthPoll();
        UpdateButtons();
    }

    private async Task BrowserSignInAsync()
    {
        var response = await Call(NxMethods.AuthLogin);
        var url = response.Ok && response.Result is { } result ? NxJson.Text(result, "url") : "";
        if (url.Length == 0)
        {
            _connectResult.Text = response.Error ?? "Could not start sign-in.";
            return;
        }
        try
        {
            Process.Start(new ProcessStartInfo(url) { UseShellExecute = true });
            _accountState.Text = "Finish signing in in your browser. This page updates when you're done.";
            StartAuthPoll();
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("auth", "could not open the sign-in page", ex);
            _connectResult.Text = "Could not open the browser: " + ex.Message;
        }
    }

    private void StartAuthPoll()
    {
        _authPollUntil = DateTime.UtcNow.AddMinutes(5);
        if (_authPoll is null && DispatcherQueue is { } queue)
        {
            _authPoll = queue.CreateTimer();
            _authPoll.Interval = TimeSpan.FromSeconds(2);
            _authPoll.Tick += async (_, _) =>
            {
                if (DateTime.UtcNow > _authPollUntil) { StopAuthPoll(); return; }
                await RefreshAccountAsync();
            };
        }
        _authPoll?.Start();
    }

    private void StopAuthPoll() => _authPoll?.Stop();

    private async Task TokenConnectAsync(bool save)
    {
        var server = _server.Text.Trim();
        var token = _token.Password.Trim();
        if (save && token.Length == 0)
        {
            _connectResult.Text = "Paste an API token first, or use browser sign-in.";
            _token.Focus(FocusState.Programmatic);
            return;
        }
        _connectResult.Foreground = NexusPalette.Muted;
        _connectResult.Text = save ? "Connecting…" : "Testing…";
        var response = await Call(NxMethods.AuthConnect, new { server_url = server, token, save });
        if (!response.Ok || response.Result is not { } result)
        {
            ShowConnectResult(false, response.Error ?? "Could not check the connection.");
            return;
        }
        var reachable = NxJson.Text(result, "reachable") == "true";
        var valid = NxJson.Text(result, "token_valid") == "true";
        var message = NxJson.Text(result, "message");
        var version = NxJson.Text(result, "server_version");
        var ok = reachable && (token.Length == 0 || valid);
        if (ok && version.Length > 0) message += $" Server version {version}.";
        if (ok && token.Length == 0) message += " Add a token to connect, or use browser sign-in.";
        ShowConnectResult(ok, message);
        if (save && NxJson.Text(result, "saved") == "true")
        {
            _token.Password = "";
            await Task.Run(() => App.InitializeCore());
            await UiThread.Resume(DispatcherQueue);
            await RefreshAccountAsync();
        }
    }

    private void ShowConnectResult(bool ok, string text)
    {
        _connectResult.Foreground = ok ? NexusPalette.Muted : NexusPalette.Danger;
        _connectResult.Text = (ok ? "✓ " : "") + text;
    }

    // ---- Agents

    private UIElement BuildAgents()
    {
        var panel = new StackPanel { Spacing = 14 };
        panel.Children.Add(Card(_agentRows));
        panel.Children.Add(_agentState);
        panel.Children.Add(Para("Connecting adds the Nexus memory server to each agent's own settings so it can search your project memory. Each agent gets its own revocable token, never your sign-in. Changed files are backed up first.", 0.7, NexusTokens.FontCaption));
        panel.Children.Add(_agentConnect);
        panel.Children.Add(_agentReport);
        panel.Children.Add(Para("Sharing skills and sub-agents between agents, and project-only installs, are on the Agents page.", 0.55, 12));
        return panel;
    }

    private async Task LoadAgentsAsync()
    {
        if (_agentRows.Children.Count == 0)
        {
            var looking = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10, Padding = new Thickness(0, 8, 0, 8) };
            looking.Children.Add(new ProgressRing { IsActive = true, Width = 16, Height = 16 });
            looking.Children.Add(Para("Looking for coding agents on this computer…", 0.7, NexusTokens.FontCaption));
            _agentRows.Children.Add(looking);
        }
        var response = await Call("agents.setup.browse", new { workspace = "" });
        _agentRows.Children.Clear();
        if (!response.Ok || response.Result is not { } inventory)
        {
            _agentRows.Children.Add(Para(response.Error ?? "Could not read your agents' setup.", 0.8, NexusTokens.FontCaption));
            return;
        }
        var agents = inventory.TryGetProperty("agents", out var list) && list.ValueKind == JsonValueKind.Array ? list.EnumerateArray().ToList() : new List<JsonElement>();
        var missing = new List<string>();
        _agentsInstalled = 0;
        _agentsConnected = 0;
        foreach (var agent in agents)
        {
            var name = NxJson.Text(agent, "name");
            if (name.Length == 0) name = NxJson.Text(agent, "agent");
            if (NxJson.Text(agent, "installed") != "true") { missing.Add(name); continue; }
            _agentsInstalled++;
            var connected = NxJson.Text(agent, "nexus_mcp") == "true";
            if (connected) _agentsConnected++;
            var version = NxJson.Text(agent, "version");
            var row = new Grid { Padding = new Thickness(0, 9, 0, 9), ColumnSpacing = 12 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            var check = new Border
            {
                Width = 20, Height = 20, CornerRadius = new CornerRadius(10),
                Background = connected ? BrandFill : NexusPalette.AccentFill,
                Child = new FontIcon { Glyph = "\uE73E", FontSize = 10, Foreground = connected ? OnBrand : Brand }
            };
            row.Children.Add(check);
            var label = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
            Grid.SetColumn(label, 1);
            label.Children.Add(new TextBlock { Text = name, FontWeight = Microsoft.UI.Text.FontWeights.Medium });
            if (version.Length > 0) label.Children.Add(new TextBlock { Text = version, FontSize = NexusTokens.FontMicro, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center });
            row.Children.Add(label);
            var state = new TextBlock { Text = connected ? "Connected to Nexus" : "Not connected", FontSize = 12, Foreground = connected ? NexusPalette.Accent : NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(state, 2);
            row.Children.Add(state);
            AutomationProperties.SetName(row, $"{name} {version}, found, {state.Text}");
            _agentRows.Children.Add(row);
            // Live detection: each agent slides in, then its check pops.
            Motion.FadeSlideIn(row, _agentsInstalled * 90, 0, 10, 380);
            Motion.PopIn(check, _agentsInstalled * 90 + 200, 0.2f, 480);
        }
        if (_agentsInstalled == 0)
            _agentRows.Children.Add(Para("No supported coding agents found yet. Install Claude Code, Codex, Cursor or another supported agent, then open Settings > Agents.", 0.75, NexusTokens.FontCaption));
        _agentState.Text = (_agentsInstalled > 0 ? $"{_agentsInstalled} found, {_agentsConnected} connected." : "") +
            (missing.Count > 0 ? (_agentsInstalled > 0 ? " " : "") + "Also supported: " + string.Join(", ", missing.Take(8)) + (missing.Count > 8 ? ", …" : "") + "." : "");
        var canConnect = _agentsInstalled > _agentsConnected;
        _agentConnect.Visibility = canConnect ? Visibility.Visible : Visibility.Collapsed;
        _agentConnect.IsEnabled = _signedIn || HasEnvToken();
        ToolTipService.SetToolTip(_agentConnect, _agentConnect.IsEnabled ? null : "Sign in first: each agent gets its own token from your account.");
        if (!_agentConnect.IsEnabled && canConnect && _agentReport.Children.Count == 0)
            _agentReport.Children.Add(Para("Sign in on the previous step to connect your agents.", 0.7, 12));
    }

    private async Task ConnectAgentsAsync()
    {
        var args = new
        {
            dry_run = !_agentPreviewed,
            include_setup_share = false,
            include_memory_files = false,
            scope = "user",
            project_id = "",
            workspace_root = "",
            app_dir = AppContext.BaseDirectory
        };
        _agentConnect.IsEnabled = false;
        _agentReport.Children.Clear();
        _agentReport.Children.Add(Para(_agentPreviewed ? "Connecting your agents…" : "Checking what would change…", 0.6, NexusTokens.FontCaption));
        var response = await Call("agents.install_all", args);
        _agentReport.Children.Clear();
        _agentConnect.IsEnabled = true;
        if (!response.Ok || response.Result is not { } result)
        {
            _agentReport.Children.Add(Para(response.Error ?? "Could not connect your agents.", 0.85, NexusTokens.FontCaption));
            return;
        }
        var changes = AgentSetupPage.RenderInstallReport(_agentReport, result, preview: !_agentPreviewed);
        if (!_agentPreviewed && changes > 0)
        {
            _agentPreviewed = true;
            _agentConnect.Content = "Apply these changes";
            return;
        }
        _agentPreviewed = false;
        _agentConnect.Content = "Connect Nexus to these agents";
        if (args.dry_run == false)
            _agentReport.Children.Add(Para("Restart any open agents so they pick up the Nexus memory server.", 0.7, 12));
        await LoadAgentsAsync();
    }

    // ---- Workspaces

    private bool WorkspaceSaveNeeded => _workspacesDirty || (_approved.Count == 0 && _chooser?.Selected.Length > 0);

    private async Task LoadWorkspacesAsync()
    {
        if (_workspacesLoaded) { UpdateButtons(); return; }
        _workspaceHost.Children.Clear();
        _workspaceHost.Children.Add(Para("Looking for folders where your agents have run…", 0.6, NexusTokens.FontCaption));
        var response = await Call(NxMethods.StatusGet);
        _workspaceHost.Children.Clear();
        var capture = response.Ok && response.Result is { } status && status.TryGetProperty("capture", out var c) ? c : default;
        if (capture.ValueKind != JsonValueKind.Object)
        {
            _workspaceHost.Children.Add(Para(response.Error ?? "Capture isn't available in this copy of Nexus. You can set it up later in Settings > Workspaces & capture.", 0.8, NexusTokens.FontCaption));
            return;
        }
        var suggested = new List<string>();
        _approved.Clear();
        SettingsPage.ReadPaths(capture, "approved_workspaces", _approved);
        SettingsPage.ReadPaths(capture, "suggested_workspaces", suggested);
        CaptureSetup.ReadDiscovered(capture);
        _chooser = new CaptureSetup.Chooser(_approved, suggested);
        _chooser.Changed += () => { _workspacesDirty = true; UpdateButtons(); };
        _consent = CaptureSetup.ConsentBox();
        _consent.Checked += (_, _) => UpdateButtons();
        _consent.Unchecked += (_, _) => UpdateButtons();
        _workspaceHost.Children.Add(_chooser.View);
        _workspaceHost.Children.Add(Para(CaptureSetup.WhatIsCaptured, 0.7, NexusTokens.FontCaption));
        _workspaceHost.Children.Add(_consent);
        _workspaceHost.Children.Add(_workspaceState);
        if (_approved.Count > 0)
            _workspaceState.Text = $"{_approved.Count} folder{(_approved.Count == 1 ? " is" : "s are")} already monitored.";
        _workspacesLoaded = true;
        UpdateButtons();
    }

    private async Task<bool> SaveWorkspacesAsync()
    {
        if (_chooser is null) return true;
        _next.IsEnabled = false;
        _workspaceState.Foreground = NexusPalette.Muted;
        _workspaceState.Text = "Saving…";
        var result = await CaptureSetup.SaveAsync(this, _chooser.Selected);
        if (!result.Ok)
        {
            _workspaceState.Foreground = NexusPalette.Danger;
            _workspaceState.Text = result.Error ?? "Could not save monitoring choices.";
            UpdateButtons();
            return false;
        }
        _approved.Clear();
        _approved.AddRange(_chooser.Selected);
        _workspacesDirty = false;
        _workspaceState.Text = $"Monitoring {_approved.Count} folder{(_approved.Count == 1 ? "" : "s")}.";
        return true;
    }

    // ---- Privacy

    private UIElement BuildPrivacy()
    {
        var panel = new StackPanel { Spacing = 16 };
        panel.Children.Add(Point("", "Only the folders you approve", "Nexus monitors only the folders you chose. It does not add other projects by itself."));
        panel.Children.Add(Point("", "What is captured", CaptureSetup.WhatIsCaptured));
        panel.Children.Add(Point("", "Where it goes", "Captured sessions and the memories extracted from them upload to your account on the server you connected to. While offline they wait on this computer and upload later."));
        panel.Children.Add(Point("", "Sharing is always your choice", "Chats stay private to you. Sharing a chat with a teammate or an organization is a separate action each time; joining an organization doesn't reveal private chats."));
        panel.Children.Add(Point("", "You stay in control", "Pause capture from the tray icon at any time. Remove a folder in Settings > Workspaces & capture; your existing history stays in your account. Uninstalling Nexus asks whether to delete its data from this computer."));
        // Crash reports: ask each time unless the person opts in here.
        _autoReports.IsOn = AppPrefs.Data.AutoSendCrashReports;
        _autoReports.Toggled += (_, _) =>
        {
            AppPrefs.Data.AutoSendCrashReports = _autoReports.IsOn;
            AppPrefs.Save();
        };
        AutomationProperties.SetName(_autoReports, "Send crash reports automatically");
        var reports = ToggleRow("Send crash reports automatically",
            "If Nexus crashes it saves a short report: the error, where in Nexus's code it happened, and recent lines from its own log. Never your chats, memories, files or tokens. " +
            "Off: Nexus asks you each time and shows exactly what would be sent. On: reports go without asking, and without your account.", _autoReports);
        reports.Margin = new Thickness(0, 4, 0, 0);
        panel.Children.Add(reports);
        return panel;
    }

    // ---- Startup

    private UIElement BuildStartup()
    {
        var integration = DesktopIntegration.Current;
        _loginStart.Toggled += (_, _) => ApplyToggle(_loginStart, on => integration.SetStartsAtLogin(on));
        _desktopShortcut.Toggled += (_, _) => ApplyToggle(_desktopShortcut, on => integration.SetDesktopShortcut(on));
        var panel = new StackPanel();
        panel.Children.Add(ToggleRow("Start Nexus when I sign in", "Nexus starts hidden in the system tray, so capture keeps running without opening a window.", _loginStart));
        if (integration.SupportsDesktopShortcut)
            panel.Children.Add(ToggleRow("Desktop shortcut", "Put a Nexus shortcut on the desktop.", _desktopShortcut));
        panel.Children.Add(Para("Closing the Nexus window hides it to the system tray and capture keeps running. Quit from the tray icon's menu.", 0.65, NexusTokens.FontCaption, new Thickness(0, 14, 0, 0)));
        return panel;
    }

    private void SyncStartup()
    {
        var integration = DesktopIntegration.Current;
        bool login, shortcut;
        try { login = integration.StartsAtLogin; } catch { login = false; }
        try { shortcut = integration.HasDesktopShortcut; } catch { shortcut = false; }
        DispatcherQueue?.TryEnqueue(() =>
        {
            _startupSync = true;
            _loginStart.IsOn = login;
            _desktopShortcut.IsOn = shortcut;
            _startupSync = false;
        });
    }

    private void ApplyToggle(ToggleSwitch toggle, Action<bool> apply)
    {
        if (_startupSync) return;
        try { apply(toggle.IsOn); }
        catch (Exception ex)
        {
            _startupSync = true;
            toggle.IsOn = !toggle.IsOn;
            _startupSync = false;
            AutomationProperties.SetHelpText(toggle, ex.Message);
            ToolTipService.SetToolTip(toggle, ex.Message);
        }
    }

    // ---- Done

    // A small memory graph blooms out from a check mark.
    private UIElement Bloom()
    {
        const double w = 300, h = 150;
        var canvas = new Canvas { Width = w, Height = h, HorizontalAlignment = HorizontalAlignment.Left, Margin = new Thickness(0, -6, 0, 6) };
        AutomationProperties.SetAccessibilityView(canvas, AccessibilityView.Raw);
        var graph = new Canvas { Width = w, Height = h };
        canvas.Children.Add(graph);
        var rng = new Random(7);
        var parts = new List<(UIElement Element, double Dx, double Dy, int Delay)>();
        var lines = new List<UIElement>();
        const int count = 16;
        for (var i = 0; i < count; i++)
        {
            var a = i * Math.PI * 2 / count + rng.NextDouble() * 0.25;
            var r = 44 + rng.NextDouble() * 58;
            var x = w / 2 + Math.Cos(a) * r * 1.35;
            var y = h / 2 + Math.Sin(a) * r * 0.62;
            var line = new Microsoft.UI.Xaml.Shapes.Line { X1 = w / 2, Y1 = h / 2, X2 = x, Y2 = y, Stroke = NexusPalette.Hairline, StrokeThickness = 1 };
            graph.Children.Add(line);
            lines.Add(line);
            var size = 4 + rng.NextDouble() * 6;
            var node = new Microsoft.UI.Xaml.Shapes.Ellipse
            {
                Width = size, Height = size,
                Fill = i % 4 == 0 ? BrandFill : i % 4 == 1 ? Brand : (Brush)Application.Current.Resources["TextFillColorSecondaryBrush"]
            };
            Canvas.SetLeft(node, x - size / 2);
            Canvas.SetTop(node, y - size / 2);
            graph.Children.Add(node);
            parts.Add((node, w / 2 - x, h / 2 - y, 220 + (int)(r * 4)));
        }
        var badge = new Border
        {
            Width = 52, Height = 52, CornerRadius = new CornerRadius(26), Background = BrandFill,
            Child = new FontIcon { Glyph = "\uE73E", FontSize = 22, Foreground = OnBrand }
        };
        Canvas.SetLeft(badge, w / 2 - 26);
        Canvas.SetTop(badge, h / 2 - 26);
        canvas.Children.Add(badge);
        _playDone = () =>
        {
            Motion.PopIn(badge, 80, 0.3f, 620);
            foreach (var line in lines) Motion.FadeSlideIn(line, 260, 0, 0, 900);
            foreach (var (element, dx, dy, delay) in parts) Motion.FadeSlideIn(element, delay, (float)dx, (float)dy, 820);
            Motion.Spin(graph, 90000);
        };
        return canvas;
    }

    private UIElement BuildDone()
    {
        var panel = new StackPanel { Spacing = 12 };
        panel.Children.Add(Bloom());
        bool login;
        try { login = DesktopIntegration.Current.StartsAtLogin; } catch { login = false; }
        panel.Children.Add(Summary(_signedIn, "Account",
            _signedIn ? (_accountName.Length > 0 ? $"Signed in as {_accountName}." : "Signed in.") : "Not signed in. Sign in any time in Settings > Account."));
        panel.Children.Add(Summary(_agentsConnected > 0, "Agents",
            _agentsConnected > 0 ? $"{_agentsConnected} agent{(_agentsConnected == 1 ? "" : "s")} connected to Nexus memory." : "No agents connected yet. Connect them from the Agents page."));
        panel.Children.Add(Summary(_approved.Count > 0, "Workspaces",
            _approved.Count > 0 ? $"Monitoring {_approved.Count} folder{(_approved.Count == 1 ? "" : "s")}." : "No folders monitored yet. Choose them in Settings > Workspaces & capture."));
        panel.Children.Add(Summary(login, "Startup", login ? "Starts when you sign in to Windows." : "Starts when you open it."));
        panel.Children.Add(Para("Run this setup again any time from Settings > General.", 0.6, 12, new Thickness(0, 8, 0, 0)));
        return panel;
    }

    // ---- small builders

    private static UIElement Point(string glyph, string title, string text)
    {
        var grid = new Grid { ColumnSpacing = 14 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(new Border
        {
            Width = 34, Height = 34, CornerRadius = NexusTokens.RadiusMedium, Background = NexusPalette.AccentFill, VerticalAlignment = VerticalAlignment.Top,
            Child = new FontIcon { Glyph = glyph, FontSize = 15, Foreground = Brand }
        });
        var text2 = new StackPanel { Spacing = 2 };
        text2.Children.Add(new TextBlock { Text = title, FontWeight = Microsoft.UI.Text.FontWeights.Medium, TextWrapping = TextWrapping.Wrap });
        text2.Children.Add(new TextBlock { Text = text, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, FontSize = NexusTokens.FontCaption });
        Grid.SetColumn(text2, 1);
        grid.Children.Add(text2);
        AutomationProperties.SetName(grid, title + ". " + text);
        return grid;
    }

    private static UIElement Summary(bool done, string title, string text)
    {
        var grid = new Grid { ColumnSpacing = 12, Padding = new Thickness(0, 10, 0, 10), BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 0, 0, 1) };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(20) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(110) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(new FontIcon { Glyph = done ? "" : "", FontSize = 13, Foreground = done ? NexusPalette.Accent : NexusPalette.Muted });
        var label = new TextBlock { Text = title, FontWeight = Microsoft.UI.Text.FontWeights.Medium };
        Grid.SetColumn(label, 1);
        grid.Children.Add(label);
        var detail = new TextBlock { Text = text, TextWrapping = TextWrapping.Wrap, Foreground = NexusPalette.Muted };
        Grid.SetColumn(detail, 2);
        grid.Children.Add(detail);
        AutomationProperties.SetName(grid, title + ": " + text);
        return grid;
    }

    private static Border ToggleRow(string label, string description, ToggleSwitch toggle)
    {
        AutomationProperties.SetName(toggle, label);
        AutomationProperties.SetHelpText(toggle, description);
        var grid = new Grid { ColumnSpacing = 24 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var text = new StackPanel { Spacing = 2 };
        text.Children.Add(new TextBlock { Text = label, FontSize = NexusTokens.FontBody, FontWeight = Microsoft.UI.Text.FontWeights.Medium, TextWrapping = TextWrapping.Wrap });
        text.Children.Add(new TextBlock { Text = description, FontSize = NexusTokens.FontCaption, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap });
        grid.Children.Add(text);
        toggle.VerticalAlignment = VerticalAlignment.Center;
        Grid.SetColumn(toggle, 1);
        grid.Children.Add(toggle);
        return new Border { Padding = new Thickness(0, 12, 0, 12), BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 0, 0, 1), Child = grid };
    }

    private static Border Card(UIElement child) => new()
    {
        Child = child, Padding = new Thickness(16, 10, 16, 10), CornerRadius = NexusTokens.RadiusMedium,
        Background = NexusPalette.Surface, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
    };

    private static TextBlock Para(string text, double opacity, double size, Thickness? margin = null) => new()
    {
        Text = text, Opacity = opacity, FontSize = size, TextWrapping = TextWrapping.Wrap, Margin = margin ?? new Thickness(0)
    };

    private static TextBlock Label(string text, double top = 0)
    {
        var label = new TextBlock { Text = text, FontSize = 12, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Opacity = 0.6, Margin = new Thickness(0, top, 0, 0) };
        AutomationProperties.SetHeadingLevel(label, AutomationHeadingLevel.Level2);
        return label;
    }

    private static InputScope UrlScope()
    {
        var scope = new InputScope();
        scope.Names.Add(new InputScopeName(InputScopeNameValue.Url));
        return scope;
    }

    private static string HostOf(string url) => Uri.TryCreate(url, UriKind.Absolute, out var uri) ? uri.Host : url;

    private static bool HasEnvToken() =>
        !string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable("NEXUS_TOKEN")) ||
        !string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable("CENTRAL_MEMORY_TOKEN"));
}
