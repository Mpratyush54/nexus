using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;
using VirtualKey = Windows.System.VirtualKey;

namespace Nexus.Pages;

// The chat composer: keys, sending, drafts, pastes and focus.
//
// Enter sends and Shift+Enter starts a new line, as on Home. While the agent
// works, Enter queues behind the running reply and Ctrl/Cmd+Enter sends now;
// Esc stops. A sent message shows at once: the agent checks run after the
// bubble is up, and a failure keeps the message with Retry and Edit. Each
// chat keeps its own draft (text, attachments, pasted blocks) across chat
// switches and restarts. The send button turns into Stop in place.
public sealed partial class SessionPage
{
    private bool _imeComposing;
    private bool _stopRequested;
    private string _draftOwner = "";
    private DispatcherQueueTimer? _draftTimer;
    private readonly List<string> _pastes = new();
    private readonly StackPanel _pasteStrip = new() { Spacing = 6, Visibility = Visibility.Collapsed };
    private (string Text, int Start, int Length)? _pasteBefore;
    private SolidColorBrush? _frameBrush;
    private ComposerButton _composerButton = ComposerButton.Send;
    private readonly Dictionary<object, OutgoingMessage> _outgoing = new();

    private enum ComposerButton { Send, Queue, Stop }

    // Motion (spec section 0): bubble enter 140 ms, focus ring 150 ms, morph 120 ms.
    private const int BubbleEnterMs = 140;
    private const int FocusRingMs = 150;
    private const int MorphMs = 120;

    /// <summary>Constructor hook, after the composer is built.</summary>
    private void InitComposer()
    {
        _prompt.AddHandler(UIElement.PreviewKeyDownEvent, new KeyEventHandler(OnComposerKeyDown), true);
        _prompt.TextCompositionStarted += (_, _) => _imeComposing = true;
        _prompt.TextCompositionEnded += (_, _) => _imeComposing = false;
        _prompt.TextChanged += (_, _) => { QueueDraftSave(); UpdateComposerButton(); };
        _prompt.Paste += OnComposerPaste;
        _prompt.TextChanging += OnComposerTextChanging;
        // The macOS Edit menu's Paste raises no Paste event: it asks this
        // handler first (images, then a large text block).
        Shortcuts.SetPasteHandler(_prompt, async () => await TryPasteClipboardImageAsync() || await TryPasteLargeTextAsync());
        ScrollViewer.SetVerticalScrollBarVisibility(_prompt, ScrollBarVisibility.Auto);
        _imagePreviews.SizeChanged += (_, _) => QueueDraftSave();
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_prompt, "Message");

