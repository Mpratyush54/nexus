using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Nexus.Core.Diagnostics;
using Nexus.Pages;
using Windows.ApplicationModel.DataTransfer;
using Windows.System;

namespace Nexus;

// The shell around the chats: the command palette (Ctrl/Cmd+K), the shortcut
// sheet (Ctrl/Cmd+/), undo notices, quick ask, text size and sidebar density,
// and the chat row menu (pin, rename, archive, delete, copy link). The
// accelerators come from Core/ShortcutRegistry.cs. MainWindow.xaml.cs only
// calls in here.
public sealed partial class MainWindow
{
    // Pin/archive/rename/delete shown at once, before the server answers.
    private sealed record ChatOverride(bool? Pinned = null, bool? Archived = null, string? Title = null, bool Deleted = false);
    private readonly Dictionary<string, ChatOverride> _chatOverrides = new(StringComparer.Ordinal);
    private List<ChatPaneEntry> _pinnedEntries = new();
    private List<ChatPaneEntry> _archivedEntries = new();
    private bool _archivedView;
    private int _chatStatesVersion;
    private const int MaxPinned = 10;

    // Items in the chat list besides chats.
    private sealed record ChatListHeader(string Text);
    private sealed record ChatListLink(string Text, string Kind);

    /// <summary>A chat for the palette.</summary>
    internal sealed record PaletteChat(string Id, string Title, string Agent, string Project, bool Pinned, bool Archived);

    private void InitShell()
    {
        Toast.Attach(RootGrid);
        BindShortcuts();
        ShellPrefs.Changed += () => DispatcherQueue.TryEnqueue(() => { _chatRows.Clear(); RenderChats(); ApplyTextScale(); });
        ContentFrame.RegisterPropertyChangedCallback(ContentControl.ContentProperty, (_, _) => ApplyTextScale());
        QuickAsk.Init(this);
        _ = LoadChatStatesAsync();
    }

    // ---- Accelerators, from the registry

    private void BindShortcuts()
    {
        var all = ShortcutRegistry.All(ShellPrefs.QuickAskKeys, Shortcuts.UseCommand);
        void Bind(string id, VirtualKey key, Action action, bool shift = false, bool alt = false, bool control = false)
        {
            if (ShortcutRegistry.Find(id, all) is not { } s) return;
            var mods = VirtualKeyModifiers.None;
            if (s.Primary) mods |= Shortcuts.Primary;
            if (s.Control || control) mods |= VirtualKeyModifiers.Control;
            if (s.Shift || shift) mods |= VirtualKeyModifiers.Shift;
            if (s.Alt || alt) mods |= VirtualKeyModifiers.Menu;
            var accelerator = new KeyboardAccelerator { Key = key, Modifiers = mods };
            accelerator.Invoked += (_, e) =>
            {
                // Text boxes keep their own keys (undo in a text box undoes typing).
                if (id == ShortcutRegistry.Undo && FocusedIsText()) return;
                e.Handled = true;
                DiagLog.Info("shortcut", id);
                action();
            };
            RootGrid.KeyboardAccelerators.Add(accelerator);
        }
        Bind(ShortcutRegistry.ShortcutSheet, (VirtualKey)191, ShowShortcutSheet);
        Bind(ShortcutRegistry.Settings, (VirtualKey)188, () => ShowSettings());
        Bind(ShortcutRegistry.Undo, VirtualKey.Z, () => Toast.UndoLatest());
        Bind(ShortcutRegistry.NextChat, VirtualKey.Tab, () => StepChat(+1));
        Bind(ShortcutRegistry.PreviousChat, VirtualKey.Tab, () => StepChat(-1));
        Bind(ShortcutRegistry.NextChatBracket, (VirtualKey)221, () => StepChat(+1));
        Bind(ShortcutRegistry.PreviousChatBracket, (VirtualKey)219, () => StepChat(-1));
        for (var n = 1; n <= 9; n++)
        {
            var index = n - 1;
            Bind(ShortcutRegistry.PinnedChat, VirtualKey.Number1 + index, () => OpenChatAt(index));
        }
        Bind(ShortcutRegistry.PinChat, VirtualKey.P, () => { if (OpenChatId is { Length: > 0 } id) TogglePin(id); });
        Bind(ShortcutRegistry.ArchiveChat, VirtualKey.A, () => { if (OpenChatId is { Length: > 0 } id) ArchiveChat(id); });
        Bind(ShortcutRegistry.ToggleSidebar, VirtualKey.B, ToggleSidebar);
        Bind(ShortcutRegistry.ToggleSidebarAlt, VirtualKey.S, ToggleSidebar);
        Bind(ShortcutRegistry.ZoomIn, (VirtualKey)187, () => ZoomText(+1));            // = and +
        Bind(ShortcutRegistry.ZoomIn, (VirtualKey)187, () => ZoomText(+1), shift: true);
        Bind(ShortcutRegistry.ZoomIn, VirtualKey.Add, () => ZoomText(+1));
        Bind(ShortcutRegistry.ZoomOut, (VirtualKey)189, () => ZoomText(-1));
        Bind(ShortcutRegistry.ZoomOut, VirtualKey.Subtract, () => ZoomText(-1));
        Bind(ShortcutRegistry.ZoomReset, VirtualKey.Number0, () => ZoomText(0));
        Bind(ShortcutRegistry.ZoomReset, VirtualKey.NumberPad0, () => ZoomText(0));
        // Shortcut hints are written out where they belong; no automatic tooltips.
        RootGrid.KeyboardAcceleratorPlacementMode = KeyboardAcceleratorPlacementMode.Hidden;
    }

