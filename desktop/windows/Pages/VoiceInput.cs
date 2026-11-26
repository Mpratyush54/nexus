using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Automation.Peers;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Core;
using Windows.System;

namespace Nexus.Pages;

/// <summary>
/// Voice input (dictation) for a chat box: a microphone button next to Send,
/// as in ChatGPT, Claude and Cursor. Click (or Ctrl+Shift+Space, ⌘⇧Space on
/// macOS) to start; level bars replace the mic and the words stream into the
/// box at the caret; click again or press Esc to stop. Nothing is sent: the
/// text stays in the box to edit, and Ctrl+Z undoes it. Stops by itself
/// after 20 seconds without speech.
///
/// One call wires it to a composer, <see cref="Attach"/>: the button goes in
/// the column before Send in Send's Grid (later columns shift right), and
/// notices ("Allow microphone in Windows Settings", "Didn't catch that") go
/// in a line under that Grid. The recognizer is the OS's
/// (<see cref="DictationEngines"/>); Nexus never records or uploads audio.
/// </summary>
internal sealed class VoiceInput
{
    public static readonly TimeSpan SilenceLimit = TimeSpan.FromSeconds(20);

    /// <summary>Raised when Settings → General → Voice input changes.</summary>
    public static event Action? SettingsChanged;

    public static void NotifySettingsChanged() => SettingsChanged?.Invoke();

    public static bool Enabled => AppPrefs.Data.VoiceInput;

    /// <summary>"Ctrl+Shift+Space" / "⌘⇧Space".</summary>
    public static string ShortcutLabel => Shortcuts.UseCommand ? "⌘⇧Space" : "Ctrl+Shift+Space";

    private enum State { Idle, Starting, Listening, Stopping }

