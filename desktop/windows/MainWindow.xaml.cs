using System.Text.Json;
using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Nexus.Pages;

namespace Nexus;

public sealed partial class MainWindow : Window
{
    private bool _quitting;
    private bool _navLock;

    public new static MainWindow? Current { get; private set; }

    public ShellState State => ShellState.Current;

    public MainWindow()
    {
        InitializeComponent();
        Current = this;
        ExtendsContentIntoTitleBar = true;
        SystemBackdrop = new MicaBackdrop();
        AppWindow.Closing += OnClosing;
        TeleportBadge.Visibility = Visibility.Collapsed;
        Nav.SelectedItem = TimelineItem;
        ContentFrame.Navigate(typeof(TimelinePage));
        App.Core.Subscribe(DispatcherQueue, OnCoreEvent);
        _ = App.Core.CallAsync(NxMethods.EventsSubscribe);
        TryEnableLoginStart();
        ApplyPendingLink();
        _ = RefreshBannerAsync();
    }

    public void OpenSession(string sessionId)
    {
        Activate();
        AppWindow.Show();
        ContentFrame.Navigate(typeof(SessionPage), sessionId);
    }

    public void OpenTeleport(string? teleportId = null)
    {
        Activate();
        AppWindow.Show();
        _navLock = true;
        Nav.SelectedItem = TeleportItem;
        _navLock = false;
        ContentFrame.Navigate(typeof(TeleportPage), teleportId ?? "");
    }

    public void HandleRemote(string payload)
    {
        var lines = payload.Split('\n', StringSplitOptions.RemoveEmptyEntries);
        App.ReadDeepLink(lines);
        ApplyPendingLink();
        Activate();
        AppWindow.Show();
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
        sender.Hide();
    }

    private void Nav_ItemInvoked(NavigationView sender, NavigationViewItemInvokedEventArgs args)
    {
        if (_navLock || args.InvokedItemContainer is not NavigationViewItem item || item.Tag is not string tag)
        {
            return;
        }
        ContentFrame.Navigate(tag switch
        {
            "memory" => typeof(MemoryPage),
            "teleport" => typeof(TeleportPage),
            "agents" => typeof(AgentsPage),
            "settings" => typeof(SettingsPage),
            _ => typeof(TimelinePage)
        });
    }

    private void Tray_Open(object sender, RoutedEventArgs e)
    {
        Activate();
        AppWindow.Show();
    }

    private async void Tray_Pause(object sender, RoutedEventArgs e)
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

    private void Tray_Teleport(object sender, RoutedEventArgs e) => OpenTeleport();

    private void Tray_Quit(object sender, RoutedEventArgs e)
    {
        _quitting = true;
        App.Core.Shutdown();
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
        ContentFrame.Navigate(typeof(SearchPage), box.Text);
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
        var pending = 0;
        if (uploads.Ok && uploads.Result is { } uploadBody &&
            uploadBody.TryGetProperty("pending", out var pendingEl) &&
            pendingEl.TryGetInt32(out var parsed))
        {
            pending = parsed;
        }
        var timelineOffline = timeline.Error?.Contains("offline", StringComparison.OrdinalIgnoreCase) == true;
        var offline = !online || timelineOffline;
        State.PendingUploads = pending;
        State.Offline = offline;
        State.Banner = offline
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
