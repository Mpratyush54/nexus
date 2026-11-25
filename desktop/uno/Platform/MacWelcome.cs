using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Automation.Peers;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Microsoft.UI.Xaml.Media.Imaging;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Pages;
using Color = Windows.UI.Color;

namespace Nexus.Platform;

/// <summary>
/// The macOS counterpart of the Windows installer's last screen. A DMG has no
/// installer window, so the first launch from Applications opens on the same
/// ink, cream and gold "You're all set": the memory graph forms node by node
/// around the Nexus mark, a gold ring blooms, and "Get started" leads into
/// the setup guide (already open underneath). When the app runs from the
/// disk image or Downloads, the same screen offers "Move to Applications"
/// (see <see cref="MacInstall"/>) instead.
///
/// Animations use the setup guide's Motion helpers, so they follow the
/// system's reduce-motion setting the same way.
/// </summary>
internal sealed class MacWelcome : Grid
{
    private static readonly Color Ink = Color.FromArgb(255, 0x0E, 0x0F, 0x0E);
    private static readonly Color Cream = Color.FromArgb(255, 0xF2, 0xEF, 0xE6);
    private static readonly Color Gold = Color.FromArgb(255, 0xE8, 0xBB, 0x67);
    private static readonly Color GoldWarm = Color.FromArgb(255, 0xF5, 0xD7, 0x9A);
    private static readonly Color Muted = Color.FromArgb(255, 0x9A, 0x97, 0x8E);
    private static readonly Color Faint = Color.FromArgb(255, 0x6E, 0x6C, 0x66);
    private static readonly Color Indigo = Color.FromArgb(255, 0x8E, 0xA2, 0xFF);

    private enum Mode { Welcome, Move }

    private readonly MainWindow _window;
    private readonly Mode _mode;
    private readonly TextBlock _title;
    private readonly TextBlock _sub;
    private readonly Button _primary;
    private readonly Button _secondary;
    private bool _busy;
    // The page underneath (setup guide or shell) while this screen covers it:
    // collapsed so VoiceOver and the keyboard reach only this screen.
    private readonly List<UIElement> _covered = new();
    private Grid? _root;
    private Microsoft.UI.Xaml.Input.KeyEventHandler? _keys;

    /// <summary>
    /// MainWindow's constructor, after the setup guide (if any) is open:
    /// shows the move offer or the first-run welcome when either applies.
    /// </summary>
    public static void MaybeShow(MainWindow window)
    {
        if (!OperatingSystem.IsMacOS() || window.Content is not Grid root) return;
        Mode? mode = null;
        if (MacInstall.ShouldOfferMove && !AppPrefs.Data.MacMoveDeclined) mode = Mode.Move;
        else if (!AppPrefs.SetupCompleted && !AppPrefs.Data.MacWelcomeSeen) mode = Mode.Welcome;
        if (mode is null) return;
        MacShell.Log($"first-run screen: {mode}");
        // One continuous look, like the DMG and the Windows installer: the
        // setup guide underneath stays on the same dark canvas until setup
        // ends, whatever the system theme (nothing is saved).
        if (!AppPrefs.SetupCompleted) ThemeService.Hold(window, ThemeService.Dark);
        var screen = new MacWelcome(window, mode.Value);
        Grid.SetRowSpan(screen, 2);
        screen.Cover(root);
        root.Children.Add(screen);
    }

    // VoiceOver saw only the setup guide underneath (the welcome was not in
    // the Accessibility tree) and Return went to whatever the guide focused.
    // Hide what's underneath until this screen goes, and take Return/Space
    // (and Esc for "Not now") from anywhere in the window.
    private void Cover(Grid root)
    {
        _root = root;
        // Only once the page underneath has finished its own entrance: on Uno
        // an animation that runs while its element is collapsed stays on its
        // first frame (the guide's sign-in card came back invisible).
        var later = DispatcherQueue.CreateTimer();
        later.Interval = TimeSpan.FromMilliseconds(2500);
        later.IsRepeating = false;
        later.Tick += (_, _) =>
        {
            // Over the setup guide only (the welcome); the move offer can sit
            // over the shell, whose lists animate in as they load.
            if (_root is null || _mode != Mode.Welcome) { FocusPrimary(); return; }
            foreach (var child in _root.Children)
            {
                if (ReferenceEquals(child, this) || child.Visibility != Visibility.Visible) continue;
                _covered.Add(child);
                child.Visibility = Visibility.Collapsed;
            }
            FocusPrimary();
        };
        later.Start();
        _keys = (_, e) =>
        {
            if (e.Key is Windows.System.VirtualKey.Enter or Windows.System.VirtualKey.Space)
            {
                e.Handled = true;
                if (_primary.IsEnabled) _ = PrimaryAsync();
            }
            else if (e.Key == Windows.System.VirtualKey.Escape && _mode == Mode.Move && _secondary.IsEnabled)
            {
                e.Handled = true;
                Decline();
            }
        };
        root.AddHandler(KeyDownEvent, _keys, true);
    }

