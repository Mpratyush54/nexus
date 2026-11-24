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

public partial class MainWindow : Window
{
    private readonly Payload _payload = Payload.Read();
    private readonly Existing? _existing = Existing.Find();
    private readonly HeroMark _hero = new();
    private readonly MemoryGraph _graph = new();
    private readonly bool _demoInstall, _demoError;
    private string _root;
    private bool _busy, _optionsOpen, _wasRunning, _opened;
    private double _target, _shown;
    private string _status = "";
    private InstallEngine? _engine;

    public MainWindow(string[] args)
    {
        _demoInstall = args.Contains("--demo-install");
        _demoError = args.Contains("--demo-error");
        _root = _existing?.Root ?? InstallEngine.DefaultRoot;
        InitializeComponent();
        WindowStyle = WindowStyle.SingleBorderWindow;
        TitleMark.Source = Brand.Mark;
        HeroHost.Child = _hero;
        GraphHost.Child = _graph;
        VersionText.Text = (_payload.Version.Length > 0 ? "Version " + _payload.Version + "  ·  " : "") + "For you only, no admin needed";
        LocationText.Text = _root;
        if (_existing is not null)
        {
            var running = _existing.Running().Any();
            ExistingPill.Visibility = Visibility.Visible;
            ExistingText.Text = running
                ? "Nexus is running. It will restart to update."
                : $"Nexus {_existing.Version} is installed. This updates it.";
            InstallButton.Content = "Update";
            AutomationProperties_Set(InstallButton, "Update Nexus");
            ChangeLocation.IsEnabled = false;
        }

        InstallButton.IsDefault = true;
        InstallButton.Click += async (_, _) => await InstallAsync();
        RetryButton.Click += async (_, _) => await InstallAsync();
        OpenButton.Click += (_, _) => OpenNexus();
        OptionsButton.Click += (_, _) => ToggleOptions();
        ChangeLocation.Click += (_, _) => PickLocation();
        LogButton.Click += (_, _) => Shell(_engine?.LogPath ?? "");
        PrivacyLink.Click += (_, _) => { PrivacySheet.Visibility = Visibility.Visible; Anim.Rise(PrivacySheet, 0, 16, 360); };
        PrivacyClose.Click += (_, _) => PrivacySheet.Visibility = Visibility.Collapsed;
        SourceLink.Click += (_, _) => Shell("https://github.com/Mpratyush54/nexus");
        MinButton.Click += (_, _) => WindowState = WindowState.Minimized;
        CloseButton.Click += async (_, _) => await CloseAsync();
        KeyDown += async (_, e) => { if (e.Key == Key.Escape && !_busy) await CloseAsync(); };
        Root.MouseLeftButtonDown += (_, e) =>
        {
            if (e.OriginalSource is DependencyObject d && FindButton(d) is null && e.ButtonState == MouseButtonState.Pressed) DragMove();
        };
        SourceInitialized += (_, _) => ApplyChrome();
        Loaded += (_, _) => Entrance();
        CompositionTargetTick();
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
        Content.Opacity = Anim.Enabled ? 0 : 1;
        Anim.To(Content, OpacityProperty, 1, 380, 40, Anim.Out, 0);
        Anim.To(scale, ScaleTransform.ScaleXProperty, 1, 520, 40, Anim.Out, 0.965);
        Anim.To(scale, ScaleTransform.ScaleYProperty, 1, 520, 40, Anim.Out, 0.965);
        _hero.Play();
        Anim.Rise(Headline, 260, 10, 520);
        Anim.Rise(Tagline, 340, 10, 520);
        if (ExistingPill.Visibility == Visibility.Visible) Anim.Rise(ExistingPill, 400, 10, 520);
        Anim.Rise(InstallButton, 440, 12, 560);
        Anim.Rise(OptionsButton, 520, 8, 520);
        Anim.Rise(FinePrint, 600, 6, 520);
        InstallButton.Focus();
    }

    // ---- welcome options

