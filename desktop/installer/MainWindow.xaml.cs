using System;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Threading;
using System.Threading.Tasks;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Input;
using System.Windows.Interop;
using System.Windows.Media;
using System.Windows.Shell;
using System.Windows.Threading;

namespace Nexus.Installer;

public static class Program
{
    [STAThread]
    public static void Main(string[] args)
    {
        var app = new Application { ShutdownMode = ShutdownMode.OnMainWindowClose };
        app.Run(new MainWindow(args));
    }
}

/// <summary>What this run will do, decided once from the installed and bundled versions.</summary>
internal enum Plan
{
    Install,  // nothing installed yet
    Update,   // the bundled version is newer (or a version can't be read)
    Current,  // the same version is installed: offer Open, with Repair aside
    Newer     // a newer version is installed: offer Open, with installing this one aside
}

/// <summary>
/// The demo modes, for recordings and design reviews. A demo never reads or
/// changes the real installation: it pretends one exists when asked to.
///   --demo-install          a clean install that always succeeds
///   --demo-error            fails part-way, after files would have been written
///   --demo-error-early      fails before anything would have been written
///   --demo-uptodate         the same version is already installed
///   --demo-installed=1.2.3  that version is already installed
/// </summary>
internal sealed class Demo
{
    public bool Fail, FailEarly;
    public string? Installed;

    public static Demo? From(string[] args)
    {
        var flags = args.Where(a => a.StartsWith("--demo-", StringComparison.OrdinalIgnoreCase)).ToArray();
        if (flags.Length == 0) return null;
        bool Has(string f) => flags.Any(a => a.Equals(f, StringComparison.OrdinalIgnoreCase));
        var demo = new Demo { Fail = Has("--demo-error"), FailEarly = Has("--demo-error-early") };
        if (Has("--demo-uptodate")) demo.Installed = "";
        var installed = flags.FirstOrDefault(a => a.StartsWith("--demo-installed=", StringComparison.OrdinalIgnoreCase));
        if (installed is not null) demo.Installed = installed.Substring("--demo-installed=".Length);
        return demo;
    }
}

public partial class MainWindow : Window
{
    private readonly Demo? _demo;
    private readonly Payload _payload;
    private readonly Existing? _existing;
    private readonly string _bundled;
    private readonly Plan _plan;
    private readonly HeroMark _hero = new();
    private readonly MemoryGraph _graph = new();
    private string _root;
    private bool _busy, _optionsOpen, _privacyOpen, _wasRunning, _opened, _demoWroteFiles, _graphLive, _mouseClick;
    private FrameworkElement _current = null!;
    private int _swap, _sheet;
    private TaskCompletionSource<bool>? _graphReady;
    private double _target, _shown;
    private string _status = "";
    private InstallEngine? _engine;

