using System.Text.Json;
using CommunityToolkit.Mvvm.Input;
using H.NotifyIcon;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Nexus.Pages;
using Windows.Graphics;

namespace Nexus;

public sealed partial class MainWindow : Window
{
    private bool _quitting;
    private bool _navLock;

    public static new MainWindow? Current { get; private set; }

    public ShellState State => ShellState.Current;
    public IRelayCommand TrayOpenCommand { get; }
    public IAsyncRelayCommand TrayPauseCommand { get; }
    public IRelayCommand TrayTeleportCommand { get; }
    public IRelayCommand TrayQuitCommand { get; }

    public MainWindow()
    {
        // H.NotifyIcon converts a MenuFlyout into a native Win32 popup. Commands
        // cross that boundary reliably; Click handlers do not in an unpackaged
        // WinUI application.
        TrayOpenCommand = new RelayCommand(RestoreWindow);
        TrayPauseCommand = new AsyncRelayCommand(PauseCaptureAsync);
        TrayTeleportCommand = new RelayCommand(() => OpenTeleport());
        TrayQuitCommand = new RelayCommand(Quit);
        InitializeComponent();
        // The tray lives outside the XAML visual tree. Create it explicitly so
        // its native menu remains available after the main window is hidden.
        Tray.ForceCreate();
        Current = this;
        AppWindow.Resize(new SizeInt32(1240, 820));
        ExtendsContentIntoTitleBar = true;
        SystemBackdrop = new MicaBackdrop();
        AppWindow.Closing += OnClosing;
        TeleportBadge.Visibility = Visibility.Collapsed;
        Nav.SelectedItem = TimelineItem;
        // Navigate after the window/frame is ready — ctor Navigate can AV on unpackaged WinUI.
        ContentFrame.Loaded += OnContentFrameLoaded;
        App.Core.Subscribe(DispatcherQueue, OnCoreEvent);
        _ = App.Core.CallAsync(NxMethods.EventsSubscribe);
        TryEnableLoginStart();
        _ = RefreshBannerAsync();
    }

    private void OnContentFrameLoaded(object sender, RoutedEventArgs e)
    {
        ContentFrame.Loaded -= OnContentFrameLoaded;
        if (ContentFrame.Content is null)
        {
            // Set Content directly — Frame.Navigate(typeof(...)) AVs for pure C# pages
            // when the VS Appx/PRI pipeline isn't fully available.
            ShowPage(new TimelinePage());
        }
        ApplyPendingLink();
    }

    private void ShowPage(NexusPage page, object? parameter = null)
    {
        // Enter before parenting so Loaded won't fire a second null Enter.
        page.Enter(parameter);
        ContentFrame.Content = page;
    }

    public void OpenSession(string sessionId)
    {
        RestoreWindow();
        ShowPage(new SessionPage(), sessionId);
    }

    public void ShowTimeline(string? query = null)
    {
        _navLock = true;
        Nav.SelectedItem = TimelineItem;
        _navLock = false;
        ShowPage(new TimelinePage(), query ?? "");
    }

    public void ShowSettings()
    {
        _navLock = true;
        Nav.SelectedItem = SettingsItem;
        _navLock = false;
        ShowPage(new SettingsPage());
    }

    public void OpenTeleport(string? teleportId = null)
    {
        RestoreWindow();
        _navLock = true;
        Nav.SelectedItem = TeleportItem;
        _navLock = false;
        ShowPage(new TeleportPage(), teleportId ?? "");
    }

    public void HandleRemote(string payload)
    {
        var lines = payload.Split('\n', StringSplitOptions.RemoveEmptyEntries);
        App.ReadDeepLink(lines);
        ApplyPendingLink();
        RestoreWindow();
    }

    private void ApplyPendingLink()
    {
        if (App.PendingSessionId is { Length: > 0 } sessionId)
        {
            OpenSession(sessionId);
        }
        else if (App.PendingTeleportId is { Length: > 0 } teleportId)
        {
            OpenTeleport(teleportId);
        }
        if (App.PendingAuthUrl is { Length: > 0 } authUrl)
        {
            _ = App.Core.CallAsync(NxMethods.AuthCallback, new { url = authUrl });
        }
        App.ClearPending();
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
        ShowPage(tag switch
        {
            "memory" => new MemoryPage(),
            "teleport" => new TeleportPage(),
            "agents" => new AgentsPage(),
            "settings" => new SettingsPage(),
            _ => new TimelinePage()
        });
    }