    private void ToggleOptions()
    {
        _optionsOpen = !_optionsOpen;
        Anim.To(OptionsPanel, MaxHeightProperty, _optionsOpen ? 180 : 0, 380, 0, Anim.Out);
        Anim.To(OptionsPanel, OpacityProperty, _optionsOpen ? 1 : 0, 300, _optionsOpen ? 80 : 0, Anim.Out);
        Anim.To(HeroHost, HeightProperty, _optionsOpen ? 0 : 150, 380, 0, Anim.Out);
        Anim.To(HeroHost, OpacityProperty, _optionsOpen ? 0 : 1, 240, 0, Anim.Out);
        Anim.To(ChevronTurn, System.Windows.Media.RotateTransform.AngleProperty, _optionsOpen ? 180 : 0, 300, 0, Anim.Out);
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
        _engine = new InstallEngine(_root, _payload);
        _target = _shown = 0;
        Show(Installing);
        SetStatus("Preparing…");
        try
        {
            if (_existing is not null && _existing.Running().Any())
            {
                _wasRunning = true;
                SetStatus("Closing Nexus to update it…");
                await QuitRunningAsync(_existing);
            }
            var progress = new Progress<Progress>(p => { _target = p.Percent; SetStatus(p.Status); });
            if (_demoInstall || _demoError) await DemoAsync(progress);
            else await _engine.RunAsync(progress, CancellationToken.None);
            _target = 100;
            while (_shown < 99.5) await Task.Delay(30);
            ApplyOptions();
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

    private static async Task QuitRunningAsync(Existing existing)
    {
        var exe = Path.Combine(existing.Root, "current", "Nexus.exe");
        try { Process.Start(new ProcessStartInfo(exe, "--quit") { UseShellExecute = false })?.Dispose(); }
        catch (Exception) { }
        for (var i = 0; i < 40 && existing.Running().Any(); i++) await Task.Delay(200);
        // Velopack force-stops whatever is left during a silent update.
    }

    // Exercises every screen without touching the system (recordings, design).
    private async Task DemoAsync(IProgress<Progress> progress)
    {
        (double At, string Status)[] script =
        [
            (5, "Unpacking Nexus…"), (22, "Installing the Windows App runtime…"), (58, "Setting up the capture engine…"),
            (84, "Adding the nexus command line…"), (92, "Adding Nexus to Start…"), (96, "Registering nexus:// links…"), (99, "Finishing up…")
        ];
        var pct = 1.0;
        foreach (var (at, status) in script)
        {
            while (pct < at) { pct += 0.6; progress.Report(new Progress(pct, status)); await Task.Delay(40); }
            if (_demoError && at >= 58) throw new InstallFailedException("Couldn't write to the install folder. Another program may be using it.", "");
        }
    }

    private void ApplyOptions()
    {
        if (_engine is null || _demoInstall) return;
        try { if (DesktopShortcut.IsChecked == true) _engine.SetDesktopShortcut(true); } catch (Exception) { }
        try { if (StartAtLogin.IsChecked == true) _engine.SetStartAtLogin(true); } catch (Exception) { }
    }

    // Displayed progress glides toward the real value at 60 fps, so the
    // graph forms smoothly even when Velopack reports in bursts.
    private void CompositionTargetTick()
    {
        CompositionTarget.Rendering += (_, _) =>
        {
            if (_shown >= _target) return;
            _shown = Math.Min(_target, _shown + Math.Max(0.25, (_target - _shown) * 0.06));
            _graph.SetProgress(_shown);
            Percent.Text = $"{Math.Floor(_shown)}%";
        };
    }

    private void SetStatus(string text)
    {
        if (text == _status || text.Length == 0) return;
        _status = text;
        // Crossfade between two lines.
        var (inc, outg) = StatusA.Opacity > 0.5 ? (StatusB, StatusA) : (StatusA, StatusB);
        inc.Text = text;
        Anim.To(outg, OpacityProperty, 0, 220, 0, Anim.Out);
        Anim.Rise(inc, 60, 8, 360);
    }

    // ---- done / failed

    private void ShowDone()
    {
        GraphHost.Child = null;
        DoneGraphHost.Child = _graph;
        Show(Done);
        _graph.Bloom();
        DoneTitle.Text = _wasRunning ? "Nexus is updated" : _existing is not null ? "Nexus is up to date" : "You're all set";
        Anim.Rise(DoneTitle, 260, 10, 520);
        Anim.Rise(DoneSub, 340, 8, 520);
        Anim.Rise(OpenButton, 420, 10, 520);
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

    private void ShowFailed(Exception ex)
    {
        FailedText.Text = ex is InstallFailedException
            ? "Something stopped the install before it finished. Nothing was changed on your PC, so it's safe to try again."
            : "Something unexpected happened. Nothing was changed on your PC, so it's safe to try again.";
        FailedDetail.Text = ex.Message;
        LogButton.Visibility = ex is InstallFailedException f && f.LogPath.Length > 0 && File.Exists(f.LogPath) ? Visibility.Visible : Visibility.Collapsed;
        Show(Failed);
        RetryButton.Focus();
    }

    private async void OpenNexus()
    {
        if (_opened) return;
        _opened = true;
        // The button bows out instead of greying: the window is handing over.
        OpenButton.IsHitTestVisible = false;
        Anim.To(OpenButton, OpacityProperty, 0, 220, 0, Anim.Out, null, () => OpenButton.Visibility = Visibility.Hidden);
        DoneSub.Text = _wasRunning ? "Restarting Nexus…" : "Opening Nexus…";
        if (!_demoInstall && _engine is not null && File.Exists(_engine.MainExe))
        {
            // Upgrade: the old copy must be fully gone first, or the new one
            // would hand off to it (single instance) and the old version stays.
            for (var i = 0; i < 40 && InstallEngine.ProcessesUnder(_engine.Root).Any(); i++) await Task.Delay(150);
            try { Process.Start(new ProcessStartInfo(_engine.MainExe) { UseShellExecute = true, WorkingDirectory = Path.GetDirectoryName(_engine.MainExe) })?.Dispose(); }
            catch (Exception ex) { ShowFailed(ex); return; }
        }
        await Task.Delay(700);
        await CloseAsync();
    }

    // ---- screens

    private void Show(FrameworkElement screen)
    {
        foreach (var s in new FrameworkElement[] { Welcome, Installing, Done, Failed })
        {
            if (s == screen || s.Visibility != Visibility.Visible) continue;
            var leaving = s;
            Anim.To(leaving, OpacityProperty, 0, 240, 0, Anim.Out, null, () => leaving.Visibility = Visibility.Collapsed);
        }
        PrivacySheet.Visibility = Visibility.Collapsed;
        screen.Visibility = Visibility.Visible;
        Anim.Rise(screen, 140, 18, 520);
        FinePrint.Visibility = screen == Welcome ? Visibility.Visible : Visibility.Hidden;
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