    public MainWindow(string[] args)
    {
        _demo = Demo.From(args);
        _payload = Payload.Read();
        // A build without a payload still needs a version to show in the demos.
        _bundled = _payload.Version.Length > 0 || _demo is null ? _payload.Version : "0.4.0";
        // In a demo the real installation is never even looked at.
        _existing = _demo is null
            ? Existing.Find()
            : _demo.Installed is { } v ? new Existing { Version = v.Length > 0 ? v : _bundled, Root = InstallEngine.DefaultRoot, Simulated = true } : null;
        _plan = _existing is null ? Plan.Install
            : Versions.Compare(_bundled, _existing.Version) switch { 0 => Plan.Current, < 0 => Plan.Newer, _ => Plan.Update };
        _root = _existing?.Root ?? InstallEngine.DefaultRoot;
        InitializeComponent();
        _current = Welcome;
        // Hidden from the very first frame: the entrance fades it in, so the
        // finished layout never flashes before the animation starts.
        if (Anim.Enabled) ContentGrid.Opacity = 0;
        WindowStyle = WindowStyle.SingleBorderWindow;
        TitleMark.Source = Brand.Mark;
        HeroHost.Child = _hero;
        GraphHost.Child = _graph;
        VersionText.Text = (_bundled.Length > 0 ? "Version " + _bundled + "  ·  " : "") + "For you only, no admin needed";
        LocationText.Text = _root;
        if (_existing is not null) DescribeExisting(_existing);

        InstallButton.IsDefault = true;
        // A mouse press already sinks the button; Enter (or a screen reader)
        // gets the same tap, so every way of pressing Install feels the same.
        InstallButton.PreviewMouseLeftButtonUp += (_, _) => _mouseClick = true;
        InstallButton.Click += async (_, _) =>
        {
            if (!_mouseClick && InstallButton.Template.FindName("Press", InstallButton) is ScaleTransform press) Anim.Tap(press);
            _mouseClick = false;
            if (_plan is Plan.Current or Plan.Newer) await OpenInstalledAsync();
            else await InstallAsync();
        };
        RepairButton.Click += async (_, _) => await InstallAsync();
        RetryButton.Click += async (_, _) => await InstallAsync();
        OpenButton.Click += (_, _) => OpenNexus();
        OptionsButton.Click += (_, _) => ToggleOptions();
        ChangeLocation.Click += (_, _) => PickLocation();
        LogButton.Click += (_, _) => Shell(_engine?.LogPath ?? "");
        PrivacyLink.Click += (_, _) => SetPrivacy(!_privacyOpen);
        PrivacyClose.Click += (_, _) => SetPrivacy(false);
        SourceLink.Click += (_, _) => Shell("https://github.com/Mpratyush54/nexus");
        MinButton.Click += (_, _) => WindowState = WindowState.Minimized;
        CloseButton.Click += async (_, _) => await CloseAsync();
        KeyDown += async (_, e) =>
        {
            if (e.Key != Key.Escape) return;
            if (_privacyOpen) { SetPrivacy(false); e.Handled = true; }
            else if (!_busy) await CloseAsync();
        };
        Root.MouseLeftButtonDown += (_, e) =>
        {
            if (e.OriginalSource is DependencyObject d && FindButton(d) is null && e.ButtonState == MouseButtonState.Pressed) DragMove();
        };
        SourceInitialized += (_, _) => ApplyChrome();
        // ContentRendered, not Loaded: the first frame is on screen (and the
        // backdrop is up) before anything moves, so no frames are skipped.
        ContentRendered += (_, _) => Entrance();
        CompositionTargetTick();
    }

    // ---- what is already installed

    private void DescribeExisting(Existing existing)
    {
        var running = existing.Running().Any();
        var installed = existing.Version;
        ExistingPill.Visibility = Visibility.Visible;
        ChangeLocation.IsEnabled = false;
        switch (_plan)
        {
            case Plan.Current:
                ExistingText.Text = installed.Length > 0 ? $"Nexus {installed} is already up to date" : "Nexus is already up to date";
                SetPrimary("Open Nexus");
                RepairButton.Visibility = Visibility.Visible;
                break;
            case Plan.Newer:
                ExistingText.Text = $"A newer Nexus ({installed}) is already installed";
                SetPrimary("Open Nexus");
                RepairButton.Content = $"Install {_bundled} instead";
                AutomationProperties_Set(RepairButton, $"Install Nexus {_bundled} instead");
                RepairButton.Visibility = Visibility.Visible;
                break;
            default:
                var from = installed.Length > 0 ? $"Nexus {installed}" : "Nexus";
                var to = _bundled.Length > 0 ? $" to {_bundled}" : "";
                ExistingText.Text = running
                    ? $"{from} is running. It will restart to update{to}."
                    : $"{from} is installed. This updates it{to}.";
                SetPrimary("Update", "Update Nexus");
                break;
        }
    }

    private void SetPrimary(string text, string? name = null)
    {
        InstallButton.Content = text;
        AutomationProperties_Set(InstallButton, name ?? text);
    }

    // ---- the one gate in front of anything that changes this PC