        // Send and Stop share one 32 px circle and cross-fade.
        Grid.SetColumn(_cancelHere, Grid.GetColumn(_sendHere));
        foreach (var button in new[] { _sendHere, _cancelHere })
        {
            button.RenderTransform = new ScaleTransform { ScaleX = 1, ScaleY = 1 };
            button.RenderTransformOrigin = new Windows.Foundation.Point(0.5, 0.5);
        }
        ToolTipService.SetToolTip(_cancelHere, ComposerKeys.StopTip);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_cancelHere, "Stop");
        _sendProgress.Visibility = Visibility.Collapsed;

        var inputAt = _composer.Children.IndexOf(_inputFrame);
        _composer.Children.Insert(Math.Max(0, inputAt), _pasteStrip);
        InitPickers();

        // The prompt grows with its text up to about a third of the page, then scrolls.
        SizeChanged += (_, e) => _prompt.MaxHeight = Math.Clamp(e.NewSize.Height * 0.34, 132, 320);
        ActualThemeChanged += (_, _) => { _frameBrush = null; AnimateComposerFocus(_prompt.FocusState != FocusState.Unfocused, animate: false); };
        _composer.Loaded += (_, _) => AnimateComposerFocus(_prompt.FocusState != FocusState.Unfocused, animate: false);

        // Keys anywhere in the chat: Esc and Ctrl/Cmd+Shift+Backspace stop,
        // Ctrl/Cmd+Down jumps to the newest message; typing goes to the composer.
        AddHandler(UIElement.KeyDownEvent, new KeyEventHandler(OnChatKeyDown), false);
        CharacterReceived += OnChatCharacterReceived;
        Unloaded += (_, _) => SaveDraftNow();
        UpdateComposerButton();
    }

    // ── Keys ─────────────────────────────────────────────────────────────

    private static bool IsDown(VirtualKey key) =>
        Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(key).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);

    private static ComposerKeys.Key KeyOf(VirtualKey key) => key switch
    {
        VirtualKey.Enter => ComposerKeys.Key.Enter,
        VirtualKey.Escape => ComposerKeys.Key.Escape,
        VirtualKey.Back => ComposerKeys.Key.Backspace,
        VirtualKey.Down => ComposerKeys.Key.Down,
        _ => ComposerKeys.Key.Other
    };

    private ComposerKeys.Run CurrentRun() =>
        LiveTurnActive && !_preparingHarness ? ComposerKeys.Run.Live
        : _sendInFlight || _preparingHarness ? ComposerKeys.Run.Busy
        : ComposerKeys.Run.Idle;

    private bool HasComposerContent() =>
        _prompt.Text.Trim().Length > 0 || _pastes.Count > 0 || _images.Count > 0 || _files.Count > 0 || _mentions.Count > 0;

    private ComposerKeys.Press PressOf(KeyRoutedEventArgs e) =>
        new(KeyOf(e.Key), IsDown(VirtualKey.Shift), Shortcuts.IsPrimaryDown(), IsDown(VirtualKey.Menu), _imeComposing);

    private async void OnComposerKeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Handled) return;
        // The / and @ list, ↑ recall and Esc over a recalled prompt come first.
        if (HandlePickerKey(e)) { e.Handled = true; return; }
        if (KeyOf(e.Key) == ComposerKeys.Key.Other) return;
        var action = ComposerKeys.Decide(PressOf(e), CurrentRun(), HasComposerContent());
        // Ctrl/Cmd+Down inside the text moves the caret, as in any editor.
        if (action is ComposerKeys.Action.None or ComposerKeys.Action.JumpToLatest) return;
        e.Handled = true;
        await RunComposerAction(action);
    }

    private void OnChatKeyDown(object sender, KeyRoutedEventArgs e)
    {
        if (e.Handled || KeyOf(e.Key) == ComposerKeys.Key.Other) return;
        if (e.OriginalSource is TextBox or PasswordBox or AutoSuggestBox) return;
        var action = ComposerKeys.Decide(PressOf(e), CurrentRun(), false);
        if (action is not (ComposerKeys.Action.Stop or ComposerKeys.Action.JumpToLatest)) return;
        e.Handled = true;
        _ = RunComposerAction(action);
    }

    private async Task RunComposerAction(ComposerKeys.Action action)
    {
        switch (action)
        {
            case ComposerKeys.Action.Send: await SendHereAsync(); break;
            case ComposerKeys.Action.Queue: await SendFollowupAsync(now: false); break;
            case ComposerKeys.Action.SendNow: await SendFollowupAsync(now: true); break;
            case ComposerKeys.Action.Stop: await CancelHereAsync(); break;
            case ComposerKeys.Action.JumpToLatest: await JumpToLatestAsync(); break;
        }
    }

    // Typing while the transcript (or a button in it) has focus types into
    // the composer, as in Claude and ChatGPT.
    private void OnChatCharacterReceived(UIElement sender, CharacterReceivedRoutedEventArgs e)
    {
        if (e.Handled || char.IsControl(e.Character) || char.IsWhiteSpace(e.Character)) return;
        if (e.OriginalSource is TextBox or PasswordBox or AutoSuggestBox or ComboBox or RichEditBox) return;
        if (!_prompt.IsEnabled || _prompt.XamlRoot is null || Shortcuts.IsPrimaryDown() || IsDown(VirtualKey.Menu)) return;
        e.Handled = true;
        var at = _prompt.Text.Length;
        _prompt.Text += e.Character.ToString();
        _prompt.Focus(FocusState.Keyboard);
        _prompt.SelectionStart = at + 1;
    }

    // ── Send and Stop: one button that morphs ───────────────────────────

    /// <summary>Send (idle), Queue (the agent works and there is a message), or Stop.</summary>
    private void UpdateComposerButton()
    {
        var run = CurrentRun();
        var mode = run == ComposerKeys.Run.Idle ? ComposerButton.Send
            : run == ComposerKeys.Run.Live && HasComposerContent() ? ComposerButton.Queue
            : ComposerButton.Stop;
        _sendGlyph.Glyph = mode == ComposerButton.Queue ? "" : "";
        _sendGlyph.Visibility = Visibility.Visible;
        _sendProgress.IsActive = false;
        _sendProgress.Visibility = Visibility.Collapsed;
        // A disabled button keeps the reason UpdateActions gave it (no folder, offline…).
        if (mode != ComposerButton.Stop && _sendHere.IsEnabled)
            ToolTipService.SetToolTip(_sendHere, ComposerKeys.SendTip(run, Shortcuts.Label));
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_sendHere, mode == ComposerButton.Queue ? "Queue message" : "Send message");
        // Filled with the accent once there is something to send.
        var ready = _sendHere.IsEnabled && HasComposerContent();
        _sendHere.Background = ready ? NexusPalette.Accent : new SolidColorBrush(Windows.UI.Color.FromArgb(255, 0x33, 0x33, 0x33));
        _cancelHere.IsEnabled = mode == ComposerButton.Stop && !_stopRequested;
        _composerButton = mode;
        var (incoming, outgoing) = mode == ComposerButton.Stop ? (_cancelHere, _sendHere) : (_sendHere, _cancelHere);
        if (ReferenceEquals(_morphTarget, incoming) && outgoing.Visibility == Visibility.Collapsed) return;
        CrossFade(incoming, outgoing);
    }

    private Button? _morphTarget;
    private Storyboard? _morph;

    // Both buttons sit in the same cell. The animations run with
    // FillBehavior.Stop over final local values, so a later morph (or the
    // reduced-motion path) never fights a held animation value.
    private void CrossFade(Button incoming, Button outgoing)
    {
        _morph?.Stop();
        _morphTarget = incoming;
        var wasShowing = outgoing.Visibility == Visibility.Visible;
        incoming.Visibility = Visibility.Visible;
        incoming.Opacity = 1;
        SetScale(incoming, 1);
        if (!Motion.Enabled || !wasShowing)
        {
            outgoing.Visibility = Visibility.Collapsed;
            return;
        }
        var board = new Storyboard();
        void Add(DependencyObject target, string property, double from, double to)
        {
            var animation = new DoubleAnimation
            {
                From = from, To = to, Duration = TimeSpan.FromMilliseconds(MorphMs), FillBehavior = FillBehavior.Stop,
                EasingFunction = new CubicEase { EasingMode = EasingMode.EaseOut }
            };
            Storyboard.SetTarget(animation, target);
            Storyboard.SetTargetProperty(animation, property);
            board.Children.Add(animation);
        }
        Add(incoming, "Opacity", 0, 1);
        Add(outgoing, "Opacity", 1, 0);
        if (incoming.RenderTransform is ScaleTransform inScale) { Add(inScale, "ScaleX", 0.8, 1); Add(inScale, "ScaleY", 0.8, 1); }
        if (outgoing.RenderTransform is ScaleTransform outScale) { Add(outScale, "ScaleX", 1, 0.8); Add(outScale, "ScaleY", 1, 0.8); }
        board.Completed += (_, _) =>
        {
            if (!ReferenceEquals(_morph, board)) return;
            _morph = null;
            if (!ReferenceEquals(_morphTarget, outgoing)) outgoing.Visibility = Visibility.Collapsed;
        };
        _morph = board;
        board.Begin();
    }

    private static void SetScale(UIElement element, double scale)
    {
        if (element.RenderTransform is ScaleTransform s) { s.ScaleX = scale; s.ScaleY = scale; }
    }

    /// <summary>Hook in CancelHereAsync: the line in the reply says "Stopping".</summary>
    private void NoteStopRequested()
    {
        _stopRequested = true;
        TurnStatusStopping();
        UpdateComposerButton();
    }

    // ── Focus ────────────────────────────────────────────────────────────

    /// <summary>Puts the caret in the composer (opening a chat, after a send).</summary>
    private void FocusComposerSoon(bool force)
    {
        DispatcherQueue?.TryEnqueue(DispatcherQueuePriority.Low, () =>
        {
            if (!IsLoaded || !_prompt.IsEnabled || _prompt.XamlRoot is null || _approvalCards.Count > 0) return;
            var focused = FocusManager.GetFocusedElement(_prompt.XamlRoot);
            if (ReferenceEquals(focused, _prompt)) return;
            // Someone already typing elsewhere (find, a picker) keeps their focus.
            if (!force && focused is TextBox or PasswordBox or AutoSuggestBox or ComboBox) return;
            _prompt.Focus(FocusState.Programmatic);
            _prompt.SelectionStart = _prompt.Text.Length;
        });
    }

    /// <summary>The composer frame's border: hairline at rest, accent at 40% when focused.</summary>
    private void AnimateComposerFocus(bool focused, bool animate = true)
    {
        if (_composer.Parent is not Border frame) return;
        var hairline = (Surface("NexusHairlineBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255)) as SolidColorBrush)?.Color ?? Windows.UI.Color.FromArgb(20, 255, 255, 255);
        var accent = (NexusPalette.Accent as SolidColorBrush)?.Color ?? Windows.UI.Color.FromArgb(255, 0x7C, 0x8C, 0xFF);
        var target = focused ? Windows.UI.Color.FromArgb(102, accent.R, accent.G, accent.B) : hairline;
        if (_frameBrush is null || !ReferenceEquals(frame.BorderBrush, _frameBrush))
        {
            // Our own brush: animating the shared theme brush would recolour every hairline.
            _frameBrush = new SolidColorBrush(frame.BorderBrush is SolidColorBrush current ? current.Color : hairline);
            frame.BorderBrush = _frameBrush;
        }
        if (!animate || !Motion.Enabled)
        {
            _frameBrush.Color = target;
            return;
        }
        var color = new ColorAnimation
        {
            From = _frameBrush.Color, To = target, Duration = TimeSpan.FromMilliseconds(FocusRingMs),
            EnableDependentAnimation = true, EasingFunction = new CubicEase { EasingMode = EasingMode.EaseOut }
        };
        Storyboard.SetTarget(color, _frameBrush);
        Storyboard.SetTargetProperty(color, "Color");
        var board = new Storyboard();
        board.Children.Add(color);
        board.Begin();
    }

    // ── Drafts ───────────────────────────────────────────────────────────

    private ComposerDraft CurrentDraft() => new()
    {
        Text = _prompt.Text,
        Caret = _prompt.SelectionStart,
        Images = _images.ToList(),
        Files = _files.ToList(),
        Pastes = _pastes.ToList(),
        Mentions = _mentions.ToList()
    };

    private void QueueDraftSave()
    {
        if (_draftOwner.Length == 0 || DispatcherQueue is null) return;
        if (_draftTimer is null)
        {
            _draftTimer = DispatcherQueue.CreateTimer();
            _draftTimer.Interval = TimeSpan.FromMilliseconds(300);
            _draftTimer.IsRepeating = false;
            _draftTimer.Tick += (_, _) => SaveDraftNow();
        }
        _draftTimer.Stop();
        _draftTimer.Start();
    }

    private void SaveDraftNow()
    {
        _draftTimer?.Stop();
        if (_draftOwner.Length == 0 || _draftOwner != _id) return;
        var store = ComposerDraftStore.Default;
        if (store.Set(_draftOwner, CurrentDraft())) store.Save();
    }

    /// <summary>OnEnter hook, before the page switches chats: keeps the draft of the chat being left.</summary>
    private void StashDraft()
    {
        SaveDraftNow();
        _draftOwner = "";
    }

    /// <summary>OnEnter hook, after the composer is reset: brings back this chat's draft.</summary>
    private void RestoreDraft()
    {
        ClearPastes();
        ResetPickers();
        _draftOwner = _id;
        if (ComposerDraftStore.Default.Get(_id) is not { } draft) return;
        SetComposerText(draft.Text, draft.Caret);
        foreach (var paste in draft.Pastes) AddPasteChip(paste, animate: false);
        foreach (var mention in draft.Mentions) AddMention(mention, animate: false);
        if (draft.Images.Count + draft.Files.Count > 0) _ = RestoreAttachmentsAsync(_id, draft.Images.Concat(draft.Files).ToList());
    }

    private async Task RestoreAttachmentsAsync(string sessionId, IReadOnlyList<string> paths)
    {
        foreach (var path in paths)
        {
            if (_id != sessionId) return;
            if (!System.IO.File.Exists(path)) continue;
            try { await AddAttachmentAsync(await Windows.Storage.StorageFile.GetFileFromPathAsync(path)); }
            catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Warn("composer", "could not restore a draft attachment", ex); }
        }
    }

    /// <summary>Empties the composer after a send; the chat's draft goes with it.</summary>
    private void ClearComposer()
    {
        _prompt.Text = "";
        ClearImageAttachments();
        ClearPastes();
        ClearMentions();
        if (_id.Length > 0 && ComposerDraftStore.Default.Remove(_id)) ComposerDraftStore.Default.Save();
        UpdateComposerButton();
    }

    /// <summary>Puts a message back in the composer (Edit, a failed follow-up). A draft already there stays after it.</summary>
    private void LoadIntoComposer(string text, IEnumerable<string>? attachments = null, IEnumerable<string>? pastes = null, IEnumerable<ComposerMention>? mentions = null)
    {
        var current = _prompt.Text.Trim();
        SetComposerText(current.Length == 0 ? text : text.TrimEnd() + "\n\n" + current, 0);
        foreach (var paste in pastes ?? Enumerable.Empty<string>()) AddPasteChip(paste, animate: true);
        foreach (var mention in mentions ?? Enumerable.Empty<ComposerMention>()) AddMention(mention, animate: true);
        if (attachments?.ToList() is { Count: > 0 } paths) _ = RestoreAttachmentsAsync(_id, paths);
        _prompt.Focus(FocusState.Programmatic);
        _prompt.SelectionStart = Math.Min(text.TrimEnd().Length, _prompt.Text.Length);
        if (Motion.Enabled && _composer.Parent is UIElement frame) Motion.PopIn(frame, from: 0.985f, durationMs: 220);
        QueueDraftSave();
        UpdateComposerButton();
    }

    // ── Large pastes become chips ────────────────────────────────────────

    private void OnComposerPaste(object sender, TextControlPasteEventArgs e)
    {
        // An image paste was taken by Prompt_Paste (registered first).
        _pasteBefore = e.Handled ? null : (_prompt.Text, _prompt.SelectionStart, _prompt.SelectionLength);
    }

    // Runs before the pasted text is drawn: a big block is taken out of the
    // text and kept as a chip.
    private void OnComposerTextChanging(TextBox sender, TextBoxTextChangingEventArgs e)
    {
        if (_pasteBefore is not { } before) return;
        _pasteBefore = null;
        var now = sender.Text;
        var prefix = before.Text[..Math.Min(before.Start, before.Text.Length)];
        var suffixStart = Math.Min(before.Text.Length, before.Start + before.Length);
        var suffix = before.Text[suffixStart..];
        var inserted = now.Length - prefix.Length - suffix.Length;
        if (inserted <= 0 || !now.StartsWith(prefix, StringComparison.Ordinal) || !now.EndsWith(suffix, StringComparison.Ordinal)) return;
        var pasted = PastedText.Normalize(now.Substring(prefix.Length, inserted));
        if (!PastedText.IsLarge(pasted) || _pastes.Count >= 5) return;
        sender.Text = prefix + suffix;
        sender.SelectionStart = prefix.Length;
        AddPasteChip(pasted, animate: true);
    }

    private async Task<bool> TryPasteLargeTextAsync()
    {
        try
        {
            var content = Clipboard.GetContent();
            if (!content.Contains(StandardDataFormats.Text)) return false;
            var text = PastedText.Normalize(await content.GetTextAsync());
            if (!PastedText.IsLarge(text) || _pastes.Count >= 5) return false;
            AddPasteChip(text, animate: true);
            return true;
        }
        catch (Exception) { return false; }
    }

    private void ClearPastes()
    {
        _pastes.Clear();
        _pasteStrip.Children.Clear();
        _pasteStrip.Visibility = Visibility.Collapsed;
    }

    private void AddPasteChip(string text, bool animate)
    {
        _pastes.Add(text);
        var mono = new FontFamily("Cascadia Mono, Consolas, Menlo, monospace");
        var icon = new FontIcon { Glyph = "", FontSize = 14, Opacity = 0.8, VerticalAlignment = VerticalAlignment.Center };
        var label = new TextBlock { Text = PastedText.Label(text), FontSize = 12.5, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold };
        var preview = new TextBlock { Text = PastedText.Preview(text), FontSize = 11.5, FontFamily = mono, Foreground = NexusPalette.Muted, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 460, HorizontalAlignment = HorizontalAlignment.Left };
        var words = new StackPanel { Spacing = 1, VerticalAlignment = VerticalAlignment.Center, Children = { label, preview } };
        var chevronIcon = new FontIcon { Glyph = "", FontSize = 10, RenderTransformOrigin = new Windows.Foundation.Point(0.5, 0.5), RenderTransform = new RotateTransform() };
        var chevron = GhostIconButton(chevronIcon, "Show pasted text");
        var remove = GhostIconButton(new FontIcon { Glyph = "", FontSize = 10 }, "Remove pasted text");
        var head = new Grid { ColumnSpacing = 10 };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(words, 1); Grid.SetColumn(chevron, 2); Grid.SetColumn(remove, 3);
        head.Children.Add(icon); head.Children.Add(words); head.Children.Add(chevron); head.Children.Add(remove);
        var full = new ScrollViewer
        {
            MaxHeight = 200, Visibility = Visibility.Collapsed, Margin = new Thickness(0, 8, 0, 0),
            HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            Content = new TextBlock { Text = text.Length > 20000 ? text[..20000] + "\n…" : text, FontFamily = mono, FontSize = 11.5, Foreground = NexusPalette.Muted, IsTextSelectionEnabled = true }
        };
        var body = new StackPanel { Children = { head, full } };
        var chip = new Border
        {
            Child = body, Padding = new Thickness(10, 6, 6, 6), CornerRadius = new CornerRadius(10),
            Background = SessionChatRender.SubtleFill, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1),
            Tag = text
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chip, PastedText.Label(text));
        ToolTipService.SetToolTip(chip, "Sent after your message. Click to show it.");
        void Toggle()
        {
            var open = full.Visibility != Visibility.Visible;
            full.Visibility = open ? Visibility.Visible : Visibility.Collapsed;
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chevron, open ? "Hide pasted text" : "Show pasted text");
            var rotate = (RotateTransform)chevronIcon.RenderTransform;
            if (!Motion.Enabled) rotate.Angle = open ? 180 : 0;
            else
            {
                var spin = new DoubleAnimation { To = open ? 180 : 0, Duration = TimeSpan.FromMilliseconds(180), EasingFunction = new CubicEase { EasingMode = EasingMode.EaseOut } };
                Storyboard.SetTarget(spin, rotate);
                Storyboard.SetTargetProperty(spin, "Angle");
                var board = new Storyboard();
                board.Children.Add(spin);
                board.Begin();
            }
            if (open) Motion.FadeSlideIn(full, dy: -4, durationMs: 180);
        }
        chevron.Click += (_, _) => Toggle();
        head.Tapped += (_, e) => { if (e.OriginalSource is not FrameworkElement { Parent: Button }) Toggle(); };
        remove.Click += (_, _) =>
        {
            var at = _pasteStrip.Children.IndexOf(chip);
            if (at >= 0 && at < _pastes.Count) _pastes.RemoveAt(at);
            _pasteStrip.Children.Remove(chip);
            _pasteStrip.Visibility = _pastes.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
            QueueDraftSave();
            UpdateComposerButton();
            _prompt.Focus(FocusState.Programmatic);
        };
        _pasteStrip.Children.Add(chip);
        _pasteStrip.Visibility = Visibility.Visible;
        if (animate) Motion.FadeSlideIn(chip, dy: 6, durationMs: 160);
        QueueDraftSave();
        UpdateComposerButton();
    }

    private static Button GhostIconButton(UIElement icon, string name)
    {
        var button = new Button
        {
            Content = icon, Width = 26, Height = 26, MinWidth = 26, MinHeight = 26, Padding = new Thickness(0),
            CornerRadius = new CornerRadius(13), Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            BorderThickness = new Thickness(0), VerticalAlignment = VerticalAlignment.Center
        };
        ToolTipService.SetToolTip(button, name);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, name);
        return button;
    }

    // ── Sending: the bubble first, the checks after ──────────────────────

    /// <summary>Send (Enter or the button) from the composer.</summary>
    private async Task SendFromComposerAsync()
    {
        // While the agent works, a message waits for its turn.
        if (LiveTurnActive && !_preparingHarness)
        {
            await SendFollowupAsync(now: false);
            return;
        }
        if (await TryRunTypedCommandAsync()) return;
        if (_sendInFlight || _preparingHarness) return;
        var typed = _prompt.Text.Trim();
        if (typed.Length == 0 && _pastes.Count == 0 && _mentions.Count == 0)
        {
            if (_images.Count + _files.Count > 0) SetComposerStatus("Add a message to send with your attachments.");
            return;
        }
        if (_viewingHistoricalPage)
        {
            await ReturnToLatestAsync();
            if (_viewingHistoricalPage) { SetComposerStatus("Could not return to the latest messages. Scroll to the end, then send again."); return; }
        }
        // What the person can fix before anything is sent stays in the composer.
        var agent = SelectedHarness();
        if (!SupportsInAppReply(agent)) { SetComposerStatus("Choose an agent that can chat in Nexus: Claude Code, Codex, Cursor, OpenCode, Gemini or Kimi."); return; }
        if (_images.Count > 0 && ComposerAttachments.ImageProblem(agent, _images[0], 1) is { } imageProblem) { SetComposerStatus(imageProblem); return; }
        if (!System.IO.Directory.Exists(_workspace.Text.Trim())) { SetComposerStatus("Select an existing local project folder before sending."); return; }
        var message = new OutgoingMessage(_id, typed, _images, _files, _pastes, _mentions);
        RememberSent(typed);
        ClearComposer();
        SetComposerStatus("");
        await SendMessageAsync(message);
    }

    /// <summary>
    /// Sends one message: its bubble and the reply's status line show at
    /// once, then the agent is checked and the message goes to it. Typing
    /// meanwhile changes nothing: the message was captured when sent.
    /// </summary>
    private async Task SendMessageAsync(OutgoingMessage message)
    {
        var agent = SelectedHarness();
        var model = SelectedModel();
        SaveModelChoice(agent, model);
        var images = message.Images.ToArray();
        var prompt = ComposerAttachments.WithFiles(message.PromptFor(agent), message.Files.ToList());
        var useLive = SupportsLive(agent) && (images.Length == 0 || ComposerAttachments.SendsImagesLive(agent));
        var workspace = _workspace.Text.Trim();
        ProjectFolders.Remember(_projectID, workspace);
        var sessionID = _id;
        var requestID = Guid.NewGuid().ToString("N");
        _stopRequested = false;
        _sendInFlight = true;
        SetSending(true);
        var view = BeginTurnView(agent, prompt, images, requestID);
        _outgoing[view] = message;
        Motion.FadeSlideIn(view.UserBubble, dy: 6, durationMs: BubbleEnterMs);
        Motion.FadeSlideIn(view.StreamBubble, dy: 6, durationMs: BubbleEnterMs, delayMs: 40);
        FocusComposerSoon(force: false);

        string? notSent = null;
        var trustWorkspace = false;
        _preparingHarness = true;
        UpdateComposerButton();
        try
        {
            var before = _composerStatus.Text;
            if (await HarnessStatusAsync(agent) is null)
            {
                // The check says why in the composer's status line; the bubble says it now.
                notSent = _composerStatus.Text != before && _composerStatus.Text.Length > 0 ? _composerStatus.Text : "Nexus couldn't reach " + AgentAvailability.DisplayName(agent) + ".";
                SetComposerStatus(before);
            }
            else if (agent == "cursor" && !useLive)
            {
                // One-shot Cursor runs need --trust up front; live chats ask per action instead.
                trustWorkspace = await ConfirmCursorWorkspaceAsync(workspace);
                if (!trustWorkspace) notSent = "Cursor needs this folder trusted before it can work in it.";
            }
        }
        finally { _preparingHarness = false; }
        if (notSent is null && _stopRequested) notSent = "You stopped it before it was sent.";

        if (_id != sessionID)
        {
            // The chat was closed while the checks ran. A live agent still
            // gets the message (the core runs it in the background); otherwise
            // it goes back into that chat's draft.
            message.Detach();
            _outgoing.Remove(view);
            EndTurnStreaming(view);
            if (notSent is null && useLive)
                _ = Core.CallAsync("live.send", new
                {
                    session_id = sessionID, project_id = _projectID, agent, workspace_root = workspace, model,
                    context = _continuationContext, text = prompt, images
                });
            else
                KeepAsDraft(sessionID, message);
            _ = AttachLiveAsync();
            return;
        }
        if (notSent is not null)
        {
            // Never reached the agent: "Not sent" with Retry and Edit, not "Stopped · Continue".
            _stopRequested = false;
            EndTurnStreaming(view);
            FinishTurnView(view, new NxResponse(false, notSent, null), sessionID, message.Text);
            return;
        }
        message.ChecksPassed();
        // Captured IDs may originate on another device or in an IDE store that
        // the CLI cannot resume. Seed from readable chat, never from shell ops.
        var resumeNative = (images.Length == 0 || useLive) && _localNativeID.Length > 0 && string.Equals(_localNativeAgent, agent, StringComparison.OrdinalIgnoreCase);
        var context = resumeNative ? "" : _continuationContext;
        var nativeID = resumeNative ? _localNativeID : "";
        NxResponse response;
        try
        {
            if (!useLive) message.Accepted();
            response = useLive
                ? await SendLiveAsync(view, sessionID, agent, model, prompt, _continuationContext, nativeID, workspace, images)
                : await Call(NxMethods.ContinueHere, new { session_id = sessionID, project_id = _projectID, native_id = nativeID, agent, model, resume = resumeNative ? "native" : "seeded", prompt, context, images, workspace_root = workspace, trust_workspace = trustWorkspace, request_id = requestID });
        }
        finally { EndTurnStreaming(view); }
        FinishTurnView(view, response, sessionID, message.Text.Length > 0 ? message.Text : prompt);
    }

    /// <summary>Hook in SendLiveAsync: the core took the message (it may still wait for a slot).</summary>
    private void OutgoingAccepted(object view)
    {
        if (_outgoing.TryGetValue(view, out var message)) message.Accepted();
    }

    private static void KeepAsDraft(string sessionId, OutgoingMessage message)
    {
        var store = ComposerDraftStore.Default;
        var existing = store.Get(sessionId);
        var text = existing is { Text.Length: > 0 } ? message.Text + "\n\n" + existing.Text : message.Text;
        store.Set(sessionId, new ComposerDraft
        {
            Text = text, Caret = message.Text.Length,
            Images = message.Images.Concat(existing?.Images ?? new()).Distinct().ToList(),
            Files = message.Files.Concat(existing?.Files ?? new()).Distinct().ToList(),
            Pastes = message.Pastes.Concat(existing?.Pastes ?? new()).ToList(),
            Mentions = message.Mentions.Concat(existing?.Mentions ?? new()).Distinct().ToList()
        });
        store.Save();
    }

    // ── A send that failed keeps its message ─────────────────────────────

    /// <summary>
    /// Hook in FinishTurnView for a failed turn (or one with no reply). The
    /// message stays; a line under it says why, with Retry and Edit. A reply
    /// that got part of the way stays as it was. Returns true when handled.
    /// </summary>
    private bool ShowTurnFailed(object turn, NxResponse response, string agent, UIElement userBubble, Border streamBubble, StackPanel streamStack, TextBlock streamText, bool hadOutput, string userKey)
    {
        if (!_outgoing.Remove(turn, out var message))
        {
            // A turn reopened from the core: its text is what it sent.
            message = new OutgoingMessage(_id, ConversationText(userKey));
            message.ChecksPassed();
            message.Accepted();
        }
        var stopped = _stopRequested;
        _stopRequested = false;
        var reason = stopped ? "You stopped this reply." : NxErrors.ForAgent(response.Error, AgentAvailability.DisplayName(agent));
        message.Fail(reason, hadOutput);
        if (hadOutput)
        {
            // Keep what the agent wrote and did; say it stopped short.
            streamBubble.Background = SessionChatRender.SubtleFill;
            streamBubble.BorderBrush = stopped ? NexusPalette.Hairline : NexusPalette.Amber;
            streamBubble.BorderThickness = new Thickness(1);
            streamBubble.CornerRadius = new CornerRadius(8);
            streamBubble.Padding = new Thickness(12);
            if (streamText.Text.Length == 0) streamText.Visibility = Visibility.Collapsed;
        }
        else
        {
            _chat.Children.Remove(streamBubble);
        }
        var anchor = hadOutput ? (UIElement)streamBubble : userBubble;
        var row = stopped ? StoppedRow(message, anchor) : FailureRow(message, userBubble, hadOutput ? streamBubble : null, userKey);
        var at = _chat.Children.IndexOf(anchor);
        if (at >= 0) _chat.Children.Insert(at + 1, row); else _chat.Children.Add(row);
        Motion.FadeSlideIn(row, dy: 4, durationMs: BubbleEnterMs);
        RefreshConversationNavigation();
        KeepLatestInView();
        if (!stopped && (response.RawError ?? "").Contains("model", StringComparison.OrdinalIgnoreCase)) _model.Focus(FocusState.Programmatic);
        return true;
    }

    private string ConversationText(string key) =>
        _conversation.Entries.FirstOrDefault(e => e.Key == key)?.Text ?? "";

    /// <summary>Hook in FinishTurnView after the reply is saved.</summary>
    private void OnTurnSaved(object turn, string answerKey, System.Text.Json.JsonElement result)
    {
        _outgoing.Remove(turn, out var message);
        message?.Completed();
        var stopped = _stopRequested;
        _stopRequested = false;
        NoteNewBelow(turn);
        var anchor = _messageAnchors.TryGetValue(answerKey, out var a) ? a : null;
        var at = anchor is null ? -1 : _chat.Children.IndexOf(anchor);
        // Success is silent: only a reply that could not sync says so, on the reply.
        var savedLocally = result.TryGetProperty("saved_locally", out var saved) && saved.ValueKind == System.Text.Json.JsonValueKind.True;
        var synced = result.TryGetProperty("cloud_saved", out var cloud) && cloud.ValueKind == System.Text.Json.JsonValueKind.True;
        if (!savedLocally)
            SetComposerStatus("This reply could not be saved: " + NxJson.Text(result, "save_error") + ". Copy anything you need from it.");
        else if (!synced && at >= 0)
            _chat.Children.Insert(at + 1, QuietNote("", "Saved on this device · not synced yet", "Nexus couldn't reach the cloud. The reply is saved here and syncs when Nexus reconnects."));
        if (stopped && message is not null && at >= 0) _chat.Children.Insert(at + 1, StoppedRow(message, anchor!));
    }

    private static UIElement QuietNote(string glyph, string text, string tip)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, Opacity = 0.7, Margin = new Thickness(2, -4, 0, 4) };
        row.Children.Add(new FontIcon { Glyph = glyph, FontSize = 11, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(new TextBlock { Text = text, FontSize = 11.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center });
        ToolTipService.SetToolTip(row, tip);
        return row;
    }

    private static HyperlinkButton InlineAction(string text, string name, Brush? foreground = null)
    {
        var link = new HyperlinkButton { Content = text, FontSize = 12, Padding = new Thickness(6, 1, 6, 1), MinHeight = 0, VerticalAlignment = VerticalAlignment.Center };
        if (foreground is not null) link.Foreground = foreground;
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(link, name);
        return link;
    }

    /// <summary>"Not sent · reason · Retry · Edit" under a message, with Details for a long reason.</summary>
    private FrameworkElement FailureRow(OutgoingMessage message, UIElement userBubble, Border? partial, string userKey)
    {
        var (shortReason, hasDetails) = message.ShortReason();
        var red = NexusPalette.Danger;
        var line = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, HorizontalAlignment = partial is null ? HorizontalAlignment.Right : HorizontalAlignment.Left };
        line.Children.Add(new FontIcon { Glyph = "", FontSize = 12, Foreground = red, VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(0, 0, 2, 0) });
        line.Children.Add(new TextBlock
        {
            Text = message.FailureLabel + (shortReason.Length > 0 ? " · " + shortReason : ""), FontSize = 12, Foreground = red,
            VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 560
        });
        ToolTipService.SetToolTip(line, message.Reason);
        var retry = InlineAction("Retry", "Retry sending this message");
        var edit = InlineAction("Edit", "Edit this message in the composer");
        ToolTipService.SetToolTip(retry, partial is null ? "Send the same message again" : "Send the same message again. The agent may already have changed files; check its steps above first.");
        ToolTipService.SetToolTip(edit, "Put it back in the message box");
        line.Children.Add(retry);
        line.Children.Add(edit);
        var details = new TextBlock
        {
            Text = message.Reason, FontSize = 12, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true,
            Foreground = NexusPalette.Muted, Visibility = Visibility.Collapsed, MaxWidth = 560, HorizontalAlignment = line.HorizontalAlignment
        };
        if (hasDetails)
        {
            var more = InlineAction("Details", "Show the full error");
            more.Click += (_, _) =>
            {
                var open = details.Visibility != Visibility.Visible;
                details.Visibility = open ? Visibility.Visible : Visibility.Collapsed;
                more.Content = open ? "Hide details" : "Details";
                if (open) Motion.FadeSlideIn(details, dy: -3, durationMs: 160);
            };
            line.Children.Add(more);
        }
        var row = new StackPanel { Spacing = 2, Margin = new Thickness(0, -2, 0, 6), Children = { line, details } };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(row, message.FailureLabel + ": " + message.Reason);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetLiveSetting(row, Microsoft.UI.Xaml.Automation.Peers.AutomationLiveSetting.Assertive);

        void Withdraw(bool keepPartial)
        {
            _chat.Children.Remove(row);
            if (keepPartial && partial is not null) return;
            _chat.Children.Remove(userBubble);
            _conversation.Remove(userKey);
            _messageAnchors.Remove(userKey);
            RefreshConversationNavigation();
        }
        retry.Click += async (_, _) =>
        {
            if ((_sendInFlight && !LiveTurnActive) || _preparingHarness) return;
            if (!message.Retry()) return;
            // A reply that got part of the way stays; the message goes again as a new turn.
            Withdraw(keepPartial: true);
            if (LiveTurnActive) { await QueueMessageAsync(message, now: false); return; }
            await SendMessageAsync(message);
        };
        edit.Click += (_, _) =>
        {
            if (!message.Withdraw()) return;
            Withdraw(keepPartial: true);
            LoadIntoComposer(message.Text, message.Images.Concat(message.Files), message.Pastes, message.Mentions);
        };
        return row;
    }

    /// <summary>"Stopped · Continue" after a reply the person stopped.</summary>
    private FrameworkElement StoppedRow(OutgoingMessage message, UIElement anchor)
    {
        var line = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, Margin = new Thickness(0, -2, 0, 6) };
        line.Children.Add(new TextBlock { Text = "Stopped ·", FontSize = 12, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(2, 0, 0, 0) });
        var resume = InlineAction("Continue", "Continue the stopped reply");
        ToolTipService.SetToolTip(resume, "Ask the agent to carry on");
        line.Children.Add(resume);
        resume.Click += async (_, _) =>
        {
            if ((_sendInFlight && !LiveTurnActive) || _preparingHarness) return;
            _chat.Children.Remove(line);
            var next = new OutgoingMessage(_id, "continue");
            if (LiveTurnActive) await QueueMessageAsync(next, now: false);
            else await SendMessageAsync(next);
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(line, "Stopped");
        return line;
    }

    /// <summary>Queues a message behind the running reply (Retry or Continue while the agent works).</summary>
    private async Task QueueMessageAsync(OutgoingMessage message, bool now)
    {
        var agent = _liveAgent.Length > 0 ? _liveAgent : SelectedHarness();
        var response = await Call("live.send", new
        {
            session_id = _id, project_id = _projectID, agent,
            workspace_root = _workspace.Text.Trim(), model = SelectedModel(), mode = PermissionMode(),
            context = _continuationContext, text = ComposerAttachments.WithFiles(message.PromptFor(agent), message.Files.ToList()),
            images = message.Images.ToArray(), when = now ? "now" : "queue"
        });
        if (!response.Ok)
        {
            LoadIntoComposer(message.Text, message.Images.Concat(message.Files), message.Pastes, message.Mentions);
            SetComposerStatus(response.Error ?? "Could not queue that message. It's back in the message box.");
        }
    }

    /// <summary>
    /// The composer's message waits behind the running reply ("queue") or
    /// stops it and goes next ("now"). The composer empties at once; a
    /// refusal puts the message back.
    /// </summary>
    private async Task SendFollowupFromComposerAsync(bool now)
    {
        if (await TryRunTypedCommandAsync()) return;
        var typed = _prompt.Text.Trim();
        if (typed.Length == 0 && _pastes.Count == 0 && _mentions.Count == 0) return;
        var message = new OutgoingMessage(_id, typed, _images, _files, _pastes, _mentions);
        RememberSent(typed);
        ClearComposer();
        if (now) TurnStatusStopping("Stopping to send yours next");
        await QueueMessageAsync(message, now);
    }

    // ── Hooks for the message action row (feat/chat-rendering) ──────────

    partial void ComposerEditHook(string text, ref bool handled)
    {
        handled = true;
        LoadIntoComposer(text);
    }

    partial void ComposerRetryHook(string text, ref bool handled)
    {
        handled = true;
        if ((_sendInFlight && !LiveTurnActive) || _preparingHarness) return;
        var message = new OutgoingMessage(_id, text);
        _ = LiveTurnActive ? QueueMessageAsync(message, now: false) : SendMessageAsync(message);
    }
}