    private bool FocusedIsText() =>
        Content?.XamlRoot is { } root && FocusManager.GetFocusedElement(root) is TextBox or PasswordBox or RichEditBox or AutoSuggestBox;

    // ---- Palette and sheet

    /// <summary>Ctrl/Cmd+K and File › Search on macOS.</summary>
    internal void ShowPalette(string query = "") => CommandPalette.Show(this, query);

    internal void ShowShortcutSheet() => ShortcutHelp.Show(this);

    internal void ShowQuickAsk() => QuickAsk.Open(this);

    private void QuickAskItem_Click(object sender, RoutedEventArgs e) => ShowQuickAsk();

    /// <summary>Quick ask sent in the background: say so, then the usual "finished" notification follows.</summary>
    internal void QuickAskStarted(string sessionId, string title, string agent)
    {
        RefreshChats();
        var line = $"{agent} is working on “{Short(title)}”. Nexus will tell you when it finishes.";
        if (AppWindow.IsVisible) Toast.Show(line, onUndo: () => OpenSession(sessionId), actionLabel: "Open");
        else Notify(sessionId, "Quick ask started", line);
    }

    internal void OpenChatFromQuickAsk(string sessionId)
    {
        RestoreWindow();
        OpenSession(sessionId);
        RefreshChats();
    }

    /// <summary>Where a floating window can't be made: Home's start composer does the same.</summary>
    internal void ShowQuickAskInWindow()
    {
        RestoreWindow();
        StartNewChat();
    }

    internal string OpenChatId => (ContentFrame.Content as SessionPage)?.SessionId ?? "";

    internal void ShowSearch(string query) => ShowPage(new SearchPage(), query);

    internal void ShowNav(string tag)
    {
        AppPrefs.LastNavTag = tag;
        ShowPage(CreatePageForTag(tag));
    }

    internal void SwitchToProject(string projectId) => SetProjectFilter(projectId);

    internal void FindInOpenChat() => (ContentFrame.Content as SessionPage)?.FindText("");

    internal void ToggleSidebar() => Nav.IsPaneOpen = !Nav.IsPaneOpen;

    internal void ToggleTheme() => ThemeService.Set(this, ThemeService.EffectiveIsDark ? ThemeService.Light : ThemeService.Dark);

    internal IEnumerable<PaletteChat> PaletteChats()
    {
        var seen = new HashSet<string>(StringComparer.Ordinal);
        foreach (var entry in _pinnedEntries.Concat(_chatEntries).Concat(_chatCache.Values.SelectMany(c => c.Entries)).Concat(_archivedEntries))
        {
            var e = Effective(entry);
            if (e is null || !seen.Add(e.Id)) continue;
            yield return new PaletteChat(e.Id, NxJson.Scrub(e.Title), ChatTitles.AgentName(e.Agent), ProjectName(e.ProjectId), e.Pinned, e.Archived);
            if (seen.Count >= 400) yield break;
        }
    }

    // ---- Text size and density