    /// <summary>
    /// Every step that touches the installed app or the PC (closing Nexus,
    /// running Setup, shortcuts, sign-in start, launching Nexus) calls this
    /// first. Demo modes never pass it, whatever path they take.
    /// </summary>
    private void RequireLive()
    {
        if (_demo is not null) throw new InvalidOperationException("Demo mode never changes this PC.");
    }

    // ---- window chrome: borderless, rounded, shadow, acrylic (Windows 11)

    private void ApplyChrome()
    {
        var acrylic = Dwm.SupportsBackdrop;
        WindowChrome.SetWindowChrome(this, new WindowChrome
        {
            CaptionHeight = 0, ResizeBorderThickness = new Thickness(0), UseAeroCaptionButtons = false,
            GlassFrameThickness = acrylic ? new Thickness(-1) : new Thickness(0, 0, 0, 1), CornerRadius = new CornerRadius(0)
        });
        var hwnd = new WindowInteropHelper(this).Handle;
        if (acrylic)
        {
            Background = Brushes.Transparent;
            if (HwndSource.FromHwnd(hwnd)?.CompositionTarget is { } target) target.BackgroundColor = Colors.Transparent;
        }
        else
        {
            Tint.Fill = new SolidColorBrush(Brand.Ink);
        }
        Dwm.Style(hwnd, acrylic);
    }

    private void Entrance()
    {
        var (scale, _) = Anim.Rig(Content);
        Anim.To(Content, OpacityProperty, 1, Anim.Enter, 0, Anim.Out, 0);
        Anim.To(scale, ScaleTransform.ScaleXProperty, 1, Anim.Enter + 60, 0, Anim.Out, 0.975);
        Anim.To(scale, ScaleTransform.ScaleYProperty, 1, Anim.Enter + 60, 0, Anim.Out, 0.975);
        _hero.Play();
        // One cascade, top to bottom, on the same curve and length.
        var d = 200;
        Anim.Rise(Headline, d, 8, Anim.Enter);
        Anim.Rise(Tagline, d += Anim.Stagger, 8, Anim.Enter);
        if (ExistingPill.Visibility == Visibility.Visible) Anim.Rise(ExistingPill, d += Anim.Stagger, 8, Anim.Enter);
        Anim.Rise(InstallButton, d += Anim.Stagger, 8, Anim.Enter);
        Anim.Rise(SecondaryRow, d += Anim.Stagger, 8, Anim.Enter);
        Anim.Rise(FinePrint, d + Anim.Stagger, 6, Anim.Enter);
        InstallButton.Focus();
    }

    // ---- welcome options

    // In sequence, never overlapping: what leaves fades out (the hero shrinks
    // with its space, so it never draws over the headline) before what
    // arrives fades in.
    private void ToggleOptions()
    {
        _optionsOpen = !_optionsOpen;
        if (HeroHost.RenderTransform is not ScaleTransform heroScale)
            HeroHost.RenderTransform = heroScale = new ScaleTransform(1, 1);
        const int fade = Anim.Exit, resize = Anim.Enter;
        if (_optionsOpen)
        {
            Anim.To(HeroHost, OpacityProperty, 0, fade - 60, 0, Anim.Out);
            Anim.To(heroScale, ScaleTransform.ScaleXProperty, 0.85, fade, 0, Anim.Out);
            Anim.To(heroScale, ScaleTransform.ScaleYProperty, 0.85, fade, 0, Anim.Out);
            Anim.To(HeroHost, HeightProperty, 0, resize, 60, Anim.Out);
            Anim.To(OptionsPanel, MaxHeightProperty, 180, resize, 60, Anim.Out);
            Anim.To(OptionsPanel, OpacityProperty, 1, Anim.Enter, fade, Anim.Out);
        }
        else
        {
            Anim.To(OptionsPanel, OpacityProperty, 0, fade - 60, 0, Anim.Out);
            Anim.To(OptionsPanel, MaxHeightProperty, 0, resize, 60, Anim.Out);
            Anim.To(HeroHost, HeightProperty, 150, resize, 60, Anim.Out);
            Anim.To(HeroHost, OpacityProperty, 1, Anim.Enter, fade, Anim.Out);
            Anim.To(heroScale, ScaleTransform.ScaleXProperty, 1, Anim.Enter, fade, Anim.Out);
            Anim.To(heroScale, ScaleTransform.ScaleYProperty, 1, Anim.Enter, fade, Anim.Out);
        }
        Anim.To(ChevronTurn, System.Windows.Media.RotateTransform.AngleProperty, _optionsOpen ? 180 : 0, Anim.Enter, 0, Anim.Out);
    }

