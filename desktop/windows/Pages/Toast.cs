using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Automation.Peers;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Short notices at the bottom centre of the window, with Undo for
/// archive, delete and other reversible actions. At most three stack; each
/// slides up in 180 ms, stays 5 s (8 s with Undo; the pointer over it stops
/// the clock) and fades out in 150 ms. Ctrl/Cmd+Z undoes the newest one while
/// it shows. Screen readers hear each notice as it appears. State and timing
/// live in <see cref="ToastQueue"/>; this class only draws it.
/// </summary>
internal static class Toast
{
    private static readonly ToastQueue s_queue = new();
    private static readonly Dictionary<long, FrameworkElement> s_views = new();
    private static readonly Dictionary<string, long> s_keyed = new(StringComparer.Ordinal);
    private static StackPanel? s_host;
    private static DispatcherQueueTimer? s_timer;

    /// <summary>Adds the host to the window's root grid (once).</summary>
    public static void Attach(Grid root)
    {
        if (s_host is not null) return;
        s_host = new StackPanel
        {
            Spacing = 8,
            HorizontalAlignment = HorizontalAlignment.Center,
            VerticalAlignment = VerticalAlignment.Bottom,
            // Above the chat composer, so a notice never covers what you type.
            Margin = new Thickness(16, 0, 16, 104),
            MaxWidth = 520
        };
        Grid.SetRowSpan(s_host, Math.Max(1, root.RowDefinitions.Count));
        AutomationProperties.SetName(s_host, "Notices");
        root.Children.Add(s_host);
        s_timer = root.DispatcherQueue.CreateTimer();
        s_timer.Interval = TimeSpan.FromMilliseconds(200);
        s_timer.Tick += (_, _) =>
        {
            s_queue.Tick(DateTime.UtcNow);
            if (s_queue.Visible.Count == 0) s_timer.Stop();
        };
        s_queue.Ended += (item, outcome) => _ = RemoveAsync(item.Id);
    }

    /// <summary>
    /// A notice; with <paramref name="onUndo"/> it offers Undo (or <paramref name="actionLabel"/>).
    /// A notice with the same <paramref name="key"/> replaces the previous one
    /// (text size 110%, 120%, 130% is one notice, not three).
    /// </summary>
    public static void Show(string message, Action? onUndo = null, Action? onExpire = null, string? actionLabel = null, string? key = null)
    {
        if (key is not null && s_keyed.TryGetValue(key, out var previous))
        {
            s_keyed.Remove(key);
            s_queue.Dismiss(previous);
        }
        if (s_host is null)
        {
            // No window yet: nothing to undo with, so commit right away.
            onExpire?.Invoke();
            return;
        }
        var item = s_queue.Show(message, DateTime.UtcNow, actionLabel, onUndo, onExpire);
        if (key is not null) s_keyed[key] = item.Id;
        var view = Build(item);
        s_views[item.Id] = view;
        s_host.Children.Add(view);
        Motion.FadeIn(view, rise: 12, duration: TimeSpan.FromMilliseconds(180));
        Announce(view, item.HasUndo ? message + ". " + (item.ActionLabel ?? "Undo") + " is available, or press " + ShortcutRegistry.Label(ShortcutRegistry.Find(ShortcutRegistry.Undo)!, Shortcuts.UseCommand) + "." : message);
        s_timer?.Start();
    }

    /// <summary>Ctrl/Cmd+Z outside a text box. True when a notice was undone.</summary>
    public static bool UndoLatest() => s_queue.UndoLatest();

    /// <summary>The app is closing: pending deletes go ahead now.</summary>
    public static void Flush() => s_queue.Flush();

    public static int VisibleCount => s_queue.Visible.Count;

    private static FrameworkElement Build(ToastQueue.Item item)
    {
        var row = new Grid { ColumnSpacing = 8, VerticalAlignment = VerticalAlignment.Center };
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var text = new TextBlock
        {
            Text = item.Message, FontSize = NexusTokens.FontBody, TextWrapping = TextWrapping.Wrap,
            VerticalAlignment = VerticalAlignment.Center, MaxWidth = 380
        };
        row.Children.Add(text);
        if (item.HasUndo)
        {
            var undo = new Button
            {
                Content = item.ActionLabel, Padding = new Thickness(10, 4, 10, 4), MinHeight = 28,
                Foreground = NexusPalette.Accent, Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
                BorderThickness = new Thickness(0), FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                VerticalAlignment = VerticalAlignment.Center
            };
            AutomationProperties.SetName(undo, (item.ActionLabel ?? "Undo") + ": " + item.Message);
            ToolTipService.SetToolTip(undo, (item.ActionLabel ?? "Undo") + " (" + ShortcutRegistry.Label(ShortcutRegistry.Find(ShortcutRegistry.Undo)!, Shortcuts.UseCommand) + ")");
            var id = item.Id;
            undo.Click += (_, _) => s_queue.Act(id);
            Grid.SetColumn(undo, 1);
            row.Children.Add(undo);
        }
        var close = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 10 }, Padding = new Thickness(6), MinWidth = 26, MinHeight = 26,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
            VerticalAlignment = VerticalAlignment.Center, Opacity = 0.7
        };
        AutomationProperties.SetName(close, "Dismiss notice");
        var closeId = item.Id;
        close.Click += (_, _) => s_queue.Dismiss(closeId);
        Grid.SetColumn(close, 2);
        row.Children.Add(close);

        var card = new Border
        {
            Child = row,
            Padding = new Thickness(14, 8, 6, 8),
            CornerRadius = NexusTokens.RadiusMedium,
            Background = ThemeService.Brush("NexusSurfaceBrush", NexusTokens.BrandInk),
            BorderBrush = NexusPalette.Hairline,
            BorderThickness = new Thickness(1),
            HorizontalAlignment = HorizontalAlignment.Center,
            MinWidth = 260
        };
#if !HAS_UNO
        card.Translation = new System.Numerics.Vector3(0, 0, 24);
        card.Shadow = new ThemeShadow();
#endif
        AutomationProperties.SetName(card, item.Message);
        AutomationProperties.SetLiveSetting(card, AutomationLiveSetting.Polite);
        var pauseId = item.Id;
        card.PointerEntered += (_, _) => s_queue.Pause(pauseId, DateTime.UtcNow);
        card.PointerExited += (_, _) => s_queue.Resume(pauseId, DateTime.UtcNow);
        return card;
    }

    private static async Task RemoveAsync(long id)
    {
        foreach (var stale in s_keyed.Where(pair => pair.Value == id).Select(pair => pair.Key).ToList()) s_keyed.Remove(stale);
        if (!s_views.Remove(id, out var view) || s_host is null) return;
        try { await Motion.FadeOutAsync(view, 150); }
        catch { /* decoration */ }
        s_host.Children.Remove(view);
    }

    private static void Announce(FrameworkElement view, string text)
    {
        try
        {
            if ((FrameworkElementAutomationPeer.FromElement(view) ?? FrameworkElementAutomationPeer.CreatePeerForElement(view)) is { } peer)
                peer.RaiseNotificationEvent(AutomationNotificationKind.ActionCompleted, AutomationNotificationProcessing.ImportantMostRecent, text, "nexus-toast");
        }
        catch
        {
            // Uno and older peers: the live region on the card still announces it.
        }
    }
}
