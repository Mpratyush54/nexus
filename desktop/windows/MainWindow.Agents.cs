#if !HAS_UNO
using H.NotifyIcon;
#endif
using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using Nexus.Core.Diagnostics;
using Nexus.Pages;

namespace Nexus;

// Agents working in the background, app-wide: the tray (menu bar on macOS)
// shows how many run, a notification says when a chat finishes or needs
// approval while the window isn't focused, and quitting asks first.
public sealed partial class MainWindow
{
    private bool _windowFocused = true;
    private string _notifiedSessionId = "";
    private bool _runningFilter;

    private void InitAgentSessions()
    {
        // By name: WinUI and Uno use different enum types for this.
        Activated += (_, e) =>
        {
            _windowFocused = e.WindowActivationState.ToString() != "Deactivated";
            // Coming back to a chat that finished meanwhile: it is seen now.
            if (_windowFocused) MarkSeenIfWatching((ContentFrame.Content as SessionPage)?.SessionId ?? "");
        };
        LiveSessions.Current.Changed += (previous, next) => DispatcherQueue.TryEnqueue(() => OnAgentStatusChanged(previous, next));
#if !HAS_UNO
        // A click on the notification (a tray balloon, shown as a toast).
        Tray.TrayIcon.MessageWindow.MouseEventReceived += (_, e) =>
        {
            if (e.MouseEvent == H.NotifyIcon.Core.MouseEvent.BalloonToolTipClicked) DispatcherQueue.TryEnqueue(OpenNotifiedChat);
        };
#endif
        _ = LoadAgentSessionsAsync();
    }

    private async Task LoadAgentSessionsAsync()
    {
        var rules = await App.Core.CallAsync("live.rules.list");
        if (rules.Ok && rules.Result is { } r && NxJson.Text(r, "default_mode") is { Length: > 0 } mode)
            LiveSessions.Current.DefaultMode = mode;
        var list = await App.Core.CallAsync("live.list");
        await UiThread.Resume(DispatcherQueue);
        if (list.Ok && list.Result is { } l) LiveSessions.Current.Load(l);
        UpdateAgentsTray();
    }

    private void OnAgentStatusChanged(LiveSessions.ChatStatus? previous, LiveSessions.ChatStatus next)
    {
        UpdateAgentsTray();
        if (_runningFilter) RenderChats();
        var visible = (ContentFrame.Content as SessionPage)?.SessionId ?? "";
        // The chat on screen in a focused window was seen as it happened.
        if (next.Unread && next.Status is "done" or "error" && string.Equals(visible, next.SessionId, StringComparison.OrdinalIgnoreCase))
            MarkSeenIfWatching(next.SessionId);
        if (LiveSessions.NotificationFor(previous, next, _windowFocused && AppWindow.IsVisible, AppPrefs.Data.AgentNotifications ?? "all") is { } note)
            Notify(next.SessionId, note.Title, note.Body);
    }

    /// <summary>A chat on screen in the focused window has no unread reply.</summary>
    internal void MarkSeenIfWatching(string sessionId)
    {
        if (sessionId.Length == 0 || !_windowFocused || !AppWindow.IsVisible) return;
        if ((ContentFrame.Content as SessionPage)?.SessionId != sessionId) return;
        if (LiveSessions.Current.Get(sessionId) is { Unread: true })
            _ = App.Core.CallAsync("live.seen", new { session_id = sessionId });
    }

    private void Notify(string sessionId, string title, string body)
    {
        _notifiedSessionId = sessionId;
        DiagLog.Info("agents", "notification: " + title);
        try
        {
#if HAS_UNO
            Nexus.Platform.MacNative.Notify(title, body, "nexus://session/" + Uri.EscapeDataString(sessionId));
#else
            Tray.ShowNotification(title, body);
#endif
        }
        catch (Exception ex) { DiagLog.Warn("agents", "notification failed", ex); }
    }

    private void OpenNotifiedChat()
    {
        RestoreWindow();
        if (_notifiedSessionId.Length > 0) OpenSession(_notifiedSessionId);
    }

    /// <summary>The tray tooltip and menu line, and the macOS menu bar item: "2 agents working".</summary>
    private void UpdateAgentsTray()
    {
        var summary = LiveSessions.Current.Summary();
#if HAS_UNO
        Nexus.Platform.MacNative.SetAgentsStatus(summary);
#else
        AgentsItem.Text = summary.Length > 0 ? summary : "No agents working";
        AgentsItem.Visibility = summary.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        AgentsSeparator.Visibility = AgentsItem.Visibility;
        Tray.ToolTipText = summary.Length > 0 ? "Nexus · " + summary : "Nexus";
#endif
    }

    /// <summary>Tray "N agents working": open Nexus showing the chats that are working.</summary>
    private void ShowWorkingChats()
    {
        RestoreWindow();
        _runningFilter = true;
        UpdateProjectFilterLabel();
        RenderChats();
    }

    /// <summary>A project's name as the sidebar shows it, or "".</summary>
    internal string ProjectDisplayName(string projectId) => ProjectName(projectId);

    private void AgentsItem_Click(object sender, RoutedEventArgs e) => ShowWorkingChats();

    private bool PassesRunningFilter(ChatPaneEntry entry) =>
        !_runningFilter || LiveSessions.Current.IndicatorFor(entry.Id) != LiveSessions.Indicator.None;

    private void AddRunningFilter(MenuFlyout menu)
    {
        var item = new ToggleMenuFlyoutItem { Text = "Working now or needs you", IsChecked = _runningFilter, Icon = new FontIcon { Glyph = "" } };
        item.Click += (_, _) => { _runningFilter = !_runningFilter; UpdateProjectFilterLabel(); RenderChats(); };
        menu.Items.Add(item);
        menu.Items.Add(new MenuFlyoutSeparator());
    }

    /// <summary>
    /// Asks before quitting while agents work. Returns true to quit. "Keep
    /// running" hides Nexus to the tray (menu bar) and the agents carry on.
    /// </summary>
    private async Task<bool> ConfirmQuitWithAgentsAsync()
    {
        var working = LiveSessions.Current.Working;
        if (working == 0 || Content?.XamlRoot is null) return true;
        RestoreWindow();
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot,
            Title = LiveSessions.QuitQuestion(working),
            Content = new TextBlock
            {
                Text = "Quitting stops " + (working == 1 ? "it" : "them") + "; each chat offers Resume the next time you open Nexus. "
                    + "Or keep Nexus running in the background: " + (OperatingSystem.IsMacOS() ? "the menu bar" : "the tray") + " shows when they finish.",
                TextWrapping = TextWrapping.Wrap
            },
            PrimaryButtonText = "Keep running in the background",
            SecondaryButtonText = "Quit anyway",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary
        };
        ContentDialogResult choice;
        try { choice = await dialog.ShowThemedAsync(); }
        catch (Exception) { return true; } // another dialog is open: the explicit Quit wins
        if (choice == ContentDialogResult.Primary)
        {
            WindowExtensions.Hide(this);
            return false;
        }
        return choice == ContentDialogResult.Secondary;
    }
}
