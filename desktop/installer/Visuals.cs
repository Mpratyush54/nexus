using System;
using System.Collections.Generic;
using System.Windows;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Media.Animation;
using System.Windows.Media.Imaging;
using System.Windows.Shapes;

namespace Nexus.Installer;

internal static class Brand
{
    public static readonly Color Ink = Color.FromRgb(0x0E, 0x0F, 0x0E);
    public static readonly Color Cream = Color.FromRgb(0xF2, 0xEF, 0xE6);
    public static readonly Color Gold = Color.FromRgb(0xE8, 0xBB, 0x67);
    public static readonly Color Muted = Color.FromRgb(0x9A, 0x97, 0x8E);
    public static readonly Color Indigo = Color.FromRgb(0x8E, 0xA2, 0xFF);
    public static SolidColorBrush Brush(Color c, byte a = 255) { var b = new SolidColorBrush(Color.FromArgb(a, c.R, c.G, c.B)); b.Freeze(); return b; }
    public static ImageSource Mark { get; } = new BitmapImage(new Uri("pack://application:,,,/Assets/Nexus.png"));
}

/// <summary>Animation helpers; all of them honour the Windows animation setting.</summary>
internal static class Anim
{
    public static bool Enabled => SystemParameters.ClientAreaAnimation && !SystemParameters.HighContrast;

    public static readonly IEasingFunction Out = new CubicEase { EasingMode = EasingMode.EaseOut };
    public static readonly IEasingFunction Back = new BackEase { EasingMode = EasingMode.EaseOut, Amplitude = 0.45 };
    public static readonly IEasingFunction InOut = new SineEase { EasingMode = EasingMode.EaseInOut };

    public static void To(Animatable target, DependencyProperty prop, double to, int ms, int delay = 0, IEasingFunction? ease = null, double? from = null, Action? done = null)
    {
        if (!Enabled)
        {
            target.BeginAnimation(prop, null);
            target.SetValue(prop, to);
            done?.Invoke();
            return;
        }
        var a = new DoubleAnimation(to, TimeSpan.FromMilliseconds(ms)) { BeginTime = TimeSpan.FromMilliseconds(delay), EasingFunction = ease ?? Out };
        if (from.HasValue) a.From = from.Value;
        if (done is not null) a.Completed += (_, _) => done();
        target.BeginAnimation(prop, a, HandoffBehavior.SnapshotAndReplace);
    }

    public static void To(UIElement target, DependencyProperty prop, double to, int ms, int delay = 0, IEasingFunction? ease = null, double? from = null, Action? done = null)
    {
        if (!Enabled)
        {
            target.BeginAnimation(prop, null);
            target.SetValue(prop, to);
            done?.Invoke();
            return;
        }
        var a = new DoubleAnimation(to, TimeSpan.FromMilliseconds(ms)) { BeginTime = TimeSpan.FromMilliseconds(delay), EasingFunction = ease ?? Out };
        if (from.HasValue) a.From = from.Value;
        if (done is not null) a.Completed += (_, _) => done();
        target.BeginAnimation(prop, a, HandoffBehavior.SnapshotAndReplace);
    }

    public static void Forever(Animatable target, DependencyProperty prop, double from, double to, int ms, int delay = 0)
    {
        if (!Enabled) return;
        target.BeginAnimation(prop, new DoubleAnimation(from, to, TimeSpan.FromMilliseconds(ms))
        {
            BeginTime = TimeSpan.FromMilliseconds(delay), AutoReverse = true, RepeatBehavior = RepeatBehavior.Forever, EasingFunction = InOut
        });
    }

    public static void Spin(RotateTransform rotate, int ms)
    {
        if (!Enabled) return;
        rotate.BeginAnimation(RotateTransform.AngleProperty, new DoubleAnimation(0, 360, TimeSpan.FromMilliseconds(ms)) { RepeatBehavior = RepeatBehavior.Forever });
    }

    /// <summary>Scale (centre) + translate transforms ready to animate.</summary>
    public static (ScaleTransform Scale, TranslateTransform Move) Rig(UIElement e)
    {
        var scale = new ScaleTransform(1, 1);
        var move = new TranslateTransform();
        e.RenderTransformOrigin = new Point(0.5, 0.5);
        e.RenderTransform = new TransformGroup { Children = { scale, move } };
        return (scale, move);
    }

    public static void Pop(UIElement e, int delay, double from = 0.3, int ms = 520)
    {
        var (scale, _) = Rig(e);
        e.Opacity = Enabled ? 0 : 1;
        To(scale, ScaleTransform.ScaleXProperty, 1, ms, delay, Back, from);
        To(scale, ScaleTransform.ScaleYProperty, 1, ms, delay, Back, from);
        To(e, UIElement.OpacityProperty, 1, ms * 2 / 3, delay, Out, 0);
    }