    /// <summary>+1 bigger, −1 smaller, 0 back to 100%. Applies to the chat transcript.</summary>
    internal void ZoomText(int direction)
    {
        var current = ShellPrefs.Current.TextScale;
        var next = direction == 0 ? 100 : ShellPrefs.ClampScale(current + direction * ShellPrefs.ScaleStep);
        if (next == current && direction != 0)
        {
            Toast.Show(direction > 0 ? $"Text is at the largest size ({next}%)." : $"Text is at the smallest size ({next}%).", key: "text-size");
            return;
        }
        ShellPrefs.Update(p => p.TextScale = next);
        Toast.Show($"Text size {next}%" + (next == 100 ? "" : " · " + ShortcutRegistry.Label(ShortcutRegistry.Find(ShortcutRegistry.ZoomReset)!, Shortcuts.UseCommand) + " resets"), key: "text-size");
    }

    private void ApplyTextScale()
    {
        if (ContentFrame.Content is SessionPage page) page.ApplyTextScale(ShellPrefs.Current.TextScale / 100.0);
    }

    internal void ToggleDensity() => ShellPrefs.Update(p => p.Density = ShellPrefs.Compact ? "comfortable" : "compact");

    // ---- Keyboard chat switching

    private List<ChatPaneEntry> ShownChats() => _chatItems.OfType<ChatPaneEntry>().ToList();

    private void StepChat(int step)
    {
        var shown = ShownChats();
        if (shown.Count == 0) return;
        var current = shown.FindIndex(e => e.Id == OpenChatId);
        var next = current < 0 ? (step > 0 ? 0 : shown.Count - 1) : (current + step + shown.Count) % shown.Count;
        OpenSession(shown[next].Id);
    }

    /// <summary>Ctrl/Cmd+1..9: pinned chat N, or the Nth chat when nothing is pinned.</summary>
    private void OpenChatAt(int index)
    {
        var pinned = VisiblePinned();
        var list = pinned.Count > 0 ? pinned : ShownChats();
        if (index < list.Count) OpenSession(list[index].Id);
    }

    // ---- Pinned and archived chats

    private ChatPaneEntry? Effective(ChatPaneEntry entry)
    {
        if (!_chatOverrides.TryGetValue(entry.Id, out var o)) return entry;
        if (o.Deleted) return null;
        return entry with
        {
            Pinned = o.Pinned ?? entry.Pinned,
            Archived = o.Archived ?? entry.Archived,
            Title = o.Title is { Length: > 0 } t ? t : entry.Title
        };
    }

    internal bool IsChatPinned(string id) => FindEntry(id) is { Pinned: true };

    private ChatPaneEntry? FindEntry(string id)
    {
        var raw = _pinnedEntries.Concat(_chatEntries).Concat(_archivedEntries).FirstOrDefault(e => e.Id == id);
        return raw is null ? null : Effective(raw);
    }

    private List<ChatPaneEntry> VisiblePinned()
    {
        var pinned = new List<ChatPaneEntry>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        // Server list first (its order), then chats pinned a moment ago.
        foreach (var raw in _pinnedEntries.Concat(_chatEntries))
        {
            if (Effective(raw) is not { Pinned: true, Archived: false } e || !seen.Add(e.Id)) continue;
            if (!ChatMatches(e, _projectFilter, "") || !PassesTagFilter(e)) continue;
            pinned.Add(e);
        }
        // Pinned just now go first, like the server's newest-pin-first order.
        return pinned.OrderByDescending(e => _chatOverrides.TryGetValue(e.Id, out var o) && o.Pinned == true).Take(MaxPinned).ToList();
    }

    /// <summary>
    /// RenderChats: the items between the "Updating…" line and the status
    /// line. A "Pinned" group on top, then the other chats; an "Archived (N)"
    /// link at the bottom. In the archived view, only archived chats.
    /// </summary>
    private void ComposeChatItems(List<object> items, HashSet<string> seen)
    {
        if (_archivedView)
        {
            items.Add(new ChatListLink("← Back to chats", "back"));
            foreach (var raw in _archivedEntries.Concat(_chatEntries))
            {
                if (Effective(raw) is not { Archived: true } e) continue;
                if (!ChatMatches(e, _projectFilter, ChatQuery) || !seen.Add(e.Id)) continue;
                items.Add(e);
            }
            if (seen.Count == 0) items.Add(new ChatListNotice("No archived chats" + (_projectFilter.Length > 0 ? " in this project." : ".")));
            return;
        }
        var searching = ChatQuery.Length > 0;
        if (!searching && !_runningFilter)
        {
            var pinned = VisiblePinned();
            if (pinned.Count > 0)
            {
                items.Add(new ChatListHeader("Pinned"));
                foreach (var e in pinned) if (seen.Add(e.Id)) items.Add(e);
                items.Add(new ChatListHeader("Chats"));
            }
        }
        foreach (var raw in _chatEntries)
        {
            if (Effective(raw) is not { } e || e.Archived) continue;
            if (PassesTagFilter(e) && seen.Add(e.Id)) items.Add(e);
        }
    }