    // The sheet is solid and the welcome screen steps out (and is disabled) while
    // it is open, so nothing behind it shows through or takes a click or Tab.
    private void SetPrivacy(bool open)
    {
        if (open == _privacyOpen) return;
        _privacyOpen = open;
        var token = ++_sheet;
        Welcome.IsEnabled = !open;
        if (open)
        {
            PrivacyLayer.Visibility = Visibility.Visible;
            PrivacySheet.Opacity = 0;
            ParkFocus(Welcome);
            Anim.Leave(Welcome, () =>
            {
                if (token != _sheet) return;
                Anim.Arrive(PrivacySheet, 0, 12);
                PrivacyClose.Focus();
            });
        }
        else
        {
            ParkFocus(PrivacySheet);
            Anim.Leave(PrivacySheet, () =>
            {
                if (token != _sheet) return;
                PrivacyLayer.Visibility = Visibility.Collapsed;
                if (_current == Welcome) Anim.Arrive(Welcome, 0, 6);
                PrivacyLink.Focus();
            });
        }
    }

    private void PickLocation()
    {
        using var dialog = new System.Windows.Forms.FolderBrowserDialog
        {
            Description = "Choose where to install Nexus. A \"Nexus\" folder is created inside it.",
            SelectedPath = Path.GetDirectoryName(_root) ?? ""
        };
        if (dialog.ShowDialog() != System.Windows.Forms.DialogResult.OK) return;
        var chosen = dialog.SelectedPath;
        _root = Path.GetFileName(chosen).Equals("Nexus", StringComparison.OrdinalIgnoreCase) ? chosen : Path.Combine(chosen, "Nexus");
        LocationText.Text = _root;
    }

    // ---- install

    private async Task InstallAsync()
    {
        if (_busy) return;
        _busy = true;
        // Closing mid-install isn't possible: the button steps aside cleanly.
        CloseButton.IsHitTestVisible = false;
        Anim.To(CloseButton, OpacityProperty, 0, 200, 0, Anim.Out);
        _target = _shown = 0;
        _demoWroteFiles = false;
        _graphLive = false;
        _graph.Reset();
        Percent.Text = "0%";
        _graphReady = new TaskCompletionSource<bool>();
        if (_current == Welcome && CanMorph()) MorphToInstalling();
        else Show(Installing, s => Anim.Arrive(s, 0, 8, GraphLive));
        SetStatus("Preparing…");
        try
        {
            // The work starts once the graph is on screen (at most a moment),
            // so the first status lines are read, not flashed past.
            await Task.WhenAny(_graphReady.Task, Task.Delay(1500));
            var progress = new Progress<Progress>(p => { _target = p.Percent; SetStatus(p.Status); });
            // The fork: a demo takes only the scripted path below and never
            // reaches RunLiveAsync, which is the only way to the quit/Setup code.
            if (_demo is not null) await DemoAsync(_demo, progress);
            else await RunLiveAsync(progress);
            _target = 100;
            while (_shown < 99.5) await Task.Delay(30);
            if (_demo is null) ApplyOptions();
            await Task.Delay(250);
            ShowDone();
        }
        catch (Exception ex)
        {
            ShowFailed(ex);
        }
        finally
        {
            _busy = false;
            CloseButton.IsHitTestVisible = true;
            Anim.To(CloseButton, OpacityProperty, 1, 260, 0, Anim.Out);
        }
    }