    public static void Rise(UIElement e, int delay, double dy = 14, int ms = 480, double dx = 0)
    {
        var (_, move) = Rig(e);
        e.Opacity = Enabled ? 0 : 1;
        To(move, TranslateTransform.YProperty, 0, ms, delay, Out, dy);
        To(move, TranslateTransform.XProperty, 0, ms, delay, Out, dx);
        To(e, UIElement.OpacityProperty, 1, ms, delay, Out, 0);
    }
}

/// <summary>Welcome: the Nexus mark assembling inside a ring of orbiting memory nodes.</summary>
internal sealed class HeroMark : Grid
{
    private readonly Image _mark = new() { Source = Brand.Mark, Width = 76, Height = 76 };
    private readonly Border _markBox = new();
    private readonly Canvas _orbit = new() { Width = 150, Height = 150 };
    private readonly Ellipse _glow;
    private readonly List<Ellipse> _dots = new();

    public HeroMark()
    {
        Width = 150; Height = 150;
        _glow = new Ellipse
        {
            Width = 120, Height = 120,
            Fill = new RadialGradientBrush(Color.FromArgb(110, Brand.Gold.R, Brand.Gold.G, Brand.Gold.B), Color.FromArgb(0, Brand.Gold.R, Brand.Gold.G, Brand.Gold.B))
        };
        Children.Add(_glow);
        var ring = new Ellipse { Width = 136, Height = 136, Stroke = Brand.Brush(Brand.Cream, 26), StrokeThickness = 1 };
        Canvas.SetLeft(ring, 7); Canvas.SetTop(ring, 7);
        _orbit.Children.Add(ring);
        for (var i = 0; i < 7; i++)
        {
            var a = i * Math.PI * 2 / 7 + 0.5;
            var size = i % 3 == 0 ? 9.0 : 6.0;
            var dot = new Ellipse { Width = size, Height = size, Fill = i % 3 == 0 ? Brand.Brush(Brand.Gold) : Brand.Brush(Brand.Cream, 200) };
            Canvas.SetLeft(dot, 75 + Math.Cos(a) * 68 - size / 2);
            Canvas.SetTop(dot, 75 + Math.Sin(a) * 68 - size / 2);
            _orbit.Children.Add(dot);
            _dots.Add(dot);
        }
        Children.Add(_orbit);
        _markBox.Child = _mark;
        Children.Add(_markBox);
        RenderOptions.SetBitmapScalingMode(_mark, BitmapScalingMode.HighQuality);
    }

    public void Play()
    {
        Anim.Pop(_markBox, 120, 0.5, 700);
        Anim.Pop(_glow, 260, 0.2, 1100);
        var rotate = new RotateTransform(0, 75, 75);
        _orbit.RenderTransform = rotate;
        Anim.Spin(rotate, 40000);
        var d = 380;
        foreach (var dot in _dots) { Anim.Pop(dot, d, 0, 460); d += 70; }
        // Resting state: the mark breathes, the glow pulses.
        var (breath, _) = Anim.Rig(_mark);
        Anim.Forever(breath, ScaleTransform.ScaleXProperty, 1, 1.045, 1600, 900);
        Anim.Forever(breath, ScaleTransform.ScaleYProperty, 1, 1.045, 1600, 900);
        if (_glow.Fill is Brush glowFill) Anim.Forever(glowFill, Brush.OpacityProperty, 1, 0.55, 1600, 900);
    }
}

/// <summary>
/// Install progress as a memory graph forming: each node appears (and links
/// to its nearest earlier neighbour) when real progress passes its share.
/// </summary>
internal sealed class MemoryGraph : Grid
{
    private const int Count = 28;
    private const double W = 340, H = 210;
    private readonly Canvas _edges = new() { Width = W, Height = H };
    private readonly Canvas _nodes = new() { Width = W, Height = H };
    private readonly Canvas _layer = new() { Width = W, Height = H };
    private readonly Image _mark = new() { Source = Brand.Mark, Width = 48, Height = 48 };
    private readonly Ellipse _glow;
    private readonly Ellipse _ring;
    private readonly Point[] _pos = new Point[Count];
    private readonly int[] _parent = new int[Count];
    private readonly bool[] _shown = new bool[Count];
    private readonly List<UIElement> _nodeViews = new();

