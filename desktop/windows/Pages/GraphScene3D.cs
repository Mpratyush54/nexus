using System.Diagnostics;
using System.Numerics;
using Microsoft.UI;
using Microsoft.UI.Composition;
using Microsoft.UI.Input;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Hosting;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.Foundation;
using Windows.UI;

namespace Nexus.Pages;

/// <summary>Colours for the 3D scene (light or dark theme).</summary>
internal sealed record GraphScenePalette(
    Color Memory, Color Session, Color File, Color Background, Color Edge, Color Highlight,
    Color Select, Color Stale, Brush LabelBg, Brush LabelFg);

/// <summary>
/// The 3D memory graph, drawn with the Composition layer. Every node is a
/// SpriteVisual showing a pre-baked texture (a lit ball, rounded square or
/// diamond with a glow, NodeSprite), and every edge a thin rotated
/// SpriteVisual quad: moving, scaling and fading them is pure GPU work, with
/// no vector shapes to re-tessellate. Each frame the camera projects the
/// node positions (Camera3D), sorts them far to near (Graph3D), and writes
/// only the properties that changed; slot k always paints the k-th farthest
/// node, so the visual tree is never reordered. XAML is touched only for the
/// few labels (moved through Translation, never re-laid out).
/// The force layout (ForceLayout3D) ticks on the thread pool, two ticks per
/// frame while it settles. Interaction runs on CompositionTarget.Rendering;
/// an idle auto-rotate runs on a ~30 fps timer; with nothing moving the loop
/// stops entirely.
/// </summary>
internal sealed class GraphScene3D : Grid
{
    private const int Levels = 12;
    private const int SpriteSize = 128;
    private const double MaxRadiusPx = 40;
    private const double LabelHeight = 20;
    private const double LabelFont = 12;
    private const int TopLabels = 14;
    private const int MaxLabels = 48;
    private const double AutoRotateSpeed = 0.09; // rad/s
    private const double AutoRotateResume = 3.0; // s after the last interaction
    private const double FlySeconds = 0.75;

    /// <summary>Hovered node changed (−1 = none) with the pointer position.</summary>
    public event Action<int, Point>? HoverChanged;
    public event Action<Point>? PointerMovedOver;
    public event Action<int>? NodeClicked;
    public event Action<int>? NodeDoubleClicked;
    public event Action? BackgroundClicked;

    private readonly Canvas _surface = new() { Background = new SolidColorBrush(Colors.Transparent) };
    private readonly Canvas _labelLayer = new() { IsHitTestVisible = false };
    private readonly List<Label> _labelPool = [];
    private Compositor _compositor = null!;
    private ContainerVisual _root = null!, _edgeLayer = null!, _nodeLayer = null!;
    private SpriteVisual _backdrop = null!;
    private ShapeVisual _overlay = null!;
    private CompositionSpriteShape _selectRing = null!, _hoverRing = null!;
    private CompositionEllipseGeometry _selectRingGeo = null!, _hoverRingGeo = null!;
    private readonly CompositionSurfaceBrush?[] _spriteBrushes = new CompositionSurfaceBrush?[6];
    private readonly LoadedImageSurface?[] _spriteSurfaces = new LoadedImageSurface?[6];
    private readonly CompositionColorBrush?[] _edgeBrushes = new CompositionColorBrush?[Levels * 3];

    private sealed class Slot
    {
        public SpriteVisual Visual = null!;
        public bool Visible;
        public float X = -1e6f, Y = -1e6f, Size = -1, Opacity = -1;
        public int Brush = -1;
    }

    private sealed class Edge
    {
        public SpriteVisual Visual = null!;
        public bool Visible;
        public float X = -1e6f, Y = -1e6f, Length = -1, Angle = 1e6f, Thickness = -1;
        public int Brush = -1;
    }

    private sealed class Label
    {
        public Border Border = null!;
        public TextBlock Text = null!;
        public int Tag = -1;
        public bool Visible;
        public float X = -1e6f, Y = -1e6f, Scale = -1;
        public double Opacity = -1;
        public bool Bold;
    }

    private readonly List<Slot> _slots = [];
    private readonly List<Edge> _lines = [];
    private GraphScenePalette? _palette;

    // Graph data.
    private int _n;
    private int[] _edges = [];
    private int[] _kinds = [], _degree = [];
    private bool[] _stale = [];
    private string[] _labelText = [];
    private double[] _worldR = [], _labelRight = [];
    private int[][] _adj = [];       // neighbours, busiest first
    private int[] _byDegree = [];    // linked nodes, busiest first
    private bool[] _shown = [], _all = [];
    private double[] _pos = [];
    private SimRunner? _sim;
    private long _simSeen;

    // Per frame (reused; nothing is allocated per frame).
    private double[] _sx = [], _sy = [], _sr = [], _depth = [], _cue = [], _keys = [];
    private bool[] _drawn = [];
    private int[] _order = [];
    private int[] _labelSeen = [];
    private int _labelStamp;
    private readonly List<(double X, double Y, double W)> _taken = [];

    // State.
    private int _selected = -1, _hovered = -1;
    private bool[] _selectedSet = [], _hoverSet = [];
    private bool _focusDim = true;
    private readonly Camera3D _cam = new() { Pitch = 0.28, Yaw = 0.6 };
    private readonly Camera3D _flyFrom = new(), _flyTo = new(), _flyEff = new();
    private bool _flying;
    private int _flyNode = -1;
    private double _flyT;
    private bool _flyFit;
    private double _yawVel, _pitchVel, _panVelX, _panVelY;
    private double _idleSeconds;
    private bool _autoRotate;
    private bool _active;
    private bool _rendering;
    private bool _dirty = true;
    private bool _labelsDirty = true;
    private int _labelFrame;
    private int _labelCount;
    private int[] _labelNodes = [];
    private double _insetLeft, _insetTop, _insetRight, _insetBottom;
    private readonly Stopwatch _clock = Stopwatch.StartNew();
    private double _lastFrame;
    private Microsoft.UI.Dispatching.DispatcherQueueTimer? _resumeTimer, _spinTimer;

    // Pointer.
    private enum Drag { None, Pending, Orbit, Pan, Node }
    private Drag _drag;
    private uint _pointerId;
    private bool _panButton;
    private Point _pressAt, _lastAt;
    private Point? _pointerPos;
    private double _lastMoveTime;
    private int _pressNode = -1;
    private double _pressNodeDepth;
    private double _pressX, _pressY, _pressZ, _dragX, _dragY, _dragZ;
    private int _lastClickNode = -1;
    private double _lastClickTime;