    /// <summary>The "Archived (N)" link under the list (after any status line).</summary>
    private void AddArchivedLink(List<object> items)
    {
        if (_archivedView || ChatQuery.Length > 0) return;
        var count = _archivedEntries.Concat(_chatEntries).Select(Effective).Where(e => e is { Archived: true } && ChatMatches(e, _projectFilter, "")).Select(e => e!.Id).Distinct().Count();
        if (count > 0) items.Add(new ChatListLink($"Archived ({count})", "archived"));
    }

    private UIElement? ShellListElement(object? data)
    {
        switch (data)
        {
            case ChatListHeader header:
                return new TextBlock
                {
                    Text = header.Text, FontSize = NexusTokens.FontMicro, Opacity = 0.5, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                    Margin = new Thickness(10, 8, 8, 2)
                };
            case ChatListLink link:
                var button = new Button
                {
                    Content = new TextBlock { Text = link.Text, FontSize = 12, Opacity = 0.7 },
                    HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Left,
                    Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
                    Padding = new Thickness(10, 6, 10, 6), Margin = new Thickness(0, 6, 0, 0), CornerRadius = new CornerRadius(6)
                };
                AutomationProperties.SetName(button, link.Kind == "back" ? "Back to chats" : link.Text.Replace("(", "chats, ").Replace(")", ""));
                button.Click += (_, _) => { if (link.Kind == "back") HideArchivedChats(); else ShowArchivedChats(); };
                return button;
        }
        return null;
    }

    internal void ShowArchivedChats()
    {
        RestoreWindowIfHidden();
        _archivedView = true;
        _runningFilter = false;
        UpdateProjectFilterLabel();
        RenderChats();
        _ = LoadChatStatesAsync();
    }

    private void HideArchivedChats()
    {
        _archivedView = false;
        UpdateProjectFilterLabel();
        RenderChats();
    }

    private void RestoreWindowIfHidden()
    {
        if (!Nav.IsPaneOpen) Nav.IsPaneOpen = true;
    }

    /// <summary>Pinned and archived chats from the server (each list is short).</summary>
    private async Task LoadChatStatesAsync()
    {
        var version = ++_chatStatesVersion;
        var pinned = App.Core.CallAsync(NxMethods.TimelineList, new { pinned = "only" });
        var archived = App.Core.CallAsync(NxMethods.TimelineList, new { archived = "only" });
        await Task.WhenAll(pinned, archived);
        await UiThread.Resume(DispatcherQueue);
        if (version != _chatStatesVersion) return;
        // An older server ignores the filter and sends ordinary chats: only
        // rows that say they are pinned (archived) count.
        _pinnedEntries = Parse(pinned.Result).Where(e => e.Pinned).ToList();
        _archivedEntries = Parse(archived.Result).Where(e => e.Archived).ToList();
        _chatRows.Clear();
        RenderChats();

        static List<ChatPaneEntry> Parse(NxResponse response)
        {
            var list = new List<ChatPaneEntry>();
            if (response.Ok) AppendChats(response, "", list);
            return list;
        }
    }

    private async Task<bool> SetChatStateAsync(string id, object args, string failure)
    {
        var response = await App.Core.CallAsync(NxMethods.SessionsState, args);
        await UiThread.Resume(DispatcherQueue);
        if (response.Ok)
        {
            _ = LoadChatStatesAsync();
            return true;
        }
        DiagLog.Warn("chats", $"chat state change failed for {id}: {response.RawError}");
        Toast.Show(NxErrors.IsUnsupported(response.RawError) ? failure + " Your Nexus server needs an update for this." : failure + " " + (StateView.Friendly(response.Error) is { Length: > 0 } why ? why : "Try again."));
        return false;
    }

    private void Override(string id, Func<ChatOverride, ChatOverride> change)
    {
        _chatOverrides[id] = change(_chatOverrides.TryGetValue(id, out var o) ? o : new ChatOverride());
        RenderChats();
    }