    // Keyboard focus on the main button, so Return and Space work at once.
    // At launch the window may not be key yet when Loaded runs, so this is
    // repeated when the window activates and once the page underneath hides.
    private void FocusPrimary()
    {
        if (_root is not null && _primary.IsEnabled) _primary.Focus(FocusState.Keyboard);
    }

    private void Uncover()
    {
        if (_root is null) return;
        if (_keys is not null) _root.RemoveHandler(KeyDownEvent, _keys);
        foreach (var child in _covered)
        {
            child.Visibility = Visibility.Visible;
            // Its step was built while hidden; build it again so its
            // entrance plays where it can be seen.
            if (child is Nexus.Pages.SetupWizardPage guide) guide.ThemeChanged();
        }
        _covered.Clear();
        _root = null;
    }

    private void Decline()
    {
        AppPrefs.Data.MacMoveDeclined = true;
        AppPrefs.Save();
        MacShell.Log("move to applications declined");
        _ = DismissAsync();
    }

    private MacWelcome(MainWindow window, Mode mode)
    {
        _window = window;
        _mode = mode;
        Background = new SolidColorBrush(Ink);
        RequestedTheme = ElementTheme.Dark;
        AutomationProperties.SetName(this, mode == Mode.Move ? "Move Nexus to Applications" : "Welcome to Nexus");

        // Ambient light, as in the installer: warm top-left, cool bottom-right.
        Children.Add(Ambient(Gold, 0x30, 620, 520, HorizontalAlignment.Left, VerticalAlignment.Top, new Thickness(-240, -260, 0, 0)));
        Children.Add(Ambient(Indigo, 0x22, 680, 560, HorizontalAlignment.Right, VerticalAlignment.Bottom, new Thickness(0, 0, -300, -320)));

        var column = new StackPanel { HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center, Margin = new Thickness(24, -10, 24, 0) };
        var graph = new BloomGraph();
        column.Children.Add(graph);

        var welcome = mode == Mode.Welcome;
        _title = new TextBlock
        {
            Text = welcome ? "You're all set" : "Move Nexus to Applications",
            FontSize = 30, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            Foreground = new SolidColorBrush(Cream), HorizontalAlignment = HorizontalAlignment.Center,
            TextAlignment = TextAlignment.Center, Margin = new Thickness(0, 10, 0, 0)
        };
        _sub = new TextBlock
        {
            Text = welcome
                ? "Nexus is in your Applications folder and keeps itself up to date."
                : MacInstall.IsOnDiskImage
                    ? "Nexus is running from the disk image. Move it to Applications to keep it and get updates."
                    : "Nexus is running from your Downloads. Move it to Applications to keep it and get updates.",
            FontSize = 14, Foreground = new SolidColorBrush(Muted), HorizontalAlignment = HorizontalAlignment.Center,
            TextAlignment = TextAlignment.Center, TextWrapping = TextWrapping.Wrap, MaxWidth = 460, Margin = new Thickness(0, 6, 0, 0)
        };
        _primary = PrimaryButton(welcome ? "Get started" : "Move to Applications");
        _primary.Margin = new Thickness(0, 22, 0, 0);
        _primary.Click += async (_, _) => await PrimaryAsync();
        _secondary = LinkButton(welcome ? "" : "Not now");
        _secondary.Visibility = welcome ? Visibility.Collapsed : Visibility.Visible;
        _secondary.Margin = new Thickness(0, 10, 0, 0);
        _secondary.Click += (_, _) => Decline();
        column.Children.Add(_title);
        column.Children.Add(_sub);
        column.Children.Add(_primary);
        column.Children.Add(_secondary);
        Children.Add(column);

        var version = UpdateService.Shared.CurrentVersion;
        if (version.Length == 0) version = typeof(MacWelcome).Assembly.GetName().Version?.ToString(3) ?? "";
        var footer = new TextBlock
        {
            Text = $"Version {version}  ·  For you only, no admin needed",
            FontSize = 11, Foreground = new SolidColorBrush(Faint),
            HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Bottom, Margin = new Thickness(0, 0, 0, 16)
        };
        Children.Add(footer);

        Loaded += (_, _) =>
        {
            graph.Play();
            Motion.FadeSlideIn(_title, 900, 0, 10, 520);
            Motion.FadeSlideIn(_sub, 1000, 0, 8, 520);
            Motion.FadeSlideIn(_primary, 1120, 0, 8, 520);
            if (!welcome) Motion.FadeSlideIn(_secondary, 1200, 0, 6, 520);
            Motion.FadeSlideIn(footer, 1300, 0, 0, 600);
            FocusPrimary();
        };
        window.Activated += (_, _) => DispatcherQueue.TryEnqueue(FocusPrimary);
    }