    private async Task RunLiveAsync(IProgress<Progress> progress)
    {
        RequireLive();
        _engine = new InstallEngine(_root, _payload);
        if (_existing is not null && _existing.Running().Any())
        {
            _wasRunning = true;
            SetStatus(_plan == Plan.Current ? "Closing Nexus to repair it…" : "Closing Nexus to update it…");
            await QuitRunningAsync(_existing);
        }
        await _engine.RunAsync(progress, CancellationToken.None);
    }

    private async Task QuitRunningAsync(Existing existing)
    {
        RequireLive();
        if (existing.Simulated) return;
        var exe = Path.Combine(existing.Root, "current", "Nexus.exe");
        try { Process.Start(new ProcessStartInfo(exe, "--quit") { UseShellExecute = false })?.Dispose(); }
        catch (Exception) { }
        for (var i = 0; i < 40 && existing.Running().Any(); i++) await Task.Delay(200);
        // Velopack force-stops whatever is left during a silent update.
    }

    // Exercises every screen without touching the system (recordings, design).
    private async Task DemoAsync(Demo demo, IProgress<Progress> progress)
    {
        (double At, string Status)[] script =
        [
            (5, "Unpacking Nexus…"), (22, "Installing the Windows App runtime…"), (58, "Setting up the capture engine…"),
            (84, "Adding the nexus command line…"), (92, "Adding Nexus to Start…"), (96, "Registering nexus:// links…"), (99, "Finishing up…")
        ];
        if (demo.FailEarly)
        {
            await Task.Delay(900);
            throw new InstallFailedException("This installer is missing its package. Download it again.", "");
        }
        // The real Setup is the first step that writes to the install folder;
        // the demo plays that it starts as unpacking begins.
        _demoWroteFiles = true;
        var pct = 1.0;
        foreach (var (at, status) in script)
        {
            while (pct < at) { pct += 0.6; progress.Report(new Progress(pct, status)); await Task.Delay(40); }
            if (demo.Fail && at >= 58) throw new InstallFailedException("Couldn't write to the install folder. Another program may be using it.", "");
        }
    }

    private void ApplyOptions()
    {
        RequireLive();
        if (_engine is null) return;
        try { if (DesktopShortcut.IsChecked == true) _engine.SetDesktopShortcut(true); } catch (Exception) { }
        try { if (StartAtLogin.IsChecked == true) _engine.SetStartAtLogin(true); } catch (Exception) { }
    }

    // Displayed progress glides toward the real value at 60 fps, so the
    // graph forms smoothly even when Velopack reports in bursts.
    private void CompositionTargetTick()
    {
        CompositionTarget.Rendering += (_, _) =>
        {
            // Nodes start forming only once the graph is fully on screen.
            if (!_graphLive || _shown >= _target) return;
            _shown = Math.Min(_target, _shown + Math.Max(0.25, (_target - _shown) * 0.06));
            _graph.SetProgress(_shown);
            Percent.Text = $"{Math.Floor(_shown)}%";
        };
    }

    private void SetStatus(string text)
    {
        if (text == _status || text.Length == 0) return;
        _status = text;
        // Two lines, in sequence: the old one lifts away before the new one
        // rises, so two statuses are never on screen at once.
        var (inc, outg) = StatusA.Opacity > 0.5 ? (StatusB, StatusA) : (StatusA, StatusB);
        inc.Text = text;
        const int gone = 140;
        var wait = outg.Opacity > 0 ? gone : 0;
        var (_, lift) = Anim.Rig(outg);
        Anim.To(lift, TranslateTransform.YProperty, -6, gone, 0, Anim.Out);
        Anim.To(outg, OpacityProperty, 0, gone, 0, Anim.Out);
        Anim.Rise(inc, wait, 6, Anim.Enter - 60);
    }

    // ---- done / failed