    public GraphScene3D()
    {
        Background = new SolidColorBrush(Colors.Transparent);
        Children.Add(_surface);
        Children.Add(_labelLayer);
        BuildComposition();
        SizeChanged += (_, e) =>
        {
            // Children follow through RelativeSizeAdjustment; only the root gets a size.
            _root.Size = new Vector2((float)e.NewSize.Width, (float)e.NewSize.Height);
            _labelLayer.Clip = new RectangleGeometry { Rect = new Rect(0, 0, e.NewSize.Width, e.NewSize.Height) };
            Invalidate();
        };
        PointerPressed += OnPointerPressed;
        PointerMoved += OnPointerMoved;
        PointerReleased += OnPointerReleased;
        PointerCanceled += (_, _) => EndDrag();
        PointerCaptureLost += (_, _) => EndDrag();
        PointerExited += (_, _) =>
        {
            _pointerPos = null;
            if (_drag == Drag.None) SetHover(-1, default);
        };
        PointerWheelChanged += OnWheel;
        ManipulationDelta += OnManipulationDelta;
        Tapped += OnTapped;
        Loaded += (_, _) =>
        {
            if (_palette is not null && _spriteSurfaces[0] is null)
            {
                BakeTextures();
                Invalidate();
            }
        };
        Unloaded += (_, _) =>
        {
            StopAll();
            ReleaseTextures();
        };
    }

    public int NodeCount => _n;

    /// <summary>Slow turn when idle (off by default; ~30 fps when on).</summary>
    public bool AutoRotate
    {
        get => _autoRotate;
        set
        {
            _autoRotate = value;
            if (value) _idleSeconds = AutoRotateResume;
            else _spinTimer?.Stop();
            Kick();
        }
    }

    /// <summary>The page shows this scene: run the frame loop when needed.</summary>
    public bool Active
    {
        get => _active;
        set
        {
            if (_active == value) return;
            _active = value;
            if (value) Invalidate();
            else StopAll();
        }
    }

    /// <summary>
    /// Parts of the view covered by overlays (a floating details panel on the
    /// right, the legend at the bottom): fit and fly-to keep content out of them.
    /// </summary>
    public void SetInsets(double left, double top, double right, double bottom)
    {
        _insetLeft = left;
        _insetTop = top;
        _insetRight = right;
        _insetBottom = bottom;
    }

    // ---- setup ----

    private void BuildComposition()
    {
        var host = ElementCompositionPreview.GetElementVisual(_surface);
        _compositor = host.Compositor;
        _root = _compositor.CreateContainerVisual();
        _root.Clip = _compositor.CreateInsetClip();
        _backdrop = _compositor.CreateSpriteVisual();
        _edgeLayer = _compositor.CreateContainerVisual();
        _nodeLayer = _compositor.CreateContainerVisual();
        _overlay = _compositor.CreateShapeVisual();
        foreach (var v in new Visual[] { _backdrop, _edgeLayer, _nodeLayer, _overlay })
        {
            v.RelativeSizeAdjustment = Vector2.One;
            _root.Children.InsertAtTop(v);
        }
        ElementCompositionPreview.SetElementChildVisual(_surface, _root);

        _selectRingGeo = _compositor.CreateEllipseGeometry();
        _selectRing = _compositor.CreateSpriteShape(_selectRingGeo);
        _selectRing.StrokeThickness = 2.2f;
        _selectRing.Scale = Vector2.Zero;
        _hoverRingGeo = _compositor.CreateEllipseGeometry();
        _hoverRing = _compositor.CreateSpriteShape(_hoverRingGeo);
        _hoverRing.StrokeThickness = 1.5f;
        _hoverRing.Scale = Vector2.Zero;
        _overlay.Shapes.Add(_hoverRing);
        _overlay.Shapes.Add(_selectRing);
    }

    public void SetPalette(GraphScenePalette palette)
    {
        var sameColours = _palette is { } old && old.Memory == palette.Memory && old.Session == palette.Session &&
            old.File == palette.File && old.Stale == palette.Stale && old.Background == palette.Background &&
            old.Edge == palette.Edge && old.Highlight == palette.Highlight && old.Select == palette.Select;
        _palette = palette;
        foreach (var label in _labelPool)
        {
            label.Border.Background = palette.LabelBg;
            label.Text.Foreground = palette.LabelFg;
        }
        // Same theme again (a repeated theme check): keep every texture and brush.
        if (sameColours && _spriteSurfaces[0] is not null) return;
        BakeTextures();
        Array.Clear(_edgeBrushes);
        foreach (var l in _lines) l.Brush = -1;
        var dark = palette.Background.R < 128;
        _selectRing.StrokeBrush = _compositor.CreateColorBrush(palette.Select);
        _hoverRing.StrokeBrush = _compositor.CreateColorBrush(WithAlpha(palette.Highlight, 200));
        var glow = _compositor.CreateRadialGradientBrush();
        glow.ColorStops.Add(_compositor.CreateColorGradientStop(0f, WithAlpha(palette.Memory, (byte)(dark ? 30 : 18))));
        glow.ColorStops.Add(_compositor.CreateColorGradientStop(0.55f, WithAlpha(palette.Session, (byte)(dark ? 9 : 5))));
        glow.ColorStops.Add(_compositor.CreateColorGradientStop(1f, WithAlpha(palette.Background, 0)));
        glow.EllipseRadius = new Vector2(0.62f, 0.62f);
        _backdrop.Brush = glow;
        Invalidate();
    }

    /// <summary>
    /// Node textures: 3 kinds × (normal, stale), baked once per theme and
    /// shared by every node through six surface brushes.
    /// </summary>
    // This scene's share of UiStats (memory log), adjusted by deltas.
    private int _statNodes, _statTextures;

    private void ReportStats()
    {
        var nodes = _slots.Count;
        var textures = 0;
        for (var k = 0; k < 6; k++) if (_spriteSurfaces[k] is not null) textures++;
        Interlocked.Add(ref Nexus.Core.Diagnostics.UiStats.GraphNodes, nodes - _statNodes);
        Interlocked.Add(ref Nexus.Core.Diagnostics.UiStats.GraphTextures, textures - _statTextures);
        Interlocked.Add(ref Nexus.Core.Diagnostics.UiStats.GraphTextureBytes, (long)(textures - _statTextures) * SpriteSize * SpriteSize * 4);
        _statNodes = nodes;
        _statTextures = textures;
    }