    private readonly TextBox _box;
    private readonly Button _send;
    private readonly Button _mic = new();
    private readonly Grid _host = new() { VerticalAlignment = VerticalAlignment.Center };
    private readonly FontIcon _glyph = new() { Glyph = "", FontSize = 15, Opacity = 0.72 };
    private readonly StackPanel _bars = new() { Orientation = Orientation.Horizontal, Spacing = 2, VerticalAlignment = VerticalAlignment.Center, HorizontalAlignment = HorizontalAlignment.Center, Visibility = Visibility.Collapsed };
    private readonly TextBlock _live = new() { Opacity = 0, Width = 1, Height = 1, IsHitTestVisible = false, FontSize = 1 };
    private readonly Border _notice = new() { Visibility = Visibility.Collapsed, Padding = new Thickness(10, 6, 6, 6), CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1) };
    private readonly TextBlock _noticeText = new() { TextWrapping = TextWrapping.Wrap, FontSize = 12, VerticalAlignment = VerticalAlignment.Center };
    private readonly Button _noticeAction = new() { FontSize = 12, Padding = new Thickness(10, 3, 10, 4), MinHeight = 26, VerticalAlignment = VerticalAlignment.Center };
    private readonly Microsoft.UI.Xaml.Input.KeyboardAccelerator _toggleKey = new() { Key = VirtualKey.Space, Modifiers = VirtualKeyModifiers.Control | VirtualKeyModifiers.Shift };
    private readonly Microsoft.UI.Xaml.Input.KeyboardAccelerator _escapeKey = new() { Key = VirtualKey.Escape, IsEnabled = false };
    private readonly Rectangle[] _barShapes = new Rectangle[4];
    private static readonly double[] BarShape = { 0.55, 0.9, 1.0, 0.7 };

    private IDictationEngine? _engine;
    private State _state;
    private volatile int _session;
    private Action? _noticeHandler;
    private DispatcherQueueTimerHolder? _timer;

    // The dictation's place in the box: it started at _start and our words
    // currently occupy _length characters there. _committed holds the
    // finished phrases, _interim the words still being recognized.
    private int _start;
    private int _length;
    private string _committed = "";
    private string _interim = "";
    private string _expected = "";
    private bool _heardWords;
    private DateTime _lastSpeech;
    private double _level;
    private double _phase;

    private VoiceInput(TextBox box, Button send)
    {
        _box = box;
        _send = send;
        Build();
    }

    /// <summary>
    /// Adds voice input to the composer whose text box is <paramref name="box"/>
    /// and whose Send button is <paramref name="send"/> (a child of a Grid).
    /// Returns null when this computer has no recognizer.
    /// </summary>
    public static VoiceInput? Attach(TextBox box, Button send)
    {
        try
        {
            var voice = new VoiceInput(box, send);
            // Placed once Send has its Grid: at once when it already has one,
            // otherwise when Send loads.
            if (!voice.Place()) send.Loaded += (_, _) => voice.Place();
            return voice;
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("voice", "could not add the microphone button", ex);
            return null;
        }
    }

    private static DependencyObject? ParentOf(FrameworkElement element) =>
        element.Parent ?? VisualTreeHelper.GetParent(element);

    private bool Place()
    {
        try
        {
            if (_host.Parent is null)
            {
                if (ParentOf(_send) is not Grid footer) return false;
                var column = Grid.GetColumn(_send);
                foreach (var child in footer.Children.OfType<FrameworkElement>())
                    if (Grid.GetColumn(child) >= column) Grid.SetColumn(child, Grid.GetColumn(child) + 1);
                footer.ColumnDefinitions.Insert(Math.Min(column, footer.ColumnDefinitions.Count), new ColumnDefinition { Width = GridLength.Auto });
                Grid.SetColumn(_host, column);
                footer.Children.Add(_host);
                Nexus.Core.Diagnostics.DiagLog.Info("voice", "microphone button added to the composer");
                footer.Loaded += (_, _) => Place();
            }
            if (_notice.Parent is null && _host.Parent is Grid grid && ParentOf(grid) is Panel panel)
                panel.Children.Insert(panel.Children.IndexOf(grid) + 1, _notice);
            return true;
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("voice", "could not place the microphone button", ex);
            return true;
        }
    }

    private void Build()
    {
        _mic.Padding = new Thickness(0);
        _mic.Width = 32;
        _mic.Height = 32;
        _mic.MinWidth = 32;
        _mic.MinHeight = 32;
        _mic.CornerRadius = new CornerRadius(16);
        _mic.BorderThickness = new Thickness(0);
        _mic.VerticalAlignment = VerticalAlignment.Center;
        _mic.HorizontalContentAlignment = HorizontalAlignment.Center;
        _mic.VerticalContentAlignment = VerticalAlignment.Center;
        _mic.Resources["ButtonBackgroundDisabled"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _mic.Resources["ButtonBorderBrushDisabled"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        for (var i = 0; i < _barShapes.Length; i++)
        {
            _barShapes[i] = new Rectangle { Width = 3, Height = 6, RadiusX = 1.5, RadiusY = 1.5, VerticalAlignment = VerticalAlignment.Center };
            _bars.Children.Add(_barShapes[i]);
        }
        var content = new Grid { Width = 20, Height = 20 };
        content.Children.Add(_glyph);
        content.Children.Add(_bars);
        _mic.Content = content;
        _mic.Click += (_, _) => _ = ToggleAsync();

        _mic.KeyboardAcceleratorPlacementMode = Microsoft.UI.Xaml.Input.KeyboardAcceleratorPlacementMode.Hidden;
        _toggleKey.Invoked += (sender, e) =>
        {
            if (_host.XamlRoot is null || _host.Visibility != Visibility.Visible || !_box.IsEnabled) return;
            e.Handled = true;
            _ = ToggleAsync();
        };
        _escapeKey.Invoked += (sender, e) =>
        {
            if (_state is not (State.Listening or State.Starting)) return;
            e.Handled = true;
            _ = StopAsync();
        };
        _mic.KeyboardAccelerators.Add(_toggleKey);
        _mic.KeyboardAccelerators.Add(_escapeKey);
        Shortcuts.UseCommandKey(_mic);

        AutomationProperties.SetLiveSetting(_live, AutomationLiveSetting.Polite);
        AutomationProperties.SetAccessibilityView(_live, AccessibilityView.Raw);
        _host.Children.Add(_live);
        _host.Children.Add(_mic);

        var row = new Grid { ColumnSpacing = 8 };
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var icon = new FontIcon { Glyph = "", FontSize = 13, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center };
        row.Children.Add(icon);
        Grid.SetColumn(_noticeText, 1);
        row.Children.Add(_noticeText);
        Grid.SetColumn(_noticeAction, 2);
        row.Children.Add(_noticeAction);
        var close = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 10 }, Padding = new Thickness(6), MinWidth = 26, MinHeight = 26,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0), VerticalAlignment = VerticalAlignment.Center,
        };
        AutomationProperties.SetName(close, "Dismiss");
        close.Click += (_, _) => HideNotice();
        Grid.SetColumn(close, 3);
        row.Children.Add(close);
        _noticeAction.Click += (_, _) => _noticeHandler?.Invoke();
        _notice.Child = row;
        _notice.Background = ThemeService.Brush("NexusSubtleFillBrush", Windows.UI.Color.FromArgb(18, 255, 255, 255));
        _notice.BorderBrush = ThemeService.Brush("NexusHairlineBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255));
        AutomationProperties.SetLiveSetting(_notice, AutomationLiveSetting.Polite);
        AutomationProperties.SetName(_notice, "Voice input");

        _box.Unloaded += (_, _) => { if (_state != State.Idle) _ = StopAsync(); };
        // The mic works only when the box takes text.
        _mic.IsEnabled = _box.IsEnabled;
        _box.RegisterPropertyChangedCallback(Control.IsEnabledProperty, (_, _) =>
        {
            _mic.IsEnabled = _box.IsEnabled;
            if (!_box.IsEnabled && _state != State.Idle) _ = StopAsync();
        });
        SettingsChanged += () => _host.DispatcherQueue?.TryEnqueue(Refresh);
        Refresh();
    }

    private void Refresh()
    {
        _host.Visibility = Enabled ? Visibility.Visible : Visibility.Collapsed;
        if (!Enabled && _state != State.Idle) _ = StopAsync();
        Render();
    }

    private bool Active => _state is State.Starting or State.Listening;

    private void Render()
    {
        var listening = _state == State.Listening;
        _glyph.Visibility = listening ? Visibility.Collapsed : Visibility.Visible;
        _glyph.Opacity = _state == State.Starting ? 0.4 : 0.72;
        _bars.Visibility = listening ? Visibility.Visible : Visibility.Collapsed;
        var accent = ThemeService.Brush("AccentFillColorDefaultBrush", NexusTokens.BrandGold);
        _mic.Background = listening ? accent : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        var onAccent = ThemeService.Brush("TextOnAccentFillColorPrimaryBrush", NexusTokens.BrandInk);
        foreach (var bar in _barShapes) bar.Fill = onAccent;
        var name = Active ? "Stop dictation" : "Dictate";
        AutomationProperties.SetName(_mic, name);
        AutomationProperties.SetHelpText(_mic, Active ? "Listening. Press Escape to stop." : "Speak instead of typing. " + ShortcutLabel);
        ToolTipService.SetToolTip(_mic, Active ? "Stop dictation (Esc)" : $"Dictate ({ShortcutLabel})");
        _escapeKey.IsEnabled = Active;
        if (!Motion.Enabled || !listening) SetBars(listening ? 0.6 : 0, still: true);
    }

    public Task ToggleAsync() => Active ? StopAsync() : StartAsync();

    public async Task StartAsync()
    {
        if (_state != State.Idle || !Enabled) return;
        HideNotice();
        _engine ??= DictationEngines.Create();
        if (_engine is null)
        {
            _host.Visibility = Visibility.Collapsed;
            return;
        }
        var session = ++_session;
        _state = State.Starting;
        Render();
        _start = Math.Clamp(_box.SelectionStart, 0, _box.Text.Length);
        _length = Math.Clamp(_box.SelectionLength, 0, _box.Text.Length - _start);
        _committed = "";
        _interim = "";
        _expected = _box.Text;
        _heardWords = false;
        _lastSpeech = DateTime.UtcNow;
        _level = 0;
        Wire(_engine);
        DictationProblem problem;
        try { problem = await _engine.StartAsync(AppPrefs.Data.VoiceLanguage ?? ""); }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("voice", "dictation failed to start", ex);
            problem = DictationProblem.Failed;
        }
        if (session != _session) return;
        if (problem != DictationProblem.None)
        {
            _state = State.Idle;
            Render();
            ShowProblem(problem);
            return;
        }
        _state = State.Listening;
        Render();
        Announce("Listening");
        _box.Focus(FocusState.Programmatic);
        _box.Select(_start, _length);
        StartTimer();
    }

    public async Task StopAsync()
    {
        if (_state is State.Idle or State.Stopping) return;
        var session = _session;
        _state = State.Stopping;
        Render();
        try { if (_engine is { } engine) await engine.StopAsync(); }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Warn("voice", "dictation stop failed", ex); }
        // The engine reports Ended (normally at once); never leave the mic open.
        await Task.Delay(2500);
        if (session == _session && _state == State.Stopping) Finish(session, DictationProblem.None);
    }

    private readonly HashSet<IDictationEngine> _wired = new();

    private void Wire(IDictationEngine engine)
    {
        if (!_wired.Add(engine)) return;
        var queue = _host.DispatcherQueue;
        // The session is read when the engine raises the event, so a late
        // event from an earlier session is dropped.
        engine.Hypothesis += text => { var s = _session; queue.TryEnqueue(() => OnWords(s, text, false)); };
        engine.Phrase += text => { var s = _session; queue.TryEnqueue(() => OnWords(s, text, true)); };
        engine.Level += level => queue.TryEnqueue(() => { if (Active) _level = Math.Max(_level, level); });
        engine.Ended += problem => { var s = _session; queue.TryEnqueue(() => Finish(s, problem)); };
    }

    private void OnWords(int session, string text, bool final)
    {
        if (session != _session || _state is State.Idle) return;
        text = DictationText.Tidy(text);
        if (text.Length > 0)
        {
            if (final) _heardWords = true;
            _lastSpeech = DateTime.UtcNow;
            _level = Math.Max(_level, 0.8);
        }
        if (final)
        {
            _committed = DictationText.Join(_committed, text);
            _interim = "";
        }
        else _interim = text;
        Apply();
    }

    /// <summary>Puts committed + interim words in the box, replacing what this session put there.</summary>
    private void Apply()
    {
        if (_box.Text != _expected)
        {
            // The person typed or edited while dictating: what is there stays,
            // and new words go in at the caret from here on.
            _start = Math.Clamp(_box.SelectionStart, 0, _box.Text.Length);
            _length = 0;
            _committed = "";
        }
        var words = DictationText.Join(_committed, _interim);
        var text = _box.Text;
        _start = Math.Clamp(_start, 0, text.Length);
        _length = Math.Clamp(_length, 0, text.Length - _start);
        var insert = DictationText.Insertion(text[.._start], text[(_start + _length)..], words);
        if (insert.Length == 0 && _length == 0) return;
        if (text.Substring(_start, _length) == insert) return;
        // SelectedText (not Text) so the box keeps its undo history: Ctrl+Z
        // takes the dictated words back out.
        _box.Select(_start, _length);
        _box.SelectedText = insert;
        _length = insert.Length;
        _box.Select(_start + _length, 0);
        _expected = _box.Text;
    }

    private void Finish(int session, DictationProblem problem)
    {
        if (session != _session || _state == State.Idle) return;
        _session++;
        if (_interim.Length > 0)
        {
            // Keep the last words heard even if no final phrase came.
            _committed = DictationText.Join(_committed, _interim);
            _interim = "";
            Apply();
        }
        _state = State.Idle;
        StopTimer();
        Render();
        Announce("Stopped");
        if (problem != DictationProblem.None) ShowProblem(problem);
        else if (!_heardWords && _committed.Length == 0) ShowNotice("Didn't catch that. Try again when you're ready.", null, null, autoHide: true);
    }

    // ---- level bars and the silence limit

    private sealed class DispatcherQueueTimerHolder(Microsoft.UI.Dispatching.DispatcherQueueTimer timer)
    {
        public Microsoft.UI.Dispatching.DispatcherQueueTimer Timer { get; } = timer;
    }

    private void StartTimer()
    {
        StopTimer();
        var timer = _host.DispatcherQueue.CreateTimer();
        timer.Interval = TimeSpan.FromMilliseconds(50);
        timer.IsRepeating = true;
        timer.Tick += (_, _) => Tick();
        _timer = new DispatcherQueueTimerHolder(timer);
        timer.Start();
    }

    private void StopTimer()
    {
        _timer?.Timer.Stop();
        _timer = null;
    }

    private void Tick()
    {
        if (_state != State.Listening) return;
        if (DateTime.UtcNow - _lastSpeech > SilenceLimit)
        {
            _ = StopAsync();
            return;
        }
        if (!Motion.Enabled) return;
        _phase += 0.45;
        SetBars(_level, still: false);
        _level *= 0.86;
    }

    private void SetBars(double level, bool still)
    {
        for (var i = 0; i < _barShapes.Length; i++)
        {
            var wobble = still ? 1 : 0.7 + 0.3 * Math.Sin(_phase + i * 1.7);
            // Silence still shows a small waveform, so the pill reads as "listening".
            _barShapes[i].Height = Math.Round(4 + 12 * (0.3 + 0.7 * Math.Clamp(level, 0, 1)) * BarShape[i] * wobble);
        }
    }

    // ---- notices and announcements

    private void ShowProblem(DictationProblem problem)
    {
        var mac = PlatformText.IsMac;
        switch (problem)
        {
            case DictationProblem.MicrophoneDenied:
                ShowNotice(mac ? "Nexus needs permission to use the microphone." : "Microphone access is off for desktop apps.",
                    mac ? "Open System Settings" : "Allow microphone in Windows Settings",
                    () => OpenSettings(mac ? "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone" : "ms-settings:privacy-microphone"));
                break;
            case DictationProblem.SpeechDenied:
                ShowNotice("Nexus needs permission to use speech recognition.", "Open System Settings",
                    () => OpenSettings("x-apple.systempreferences:com.apple.preference.security?Privacy_SpeechRecognition"));
                break;
            case DictationProblem.OnlineSpeechOff:
                ShowNotice("Dictation in this language needs Windows online speech recognition.", "Open speech settings",
                    () => OpenSettings("ms-settings:privacy-speech"));
                break;
            case DictationProblem.NoRecognizer:
                ShowNotice($"Speech recognition isn't available for {LanguageName()} on this {(mac ? "Mac" : "PC")}. Pick another language in Settings → General → Voice input.", null, null);
                break;
            case DictationProblem.NoMicrophone:
                ShowNotice("No microphone found. Connect one and try again.", mac ? "Open Sound settings" : "Open sound settings",
                    () => OpenSettings(mac ? "x-apple.systempreferences:com.apple.Sound-Settings.extension" : "ms-settings:sound"));
                break;
            default:
                ShowNotice("Voice input stopped unexpectedly. Try again.", null, null, autoHide: true);
                break;
        }
    }

    private static string LanguageName()
    {
        var tag = AppPrefs.Data.VoiceLanguage ?? "";
        return tag.Length == 0 ? "your system language" : DictationEngines.DisplayName(tag);
    }

    private static void OpenSettings(string uri)
    {
        try
        {
            if (PlatformText.IsMac)
            {
                var info = new System.Diagnostics.ProcessStartInfo("/usr/bin/open") { UseShellExecute = false };
                info.ArgumentList.Add(uri);
                System.Diagnostics.Process.Start(info);
            }
            else System.Diagnostics.Process.Start(new System.Diagnostics.ProcessStartInfo(uri) { UseShellExecute = true });
        }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Warn("voice", "could not open settings " + uri, ex); }
    }

    private int _noticeId;

    private void ShowNotice(string text, string? action, Action? onAction, bool autoHide = false)
    {
        var id = ++_noticeId;
        _noticeText.Text = text;
        _noticeHandler = onAction;
        _noticeAction.Content = action ?? "";
        _noticeAction.Visibility = action is null ? Visibility.Collapsed : Visibility.Visible;
        if (action is not null) AutomationProperties.SetName(_noticeAction, action);
        _notice.Visibility = Visibility.Visible;
        Motion.FadeSlideIn(_notice, dy: 6, durationMs: 220);
        Announce(text);
        if (!autoHide) return;
        _host.DispatcherQueue.TryEnqueue(async () =>
        {
            await Task.Delay(6000);
            if (id == _noticeId) HideNotice();
        });
    }

    private void HideNotice()
    {
        _noticeId++;
        _notice.Visibility = Visibility.Collapsed;
        _noticeHandler = null;
    }

    private void Announce(string text)
    {
        _live.Text = text;
        try { FrameworkElementAutomationPeer.FromElement(_live)?.RaiseAutomationEvent(AutomationEvents.LiveRegionChanged); }
        catch { }
    }
}