    // The graph is the shared element: the status lines step out, the graph
    // glides to its place on the Done screen and blooms, then the text rises.
    private void ShowDone()
    {
        var token = ++_swap;
        _current = Done;
        foreach (var e in new UIElement[] { StatusA, StatusB, Percent }) Anim.Leave(e, null, 0, 1, -4);
        void Land()
        {
            if (token != _swap) return;
            var before = _graph.TranslatePoint(new Point(), MorphLayer);
            GraphHost.Child = null;
            DoneGraphHost.Child = _graph;
            foreach (var e in new UIElement[] { DoneTitle, DoneSub, OpenButton }) e.Opacity = 0;
            Done.BeginAnimation(OpacityProperty, null);
            Done.RenderTransform = null;
            Done.Opacity = 1;
            Done.IsHitTestVisible = true;
            Done.Visibility = Visibility.Visible;
            Installing.Visibility = Visibility.Collapsed;
            UpdateLayout();
            var after = _graph.TranslatePoint(new Point(), MorphLayer);
            var (_, move) = Anim.Rig(_graph);
            Anim.To(move, TranslateTransform.XProperty, 0, Anim.Travel, 0, Anim.Move, before.X - after.X);
            Anim.To(move, TranslateTransform.YProperty, 0, Anim.Travel, 0, Anim.Move, before.Y - after.Y);
            _graph.Bloom();
            ShowDoneText();
        }
        if (Anim.Enabled) Delay(Anim.Exit, Land); else Land();
    }

    private void ShowDoneText()
    {
        DoneTitle.Text = _plan switch
        {
            Plan.Update => "Nexus is updated",
            Plan.Current => "Nexus is repaired",
            Plan.Newer => _bundled.Length > 0 ? $"Nexus {_bundled} is installed" : "Nexus is installed",
            _ => "You're all set"
        };
        Anim.Rise(DoneTitle, 160, 8, Anim.Enter);
        Anim.Rise(DoneSub, 160 + Anim.Stagger, 8, Anim.Enter);
        Anim.Rise(OpenButton, 160 + 2 * Anim.Stagger, 8, Anim.Enter);
        OpenButton.Focus();
        var left = 2;
        DoneSub.Text = $"{(_wasRunning ? "Restarting" : "Opening")} Nexus in {left}…";
        var timer = new DispatcherTimer { Interval = TimeSpan.FromSeconds(1) };
        timer.Tick += (_, _) =>
        {
            left--;
            if (_opened) { timer.Stop(); return; }
            if (left <= 0) { timer.Stop(); OpenNexus(); return; }
            DoneSub.Text = $"{(_wasRunning ? "Restarting" : "Opening")} Nexus in {left}…";
        };
        timer.Start();
    }

    private void ShowFailed(Exception ex, bool opening = false)
    {
        // Only promise "nothing changed" when Setup never started writing.
        var wrote = _demo is not null ? _demoWroteFiles : _engine?.StartedWriting == true;
        FailedTitle.Text = opening ? "Nexus couldn't be opened"
            : _plan switch { Plan.Update => "Nexus couldn't be updated", Plan.Current => "Nexus couldn't be repaired", _ => "Nexus couldn't be installed" };
        if (opening)
            FailedText.Text = "Windows couldn't start Nexus. Try opening it from the Start menu, or run this installer again to repair it.";
        else if (wrote)
            FailedText.Text = _existing is null
                ? "Nexus may be partly installed. Running the installer again will repair it."
                : "Nexus may be partly updated. Running the installer again will repair it.";
        else
            FailedText.Text = (ex is InstallFailedException
                    ? "The install stopped before anything on your PC was changed, so it's safe to try again."
                    : "Something unexpected happened before anything on your PC was changed, so it's safe to try again.")
                + (_wasRunning ? " Nexus was closed first; you can open it again from Start." : "");
        FailedDetail.Text = ex.Message;
        LogButton.Visibility = ex is InstallFailedException f && f.LogPath.Length > 0 && File.Exists(f.LogPath) ? Visibility.Visible : Visibility.Collapsed;
        Show(Failed, s => { Anim.Arrive(s); RetryButton.Focus(); });
    }