    private void BakeTextures()
    {
        if (_palette is not { } palette) return;
        var dark = palette.Background.R < 128;
        for (var k = 0; k < 6; k++)
        {
            var kind = k % 3;
            var c = kind switch { GraphLayout.Session => palette.Session, GraphLayout.File => palette.File, _ => palette.Memory };
            var rgba = NodeSprite.Render(SpriteSize, kind, (c.R, c.G, c.B), dark, k >= 3 ? (palette.Stale.R, palette.Stale.G, palette.Stale.B) : null);
            var png = Png.Encode(SpriteSize, SpriteSize, rgba);
            var surface = LoadedImageSurface.StartLoadFromStream(new MemoryStream(png).AsRandomAccessStream());
            if (_spriteBrushes[k] is { } brush)
            {
                brush.Surface = surface;
            }
            else
            {
                brush = _compositor.CreateSurfaceBrush(surface);
                brush.Stretch = CompositionStretch.Fill;
                _spriteBrushes[k] = brush;
            }
            _spriteSurfaces[k]?.Dispose();
            _spriteSurfaces[k] = surface;
        }
        ReportStats();
    }

    /// <summary>Frees the textures while the scene is off screen (rebaked on return).</summary>
    private void ReleaseTextures()
    {
        for (var k = 0; k < 6; k++)
        {
            if (_spriteBrushes[k] is { } brush) brush.Surface = null;
            _spriteSurfaces[k]?.Dispose();
            _spriteSurfaces[k] = null;
        }
        ReportStats();
    }

    public void Clear()
    {
        _sim = null;
        _n = 0;
        _edges = [];
        // Release the old graph's visuals now, not whenever the GC gets to them.
        _nodeLayer.Children.RemoveAll();
        _edgeLayer.Children.RemoveAll();
        foreach (var s in _slots) s.Visual.Dispose();
        foreach (var l in _lines) l.Visual.Dispose();
        _slots.Clear();
        _lines.Clear();
        ReportStats();
        _pointerPos = null;
        _selected = _hovered = -1;
        _flying = false;
        _labelCount = 0;
        foreach (var label in _labelPool) HideLabel(label);
        _selectRing.Scale = Vector2.Zero;
        _hoverRing.Scale = Vector2.Zero;
        StopAll();
    }

    /// <summary>New graph: builds the pools and starts the layout settling.</summary>
    public void SetGraph(int n, int[] edges, int[] kinds, int[] degree, string[] ids, string[] labels, bool[] stale)
    {
        Clear();
        _n = n;
        _edges = edges;
        _kinds = kinds;
        _degree = degree;
        _stale = stale;
        _labelText = labels;
        var adj = GraphLayout.Adjacency(n, edges);
        _adj = new int[n][];
        for (var i = 0; i < n; i++) _adj[i] = adj[i].Distinct().OrderByDescending(j => degree[j]).ThenBy(j => j).ToArray();
        _byDegree = Enumerable.Range(0, n).Where(i => degree[i] > 0).OrderByDescending(i => degree[i]).ThenBy(i => i).ToArray();
        _worldR = new double[n];
        for (var i = 0; i < n; i++) _worldR[i] = Graph3D.NodeRadius(kinds[i], degree[i]);
        _labelRight = new double[n];
        _shown = Enumerable.Repeat(true, n).ToArray();
        _all = Enumerable.Repeat(true, n).ToArray();
        _selectedSet = new bool[n];
        _hoverSet = new bool[n];
        _pos = new double[n * 3];
        _sx = new double[n]; _sy = new double[n]; _sr = new double[n]; _depth = new double[n]; _cue = new double[n];
        _keys = new double[n];
        _order = new int[n];
        _drawn = new bool[n];
        _labelSeen = new int[n];
        _labelNodes = new int[MaxLabels];
        var sim = new SimRunner(new ForceLayout3D(n, edges, ids));
        _sim = sim;
        _simSeen = 0;
        sim.TryCopy(_pos, ref _simSeen);

        for (var k = 0; k < n; k++)
        {
            var v = _compositor.CreateSpriteVisual();
            v.IsVisible = false;
            _slots.Add(new Slot { Visual = v });
            _nodeLayer.Children.InsertAtTop(v);
        }
        for (var e = 0; e + 1 < edges.Length; e += 2)
        {
            var v = _compositor.CreateSpriteVisual();
            v.IsVisible = false;
            _lines.Add(new Edge { Visual = v });
            _edgeLayer.Children.InsertAtTop(v);
        }
        ReportStats();
        // Start a little close, so the graph unfolds into view; frame it once settled.
        _cam.Yaw = 0.6;
        _cam.Pitch = 0.28;
        _cam.TargetX = _cam.TargetY = _cam.TargetZ = 0;
        _cam.Distance = Math.Max(260, ForceLayout3D.LinkDistance * (4 + Math.Sqrt(n) * 0.9));
        _flyFit = true;
        _idleSeconds = 0;
        Invalidate();
    }

    public void SetShown(bool[] shown)
    {
        if (shown.Length != _n) return;
        Array.Copy(shown, _shown, _n);
        if (_selected >= 0 && !_shown[_selected]) SetSelection(-1, false);
        Invalidate();
    }

    public void SetSelection(int selected, bool focusDim)
    {
        _selected = selected >= 0 && selected < _n ? selected : -1;
        _focusDim = focusDim;
        MarkNeighbours(_selectedSet, _selected);
        Invalidate();
    }

    private void MarkNeighbours(bool[] set, int node)
    {
        Array.Clear(set);
        if (node < 0) return;
        set[node] = true;
        foreach (var j in _adj[node]) set[j] = true;
    }

    public void Invalidate()
    {
        _dirty = true;
        _labelsDirty = true;
        Kick();
    }

    // ---- camera commands ----

    /// <summary>Flies to a node and keeps it centred in the uncovered part of the view.</summary>
    public void FlyTo(int node)
    {
        if (node < 0 || node >= _n) return;
        _flyTo.CopyFrom(_cam);
        // Close enough to read, far enough that the node's neighbours (and
        // their labels) fit in the part of the view the panel doesn't cover.
        double reach = 0;
        foreach (var j in _adj[node])
        {
            if (!_shown[j]) continue;
            var dx = _pos[j * 3] - _pos[node * 3];
            var dy = _pos[j * 3 + 1] - _pos[node * 3 + 1];
            var dz = _pos[j * 3 + 2] - _pos[node * 3 + 2];
            reach = Math.Max(reach, Math.Sqrt(dx * dx + dy * dy + dz * dz) + _worldR[j]);
        }
        var w = Math.Max(1, ActualWidth);
        var h = Math.Max(1, ActualHeight);
        var (l, t, r, b) = Graph3D.FitArea(w, h, _insetLeft, _insetTop, _insetRight, _insetBottom, 24);
        var focal = _flyTo.Focal(h);
        var halfW = Math.Max(60, (r - l) / 2 - 90); // room for a label on the far side
        var halfH = Math.Max(40, (b - t) / 2 - 12);
        var needed = reach > 0 ? reach * focal / Math.Min(halfW, halfH) : 0;
        _flyTo.Distance = Math.Clamp(Math.Max(Math.Min(_cam.Distance, 340), needed), Camera3D.MinDistance * 4, Camera3D.MaxDistance);
        StartFly(node);
    }

