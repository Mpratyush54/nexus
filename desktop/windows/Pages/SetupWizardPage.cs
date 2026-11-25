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
/// First-run setup in four steps: sign in, connect agents, choose folders
/// (with what is captured and the crash-report choice), done. Shown in place
/// of the shell on the first launch and again from Settings > General. Each
/// step has one primary action; the footer button says honestly whether it
/// moves on ("Next") or leaves the step undone ("Skip for now"). The installer
/// already asks about startup and the desktop shortcut, so this does not.
/// </summary>
public sealed class SetupWizardPage : NexusPage
{
    public const string ProductionServer = "https://api-nexus.pratyushes.dev";

    private enum Step { Connect, Agents, Workspaces, Done }

    private static readonly (Step Step, string Name)[] Steps =
    [
        (Step.Connect, "Sign in"),
        (Step.Agents, "Agents"),
        (Step.Workspaces, "Folders"),
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
    private static Brush BrandFill => ThemeService.Brush("AccentFillColorDefaultBrush", Windows.UI.Color.FromArgb(0x80, 0x80, 0x80, 0x80));
    private static Brush OnBrand => ThemeService.Brush("TextOnAccentFillColorPrimaryBrush", Windows.UI.Color.FromArgb(0x80, 0x80, 0x80, 0x80));

    // The shell's accent button (App.xaml: gold with ink text, per theme).
    private static void BrandPrimary(Button button)
    {
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accent) && accent is Style style) button.Style = style;
    }
    private Action? _playDone;
    private readonly ContentControl _body = new() { HorizontalContentAlignment = HorizontalAlignment.Stretch };
    private readonly ScrollViewer _scroller = new() { HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
    private readonly Button _back = new() { Content = "Back", MinWidth = 88, AccessKey = "B" };
    // The one footer action: "Next" (primary) once the step is done, "Skip
    // for now" (secondary) while the step's own primary action is pending.
    private readonly Button _next = new() { Content = "Next", MinWidth = 120, AccessKey = "N" };
    private readonly Button _browserSignIn = new() { Content = "Sign in with browser", Padding = new Thickness(18, 8, 18, 9) };
    private readonly StackPanel _browserBlock = new() { Spacing = 6 };
    private readonly TextBlock _browserHint = new() { Opacity = 0.65, FontSize = 12, TextWrapping = TextWrapping.Wrap };
    private readonly Expander _advanced = new() { HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch };
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
    private readonly Button _agentConnect = new() { Content = "Connect Nexus to your agents", Padding = new Thickness(18, 8, 18, 9) };
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

    // crash reports (Folders step) and, for a copy that was not installed by
    // the installer, start-at-login (one line on Done)
    private readonly ToggleSwitch _loginStart = new() { OnContent = "", OffContent = "", MinWidth = 0 };
    private readonly ToggleSwitch _autoReports = new() { OnContent = "", OffContent = "", MinWidth = 0 };
    private bool _startupSync;

    public SetupWizardPage(Action finished)
    {
        _finished = finished;
        // The app's own canvas, so the guide and the shell it opens into are
        // one continuous background.
        Background = ThemeService.Brush("NexusCanvasBrush", NexusTokens.BrandInk);
        foreach (var live in new[] { _connectResult, _accountState, _agentState, _workspaceState })
            AutomationProperties.SetLiveSetting(live, AutomationLiveSetting.Polite);
        AutomationProperties.SetHeadingLevel(_title, AutomationHeadingLevel.Level1);
        AutomationProperties.SetName(_rail, "Setup progress");
        AutomationProperties.SetName(_skipAll, "Skip setup and open Nexus");
        AutomationProperties.SetName(_back, "Back");
        AutomationProperties.SetName(_browserSignIn, "Sign in with browser");
        _server.Text = ProductionServer;
        _back.Click += (_, _) => Go(_index - 1);
        _next.Click += async (_, _) => await NextAsync();
        _browserSignIn.Click += async (_, _) => await BrowserSignInAsync();
        _server.TextChanged += (_, _) => UpdateServerHint();
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

    /// <summary>A live theme switch: steps are rebuilt with the new theme's brushes.</summary>
    public void ThemeChanged()
    {
        Background = ThemeService.Brush("NexusCanvasBrush", NexusTokens.BrandInk);
        _views.Clear();
        _railShown = -1;
        foreach (var fill in _railFill) fill.Background = Brand;
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
            _railLabels[i].Foreground = i == _index ? ThemeService.Brush("TextFillColorPrimaryBrush", Windows.UI.Color.FromArgb(0x80, 0x80, 0x80, 0x80)) : NexusPalette.Muted;
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
            Step.Connect => ("Welcome to Nexus", "One memory for all your coding agents. Sign in so your chats and memories sync to your account."),
            Step.Agents => ("Connect your coding agents", "Nexus works with the agents you already use. Connect them so they can search your project memory."),
            Step.Workspaces => ("Choose folders to capture", "Pick the folders where you use coding agents. Nothing outside them is read."),
            _ => DoneTitles()
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
        // The step's own primary action while it is pending, else Next.
        Control? target = step switch
        {
            Step.Connect when !_signedIn => _browserSignIn,
            Step.Agents when _agentConnect.Visibility == Visibility.Visible && _agentConnect.IsEnabled => _agentConnect,
            _ => null
        };
        (target ?? _next).Focus(FocusState.Programmatic);
    }

    // One primary action per step: while the step's own action is pending,
    // the footer button is a quiet "Skip for now"; once it is done, the footer
    // becomes the primary "Next".
    private void UpdateButtons()
    {
        var step = Current;
        _back.Visibility = _index == 0 ? Visibility.Collapsed : Visibility.Visible;
        _skipAll.Visibility = step == Step.Done ? Visibility.Collapsed : Visibility.Visible;
        _next.IsEnabled = true;
        var pending = step switch
        {
            Step.Connect => !_signedIn,
            Step.Agents => _agentConnect.Visibility == Visibility.Visible,
            Step.Workspaces => !WorkspaceReady && _approved.Count == 0,
            _ => false
        };
        _next.Content = step switch
        {
            Step.Done => "Open Nexus",
            Step.Workspaces when WorkspaceSaveNeeded && WorkspaceReady => "Save and continue",
            Step.Workspaces when WorkspaceSaveNeeded => "Skip for now",
            _ when pending => "Skip for now",
            _ => "Next"
        };
        SetPrimary(_next, !pending && !(step == Step.Workspaces && WorkspaceSaveNeeded && !WorkspaceReady));
        _browserSignIn.Visibility = step == Step.Connect && !_signedIn ? Visibility.Visible : Visibility.Collapsed;
        _browserBlock.Visibility = _signedIn ? Visibility.Collapsed : Visibility.Visible;
        AutomationProperties.SetName(_next, step == Step.Done ? "Finish setup and open Nexus" : (_next.Content as string ?? "Next"));
    }

    private static void SetPrimary(Button button, bool primary)
    {
        if (primary) BrandPrimary(button);
        else button.ClearValue(StyleProperty);
    }

    // Folders chosen and monitoring allowed: the step can be saved.
    private bool WorkspaceReady => _consent?.IsChecked == true && _chooser?.Selected.Length > 0;

    private async Task NextAsync()
    {
        if (!_next.IsEnabled) return;
        if (Current == Step.Done) { Finish(); return; }
        if (Current == Step.Workspaces && WorkspaceSaveNeeded && WorkspaceReady && !await SaveWorkspacesAsync()) return;
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
        Step.Done => ShowDoneAsync(),
        _ => Task.CompletedTask
    };

    // Done reads the real state (account, agents, folders) before it says
    // anything, so it never claims a step that was skipped.
    private async Task ShowDoneAsync()
    {
        await RefreshAccountAsync();
        if (!_workspacesLoaded) await LoadApprovedAsync();
        if (!_agentsLoaded) await LoadAgentsAsync();
        if (Current != Step.Done) return;
        (_title.Text, _subtitle.Text) = DoneTitles();
        var view = BuildDone();
        _views[Step.Done] = view;
        _body.Content = view;
        if (!SupportsPortableStartup) { } else _ = Task.Run(SyncStartup);
        _playDone?.Invoke();
        UpdateButtons();
    }

    private UIElement BuildStep(Step step) => step switch
    {
        Step.Connect => BuildConnect(),
        Step.Agents => BuildAgents(),
        Step.Workspaces => _workspaceHost,
        _ => BuildDone()
    };

    // ---- Connect

    private UIElement BuildConnect()
    {
        var panel = new StackPanel { Spacing = 14 };
        panel.Children.Add(Card(_accountState));

        // Browser sign-in is the way in; the token form is for self-hosted
        // servers and lives under Advanced.
        BrandPrimary(_browserSignIn);
        _browserBlock.Children.Clear();
        _browserBlock.Children.Add(_browserSignIn);
        _browserBlock.Children.Add(_browserHint);
        UpdateServerHint();
        panel.Children.Add(_browserBlock);

        var form = new StackPanel { Spacing = 12, Padding = new Thickness(0, 4, 0, 4) };
        form.Children.Add(Para("For a self-hosted Nexus server, or to sign in with an API token from your account instead of the browser.", 0.7, NexusTokens.FontCaption));
        AutomationProperties.SetName(_server, "Server address");
        AutomationProperties.SetName(_token, "API token");
        _server.Header = "Server address";
        AutomationProperties.SetHelpText(_server, "The Nexus server this computer signs in to.");
        form.Children.Add(_server);
        form.Children.Add(_token);
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        var test = new Button { Content = "Test connection" };
        var connect = new Button { Content = "Sign in with token" };
        AutomationProperties.SetName(test, "Test connection");
        AutomationProperties.SetName(connect, "Sign in with token");
        test.Click += async (_, _) => await TokenConnectAsync(false);
        connect.Click += async (_, _) => await TokenConnectAsync(true);
        actions.Children.Add(test);
        actions.Children.Add(connect);
        form.Children.Add(actions);
        form.Children.Add(_connectResult);
        _advanced.Header = "Advanced: server address and API token";
        _advanced.Content = form;
        _advanced.IsExpanded = false;
        AutomationProperties.SetName(_advanced, "Advanced: server address and API token");
        panel.Children.Add(_advanced);
        var envServer = Environment.GetEnvironmentVariable("NEXUS_SERVER") ?? Environment.GetEnvironmentVariable("CENTRAL_SERVER_URL");
        var envToken = Environment.GetEnvironmentVariable("NEXUS_TOKEN") ?? Environment.GetEnvironmentVariable("CENTRAL_MEMORY_TOKEN");
        if (!string.IsNullOrWhiteSpace(envServer) || !string.IsNullOrWhiteSpace(envToken))
        {
            panel.Children.Add(new InfoBar
            {
                IsOpen = true, IsClosable = false, Severity = InfoBarSeverity.Informational,
                Title = "Environment variables in use",
                Message = $"NEXUS_SERVER or NEXUS_TOKEN is set for your {PlatformText.UserAccount}. Those take precedence over what you save here."
            });
        }
        return panel;
    }

    // The server the app will actually sign in to, never a fixed name.
    private string ServerInUse => _serverUrl.Length > 0 ? _serverUrl : _server.Text.Trim();

    private void UpdateServerHint() =>
        _browserHint.Text = $"Opens your browser to sign in to Nexus at {HostOf(ServerInUse)}. When you finish there, Nexus signs in by itself.";

    private async Task RefreshAccountAsync()
    {
        var response = await Call(NxMethods.AuthStatus);
        if (response.Ok && response.Result is { } status)
        {
            _signedIn = NxJson.Text(status, "signed_in") == "true";
            _accountName = NxJson.Text(status, "username");
            _serverUrl = NxJson.Text(status, "server_url");
            if (_serverUrl.Length > 0 && _server.Text == ProductionServer) _server.Text = _serverUrl;
        }
        _accountState.Text = _signedIn
            ? SignedInText + $" on {HostOf(ServerInUse)}."
            : "Not signed in yet. Nexus still captures on this computer and uploads once you sign in.";
        UpdateServerHint();
        if (_signedIn) StopAuthPoll();
        UpdateButtons();
    }

    private async Task BrowserSignInAsync()
    {
        var response = await Call(NxMethods.AuthLogin);
        var url = response.Ok && response.Result is { } result ? NxJson.Text(result, "url") : "";
        if (url.Length == 0)
        {
            _accountState.Text = response.Error ?? "Nexus couldn't start sign-in. Try again in a moment.";
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
            _connectResult.Text = "Nexus couldn't open your browser. Use the API token under Advanced instead.";
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
        if (ok && token.Length == 0) message += " Add a token to sign in, or use browser sign-in.";
        ShowConnectResult(ok, message);
        if (save && NxJson.Text(result, "saved") == "true")
        {
            _token.Password = "";
            await Task.Run(() => App.InitializeCore());
            await UiThread.Resume(DispatcherQueue);
            await RefreshAccountAsync();
            if (_signedIn && Current == Step.Connect)
            {
                _advanced.IsExpanded = false;
                _next.Focus(FocusState.Programmatic);
            }
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
        // The primary action sits above the list so it is in view without
        // scrolling however many agents are found.
        var panel = new StackPanel { Spacing = 12 };
        panel.Children.Add(_agentState);
        BrandPrimary(_agentConnect);
        AutomationProperties.SetName(_agentConnect, "Connect Nexus to your agents");
        var connectRow = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 12 };
        connectRow.Children.Add(_agentConnect);
        panel.Children.Add(connectRow);
        panel.Children.Add(_agentReport);
        panel.Children.Add(Card(_agentRows));
        panel.Children.Add(Para("Connecting lets each agent search your Nexus memory. Each agent gets its own key that you can revoke, never your sign-in, and Nexus backs up any settings file before changing it. Sharing skills between agents is on the Agents page.", 0.6, 12));
        return panel;
    }

    private bool _agentsLoaded;

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
            // A check only when the agent really is connected; otherwise an
            // empty ring, so no check mark ever sits next to "Not connected".
            var check = new Border
            {
                Width = 20, Height = 20, CornerRadius = new CornerRadius(10),
                Background = connected ? BrandFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent),
                BorderBrush = connected ? null : NexusPalette.Hairline, BorderThickness = new Thickness(connected ? 0 : 1.5),
                Child = connected ? new FontIcon { Glyph = "\uE73E", FontSize = 10, Foreground = OnBrand } : null
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
            AutomationProperties.SetName(row, $"{name}{(version.Length > 0 ? " " + version : "")}: {state.Text}");
            _agentRows.Children.Add(row);
            // Live detection: each agent slides in, then its check pops.
            Motion.FadeSlideIn(row, _agentsInstalled * 90, 0, 10, 380);
            Motion.PopIn(check, _agentsInstalled * 90 + 200, 0.2f, 480);
        }
        if (_agentsInstalled == 0)
            _agentRows.Children.Add(Para("No supported coding agents found yet. Install Claude Code, Codex, Cursor or another supported agent, then connect it from the Agents page.", 0.75, NexusTokens.FontCaption));
        _agentState.Text = (_agentsInstalled > 0
                ? $"Found {Plural(_agentsInstalled, "agent")} on this computer · {_agentsConnected} connected."
                : "") +
            (missing.Count > 0 ? (_agentsInstalled > 0 ? " " : "") + "Also supported: " + string.Join(", ", missing.Take(8)) + (missing.Count > 8 ? ", …" : "") + "." : "");
        _agentsLoaded = true;
        var canConnect = _agentsInstalled > _agentsConnected;
        _agentConnect.Visibility = canConnect ? Visibility.Visible : Visibility.Collapsed;
        _agentConnect.IsEnabled = _signedIn || HasEnvToken();
        ToolTipService.SetToolTip(_agentConnect, _agentConnect.IsEnabled ? null : "Sign in first: each agent gets its own key from your account.");
        if (!_agentConnect.IsEnabled && canConnect && _agentReport.Children.Count == 0)
        {
            var back = new HyperlinkButton { Content = "Sign in first", Padding = new Thickness(0) };
            AutomationProperties.SetName(back, "Sign in first, then connect your agents");
            back.Click += (_, _) => Go(IndexOf(Step.Connect));
            _agentReport.Children.Add(back);
        }
        if (Current == Step.Agents) UpdateButtons();
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
            AutomationProperties.SetName(_agentConnect, "Apply these changes");
            _agentConnect.Focus(FocusState.Programmatic);
            return;
        }
        _agentPreviewed = false;
        _agentConnect.Content = "Connect Nexus to your agents";
        AutomationProperties.SetName(_agentConnect, "Connect Nexus to your agents");
        if (args.dry_run == false)
            _agentReport.Children.Add(Para("Restart any agents that are open so they pick up Nexus.", 0.7, 12));
        await LoadAgentsAsync();
        // Done here: the next thing to do is move on, never a skip button.
        if (Current == Step.Agents && _agentConnect.Visibility != Visibility.Visible) _next.Focus(FocusState.Programmatic);
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
        // Privacy in two sentences above the list, and the list kept short,
        // so the consent and the crash-report choice are in view without
        // scrolling.
        _workspaceHost.Children.Add(Para("Nexus saves your agents' chats and what they did (including bits of code they read or changed) from these folders only, and syncs them to your private account. Nothing outside these folders is read, and you can pause capture from " + PlatformText.TrayPlace + " at any time.", 0.75, NexusTokens.FontCaption));
        _workspaceHost.Children.Add(new ScrollViewer { Content = _chooser.View, MaxHeight = 210, VerticalScrollBarVisibility = ScrollBarVisibility.Auto, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled });
        _workspaceHost.Children.Add(_consent);
        _workspaceHost.Children.Add(_workspaceState);
        _workspaceHost.Children.Add(CrashReportsRow());
        if (_approved.Count > 0)
            _workspaceState.Text = $"{Plural(_approved.Count, "folder")} already captured.";
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
        _workspaceState.Text = $"Capturing {Plural(_approved.Count, "folder")}.";
        return true;
    }

    // Done needs the folders even when the Folders step was never opened.
    private async Task LoadApprovedAsync()
    {
        var response = await Call(NxMethods.StatusGet);
        if (response.Ok && response.Result is { } status && status.TryGetProperty("capture", out var capture) && capture.ValueKind == JsonValueKind.Object)
        {
            _approved.Clear();
            SettingsPage.ReadPaths(capture, "approved_workspaces", _approved);
        }
    }

    // ---- Crash reports (on the Folders step)

    private bool _autoReportsWired;

    private Border CrashReportsRow()
    {
        // Ask each time unless the person opts in here.
        _autoReports.IsOn = AppPrefs.Data.AutoSendCrashReports;
        if (!_autoReportsWired)
        {
            _autoReportsWired = true;
            _autoReports.Toggled += (_, _) =>
            {
                AppPrefs.Data.AutoSendCrashReports = _autoReports.IsOn;
                AppPrefs.Save();
            };
        }
        return ToggleRow("Send crash reports automatically",
            "Only the error and Nexus's own log lines, never your chats, files or tokens. Off: Nexus asks each time and shows what would be sent.", _autoReports);
    }

    // ---- Startup (only for a copy the installer did not set up)

    // The installer asks about start-at-login and the desktop shortcut; a
    // copy it installed is managed by Velopack. Anything else (a portable
    // copy, a developer build) gets one line on Done instead.
    private static bool SupportsPortableStartup
    {
        get
        {
            try { return !UpdateService.Shared.IsAvailable; }
            catch { return false; }
        }
    }

    private bool _loginStartWired;

    private UIElement StartupLine()
    {
        if (!_loginStartWired)
        {
            _loginStartWired = true;
            _loginStart.Toggled += (_, _) => ApplyToggle(_loginStart, on => DesktopIntegration.Current.SetStartsAtLogin(on));
        }
        return ToggleRow($"Start Nexus when I sign in to {PlatformText.Os}", $"Nexus waits in {PlatformText.TrayPlace} so capture keeps running.", _loginStart);
    }

    private void SyncStartup()
    {
        bool login;
        try { login = DesktopIntegration.Current.StartsAtLogin; } catch { login = false; }
        DispatcherQueue?.TryEnqueue(() =>
        {
            _startupSync = true;
            _loginStart.IsOn = login;
            _startupSync = false;
        });
    }

    private void ApplyToggle(ToggleSwitch toggle, Action<bool> apply)
    {
        if (_startupSync) return;
        try { apply(toggle.IsOn); }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("setup", "could not change a startup option", ex);
            _startupSync = true;
            toggle.IsOn = !toggle.IsOn;
            _startupSync = false;
            ToolTipService.SetToolTip(toggle, $"{PlatformText.Os} didn't allow this change. Try again from Settings.");
        }
    }

    // ---- Done

    private bool Ready => _signedIn && _agentsConnected > 0 && _approved.Count > 0;

    private string SignedInText => _accountName.Length > 0 ? $"Signed in as @{_accountName}" : "Signed in";

    private (string Title, string Subtitle) DoneTitles()
    {
        var parts = new List<string>();
        parts.Add(_signedIn ? SignedInText : "Not signed in");
        parts.Add(_agentsConnected > 0 ? $"{Plural(_agentsConnected, "agent")} connected" : "No agents connected");
        parts.Add(_approved.Count switch
        {
            0 => "No folders captured",
            1 => "Capturing " + _approved[0],
            _ => $"Capturing {_approved[0]} and {Plural(_approved.Count - 1, "more folder")}"
        });
        var line = string.Join(" · ", parts);
        return Ready
            ? ("Nexus is ready", line)
            : ("Almost there", line + ". Finish the rest now or any time in Settings.");
    }

    // A small memory graph blooms out from a check mark (only when every
    // part of setup is done; otherwise the screen says what is left).
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
                Fill = i % 4 == 0 ? BrandFill : i % 4 == 1 ? Brand : ThemeService.Brush("TextFillColorSecondaryBrush", Windows.UI.Color.FromArgb(0x80, 0x80, 0x80, 0x80))
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
        _playDone = null;
        if (Ready) panel.Children.Add(Bloom());
        panel.Children.Add(Summary(_signedIn, "Account",
            _signedIn ? SignedInText + " on " + HostOf(ServerInUse) + "." : "Not signed in. Chats stay on this computer until you sign in.",
            _signedIn ? null : ("Sign in", Step.Connect)));
        panel.Children.Add(Summary(_agentsConnected > 0, "Agents",
            _agentsConnected > 0 ? $"{Plural(_agentsConnected, "agent")} connected to Nexus." : _agentsInstalled > 0 ? $"{Plural(_agentsInstalled, "agent")} found, none connected yet." : "No coding agents found on this computer yet.",
            _agentsConnected > 0 || _agentsInstalled == 0 ? null : ("Connect agents", Step.Agents)));
        panel.Children.Add(Summary(_approved.Count > 0, "Folders",
            _approved.Count > 0 ? "Capturing " + string.Join(", ", _approved.Take(3)) + (_approved.Count > 3 ? $" and {_approved.Count - 3} more." : ".") : "No folders captured yet, so no chats are saved.",
            _approved.Count > 0 ? null : ("Choose folders", Step.Workspaces)));
        if (SupportsPortableStartup) panel.Children.Add(StartupLine());
        panel.Children.Add(Para("Run this setup again any time from Settings > General.", 0.6, 12, new Thickness(0, 8, 0, 0)));
        return panel;
    }

    private int IndexOf(Step step) => Array.FindIndex(Steps, s => s.Step == step);

    private static string Plural(int count, string noun) => count == 1 ? $"1 {noun}" : $"{count} {noun}s";

    // ---- small builders

    // One row of the Done summary; anything not done gets a link back to
    // the step that sets it up.
    private UIElement Summary(bool done, string title, string text, (string Label, Step Step)? fix = null)
    {
        var grid = new Grid { ColumnSpacing = 12, Padding = new Thickness(0, 10, 0, 10), BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 0, 0, 1) };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(20) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(110) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        if (fix is { } action)
        {
            var link = new HyperlinkButton { Content = action.Label, Padding = new Thickness(6, 2, 6, 2), VerticalAlignment = VerticalAlignment.Center };
            AutomationProperties.SetName(link, action.Label + " (" + title + ")");
            link.Click += (_, _) => Go(IndexOf(action.Step));
            Grid.SetColumn(link, 3);
            grid.Children.Add(link);
        }
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

    // Host with its port when it is not the default, e.g. "127.0.0.1:47900".
    private static string HostOf(string url) => Uri.TryCreate(url, UriKind.Absolute, out var uri) ? uri.Authority : url;

    private static bool HasEnvToken() =>
        !string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable("NEXUS_TOKEN")) ||
        !string.IsNullOrWhiteSpace(Environment.GetEnvironmentVariable("CENTRAL_MEMORY_TOKEN"));
}