    private void Restore(string id, ChatOverride? previous)
    {
        if (previous is null) _chatOverrides.Remove(id); else _chatOverrides[id] = previous;
        RenderChats();
    }

    internal async void TogglePin(string id)
    {
        var previous = _chatOverrides.GetValueOrDefault(id);
        var pin = !IsChatPinned(id);
        if (pin && VisiblePinned().Count >= MaxPinned)
        {
            Toast.Show($"You can pin up to {MaxPinned} chats. Unpin one first.");
            return;
        }
        Override(id, o => o with { Pinned = pin, Archived = pin ? false : o.Archived });
        if (pin) _archivedView = _archivedView && _archivedEntries.Any(e => e.Id != id);
        if (!await SetChatStateAsync(id, new { session_id = id, pinned = pin }, pin ? "Couldn't pin that chat." : "Couldn't unpin that chat."))
            Restore(id, previous);
        else
            Announce(pin ? "Pinned" : "Unpinned");
    }

    internal async void ArchiveChat(string id)
    {
        var previous = _chatOverrides.GetValueOrDefault(id);
        var title = FindEntry(id)?.Title ?? "Chat";
        var wasOpen = OpenChatId == id;
        Override(id, o => o with { Archived = true, Pinned = false });
        if (wasOpen) ShowDashboard();
        if (!await SetChatStateAsync(id, new { session_id = id, archived = true }, "Couldn't archive that chat."))
        {
            Restore(id, previous);
            return;
        }
        Toast.Show($"Archived “{Short(title)}”", onUndo: () => _ = UnarchiveAsync(id, previous));
    }

    private async Task UnarchiveAsync(string id, ChatOverride? previous)
    {
        Override(id, o => o with { Archived = false, Pinned = previous?.Pinned });
        if (await SetChatStateAsync(id, new { session_id = id, archived = false }, "Couldn't unarchive that chat."))
            Announce("Unarchived");
    }

    internal void UnarchiveChat(string id) => _ = UnarchiveAsync(id, null);

    /// <summary>Delete: hidden at once, deleted on the server when the 8 s Undo notice ends.</summary>
    internal void DeleteChat(string id)
    {
        var previous = _chatOverrides.GetValueOrDefault(id);
        var title = FindEntry(id)?.Title ?? "Chat";
        if (OpenChatId == id) ShowDashboard();
        Override(id, o => o with { Deleted = true });
        Toast.Show($"Deleted “{Short(title)}”",
            onUndo: () => Restore(id, previous),
            onExpire: () => _ = CommitDeleteAsync(id, previous, title));
    }

    private async Task CommitDeleteAsync(string id, ChatOverride? previous, string title)
    {
        var response = await App.Core.CallAsync(NxMethods.SessionsDelete, new { session_id = id });
        await UiThread.Resume(DispatcherQueue);
        if (response.Ok)
        {
            PageCache.ForgetSession(id);
            _chatEntries.RemoveAll(e => e.Id == id);
            foreach (var key in _chatCache.Keys.ToList()) _chatCache[key].Entries.RemoveAll(e => e.Id == id);
            _pinnedEntries.RemoveAll(e => e.Id == id);
            _archivedEntries.RemoveAll(e => e.Id == id);
            return;
        }
        Restore(id, previous);
        var forbidden = (response.RawError ?? "").Contains("403", StringComparison.Ordinal) || (response.RawError ?? "").Contains("owner", StringComparison.OrdinalIgnoreCase);
        Toast.Show(forbidden ? $"Only the person who started “{Short(title)}” can delete it." : $"Couldn't delete “{Short(title)}”. " + (StateView.Friendly(response.Error) is { Length: > 0 } why ? why : "Try again."));
    }

    internal void CopyChatLink(string id)
    {
        var data = new DataPackage();
        data.SetText("nexus://session/" + Uri.EscapeDataString(id));
        Clipboard.SetContent(data);
        Toast.Show("Link copied. It opens this chat in Nexus.");
    }

    private async void ForkChat(string id)
    {
        var response = await App.Core.CallAsync(NxMethods.SessionsFork, new { session_id = id });
        await UiThread.Resume(DispatcherQueue);
        var child = response is { Ok: true, Result: { } r } ? NxJson.Text(r, "id", "session_id") : "";
        if (child.Length == 0)
        {
            Toast.Show("Couldn't fork that chat. " + (StateView.Friendly(response.Error) is { Length: > 0 } why ? why : ""));
            return;
        }
        OpenSession(child);
        RefreshChats();
    }