    public void Fit(bool animate = true)
    {
        if (_n == 0) return;
        _flyTo.CopyFrom(_cam);
        FitInto(_flyTo);
        if (animate) StartFly(-1);
        else
        {
            _cam.CopyFrom(_flyTo);
            Invalidate();
        }
    }

    public void ResetView()
    {
        if (_n == 0) return;
        _flyTo.CopyFrom(_cam);
        _flyTo.Yaw = 0.6;
        _flyTo.Pitch = 0.28;
        FitInto(_flyTo);
        StartFly(-1);
    }

    /// <summary>Zooms toward the middle of the view (toolbar buttons).</summary>
    public void ZoomBy(double factor)
    {
        if (_n == 0) return;
        Touch();
        _flying = false;
        _flyFit = false;
        var (cx, cy) = VisibleCentre();
        _cam.ZoomAt(cx, cy, factor, Math.Max(1, ActualWidth), Math.Max(1, ActualHeight));
        Invalidate();
    }

    private (double X, double Y) VisibleCentre()
    {
        var w = Math.Max(1, ActualWidth);
        var h = Math.Max(1, ActualHeight);
        var l = _insetLeft;
        var r = w - _insetRight;
        var t = _insetTop;
        var b = h - _insetBottom;
        if (r - l < 80) { l = 0; r = w; }
        if (b - t < 80) { t = 0; b = h; }
        return ((l + r) / 2, (t + b) / 2);
    }

    /// <summary>Frames the shown nodes (labels included) in the uncovered part of the view.</summary>
    private void FitInto(Camera3D cam)
    {
        var any = false;
        for (var i = 0; i < _n && !any; i++) any = _shown[i];
        var set = any ? _shown : _all;
        // The busiest labels are what sticks out at the edges: reserve room for them.
        Array.Clear(_labelRight);
        var count = 0;
        foreach (var i in _byDegree)
        {
            if (!set[i]) continue;
            _labelRight[i] = 5 + GraphLayout.EstimateWidth(GraphLayout.Truncate(_labelText[i], 28), LabelFont) + 10;
            if (++count >= TopLabels) break;
        }
        Graph3D.FitToScreen(cam, _pos, set, _worldR, _labelRight, Math.Max(1, ActualWidth), Math.Max(1, ActualHeight),
            _insetLeft, _insetTop, _insetRight, _insetBottom);
    }

    private void StartFly(int node)
    {
        _flyFrom.CopyFrom(_cam);
        _flyNode = node;
        _flyT = 0;
        _flying = true;
        _flyFit = false; // an explicit move wins over the automatic fit after settling
        _yawVel = _pitchVel = _panVelX = _panVelY = 0;
        Kick();
    }

    // ---- frame loop ----

    /// <summary>Wants smooth frames (interaction, settling, flying): run on Rendering.</summary>
    private void Kick()
    {
        if (!_active || _n == 0) return;
        _spinTimer?.Stop();
        _resumeTimer?.Stop();
        if (_rendering) return;
        _rendering = true;
        _lastFrame = _clock.Elapsed.TotalSeconds;
        CompositionTarget.Rendering += OnRendering;
    }

    private void StopRendering()
    {
        if (!_rendering) return;
        _rendering = false;
        CompositionTarget.Rendering -= OnRendering;
    }

    private void StopAll()
    {
        StopRendering();
        _spinTimer?.Stop();
        _resumeTimer?.Stop();
    }

    private bool Interacting => _drag is Drag.Orbit or Drag.Pan or Drag.Node;

    private void OnRendering(object? sender, object e)
    {
        if (!_active || _n == 0 || ActualWidth < 2 || ActualHeight < 2)
        {
            StopAll();
            return;
        }
        var need = Frame();
        if (need == 2) return;
        StopRendering();
        if (need == 1) StartSpin();
        else if (_autoRotate) ScheduleResume(AutoRotateResume - _idleSeconds);
    }

    /// <summary>Idle auto-rotate: about 30 frames a second on a timer, not every vsync.</summary>
    private void StartSpin()
    {
        if (DispatcherQueue is null) return;
        if (_spinTimer is null)
        {
            _spinTimer = DispatcherQueue.CreateTimer();
            _spinTimer.IsRepeating = true;
            _spinTimer.Interval = TimeSpan.FromMilliseconds(33);
            _spinTimer.Tick += (_, _) =>
            {
                if (!_active || _n == 0 || ActualWidth < 2)
                {
                    _spinTimer!.Stop();
                    return;
                }
                var need = Frame();
                if (need == 2) Kick();
                else if (need == 0)
                {
                    _spinTimer!.Stop();
                    if (_autoRotate) ScheduleResume(AutoRotateResume - _idleSeconds);
                }
            };
        }
        _lastFrame = _clock.Elapsed.TotalSeconds;
        _spinTimer.Start();
    }

    private void ScheduleResume(double seconds)
    {
        if (DispatcherQueue is null) return;
        if (_resumeTimer is null)
        {
            _resumeTimer = DispatcherQueue.CreateTimer();
            _resumeTimer.IsRepeating = false;
            _resumeTimer.Tick += (_, _) =>
            {
                if (!_autoRotate || !_active || Interacting) return;
                _idleSeconds = Math.Max(_idleSeconds, AutoRotateResume);
                StartSpin();
            };
        }
        _resumeTimer.Stop();
        _resumeTimer.Interval = TimeSpan.FromSeconds(Math.Max(0.05, seconds));
        _resumeTimer.Start();
    }

    /// <summary>Marks an interaction: auto-rotate pauses and resumes AutoRotateResume s later.</summary>
    private void Touch() => _idleSeconds = 0;