    private async Task PrimaryAsync()
    {
        if (_busy) return;
        if (_mode == Mode.Welcome)
        {
            AppPrefs.Data.MacWelcomeSeen = true;
            AppPrefs.Save();
            await DismissAsync();
            return;
        }
        _busy = true;
        _primary.IsEnabled = false;
        _secondary.IsEnabled = false;
        _primary.Content = "Moving…";
        var error = await MacInstall.MoveToApplicationsAsync();
        if (error.Length == 0)
        {
            _title.Text = "Moved to Applications";
            _sub.Text = "Opening Nexus from Applications…";
            await Task.Delay(700);
            // The app's own quit (core shutdown, bounded exit); the new copy
            // opens once this process is gone.
            _window.TrayQuitCommand.Execute(null);
            return;
        }
        _title.Text = "Nexus couldn't be moved";
        _sub.Text = error + " You can also drag Nexus to Applications in Finder.";
        _primary.Content = "Try again";
        _primary.IsEnabled = true;
        _secondary.IsEnabled = true;
        _busy = false;
    }

    private async Task DismissAsync()
    {
        // What was underneath comes back first, so the fade shows it.
        Uncover();
        // Bounded: the fade's completion event is a Composition batch, and the
        // screen must go away even if it never reports back.
        await Task.WhenAny(Motion.FadeOutAsync(this, 320), Task.Delay(700));
        if (Parent is Panel panel) panel.Children.Remove(this);
        MacShell.Log("first-run screen dismissed");
    }

    private static Ellipse Ambient(Color color, byte alpha, double w, double h, HorizontalAlignment ha, VerticalAlignment va, Thickness margin)
    {
        var brush = new RadialGradientBrush();
        brush.GradientStops.Add(new GradientStop { Color = Color.FromArgb(alpha, color.R, color.G, color.B), Offset = 0 });
        brush.GradientStops.Add(new GradientStop { Color = Color.FromArgb(0, color.R, color.G, color.B), Offset = 1 });
        var e = new Ellipse { Width = w, Height = h, Fill = brush, HorizontalAlignment = ha, VerticalAlignment = va, Margin = margin, IsHitTestVisible = false };
        AutomationProperties.SetAccessibilityView(e, AccessibilityView.Raw);
        return e;
    }