    public MemoryGraph()
    {
        Width = W; Height = H;
        var centre = new Point(W / 2, H / 2);
        var golden = Math.PI * (3 - Math.Sqrt(5));
        for (var k = 0; k < Count; k++)
        {
            var r = 34 + 118 * Math.Sqrt((k + 1.0) / Count);
            var a = k * golden;
            _pos[k] = new Point(centre.X + Math.Cos(a) * r, centre.Y + Math.Sin(a) * r * 0.56);
            var best = -1; var bestD = double.MaxValue;
            for (var j = 0; j < k; j++)
            {
                var d = (_pos[j] - _pos[k]).LengthSquared;
                if (d < bestD) { bestD = d; best = j; }
            }
            _parent[k] = best;
        }
        _glow = new Ellipse
        {
            Width = 150, Height = 150, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center,
            Fill = new RadialGradientBrush(Color.FromArgb(90, Brand.Gold.R, Brand.Gold.G, Brand.Gold.B), Color.FromArgb(0, Brand.Gold.R, Brand.Gold.G, Brand.Gold.B))
        };
        _ring = new Ellipse
        {
            Width = 90, Height = 90, Stroke = Brand.Brush(Brand.Gold, 160), StrokeThickness = 1.5, Opacity = 0,
            HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center
        };
        Children.Add(_glow);
        _layer.Children.Add(_edges);
        _layer.Children.Add(_nodes);
        Children.Add(_layer);
        Children.Add(_ring);
        Children.Add(_mark);
        RenderOptions.SetBitmapScalingMode(_mark, BitmapScalingMode.HighQuality);
        Loaded += (_, _) =>
        {
            var (scale, _) = Anim.Rig(_mark);
            Anim.Forever(scale, ScaleTransform.ScaleXProperty, 1, 1.06, 1400);
            Anim.Forever(scale, ScaleTransform.ScaleYProperty, 1, 1.06, 1400);
            var rotate = new RotateTransform(0, W / 2, H / 2);
            _layer.RenderTransform = rotate;
            // Slow drift; the graph is flattened, so a gentle sway reads as depth.
            if (Anim.Enabled)
                rotate.BeginAnimation(RotateTransform.AngleProperty, new DoubleAnimation(-4, 4, TimeSpan.FromSeconds(6)) { AutoReverse = true, RepeatBehavior = RepeatBehavior.Forever, EasingFunction = Anim.InOut });
        };
    }

    public void SetProgress(double percent)
    {
        for (var k = 0; k < Count; k++)
        {
            if (_shown[k] || percent < (k + 1) * 98.0 / Count) continue;
            _shown[k] = true;
            Show(k);
        }
    }

    private void Show(int k)
    {
        var p = _pos[k];
        var from = _parent[k] >= 0 ? _pos[_parent[k]] : new Point(W / 2, H / 2);
        var edge = new Line { X1 = from.X, Y1 = from.Y, X2 = from.X, Y2 = from.Y, Stroke = Brand.Brush(Brand.Cream, 46), StrokeThickness = 1 };
        _edges.Children.Add(edge);
        Anim.To(edge, Line.X2Property, p.X, 420, 0, Anim.Out);
        Anim.To(edge, Line.Y2Property, p.Y, 420, 0, Anim.Out);
        if (k % 4 == 0)
        {
            var spoke = new Line { X1 = W / 2, Y1 = H / 2, X2 = p.X, Y2 = p.Y, Stroke = Brand.Brush(Brand.Gold, 40), StrokeThickness = 1 };
            _edges.Children.Insert(0, spoke);
            Anim.To(spoke, UIElement.OpacityProperty, 1, 600, 120, Anim.Out, 0);
        }
        var size = k % 5 == 0 ? 9.0 : k % 2 == 0 ? 6.5 : 5.0;
        var color = k % 5 == 0 ? Brand.Gold : k % 3 == 0 ? Brand.Indigo : Brand.Cream;
        var node = new Ellipse { Width = size, Height = size, Fill = Brand.Brush(color, k % 5 == 0 ? (byte)255 : (byte)215) };
        Canvas.SetLeft(node, p.X - size / 2);
        Canvas.SetTop(node, p.Y - size / 2);
        _nodes.Children.Add(node);
        _nodeViews.Add(node);
        Anim.Pop(node, 260, 0, 420);
    }

    /// <summary>Done: every node in, a gold ring blooms out and the graph settles.</summary>
    public void Bloom()
    {
        SetProgress(100);
        var (ringScale, _) = Anim.Rig(_ring);
        Anim.To(ringScale, ScaleTransform.ScaleXProperty, 3.2, 1100, 150, Anim.Out, 0.6);
        Anim.To(ringScale, ScaleTransform.ScaleYProperty, 3.2, 1100, 150, Anim.Out, 0.6);
        Anim.To(_ring, UIElement.OpacityProperty, 0, 1100, 150, Anim.Out, 0.9);
        var (glowScale, _) = Anim.Rig(_glow);
        Anim.To(glowScale, ScaleTransform.ScaleXProperty, 1.35, 900, 100, Anim.Out, 1);
        Anim.To(glowScale, ScaleTransform.ScaleYProperty, 1.35, 900, 100, Anim.Out, 1);
        var i = 0;
        foreach (var node in _nodeViews)
        {
            if (node.RenderTransform is TransformGroup g && g.Children[0] is ScaleTransform s)
            {
                Anim.To(s, ScaleTransform.ScaleXProperty, 1, 700, 120 + i * 18, Anim.Back, 1.8);
                Anim.To(s, ScaleTransform.ScaleYProperty, 1, 700, 120 + i * 18, Anim.Back, 1.8);
            }
            i++;
        }
    }
}