    private void RestoreWindow()
    {
        if (_quitting)
        {
            return;
        }

        // H.NotifyIcon also takes the process out of its background efficiency
        // mode. AppWindow.Show alone can leave an unpackaged WinUI window hidden
        // or behind the foreground window after it has been sent to the tray.
        WindowExtensions.Show(this);
        Activate();
    }

    private async Task PauseCaptureAsync()
    {
        var method = State.CapturePaused ? NxMethods.CaptureResume : NxMethods.CapturePause;
        var response = await App.Core.CallAsync(method);
        await UiThread.Resume(DispatcherQueue);
        if (response.Error is { } error && error.Contains("handled by the app shell", StringComparison.Ordinal))
        {
            State.CapturePaused = !State.CapturePaused;
            PauseItem.Text = State.CapturePaused ? "Resume capture" : "Pause capture";
            return;
        }
        if (!response.Ok)
        {
            PauseItem.Text = response.Error ?? "Pause capture";
        }
    }

    private void Quit()
    {
        if (_quitting)
        {
            return;
        }

        _quitting = true;
        App.Core.Shutdown();
        Tray.Dispose();
        AppWindow.Closing -= OnClosing;
        AppWindow.Destroy();
        Application.Current.Exit();
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
        if (await dialog.ShowAsync() != ContentDialogResult.Primary)
        {
            return;
        }
        ShowPage(new SearchPage(), box.Text);
    }

    private void OnCoreEvent(string json)
    {
        if (json.Contains("teleport", StringComparison.OrdinalIgnoreCase))
        {
            _ = RefreshInboxBadgeAsync();
        }
        _ = RefreshBannerAsync();
    }

    public void SetInboxCount(int count)
    {
        TeleportBadge.Value = count;
        TeleportBadge.Visibility = count > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private async Task RefreshInboxBadgeAsync()
    {
        var response = await App.Core.CallAsync(NxMethods.TeleportInbox);
        await UiThread.Resume(DispatcherQueue);
        if (response.Ok)
        {
            SetInboxCount(NxJson.Items(response.Result).Count());
        }
    }

    private async Task RefreshBannerAsync()
    {
        var net = await App.Core.CallAsync(NxMethods.NetStatus);
        var uploads = await App.Core.CallAsync(NxMethods.UploadsStatus);
        var timeline = await App.Core.CallAsync(NxMethods.TimelineList);
        await UiThread.Resume(DispatcherQueue);
        var online = true;
        if (net.Ok && net.Result is { } netBody && netBody.TryGetProperty("online", out var flag))
        {
            online = flag.ValueKind == JsonValueKind.True;
        }
        var auth = await App.Core.CallAsync(NxMethods.AuthStatus);
        var signedIn = auth.Ok && auth.Result is { } authBody &&
            authBody.TryGetProperty("signed_in", out var signed) &&
            signed.ValueKind == JsonValueKind.True;
        var pending = 0;
        if (uploads.Ok && uploads.Result is { } uploadBody &&
            uploadBody.TryGetProperty("pending", out var pendingEl) &&
            pendingEl.TryGetInt32(out var parsed))
        {
            pending = parsed;
        }
        var timelineOffline = timeline.Error?.Contains("offline", StringComparison.OrdinalIgnoreCase) == true
            || timeline.Error?.Contains("not signed in", StringComparison.OrdinalIgnoreCase) == true;
        var offline = !online || !signedIn || timelineOffline;
        State.PendingUploads = pending;
        State.Offline = offline;
        State.Banner = !signedIn
            ? "Sign in from Settings to load Timeline, Memory, and Teleport from the cloud."
            : offline
            ? $"Offline: showing cached data, read-only. Capture continues ({pending} items waiting to upload)."
            : "";
        OfflineBar.IsOpen = offline;
        OfflineBar.Message = State.Banner;
    }

    private static void TryEnableLoginStart()
    {
        try
        {
            if (!LoginStart.IsEnabled())
            {
                LoginStart.SetEnabled(true);
            }
        }
        catch (InvalidOperationException)
        {
        }
    }
}