    private async void OpenNexus()
    {
        if (_opened) return;
        _opened = true;
        // The button bows out instead of greying: the window is handing over.
        OpenButton.IsHitTestVisible = false;
        Anim.To(OpenButton, OpacityProperty, 0, 220, 0, Anim.Out, null, () => OpenButton.Visibility = Visibility.Hidden);
        DoneSub.Text = _wasRunning ? "Restarting Nexus…" : "Opening Nexus…";
        if (_demo is null && _engine is not null && File.Exists(_engine.MainExe))
        {
            // Upgrade: the old copy must be fully gone first, or the new one
            // would hand off to it (single instance) and the old version stays.
            for (var i = 0; i < 40 && InstallEngine.ProcessesUnder(_engine.Root).Any(); i++) await Task.Delay(150);
            try { Launch(_engine.MainExe); }
            catch (Exception ex) { ShowFailed(ex, opening: true); return; }
        }
        await Task.Delay(700);
        await CloseAsync();
    }

    // Welcome, when this version (or a newer one) is already installed.
    private async Task OpenInstalledAsync()
    {
        if (_busy || _existing is null) return;
        if (_demo is null)
        {
            try { Launch(Path.Combine(_existing.Root, "current", "Nexus.exe")); }
            catch (Exception ex) { ShowFailed(ex, opening: true); return; }
        }
        await CloseAsync();
    }

    private void Launch(string exe)
    {
        RequireLive();
        Process.Start(new ProcessStartInfo(exe) { UseShellExecute = true, WorkingDirectory = Path.GetDirectoryName(exe) })?.Dispose();
    }

    // ---- screens

    /// <summary>
    /// Sequential swap: the current screen steps out completely, then the next
    /// one comes in, so two screens are never visible together. A newer swap
    /// supersedes one still in flight.
    /// </summary>
    private void Show(FrameworkElement screen, Action<FrameworkElement>? enter = null)
    {
        var token = ++_swap;
        var leaving = _current;
        _current = screen;
        ClosePrivacyNow();
        if (screen != Welcome) HideFinePrint(token, 0);
        void In()
        {
            if (token != _swap) return;
            foreach (var s in new FrameworkElement[] { Welcome, Installing, Done, Failed })
                if (s != screen) s.Visibility = Visibility.Collapsed;
            screen.Visibility = Visibility.Visible;
            screen.Opacity = 0;
            if (enter is not null) enter(screen); else Anim.Arrive(screen);
        }
        if (leaving == screen || leaving.Visibility != Visibility.Visible || leaving.Opacity <= 0) { In(); return; }
        ParkFocus(leaving);
        Anim.Leave(leaving, In);
    }

    private void HideFinePrint(int token, int delay)
    {
        if (FinePrint.Visibility != Visibility.Visible) return;
        Anim.Leave(FinePrint, () => { if (token == _swap) FinePrint.Visibility = Visibility.Hidden; }, delay, 1, 0);
    }

    private void ClosePrivacyNow()
    {
        if (!_privacyOpen) return;
        _privacyOpen = false;
        ++_sheet;
        PrivacyLayer.Visibility = Visibility.Collapsed;
        Welcome.IsEnabled = true;
    }

    // ---- the shared-element morph: the welcome mark becomes the graph's centre

    private bool CanMorph() =>
        Anim.Enabled && !_optionsOpen && !_privacyOpen && Welcome.IsVisible && HeroHost.ActualHeight > 100;