    /// <summary>One frame. Returns 2 = wants smooth frames, 1 = only auto-rotating, 0 = idle.</summary>
    private int Frame()
    {
        var now = _clock.Elapsed.TotalSeconds;
        var dt = Math.Clamp(now - _lastFrame, 0, 0.1);
        _lastFrame = now;
        var moved = _dirty;
        _dirty = false;

        // Layout: pull the latest positions; ask for two more ticks.
        var simActive = false;
        if (_sim is { } sim)
        {
            if (sim.TryCopy(_pos, ref _simSeen)) moved = true;
            if (_drag == Drag.Node && _pressNode >= 0)
            {
                _pos[_pressNode * 3] = _dragX;
                _pos[_pressNode * 3 + 1] = _dragY;
                _pos[_pressNode * 3 + 2] = _dragZ;
            }
            simActive = sim.Active;
            if (simActive) sim.RequestTicks(2);
            else if (_flyFit && !_flying && !Interacting)
            {
                // The layout has cooled: frame it once.
                _flyFit = false;
                Fit(animate: true);
            }
        }

        // Camera: fly-to, inertia, auto-rotate.
        if (!Interacting) _idleSeconds += dt;
        var spinning = false;
        if (_flying)
        {
            _flyT = Math.Min(1, _flyT + dt / FlySeconds);
            _flyEff.CopyFrom(_flyTo);
            if (_flyNode >= 0)
            {
                // Follow the node while the layout moves it; centre it in the uncovered area.
                _flyEff.TargetX = _pos[_flyNode * 3];
                _flyEff.TargetY = _pos[_flyNode * 3 + 1];
                _flyEff.TargetZ = _pos[_flyNode * 3 + 2];
                var (cx, cy) = VisibleCentre();
                _flyEff.PlaceTargetAt(cx, cy, Math.Max(1, ActualWidth), Math.Max(1, ActualHeight));
            }
            Camera3D.LerpInto(_flyFrom, _flyEff, Camera3D.EaseInOut(_flyT), _cam);
            if (_flyT >= 1) _flying = false;
            moved = true;
        }
        else if (!Interacting)
        {
            var decay = Math.Exp(-dt * 4.5);
            if (Math.Abs(_yawVel) + Math.Abs(_pitchVel) > 0.002)
            {
                _cam.Orbit(_yawVel * dt, _pitchVel * dt);
                _yawVel *= decay;
                _pitchVel *= decay;
                moved = true;
            }
            else _yawVel = _pitchVel = 0;
            if (Math.Abs(_panVelX) + Math.Abs(_panVelY) > 2)
            {
                _cam.Pan(_panVelX * dt, _panVelY * dt, ActualHeight);
                _panVelX *= decay;
                _panVelY *= decay;
                moved = true;
            }
            else _panVelX = _panVelY = 0;
            if (_autoRotate && _idleSeconds >= AutoRotateResume && _yawVel == 0)
            {
                var ease = Math.Clamp(_idleSeconds - AutoRotateResume, 0, 1);
                _cam.Orbit(AutoRotateSpeed * ease * dt, 0);
                spinning = true;
                moved = true;
            }
        }

        if (moved)
        {
            Render();
            // Nodes move under a resting pointer (rotation, settling): keep hover true.
            if (_drag == Drag.None && _pointerPos is { } pp) SetHover(HitAt(pp), pp);
        }

        if (_flying || Interacting || _yawVel != 0 || _panVelX != 0 || _panVelY != 0 || simActive || _dirty) return 2;
        return spinning || (_autoRotate && _idleSeconds >= AutoRotateResume) ? 1 : 0;
    }

    // ---- drawing ----

    private void Render()
    {
        var w = ActualWidth;
        var h = ActualHeight;
        var view = new Camera3D.View(_cam, w, h);
        double near = double.MaxValue, far = double.MinValue;
        for (var i = 0; i < _n; i++)
        {
            if (!_shown[i]) { _drawn[i] = false; continue; }
            var p = view.Project(_pos[i * 3], _pos[i * 3 + 1], _pos[i * 3 + 2]);
            _drawn[i] = p.Visible && p.Depth > Camera3D.Near * 4;
            _sx[i] = p.X;
            _sy[i] = p.Y;
            _depth[i] = p.Depth;
            _sr[i] = p.Scale;
            if (!_drawn[i]) continue;
            near = Math.Min(near, p.Depth);
            far = Math.Max(far, p.Depth);
        }
        var count = Graph3D.DepthOrder(_depth, _drawn, _order, _keys);
        var anyFocus = _hovered >= 0 || (_selected >= 0 && _focusDim);
        for (var k = 0; k < count; k++)
        {
            var i = _order[k];
            var cue = Graph3D.DepthCue(_depth[i], near, far);
            _cue[i] = cue;
            var boost = 0.72 + 0.4 * cue;
            if (i == _hovered) boost *= 1.4;
            else if (i == _selected) boost *= 1.2;
            _sr[i] = Math.Clamp(_worldR[i] * _sr[i] * boost, 1.6, MaxRadiusPx);
        }

        // Nodes into slots, far to near.
        for (var k = 0; k < _slots.Count; k++)
        {
            var slot = _slots[k];
            if (k >= count)
            {
                if (slot.Visible) { slot.Visual.IsVisible = false; slot.Visible = false; }
                continue;
            }
            var i = _order[k];
            var (left, top, size) = Graph3D.SpriteRect(_sx[i], _sy[i], _sr[i]);
            float x = (float)left, y = (float)top, s = (float)size;
            if (Math.Abs(x - slot.X) > 0.35f || Math.Abs(y - slot.Y) > 0.35f)
            {
                slot.Visual.Offset = new Vector3(x, y, 0);
                slot.X = x;
                slot.Y = y;
            }
            if (Math.Abs(s - slot.Size) > 0.15f)
            {
                slot.Visual.Size = new Vector2(s, s);
                slot.Size = s;
            }
            var brush = _kinds[i] + (_stale[i] ? 3 : 0);
            if (slot.Brush != brush)
            {
                slot.Visual.Brush = _spriteBrushes[brush];
                slot.Brush = brush;
            }
            // Depth fog: farther nodes fade into the background; others dim around a focus.
            var cue = _cue[i];
            double opacity;
            if (!anyFocus) opacity = 0.38 + 0.62 * cue;
            else if (InFocus(i)) opacity = 1;
            else opacity = 0.1 + 0.1 * cue;
            var o = (float)(Math.Round(opacity * 32) / 32);
            if (o != slot.Opacity)
            {
                slot.Visual.Opacity = o;
                slot.Opacity = o;
            }
            if (!slot.Visible) { slot.Visual.IsVisible = true; slot.Visible = true; }
        }

        // Edges as rotated quads, fading with depth.
        for (var e = 0; e < _lines.Count; e++)
        {
            var line = _lines[e];
            int a = _edges[e * 2], b = _edges[e * 2 + 1];
            if (!_drawn[a] || !_drawn[b])
            {
                if (line.Visible) { line.Visual.IsVisible = false; line.Visible = false; }
                continue;
            }
            var cue = (_cue[a] + _cue[b]) / 2;
            var level = Math.Clamp((int)(cue * (Levels - 1) + 0.5), 0, Levels - 1);
            int state;
            if (!anyFocus) state = 0;
            else if ((_hovered >= 0 && (a == _hovered || b == _hovered)) || (_selected >= 0 && _focusDim && (a == _selected || b == _selected))) state = 2;
            else state = 1;
            var thickness = state == 2 ? 2.2f : (float)Math.Round(0.8 + 0.8 * level / (Levels - 1.0), 1);
            var (qx, qy, length, angle) = Graph3D.EdgeQuad(_sx[a], _sy[a], _sx[b], _sy[b], thickness);
            float x = (float)qx, y = (float)qy, len = (float)length, ang = (float)angle;
            if (thickness != line.Thickness)
            {
                line.Visual.CenterPoint = new Vector3(0, thickness / 2, 0);
                line.Thickness = thickness;
                line.Length = -1; // size must be rewritten with the new thickness
            }
            if (Math.Abs(x - line.X) > 0.35f || Math.Abs(y - line.Y) > 0.35f)
            {
                line.Visual.Offset = new Vector3(x, y, 0);
                line.X = x;
                line.Y = y;
            }
            if (Math.Abs(len - line.Length) > 0.3f)
            {
                line.Visual.Size = new Vector2(len, thickness);
                line.Length = len;
            }
            if (Math.Abs(ang - line.Angle) > 0.002f)
            {
                line.Visual.RotationAngle = ang;
                line.Angle = ang;
            }
            var key = state * Levels + level;
            if (line.Brush != key)
            {
                line.Visual.Brush = EdgeBrush(key);
                line.Brush = key;
            }
            if (!line.Visible) { line.Visual.IsVisible = true; line.Visible = true; }
        }

        PlaceRing(_selectRing, _selectRingGeo, _selected, 5);
        PlaceRing(_hoverRing, _hoverRingGeo, _hovered != _selected ? _hovered : -1, 3);
        RenderLabels();
    }