    // The installer's primary action: cream on ink with a gold edge, warming to gold on hover.
    private static Button PrimaryButton(string text)
    {
        var button = new Button
        {
            Content = text, FontSize = 15, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            Height = 46, MinWidth = 240, Padding = new Thickness(28, 0, 28, 0), CornerRadius = new CornerRadius(11),
            BorderThickness = new Thickness(1.5), HorizontalAlignment = HorizontalAlignment.Center
        };
        var res = button.Resources;
        res["ButtonBackground"] = new SolidColorBrush(Cream);
        res["ButtonBackgroundPointerOver"] = new SolidColorBrush(GoldWarm);
        res["ButtonBackgroundPressed"] = new SolidColorBrush(Gold);
        res["ButtonBackgroundDisabled"] = new SolidColorBrush(Color.FromArgb(0x66, Cream.R, Cream.G, Cream.B));
        res["ButtonForeground"] = new SolidColorBrush(Ink);
        res["ButtonForegroundPointerOver"] = new SolidColorBrush(Ink);
        res["ButtonForegroundPressed"] = new SolidColorBrush(Ink);
        res["ButtonForegroundDisabled"] = new SolidColorBrush(Ink);
        res["ButtonBorderBrush"] = new SolidColorBrush(Gold);
        res["ButtonBorderBrushPointerOver"] = new SolidColorBrush(Gold);
        res["ButtonBorderBrushPressed"] = new SolidColorBrush(Gold);
        res["ButtonBorderBrushDisabled"] = new SolidColorBrush(Color.FromArgb(0x55, Gold.R, Gold.G, Gold.B));
        button.Background = new SolidColorBrush(Cream);
        button.Foreground = new SolidColorBrush(Ink);
        button.BorderBrush = new SolidColorBrush(Gold);
        AutomationProperties.SetName(button, text);
        return button;
    }