    private void MorphToInstalling()
    {
        var token = ++_swap;
        _current = Installing;
        ParkFocus(Welcome);

        // Where the mark is now, and where it will be once Installing is laid out.
        var hero = _hero.Mark;
        var from = hero.TranslatePoint(new Point(hero.ActualWidth / 2, hero.ActualHeight / 2), MorphLayer);
        var fromSize = hero.ActualWidth * _hero.MarkScale;
        Installing.BeginAnimation(OpacityProperty, null);
        Installing.RenderTransform = null;
        Installing.Opacity = 0;
        Installing.IsHitTestVisible = true;
        Installing.Visibility = Visibility.Visible;
        UpdateLayout();
        var mark = _graph.Mark;
        var to = mark.TranslatePoint(new Point(mark.ActualWidth / 2, mark.ActualHeight / 2), MorphLayer);
        var toSize = mark.ActualWidth;

        // The travelling copy; both real marks stay hidden until it lands.
        var ghost = new Image { Source = Brand.Mark, Width = fromSize, Height = fromSize };
        RenderOptions.SetBitmapScalingMode(ghost, BitmapScalingMode.HighQuality);
        Canvas.SetLeft(ghost, from.X - fromSize / 2);
        Canvas.SetTop(ghost, from.Y - fromSize / 2);
        var (scale, move) = Anim.Rig(ghost);
        MorphLayer.Children.Add(ghost);
        hero.Opacity = 0;
        mark.Opacity = 0;

        // 1. Welcome steps out around the mark (the ghost stands in for it).
        Anim.Leave(Welcome, () =>
        {
            if (token != _swap) return;
            Welcome.Visibility = Visibility.Collapsed;
            // 2. Then Installing fades in around the arriving mark.
            Anim.To(Installing, OpacityProperty, 1, Anim.Enter, 0, Anim.Out, 0);
        }, 40);
        HideFinePrint(token, 40);
        // 3. Meanwhile the mark glides and settles into the centre of the graph.
        var k = toSize / fromSize;
        Anim.To(move, TranslateTransform.XProperty, to.X - from.X, Anim.Travel, 40, Anim.Move);
        Anim.To(move, TranslateTransform.YProperty, to.Y - from.Y, Anim.Travel, 40, Anim.Move);
        Anim.To(scale, ScaleTransform.ScaleXProperty, k, Anim.Travel, 40, Anim.Move);
        Anim.To(scale, ScaleTransform.ScaleYProperty, k, Anim.Travel, 40, Anim.Move, null, () =>
        {
            MorphLayer.Children.Remove(ghost);
            GraphLive();
        });
    }

    private void GraphLive()
    {
        _graph.Mark.Opacity = 1;
        _graph.Breathe();
        _graphLive = true;
        _graphReady?.TrySetResult(true);
    }

    /// <summary>
    /// Keyboard focus leaves a screen before it fades: the focus ring lives in
    /// the adorner layer and would otherwise linger at full strength.
    /// </summary>
    private void ParkFocus(UIElement leaving)
    {
        if (leaving.IsKeyboardFocusWithin) Keyboard.Focus(Frame);
    }

    private static void Delay(int ms, Action then)
    {
        var t = new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(ms) };
        t.Tick += (_, _) => { t.Stop(); then(); };
        t.Start();
    }

    private async Task CloseAsync()
    {
        if (_busy) return;
        if (Anim.Enabled)
        {
            var (scale, _) = Anim.Rig(Content);
            Anim.To(scale, ScaleTransform.ScaleXProperty, 0.97, 200, 0, Anim.Out);
            Anim.To(scale, ScaleTransform.ScaleYProperty, 0.97, 200, 0, Anim.Out);
            Anim.To(Content, OpacityProperty, 0, 200, 0, Anim.Out);
            await Task.Delay(210);
        }
        Close();
    }

    private static Button? FindButton(DependencyObject d)
    {
        for (var cur = d; cur is not null; cur = cur is Visual || cur is System.Windows.Media.Media3D.Visual3D ? VisualTreeHelper.GetParent(cur) : LogicalTreeHelper.GetParent(cur))
            if (cur is Button b) return b;
        return null;
    }

    private static void Shell(string target)
    {
        if (target.Length == 0) return;
        try { Process.Start(new ProcessStartInfo(target) { UseShellExecute = true })?.Dispose(); } catch (Exception) { }
    }

    private static void AutomationProperties_Set(UIElement e, string name) =>
        System.Windows.Automation.AutomationProperties.SetName(e, name);

    private new FrameworkElement Content => ContentGrid;
}