    private static string Short(string title)
    {
        title = NxJson.Scrub(title);
        return title.Length > 40 ? title[..39] + "…" : title;
    }

    private void Announce(string text)
    {
        try
        {
            var peer = Microsoft.UI.Xaml.Automation.Peers.FrameworkElementAutomationPeer.FromElement(ChatScroll)
                ?? Microsoft.UI.Xaml.Automation.Peers.FrameworkElementAutomationPeer.CreatePeerForElement(ChatScroll);
            peer?.RaiseNotificationEvent(Microsoft.UI.Xaml.Automation.Peers.AutomationNotificationKind.ActionCompleted,
                Microsoft.UI.Xaml.Automation.Peers.AutomationNotificationProcessing.ImportantMostRecent, text, "nexus-chat-state");
        }
        catch { /* a courtesy */ }
    }

    // ---- The row: menu, hover "⋯", F2 rename, compact layout

    /// <summary>ChatRow calls this once per row it builds.</summary>
    private void DecorateChatRow(Button button, TextBlock title, ChatPaneEntry entry)
    {
        var menu = new MenuFlyout();
        menu.Opening += (_, _) => BuildRowMenu(menu, entry.Id, button, title);
        button.ContextFlyout = menu;
        button.KeyDown += (_, e) =>
        {
            if (e.Key == VirtualKey.F2) { e.Handled = true; BeginRename(entry.Id); }
            else if (e.Key == VirtualKey.Delete) { e.Handled = true; DeleteChat(entry.Id); }
        };
        if (entry.Pinned)
            ToolTipService.SetToolTip(button, "Pinned · " + (ToolTipService.GetToolTip(button) as string ?? entry.Title));

        // Hover "⋯": the same menu, for people who don't right-click.
        if (title.Parent is not Grid line) return;
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var more = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 12 },
            Padding = new Thickness(4, 0, 4, 0), MinWidth = 24, MinHeight = 20, Height = 20,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
            VerticalAlignment = VerticalAlignment.Center, Opacity = 0, IsTabStop = false
        };
        AutomationProperties.SetName(more, "More actions for " + entry.Title);
        ToolTipService.SetToolTip(more, "Pin, rename, archive and more");
        more.Click += (_, _) => menu.ShowAt(more);
        Grid.SetColumn(more, line.ColumnDefinitions.Count - 1);
        line.Children.Add(more);
        button.PointerEntered += (_, _) => FadeTo(more, 1, 120);
        button.PointerExited += (_, _) => FadeTo(more, 0, 80);
        button.GotFocus += (_, _) => FadeTo(more, 1, 120);
        button.LostFocus += (_, _) => FadeTo(more, 0, 80);
    }

    private static void FadeTo(UIElement element, double opacity, int ms)
    {
        if (!Motion.Enabled) { element.Opacity = opacity; return; }
        var animation = new Microsoft.UI.Xaml.Media.Animation.DoubleAnimation { To = opacity, Duration = TimeSpan.FromMilliseconds(ms) };
        Microsoft.UI.Xaml.Media.Animation.Storyboard.SetTarget(animation, element);
        Microsoft.UI.Xaml.Media.Animation.Storyboard.SetTargetProperty(animation, "Opacity");
        var story = new Microsoft.UI.Xaml.Media.Animation.Storyboard();
        story.Children.Add(animation);
        story.Begin();
    }

    private void BuildRowMenu(MenuFlyout menu, string id, Button button, TextBlock title)
    {
        menu.Items.Clear();
        var entry = FindEntry(id);
        var label = entry?.Title ?? title.Text;
        MenuFlyoutItem Item(string text, string glyph, Action action, string keys = "")
        {
            var item = new MenuFlyoutItem { Text = text, Icon = new FontIcon { Glyph = glyph } };
            if (keys.Length > 0) item.KeyboardAcceleratorTextOverride = keys;
            AutomationProperties.SetName(item, text);
            item.Click += (_, _) => action();
            menu.Items.Add(item);
            return item;
        }
        string Keys(string shortcutId) => ShortcutRegistry.Find(shortcutId) is { } s ? ShortcutRegistry.Label(s, Shortcuts.UseCommand) : "";
        var pinned = entry?.Pinned == true;
        var archived = entry?.Archived == true;
        if (!archived) Item(pinned ? "Unpin" : "Pin", pinned ? "" : "", () => TogglePin(id), Keys(ShortcutRegistry.PinChat));
        Item("Rename…", "", () => BeginRename(id), "F2");
        Item("Tags…", "", () => _ = ChatTags.EditAsync(button.XamlRoot, id, label));
        Item("Fork", "", () => ForkChat(id));
        Item("Copy link", "", () => CopyChatLink(id));
#if !HAS_UNO
        Item("Open in new window", "", () => ChatWindow.Open(id, label));
#endif
        menu.Items.Add(new MenuFlyoutSeparator());
        if (archived) Item("Unarchive", "", () => UnarchiveChat(id));
        else Item("Archive", "", () => ArchiveChat(id), Keys(ShortcutRegistry.ArchiveChat));
        var delete = Item("Delete", "", () => DeleteChat(id), "Del");
        delete.Foreground = NexusPalette.Danger;
    }

    /// <summary>Inline rename in the sidebar row: Enter saves, Esc cancels.</summary>
    internal void BeginRename(string id)
    {
        if (!_shownChatRows.TryGetValue(id, out var row) || row.Title.Parent is not Grid line)
        {
            // Not on screen (e.g. from the palette with the sidebar hidden): a small dialog instead.
            _ = RenameWithDialogAsync(id);
            return;
        }
        var current = FindEntry(id)?.Title ?? row.Title.Text;
        var box = new TextBox
        {
            Text = NxJson.Scrub(current), FontSize = 13.5, Padding = new Thickness(6, 2, 6, 2), MinHeight = 26,
            VerticalAlignment = VerticalAlignment.Center
        };
        AutomationProperties.SetName(box, "Chat name");
        AutomationProperties.SetHelpText(box, "Enter saves, Escape cancels");
        Grid.SetColumn(box, Grid.GetColumn(row.Title));
        row.Title.Visibility = Visibility.Collapsed;
        line.Children.Add(box);
        var done = false;
        void Finish(bool save)
        {
            if (done) return;
            done = true;
            line.Children.Remove(box);
            row.Title.Visibility = Visibility.Visible;
            if (save) Rename(id, box.Text);
            row.Button.Focus(FocusState.Programmatic);
        }
        box.KeyDown += (_, e) =>
        {
            if (e.Key == VirtualKey.Enter) { e.Handled = true; Finish(true); }
            else if (e.Key == VirtualKey.Escape) { e.Handled = true; Finish(false); }
        };
        box.LostFocus += (_, _) => Finish(true);
        box.Loaded += (_, _) => { box.Focus(FocusState.Programmatic); box.SelectAll(); };
    }

    private async Task RenameWithDialogAsync(string id)
    {
        if (Content?.XamlRoot is null) return;
        var box = new TextBox { Text = NxJson.Scrub(FindEntry(id)?.Title ?? ""), MinWidth = 320 };
        AutomationProperties.SetName(box, "Chat name");
        var dialog = new ContentDialog
        {
            XamlRoot = Content.XamlRoot, Title = "Rename chat", Content = box,
            PrimaryButtonText = "Rename", CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Primary
        };
        box.Loaded += (_, _) => { box.Focus(FocusState.Programmatic); box.SelectAll(); };
        if (await dialog.ShowThemedAsync() == ContentDialogResult.Primary) Rename(id, box.Text);
    }

    private async void Rename(string id, string text)
    {
        var name = string.Join(' ', (text ?? "").Split((char[]?)null, StringSplitOptions.RemoveEmptyEntries));
        if (name.Length > 200) name = name[..200];
        var before = FindEntry(id)?.Title ?? "";
        if (name == NxJson.Scrub(before)) return;
        var previous = _chatOverrides.GetValueOrDefault(id);
        // An empty name goes back to the automatic title.
        Override(id, o => o with { Title = name.Length > 0 ? name : null });
        if (!await SetChatStateAsync(id, new { session_id = id, title = name }, "Couldn't rename that chat."))
            Restore(id, previous);
        else if (name.Length == 0)
            RefreshChats();
        else
            Announce("Renamed to " + name);
    }

    /// <summary>Flushes pending deletes before the window goes away.</summary>
    private void ShutdownShell()
    {
        try { Toast.Flush(); } catch (Exception ex) { DiagLog.Warn("shell", "flushing notices failed", ex); }
        try { QuickAsk.Shutdown(); } catch (Exception ex) { DiagLog.Warn("shell", "quick ask shutdown failed", ex); }
    }
}