    private bool InFocus(int i) =>
        i == _hovered || i == _selected || (_hovered >= 0 && _hoverSet[i]) || (_focusDim && _selected >= 0 && _selectedSet[i]);

    private void PlaceRing(CompositionSpriteShape ring, CompositionEllipseGeometry geo, int node, double gap)
    {
        if (node < 0 || node >= _n || !_drawn[node])
        {
            if (ring.Scale.X != 0) ring.Scale = Vector2.Zero;
            return;
        }
        var r = (float)Math.Round(_sr[node] * (_kinds[node] == GraphLayout.Memory ? 1 : 1.12) + gap, 1);
        if (geo.Radius.X != r) geo.Radius = new Vector2(r, r);
        ring.Offset = new Vector2((float)_sx[node], (float)_sy[node]);
        if (ring.Scale.X != 1) ring.Scale = Vector2.One;
    }

    private CompositionColorBrush EdgeBrush(int key)
    {
        if (_edgeBrushes[key] is { } brush) return brush;
        var p = _palette!;
        var state = key / Levels;
        var cue = (key % Levels) / (double)(Levels - 1);
        Color c;
        if (state == 2) c = WithAlpha(p.Highlight, 235);
        else
        {
            var alpha = state == 1 ? 0.04 + 0.07 * cue : 0.1 + 0.48 * cue;
            c = WithAlpha(p.Edge, (byte)(alpha * 255));
        }
        brush = _compositor.CreateColorBrush(c);
        _edgeBrushes[key] = brush;
        return brush;
    }

    private static Color WithAlpha(Color c, byte a) => Color.FromArgb(a, c.R, c.G, c.B);

    // ---- labels ----

    private void RenderLabels()
    {
        if (_labelsDirty || ++_labelFrame >= 10)
        {
            _labelFrame = 0;
            _labelsDirty = false;
            PickLabels();
        }
        var used = 0;
        var anyFocus = _hovered >= 0 || (_selected >= 0 && _focusDim);
        for (var k = 0; k < _labelCount; k++)
        {
            var i = _labelNodes[k];
            if (!_drawn[i]) continue;
            var label = LabelAt(used++);
            var forced = i == _hovered || i == _selected;
            var cue = _cue[i];
            var scale = (float)(forced ? 1.0 : Math.Round(Math.Clamp(0.78 + 0.3 * cue, 0.78, 1.08) * 20) / 20);
            // Right of the node, or left of it when it would run under the panel or off the edge.
            var width = label.Border.ActualWidth > 0 ? label.Border.ActualWidth : EstimateLabelWidth(i, forced);
            var (minX, maxX) = LabelBounds();
            var x = (float)Math.Round(Graph3D.LabelX(_sx[i], _sr[i], width * scale, minX, maxX));
            var y = (float)Math.Round(_sy[i] - LabelHeight / 2 * scale);
            if (x != label.X || y != label.Y)
            {
                label.Border.Translation = new Vector3(x, y, 0);
                label.X = x;
                label.Y = y;
            }
            if (scale != label.Scale)
            {
                label.Border.Scale = new Vector3(scale, scale, 1);
                label.Scale = scale;
            }
            var opacity = forced ? 1 : anyFocus && !InFocus(i) ? 0.3 : Math.Round((0.45 + 0.55 * cue) * 16) / 16;
            if (opacity != label.Opacity)
            {
                label.Border.Opacity = opacity;
                label.Opacity = opacity;
            }
            var tag = i * 2 + (forced ? 1 : 0);
            if (label.Tag != tag)
            {
                label.Tag = tag;
                label.Text.Text = GraphLayout.Truncate(_labelText[i], forced ? 44 : 28);
            }
            var bold = i == _selected;
            if (bold != label.Bold)
            {
                label.Text.FontWeight = bold ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
                label.Bold = bold;
            }
            if (!label.Visible)
            {
                label.Border.Visibility = Visibility.Visible;
                label.Visible = true;
            }
        }
        for (var k = used; k < _labelPool.Count; k++) HideLabel(_labelPool[k]);
    }

    private static void HideLabel(Label label)
    {
        if (!label.Visible) return;
        label.Border.Visibility = Visibility.Collapsed;
        label.Visible = false;
    }