    private static Button LinkButton(string text)
    {
        var button = new Button
        {
            Content = text, FontSize = 12.5, Padding = new Thickness(8, 3, 8, 3), HorizontalAlignment = HorizontalAlignment.Center,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0), Foreground = new SolidColorBrush(Muted)
        };
        var res = button.Resources;
        res["ButtonBackground"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        res["ButtonBackgroundPointerOver"] = new SolidColorBrush(Color.FromArgb(0x14, 0xFF, 0xFF, 0xFF));
        res["ButtonBackgroundPressed"] = new SolidColorBrush(Color.FromArgb(0x0A, 0xFF, 0xFF, 0xFF));
        res["ButtonForeground"] = new SolidColorBrush(Muted);
        res["ButtonForegroundPointerOver"] = new SolidColorBrush(Cream);
        res["ButtonForegroundPressed"] = new SolidColorBrush(Cream);
        res["ButtonBorderBrush"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        res["ButtonBorderBrushPointerOver"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        if (text.Length > 0) AutomationProperties.SetName(button, text);
        return button;
    }

    /// <summary>
    /// The installer's memory graph (desktop/installer/Visuals.cs, MemoryGraph):
    /// nodes on a flattened golden-angle spiral, each linked to its nearest
    /// earlier neighbour, forming around the mark; then the bloom.
    /// </summary>
    private sealed class BloomGraph : Grid
    {
        private const int Count = 28;
        private const double W = 360, H = 220;
        private readonly Canvas _layer = new() { Width = W, Height = H };
        private readonly List<(UIElement Edge, UIElement Node, int Delay)> _parts = new();
        private readonly Ellipse _glow;
        private readonly Ellipse _ring;
        private readonly Border _markBox;
        private readonly Image _mark;

        public BloomGraph()
        {
            Width = W;
            Height = H;
            AutomationProperties.SetAccessibilityView(this, AccessibilityView.Raw);
            var cx = W / 2;
            var cy = H / 2;
            var golden = Math.PI * (3 - Math.Sqrt(5));
            var pos = new (double X, double Y)[Count];
            var edges = new Canvas { Width = W, Height = H };
            var nodes = new Canvas { Width = W, Height = H };
            for (var k = 0; k < Count; k++)
            {
                var r = 36 + 124 * Math.Sqrt((k + 1.0) / Count);
                var a = k * golden;
                pos[k] = (cx + Math.Cos(a) * r, cy + Math.Sin(a) * r * 0.56);
                var parent = -1;
                var best = double.MaxValue;
                for (var j = 0; j < k; j++)
                {
                    var d = Math.Pow(pos[j].X - pos[k].X, 2) + Math.Pow(pos[j].Y - pos[k].Y, 2);
                    if (d < best) { best = d; parent = j; }
                }
                var from = parent >= 0 ? pos[parent] : (cx, cy);
                var edge = new Line { X1 = from.Item1, Y1 = from.Item2, X2 = pos[k].X, Y2 = pos[k].Y, Stroke = new SolidColorBrush(Color.FromArgb(46, Cream.R, Cream.G, Cream.B)), StrokeThickness = 1 };
                edges.Children.Add(edge);
                if (k % 4 == 0)
                {
                    var spoke = new Line { X1 = cx, Y1 = cy, X2 = pos[k].X, Y2 = pos[k].Y, Stroke = new SolidColorBrush(Color.FromArgb(40, Gold.R, Gold.G, Gold.B)), StrokeThickness = 1 };
                    edges.Children.Insert(0, spoke);
                    _parts.Add((spoke, spoke, 0));
                }
                var size = k % 5 == 0 ? 9.0 : k % 2 == 0 ? 6.5 : 5.0;
                var color = k % 5 == 0 ? Gold : k % 3 == 0 ? Indigo : Cream;
                var node = new Ellipse { Width = size, Height = size, Fill = new SolidColorBrush(Color.FromArgb(k % 5 == 0 ? (byte)255 : (byte)215, color.R, color.G, color.B)) };
                Canvas.SetLeft(node, pos[k].X - size / 2);
                Canvas.SetTop(node, pos[k].Y - size / 2);
                nodes.Children.Add(node);
                _parts.Add((edge, node, 180 + k * 38));
            }
            // Spokes appear with the node they reach.
            for (var i = 0; i < _parts.Count; i++)
            {
                if (_parts[i].Edge == _parts[i].Node && i + 1 < _parts.Count) _parts[i] = (_parts[i].Edge, _parts[i].Node, _parts[i + 1].Delay + 120);
            }

            var glowBrush = new RadialGradientBrush();
            glowBrush.GradientStops.Add(new GradientStop { Color = Color.FromArgb(96, Gold.R, Gold.G, Gold.B), Offset = 0 });
            glowBrush.GradientStops.Add(new GradientStop { Color = Color.FromArgb(0, Gold.R, Gold.G, Gold.B), Offset = 1 });
            _glow = new Ellipse { Width = 170, Height = 170, Fill = glowBrush, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center };
            _ring = new Ellipse
            {
                Width = 90, Height = 90, Stroke = new SolidColorBrush(Color.FromArgb(160, Gold.R, Gold.G, Gold.B)), StrokeThickness = 1.5, Opacity = 0,
                HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center,
                RenderTransformOrigin = new Windows.Foundation.Point(0.5, 0.5), RenderTransform = new ScaleTransform()
            };
            _layer.Children.Add(edges);
            _layer.Children.Add(nodes);
            _mark = new Image { Source = new BitmapImage(new Uri("ms-appx:///Assets/Nexus.png")), Width = 60, Height = 60 };
            _markBox = new Border { Child = _mark, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center };
            Children.Add(_glow);
            Children.Add(_layer);
            Children.Add(_ring);
            Children.Add(_markBox);
        }

        public void Play()
        {
            Motion.PopIn(_markBox, 60, 0.5f, 700);
            Motion.PopIn(_glow, 160, 0.3f, 1100);
            foreach (var (edge, node, delay) in _parts)
            {
                if (edge != node) Motion.FadeSlideIn(edge, delay, 0, 0, 420);
                Motion.PopIn(node, delay + (edge == node ? 0 : 140), edge == node ? 1f : 0f, 420);
            }
            Motion.Spin(_layer, 120000);
            Motion.Breathe(_mark, 0.045f, 3200, 1600);
            Bloom(180 + Count * 38 + 160);
        }

        // Every node in: a gold ring blooms out of the mark and fades.
        private void Bloom(int delayMs)
        {
            if (!Motion.Enabled || _ring.RenderTransform is not ScaleTransform scale) return;
            var story = new Storyboard();
            void Add(DependencyObject target, string property, double from, double to, int ms)
            {
                var a = new DoubleAnimation
                {
                    From = from, To = to, Duration = TimeSpan.FromMilliseconds(ms), BeginTime = TimeSpan.FromMilliseconds(delayMs),
                    EasingFunction = new CubicEase { EasingMode = EasingMode.EaseOut }, EnableDependentAnimation = true
                };
                Storyboard.SetTarget(a, target);
                Storyboard.SetTargetProperty(a, property);
                story.Children.Add(a);
            }
            Add(scale, "ScaleX", 0.6, 3.4, 1200);
            Add(scale, "ScaleY", 0.6, 3.4, 1200);
            Add(_ring, "Opacity", 0.9, 0, 1200);
            story.Begin();
        }
    }
}
