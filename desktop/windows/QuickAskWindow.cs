using Microsoft.UI.Windowing;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Nexus.Core.Diagnostics;
using Nexus.Pages;
using Windows.System;

namespace Nexus;

/// <summary>
/// The quick-ask prompt: a small always-on-top window near the top of the
/// screen with a prompt, the agent and project, and whether to open the chat
/// or keep it working in the background. Enter sends, Shift+Enter adds a
/// line, Esc (or clicking elsewhere) hides it; what was typed stays until it
/// is sent. Created once and reused.
/// </summary>
internal sealed class QuickAskWindow
{
    private readonly MainWindow _main;
    private readonly Window _window;
    private readonly TextBox _prompt;
    private readonly ComboBox _agent = new() { MinWidth = 150, PlaceholderText = "Agent" };
    private readonly ComboBox _project = new() { MinWidth = 170, PlaceholderText = "Project" };
    private readonly CheckBox _background = new() { Content = "Run in background", MinWidth = 0 };
    private readonly Button _send;
    private readonly TextBlock _status = new() { FontSize = NexusTokens.FontCaption, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
    private readonly Border _card;
    private bool _sending;
    private bool _closing;

    public bool IsShowing { get; private set; }

    private const int WidthDip = 640, HeightDip = 172;

    public QuickAskWindow(MainWindow main)
    {
        _main = main;
        _prompt = new TextBox
        {
            PlaceholderText = "Ask an agent anything…", AcceptsReturn = true, TextWrapping = TextWrapping.Wrap,
            FontSize = 15, MinHeight = 64, MaxHeight = 120, BorderThickness = new Thickness(0),
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), Padding = new Thickness(2, 6, 2, 6)
        };
        AutomationProperties.SetName(_prompt, "Quick ask prompt");
        AutomationProperties.SetHelpText(_prompt, "Enter sends. Shift+Enter adds a line. Escape closes.");
        _prompt.AddHandler(UIElement.KeyDownEvent, new KeyEventHandler(OnPromptKey), handledEventsToo: true);
        AutomationProperties.SetName(_agent, "Agent");
        AutomationProperties.SetName(_project, "Project");
        AutomationProperties.SetName(_background, "Keep working in the background and notify me when it finishes");
        ToolTipService.SetToolTip(_background, "On: the agent works without opening Nexus, and a notification says when it finishes.\nOff: the chat opens in Nexus.");

        _send = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 14 }, Width = 34, Height = 34, Padding = new Thickness(0),
            CornerRadius = new CornerRadius(17), Style = Application.Current.Resources["AccentButtonStyle"] as Style
        };
        AutomationProperties.SetName(_send, "Send");
        ToolTipService.SetToolTip(_send, "Send · Enter");
        _send.Click += async (_, _) => await SendAsync();

        var options = new Grid { ColumnSpacing = 8 };
        foreach (var width in new[] { GridLength.Auto, GridLength.Auto, new GridLength(1, GridUnitType.Star), GridLength.Auto })
            options.ColumnDefinitions.Add(new ColumnDefinition { Width = width });
        options.Children.Add(_agent);
        Grid.SetColumn(_project, 1);
        options.Children.Add(_project);
        Grid.SetColumn(_background, 2);
        _background.Margin = new Thickness(6, 0, 0, 0);
        options.Children.Add(_background);
        Grid.SetColumn(_send, 3);
        options.Children.Add(_send);

        var hint = new TextBlock
        {
            Text = "Enter to send · Shift+Enter for a new line · Esc to close",
            FontSize = NexusTokens.FontMicro, Opacity = 0.5
        };
        var body = new StackPanel { Spacing = 8 };
        body.Children.Add(_prompt);
        body.Children.Add(options);
        body.Children.Add(_status);
        body.Children.Add(hint);
        _card = new Border
        {
            Child = body, Padding = new Thickness(16, 12, 14, 10),
            Background = ThemeService.Brush("NexusSurfaceBrush", NexusTokens.BrandInk),
            BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1), CornerRadius = NexusTokens.RadiusLarge
        };
        AutomationProperties.SetName(_card, "Quick ask");
        var root = new Grid { Background = ThemeService.Brush("NexusSurfaceBrush", NexusTokens.BrandInk), RequestedTheme = ThemeService.ElementTheme };
        root.Children.Add(_card);

        _window = new Window { Title = "Nexus quick ask", Content = root };
        _window.Activated += (_, e) =>
        {
            // Clicking another app puts the prompt away (the text stays).
            if (e.WindowActivationState.ToString() == "Deactivated" && IsShowing && !_sending) HidePrompt();
        };
        _window.Closed += (_, e) =>
        {
            if (_closing) return;
            // The close button or Alt+F4: hide and reuse instead.
            e.Handled = true;
            HidePrompt();
        };
        ConfigureWindow();
        _background.IsChecked = ShellPrefs.Current.QuickAskBackground;
        _background.Checked += (_, _) => ShellPrefs.Update(p => p.QuickAskBackground = true);
        _background.Unchecked += (_, _) => ShellPrefs.Update(p => p.QuickAskBackground = false);
    }

    private void ConfigureWindow()
    {
        try
        {
            var app = _window.AppWindow;
            if (app.Presenter is OverlappedPresenter presenter)
            {
                presenter.IsAlwaysOnTop = true;
                presenter.IsResizable = false;
                presenter.IsMaximizable = false;
                presenter.IsMinimizable = false;
                presenter.SetBorderAndTitleBar(true, false);
            }
            app.IsShownInSwitchers = false;
            app.SetIcon(Path.Combine(AppContext.BaseDirectory, "Assets", "Nexus.ico"));
        }
        catch (Exception ex)
        {
            DiagLog.Info("quickask", "window chrome options unavailable: " + ex.Message);
        }
    }

    private void Place()
    {
        try
        {
            var app = _window.AppWindow;
            var area = DisplayArea.GetFromPoint(CursorPoint(), DisplayAreaFallback.Primary) ?? DisplayArea.Primary;
            var scale = ScaleFor(area);
            var width = (int)(WidthDip * scale);
            var height = (int)(HeightDip * scale);
            var work = area.WorkArea;
            app.MoveAndResize(new Windows.Graphics.RectInt32 { X = work.X + (work.Width - width) / 2, Y = work.Y + work.Height / 5, Width = width, Height = height });
        }
        catch (Exception ex)
        {
            DiagLog.Info("quickask", "placing the window failed: " + ex.Message);
        }
    }

    public void ShowPrompt()
    {
        FillChoices();
        _status.Visibility = Visibility.Collapsed;
        Place();
        IsShowing = true;
        _window.Activate();
#if !HAS_UNO
        BringToFront();
#endif
        Motion.FadeIn(_card, rise: -4, duration: TimeSpan.FromMilliseconds(120));
        _prompt.Focus(FocusState.Programmatic);
        _prompt.SelectAll();
        DiagLog.Info("quickask", "prompt shown");
    }

    public void HidePrompt()
    {
        if (!IsShowing) return;
        IsShowing = false;
        try { _window.AppWindow.Hide(); }
        catch (Exception ex) { DiagLog.Info("quickask", "hide failed: " + ex.Message); }
    }

    public void CloseForGood()
    {
        _closing = true;
        _window.Close();
    }

    private void FillChoices()
    {
        var agents = AgentAvailabilityService.PickerChoices();
        var wantedAgent = ShellPrefs.Current.QuickAskAgent is { Length: > 0 } a ? a : AppPrefs.Data.LastStartAgent;
        _agent.Items.Clear();
        foreach (var (value, name) in agents) _agent.Items.Add(new ComboBoxItem { Content = name, Tag = value });
        _agent.SelectedItem = _agent.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (string)i.Tag == AgentAvailability.Choose(agents, wantedAgent))
            ?? _agent.Items.OfType<ComboBoxItem>().FirstOrDefault();
        _agent.PlaceholderText = agents.Count == 0 ? "No agent ready" : "Agent";

        var projects = _main.Projects;
        var wantedProject = ShellPrefs.Current.QuickAskProject is { Length: > 0 } p && projects.Any(x => x.Id == p) ? p : ShellState.Current.ProjectId;
        _project.Items.Clear();
        foreach (var (id, name) in projects) _project.Items.Add(new ComboBoxItem { Content = name, Tag = id });
        _project.SelectedItem = _project.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (string)i.Tag == wantedProject)
            ?? _project.Items.OfType<ComboBoxItem>().FirstOrDefault();
        _background.IsChecked = ShellPrefs.Current.QuickAskBackground;
    }

    private void OnPromptKey(object sender, KeyRoutedEventArgs e)
    {
        if (e.Key == VirtualKey.Escape)
        {
            e.Handled = true;
            HidePrompt();
        }
        else if (e.Key == VirtualKey.Enter && !ShiftDown())
        {
            e.Handled = true;
            _ = SendAsync();
        }
    }

    private static bool ShiftDown() =>
        Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(VirtualKey.Shift).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);

    private void Status(string text)
    {
        _status.Text = text;
        _status.Visibility = text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private async Task SendAsync()
    {
        var prompt = _prompt.Text.Trim();
        if (prompt.Length == 0 || _sending) return;
        var agent = (_agent.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        var project = (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        var projectName = (_project.SelectedItem as ComboBoxItem)?.Content as string ?? "";
        if (agent.Length == 0) { Status(AgentAvailability.NoneReady); return; }
        if (!ProjectScope.LooksLikeUuid(project)) { Status("Choose a project first."); return; }
        var background = _background.IsChecked == true;
        ShellPrefs.Update(p => { p.QuickAskAgent = agent; p.QuickAskProject = project; });
        _sending = true;
        _send.IsEnabled = false;
        Status("Starting…");
        try
        {
            var workspace = await ProjectFolders.ResolveAsync(App.Core, project, projectName);
            await UiThread.Resume(_main.DispatcherQueue);
            if (workspace.Length == 0)
            {
                Status($"Nexus doesn't know where {projectName} is on this computer yet. Start one chat in it from Nexus first.");
                return;
            }
            var firstLine = prompt.Split('\n', 2)[0].Trim();
            var title = firstLine.Length > 72 ? firstLine[..69] + "…" : firstLine;
            var created = await App.Core.CallAsync(NxMethods.SessionsCreate, new
            {
                project_id = project, harness = agent, native_id = "nexus-" + Guid.NewGuid().ToString("N"),
                title, visibility = "private", workspace_root_hint = workspace
            });
            await UiThread.Resume(_main.DispatcherQueue);
            var id = created is { Ok: true, Result: { } body } ? NxJson.Text(body, "id", "session_id") : "";
            if (id.Length == 0)
            {
                Status(StateView.Friendly(created.Error) is { Length: > 0 } why ? why : "Couldn't start the chat. Try again.");
                return;
            }
            if (background)
            {
                var sent = await App.Core.CallAsync("live.send", new
                {
                    session_id = id, project_id = project, agent, workspace_root = workspace, resume_id = "",
                    model = "", context = "", mode = LiveSessions.Current.DefaultMode, text = prompt,
                    images = Array.Empty<string>(), title
                });
                await UiThread.Resume(_main.DispatcherQueue);
                if (sent.Ok)
                {
                    _prompt.Text = "";
                    Status("");
                    HidePrompt();
                    _main.QuickAskStarted(id, title, ChatTitles.AgentName(agent));
                    return;
                }
                DiagLog.Warn("quickask", "background send failed; opening the chat instead: " + sent.RawError);
            }
            _prompt.Text = "";
            Status("");
            HidePrompt();
            SessionPage.PendingStart = (id, prompt);
            _main.OpenChatFromQuickAsk(id);
        }
        catch (Exception ex)
        {
            DiagLog.Warn("quickask", "send failed", ex);
            Status("Couldn't start the chat. Try again.");
        }
        finally
        {
            _sending = false;
            _send.IsEnabled = true;
        }
    }

    private static double ScaleFor(DisplayArea area)
    {
#if HAS_UNO
        return 1;
#else
        try
        {
            var monitor = MonitorFromPoint(new Point { X = area.WorkArea.X + 1, Y = area.WorkArea.Y + 1 }, 2);
            if (GetDpiForMonitor(monitor, 0, out var dpi, out _) == 0 && dpi > 0) return dpi / 96.0;
        }
        catch { /* 100% */ }
        return 1;
#endif
    }

#if HAS_UNO
    private static Windows.Graphics.PointInt32 CursorPoint() => default;
#else
    private struct Point { public int X; public int Y; }

    [System.Runtime.InteropServices.DllImport("user32.dll")] private static extern bool GetCursorPos(out Point point);
    [System.Runtime.InteropServices.DllImport("user32.dll")] private static extern IntPtr MonitorFromPoint(Point point, uint flags);
    [System.Runtime.InteropServices.DllImport("shcore.dll")] private static extern int GetDpiForMonitor(IntPtr monitor, int type, out uint dpiX, out uint dpiY);
    [System.Runtime.InteropServices.DllImport("user32.dll")] private static extern bool SetForegroundWindow(IntPtr hwnd);

    private static Windows.Graphics.PointInt32 CursorPoint() =>
        GetCursorPos(out var p) ? new Windows.Graphics.PointInt32(p.X, p.Y) : default;

    // A hotkey handler may bring its own window forward (Windows allows the
    // process that received the hotkey to take the foreground).
    private void BringToFront()
    {
        try { SetForegroundWindow(WinRT.Interop.WindowNative.GetWindowHandle(_window)); }
        catch { /* activation above usually did it */ }
    }
#endif
}