    /// <summary>
    /// Forced labels (hovered, selected and their neighbours) first, then the
    /// busiest nodes in view; a label is skipped when it would overlap an
    /// earlier one, so the picture stays clean. Allocation-free.
    /// </summary>
    private void PickLabels()
    {
        _labelCount = 0;
        _taken.Clear();
        if (++_labelStamp == int.MaxValue)
        {
            Array.Clear(_labelSeen);
            _labelStamp = 1;
        }
        TryLabel(_hovered, true);
        TryLabel(_selected, true);
        if (_hovered >= 0)
        {
            var c = 0;
            foreach (var j in _adj[_hovered]) if (_shown[j] && TryLabel(j, false) && ++c >= 10) break;
        }
        if (_focusDim && _selected >= 0)
        {
            var c = 0;
            foreach (var j in _adj[_selected]) if (_shown[j] && TryLabel(j, false) && ++c >= 16) break;
        }
        var top = 0;
        foreach (var i in _byDegree)
        {
            if (top >= TopLabels || _labelCount >= MaxLabels) break;
            if (_cue[i] <= 0.3) continue;
            if (TryLabel(i, false)) top++;
        }
    }

    private bool TryLabel(int i, bool force)
    {
        if (i < 0 || _labelCount >= MaxLabels || !_drawn[i]) return false;
        if (_labelSeen[i] == _labelStamp) return false;
        var h = ActualHeight;
        var (minX, maxX) = LabelBounds();
        // Nodes under the details panel (or off screen) get no label unless forced.
        if (_sx[i] < minX - 20 || _sy[i] < -10 || _sy[i] > h + 10) return false;
        if (_sx[i] > maxX && !force) return false;
        _labelSeen[i] = _labelStamp;
        var lw = EstimateLabelWidth(i, force);
        var x = Graph3D.LabelX(_sx[i], _sr[i], lw, minX, maxX);
        var y = _sy[i] - LabelHeight / 2;
        if (!force)
        {
            foreach (var t in _taken)
            {
                if (x < t.X + t.W && t.X < x + lw && y < t.Y + LabelHeight && t.Y < y + LabelHeight) return false;
            }
        }
        _taken.Add((x, y, lw));
        _labelNodes[_labelCount++] = i;
        return true;
    }

    private double EstimateLabelWidth(int i, bool forced) =>
        Math.Min(320, Math.Min(_labelText[i].Length, forced ? 44 : 28) * LabelFont * 0.56 + 20);

    /// <summary>Where labels may go: inside the view and left of a floating panel.</summary>
    private (double Min, double Max) LabelBounds()
    {
        var w = ActualWidth;
        var max = w - _insetRight - 6;
        if (max < w * 0.35) max = w - 6;
        return (Math.Max(4, _insetLeft + 4), max);
    }

    private Label LabelAt(int index)
    {
        if (index < _labelPool.Count) return _labelPool[index];
        var text = new TextBlock
        {
            FontSize = LabelFont,
            Foreground = _palette?.LabelFg,
            TextTrimming = TextTrimming.CharacterEllipsis,
            TextWrapping = TextWrapping.NoWrap,
            VerticalAlignment = VerticalAlignment.Center
        };
        var border = new Border
        {
            Background = _palette?.LabelBg,
            CornerRadius = new CornerRadius(4),
            Padding = new Thickness(5, 1, 5, 2),
            Height = LabelHeight,
            MaxWidth = 320,
            Visibility = Visibility.Collapsed,
            Child = text
        };
        var label = new Label { Border = border, Text = text };
        _labelPool.Add(label);
        _labelLayer.Children.Add(border);
        return label;
    }

    // ---- input ----

    private int HitAt(Point p) => _n == 0 ? -1 : Graph3D.HitTest(_sx, _sy, _sr, _depth, _drawn, p.X, p.Y, 3);

    private void SetHover(int node, Point at)
    {
        if (node == _hovered) return;
        _hovered = node;
        if (_n > 0) MarkNeighbours(_hoverSet, node);
        ProtectedCursor = InputSystemCursor.Create(node >= 0 ? InputSystemCursorShape.Hand : InputSystemCursorShape.Arrow);
        HoverChanged?.Invoke(node, at);
        // Hover restyles the scene but is not an interaction: auto-rotate keeps going.
        _dirty = true;
        _labelsDirty = true;
        if (!_rendering && !(_spinTimer?.IsRunning ?? false)) Kick();
    }

    private void OnPointerPressed(object sender, PointerRoutedEventArgs e)
    {
        if (_n == 0) return;
        var touch = e.Pointer.PointerDeviceType == PointerDeviceType.Touch;
        ManipulationMode = touch ? ManipulationModes.TranslateX | ManipulationModes.TranslateY | ManipulationModes.Scale : ManipulationModes.None;
        Touch();
        _flying = false;
        _flyFit = false;
        _yawVel = _pitchVel = _panVelX = _panVelY = 0;
        if (touch) return;
        var point = e.GetCurrentPoint(this);
        var props = point.Properties;
        var shift = (e.KeyModifiers & Windows.System.VirtualKeyModifiers.Shift) != 0;
        if (!props.IsLeftButtonPressed && !props.IsRightButtonPressed && !props.IsMiddleButtonPressed) return;
        _panButton = props.IsRightButtonPressed || props.IsMiddleButtonPressed || shift;
        _drag = Drag.Pending;
        _pointerId = e.Pointer.PointerId;
        _pressAt = _lastAt = point.Position;
        _lastMoveTime = _clock.Elapsed.TotalSeconds;
        _pressNode = _panButton ? -1 : HitAt(point.Position);
        if (_pressNode >= 0)
        {
            _pressNodeDepth = _depth[_pressNode];
            _pressX = _dragX = _pos[_pressNode * 3];
            _pressY = _dragY = _pos[_pressNode * 3 + 1];
            _pressZ = _dragZ = _pos[_pressNode * 3 + 2];
        }
        CapturePointer(e.Pointer);
        e.Handled = true;
        Kick();
    }

    private void OnPointerMoved(object sender, PointerRoutedEventArgs e)
    {
        if (_n == 0) return;
        var p = e.GetCurrentPoint(this).Position;
        if (_drag == Drag.None)
        {
            _pointerPos = p;
            SetHover(HitAt(p), p);
            PointerMovedOver?.Invoke(p);
            return;
        }
        if (e.Pointer.PointerId != _pointerId) return;
        if (_drag == Drag.Pending)
        {
            if (Math.Abs(p.X - _pressAt.X) < 4 && Math.Abs(p.Y - _pressAt.Y) < 4) return;
            _drag = _panButton ? Drag.Pan : _pressNode >= 0 ? Drag.Node : Drag.Orbit;
            if (_drag == Drag.Node)
            {
                _sim?.Post(s => { s.AlphaTarget = 0.25; s.Reheat(0.25); });
            }
            else
            {
                SetHover(-1, p);
            }
        }
        var now = _clock.Elapsed.TotalSeconds;
        var dt = Math.Max(1e-3, now - _lastMoveTime);
        var dx = p.X - _lastAt.X;
        var dy = p.Y - _lastAt.Y;
        _lastAt = p;
        _lastMoveTime = now;
        Touch();
        switch (_drag)
        {
            case Drag.Orbit:
            {
                const double k = 0.0065;
                _cam.Orbit(-dx * k, dy * k);
                // Smoothed angular velocity for momentum after release.
                _yawVel = _yawVel * 0.6 + (-dx * k / dt) * 0.4;
                _pitchVel = _pitchVel * 0.6 + (dy * k / dt) * 0.4;
                break;
            }
            case Drag.Pan:
                _cam.Pan(dx, dy, ActualHeight);
                _panVelX = _panVelX * 0.6 + dx / dt * 0.4;
                _panVelY = _panVelY * 0.6 + dy / dt * 0.4;
                break;
            case Drag.Node:
            {
                var (wx, wy, wz) = _cam.ScreenDeltaToWorld(p.X - _pressAt.X, p.Y - _pressAt.Y, _pressNodeDepth, ActualHeight);
                _dragX = _pressX + wx;
                _dragY = _pressY + wy;
                _dragZ = _pressZ + wz;
                var node = _pressNode;
                double x = _dragX, y = _dragY, z = _dragZ;
                _sim?.Post(s => s.Pin(node, x, y, z));
                _pos[node * 3] = x;
                _pos[node * 3 + 1] = y;
                _pos[node * 3 + 2] = z;
                break;
            }
        }
        _dirty = true;
        Kick();
        e.Handled = true;
    }

    private void OnPointerReleased(object sender, PointerRoutedEventArgs e)
    {
        if (_drag == Drag.None || e.Pointer.PointerId != _pointerId) return;
        var p = e.GetCurrentPoint(this).Position;
        var was = _drag;
        var node = _pressNode;
        // No momentum if the pointer rested before letting go.
        if (_clock.Elapsed.TotalSeconds - _lastMoveTime > 0.08) _yawVel = _pitchVel = _panVelX = _panVelY = 0;
        EndDrag();
        ReleasePointerCapture(e.Pointer);
        if (was == Drag.Pending && !_panButton) Click(node);
        else if (was == Drag.Node) SetHover(HitAt(p), p);
        e.Handled = true;
    }

    private void Click(int node)
    {
        var now = _clock.Elapsed.TotalSeconds;
        if (node >= 0 && node == _lastClickNode && now - _lastClickTime < 0.45)
        {
            _lastClickNode = -1;
            NodeDoubleClicked?.Invoke(node);
            return;
        }
        _lastClickNode = node;
        _lastClickTime = now;
        if (node >= 0) NodeClicked?.Invoke(node);
        else BackgroundClicked?.Invoke();
    }

    private void EndDrag()
    {
        if (_drag == Drag.Node && _pressNode >= 0)
        {
            var node = _pressNode;
            _sim?.Post(s => { s.Unpin(node); s.AlphaTarget = 0; });
        }
        if (_drag != Drag.Orbit) _yawVel = _pitchVel = 0;
        if (_drag != Drag.Pan) _panVelX = _panVelY = 0;
        _drag = Drag.None;
        _pressNode = -1;
        Touch();
        Kick();
    }

    private void OnTapped(object sender, TappedRoutedEventArgs e)
    {
        if (e.PointerDeviceType != PointerDeviceType.Touch) return;
        Click(HitAt(e.GetPosition(this)));
    }

    private void OnWheel(object sender, PointerRoutedEventArgs e)
    {
        if (_n == 0) return;
        var point = e.GetCurrentPoint(this);
        var delta = point.Properties.MouseWheelDelta;
        if (delta == 0) return;
        e.Handled = true;
        Touch();
        _flying = false;
        _flyFit = false;
        if (point.Properties.IsHorizontalMouseWheel) _cam.Orbit(-delta * 0.002, 0);
        else _cam.ZoomAt(point.Position.X, point.Position.Y, Math.Pow(1.0018, delta), ActualWidth, ActualHeight);
        Invalidate();
    }

    private void OnManipulationDelta(object sender, ManipulationDeltaRoutedEventArgs e)
    {
        if (e.PointerDeviceType != PointerDeviceType.Touch) return;
        e.Handled = true;
        Touch();
        _flying = false;
        _cam.Orbit(-e.Delta.Translation.X * 0.006, e.Delta.Translation.Y * 0.006);
        if (Math.Abs(e.Delta.Scale - 1) > 0.0001) _cam.ZoomAt(e.Position.X, e.Position.Y, e.Delta.Scale, ActualWidth, ActualHeight);
        Invalidate();
    }

    /// <summary>
    /// Runs the force layout on the thread pool. The UI posts commands (pin,
    /// reheat) and asks for ticks each frame; finished positions are copied
    /// into a snapshot the UI reads under a short lock. Once the layout has
    /// cooled no more work is queued.
    /// </summary>
    private sealed class SimRunner
    {
        private readonly ForceLayout3D _sim;
        private readonly double[] _snapshot;
        private readonly object _lock = new();
        private readonly List<Action<ForceLayout3D>> _pending = [];
        private readonly List<Action<ForceLayout3D>> _running = [];
        private readonly WaitCallback _work;
        private long _version = 1;
        private int _busy;
        private int _ticks;
        private volatile bool _active = true;

        public SimRunner(ForceLayout3D sim)
        {
            _sim = sim;
            _snapshot = (double[])sim.Positions.Clone();
            _work = _ => Run();
        }

        public bool Active => _active;

        public void Post(Action<ForceLayout3D> command)
        {
            lock (_lock)
            {
                _pending.Add(command);
                _active = true;
            }
        }

        public void RequestTicks(int ticks)
        {
            if (Interlocked.CompareExchange(ref _busy, 1, 0) != 0) return;
            _ticks = ticks;
            ThreadPool.UnsafeQueueUserWorkItem(_work, null);
        }

        private void Run()
        {
            try
            {
                lock (_lock)
                {
                    _running.AddRange(_pending);
                    _pending.Clear();
                }
                foreach (var c in _running) c(_sim);
                _running.Clear();
                for (var t = 0; t < _ticks && _sim.Active; t++) _sim.Tick();
                lock (_lock)
                {
                    Array.Copy(_sim.Positions, _snapshot, _snapshot.Length);
                    _version++;
                    _active = _sim.Active || _pending.Count > 0;
                }
            }
            catch (Exception)
            {
                _active = false;
            }
            finally
            {
                Volatile.Write(ref _busy, 0);
            }
        }

        public bool TryCopy(double[] into, ref long seen)
        {
            lock (_lock)
            {
                if (_version == seen) return false;
                Array.Copy(_snapshot, into, Math.Min(into.Length, _snapshot.Length));
                seen = _version;
                return true;
            }
        }
    }
}
