using System.Globalization;
using System.Text.Json;
using Microsoft.UI;
using Microsoft.UI.Input;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Shapes;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;
using Windows.Foundation;
using Windows.UI;
using XPath = Microsoft.UI.Xaml.Shapes.Path;

namespace Nexus.Pages;

/// <summary>
/// The project's memory graph: memories, the chats they came from and the
/// files they touch. By default a live 3D force graph (GraphScene3D, drawn
/// with Composition); the 3D toggle switches to the flat 2D map below,
/// drawn natively on a Canvas. The 2D layout (GraphLayout) runs
/// once off the UI thread: each linked cluster on its own, clusters packed
/// in rows, unlinked nodes in a grid below. Nodes are sized by links and
/// shaped by kind; edges are two Paths (normal, highlighted); labels live in
/// screen space so they stay readable at any zoom, and only the busiest,
/// the selection's neighbours and the hovered node get one.
/// Embedded in MemoryPage behind its "Graph" toggle.
/// </summary>
public sealed class GraphPage : NexusPage
{
    private const int NodeLimit = 1000;
    private const double MinScale = 0.1;
    private const double MaxScale = 4;
    private const double LabelFont = 12;
    private const double LabelHeight = 20;

    private sealed class GNode
    {
        public int Index;
        public string Id = "", Kind = "", Ref = "", Label = "", Short = "", Sub = "", UpdatedAt = "";
        public int KindCode;
        public bool Stale, Pinned, Visible = true;
        public double X, Y, HomeX, HomeY, R;
        public int Degree;
        public Shape? Shape;
        public double Size;
        public readonly List<(GNode Node, string Relation)> Links = [];
    }

    private sealed class GEdge
    {
        public GNode From = null!, To = null!;
        public string Relation = "";
    }

    private sealed class SearchItem(GNode node)
    {
        public GNode Node { get; } = node;
        public override string ToString() => $"{GraphLayout.Truncate(Node.Label, 60)}  ·  {KindName(Node.Kind)}";
    }

    /// <summary>Raised for "Open memory": the memory id and key.</summary>
    public event Action<string, string>? OpenMemoryRequested;

    private readonly Grid _host = new() { Background = new SolidColorBrush(Colors.Transparent) };
    private readonly Canvas _canvas = new();
    private readonly XPath _edgePath = new() { IsHitTestVisible = false };
    private readonly XPath _hiEdgePath = new() { IsHitTestVisible = false };
    private readonly Canvas _nodeLayer = new();
    private readonly Canvas _labelLayer = new();
    private readonly List<Border> _labelPool = [];
    private readonly CompositeTransform _transform = new();
    private readonly RectangleGeometry _clip = new();
    private readonly Border _hover;
    private readonly TextBlock _hoverTitle = new() { FontSize = 13, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap, MaxLines = 3 };
    private readonly TextBlock _hoverSub = new() { FontSize = 12, Opacity = 0.75, TextWrapping = TextWrapping.Wrap, MaxLines = 4 };
    private readonly StackPanel _side = new() { Spacing = 8 };
    private readonly Border _sidePane;
    private readonly Grid _split;
    private readonly Border _legend;
    private readonly StackPanel _empty = new() { Spacing = 8, HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center, MaxWidth = 420 };
    private readonly TextBlock _status = new() { FontSize = 12, Opacity = 0.7, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
    private readonly AutoSuggestBox _search;
    private readonly ComboBox _view;
    private readonly ToggleButton _showMemories;
    private readonly ToggleButton _showSessions;
    private readonly ToggleButton _showFiles;
    private readonly ToggleButton _showUnlinked;
    private readonly ToggleButton _staleOnly;
    private readonly Dictionary<int, Shape> _chipIcons = [];
    private readonly Grid _stageGrid = new();
    private TextBlock _legendHint = new();
    private readonly GraphScene3D _scene = new();
    private readonly ToggleButton _mode3D;
    private readonly ToggleButton _spin;
    private bool _is3D = true;
    private bool _layout2DReady;
    private bool _drawn2D;
    private bool _loaded;

    private Brush _memBrush = null!, _chatBrush = null!, _fileBrush = null!, _edgeBrush = null!, _hiBrush = null!;
    private Brush _haloBrush = null!, _labelBg = null!, _labelFg = null!, _selectBrush = null!, _staleBrush = null!, _hoverBg = null!;

    private List<GNode> _nodes = [];
    private List<GEdge> _edges = [];
    private int[] _edgeIndex = [];
    private int[] _degrees = [];
    private GNode? _selected;
    private GNode? _hovered;
    private HashSet<GNode> _selectedNeighbours = [];
    private string _loadedProject = "";
    private int _loadVersion;
    private CancellationTokenSource? _layoutCts;
    private bool _panning;
    private Point _panStart;
    private double _panX, _panY;
    private bool _truncated;
    private bool _labelsQueued;
    private bool _narrow;
    private GNode? _focusCenter;
    private int _focusHops;

    public GraphPage()
    {
        BuildBrushes();
        _canvas.Children.Add(_edgePath);
        _canvas.Children.Add(_hiEdgePath);
        _canvas.Children.Add(_nodeLayer);
        _canvas.RenderTransform = _transform;
        _host.Children.Add(_canvas);
        _host.Children.Add(_labelLayer);
        _host.Clip = _clip;
        _host.SizeChanged += (_, e) =>
        {
            _clip.Rect = new Rect(0, 0, e.NewSize.Width, e.NewSize.Height);
            QueueLabels();
        };
        _host.PointerPressed += Host_PointerPressed;
        _host.PointerMoved += Host_PointerMoved;
        _host.PointerReleased += Host_PointerReleased;
        _host.PointerCaptureLost += (_, _) => _panning = false;
        _host.PointerWheelChanged += Host_PointerWheelChanged;
        _host.ManipulationMode = ManipulationModes.TranslateX | ManipulationModes.TranslateY | ManipulationModes.Scale;
        _host.ManipulationDelta += Host_ManipulationDelta;

        _hover = new Border
        {
            Padding = new Thickness(10, 7, 10, 8),
            CornerRadius = new CornerRadius(8),
            BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Hairline,
            Background = _hoverBg,
            MaxWidth = 340,
            HorizontalAlignment = HorizontalAlignment.Left,
            VerticalAlignment = VerticalAlignment.Top,
            IsHitTestVisible = false,
            Visibility = Visibility.Collapsed,
            Child = new StackPanel { Spacing = 3, Children = { _hoverTitle, _hoverSub } }
        };
        _legend = new Border
        {
            Padding = new Thickness(10, 6, 10, 7),
            Margin = new Thickness(10),
            CornerRadius = new CornerRadius(8),
            BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Hairline,
            Background = _hoverBg,
            HorizontalAlignment = HorizontalAlignment.Left,
            VerticalAlignment = VerticalAlignment.Bottom,
            IsHitTestVisible = false,
            Visibility = Visibility.Collapsed
        };
        BuildLegend();
        // The 2D canvas and the 3D scene share one stage; the legend, hover
        // card and empty state float over whichever is showing.
        _host.Visibility = Visibility.Collapsed;
        _stageGrid.Children.Add(_host);
        _stageGrid.Children.Add(_scene);
        _stageGrid.Children.Add(_legend);
        _stageGrid.Children.Add(_hover);
        _stageGrid.Children.Add(_empty);
        _scene.HoverChanged += (i, p) =>
        {
            if (i >= 0 && i < _nodes.Count)
            {
                _hovered = _nodes[i];
                ShowHoverCardAt(_nodes[i], p);
            }
            else
            {
                _hovered = null;
                _hover.Visibility = Visibility.Collapsed;
            }
        };
        _scene.PointerMovedOver += MoveHoverTo;
        _scene.NodeClicked += i =>
        {
            if (i < 0 || i >= _nodes.Count) return;
            Select(_nodes[i]);
            if (_focusHops == 0) _scene.FlyTo(i);
        };
        _scene.NodeDoubleClicked += i =>
        {
            if (i < 0 || i >= _nodes.Count || _nodes[i].Degree == 0) return;
            if (!ReferenceEquals(_selected, _nodes[i])) Select(_nodes[i]);
            // _view is built later in the constructor, before any click can arrive.
            if (_view!.SelectedIndex == 0) _view.SelectedIndex = 1;
            else ApplyView(fit: true);
        };
        _scene.BackgroundClicked += () =>
        {
            if (_selected is not null && _focusHops == 0) Select(null);
        };
        RegisterPropertyChangedCallback(VisibilityProperty, (_, _) => UpdateSceneActive());

        _search = new AutoSuggestBox
        {
            PlaceholderText = "Find in graph",
            QueryIcon = new SymbolIcon(Symbol.Find),
            Width = 240,
            VerticalAlignment = VerticalAlignment.Center
        };
        _search.TextChanged += Search_TextChanged;
        _search.QuerySubmitted += Search_QuerySubmitted;
        ToolTipService.SetToolTip(_search, "Find a memory, chat or file and focus it (Ctrl+F)");

        _view = new ComboBox { MinHeight = 30, FontSize = 12, VerticalAlignment = VerticalAlignment.Center };
        _view.Items.Add("Whole graph");
        _view.Items.Add("Focus: 1 hop");
        _view.Items.Add("Focus: 2 hops");
        _view.SelectedIndex = 0;
        _view.SelectionChanged += (_, _) =>
        {
            _focusHops = Math.Max(0, _view.SelectedIndex);
            ApplyView(fit: true);
            if (_selected is not null) RenderSide(_selected);
        };
        ToolTipService.SetToolTip(_view, "Focus shows only the selected item and what it links to");

        _showMemories = FilterChip("Memories", GraphLayout.Memory, true);
        _showSessions = FilterChip("Chats", GraphLayout.Session, true);
        _showFiles = FilterChip("Files", GraphLayout.File, true);
        _showUnlinked = FilterChip("Unlinked", -1, false);
        ToolTipService.SetToolTip(_showUnlinked, "Show items with no links (drawn in a grid under the clusters)");
        _staleOnly = FilterChip("Stale only", -2, false);

        var zoomOut = ToolButton("", "Zoom out (wheel)", () => ZoomBy(1 / 1.3));
        var zoomIn = ToolButton("", "Zoom in (wheel)", () => ZoomBy(1.3));
        var fit = ToolButton("", "Fit everything in view (Ctrl+0)", FitView);
        var reset = ToolButton("\uE72C", "Reset view", ResetView);
        _mode3D = new ToggleButton
        {
            Content = "3D",
            IsChecked = true,
            MinHeight = 30,
            MinWidth = 40,
            FontSize = 12,
            Padding = new Thickness(8, 4, 8, 4)
        };
        ToolTipService.SetToolTip(_mode3D, "3D view; turn off for the flat 2D map");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_mode3D, "3D view");
        _mode3D.Click += (_, _) => _ = SetModeAsync(_mode3D.IsChecked == true);
        _spin = new ToggleButton
        {
            Content = new FontIcon { Glyph = "\uE7AD", FontSize = 13 },
            IsChecked = false,
            MinHeight = 30,
            MinWidth = 34,
            Padding = new Thickness(8, 4, 8, 4)
        };
        ToolTipService.SetToolTip(_spin, "Auto-rotate: slowly turn the graph when idle");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_spin, "Auto-rotate");
        _spin.Click += (_, _) => _scene.AutoRotate = _spin.IsChecked == true;
        var refresh = ToolButton("", "Reload the graph", () => _ = LoadAsync(force: true));

        var top = new Grid { ColumnSpacing = 8 };
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var tools = Row(_mode3D, _spin, zoomOut, zoomIn, fit, reset, refresh);
        tools.Spacing = 4;
        Grid.SetColumn(_search, 0);
        Grid.SetColumn(_view, 1);
        Grid.SetColumn(tools, 3);
        top.Children.Add(_search);
        top.Children.Add(_view);
        top.Children.Add(tools);

        var filters = new Grid { ColumnSpacing = 8 };
        filters.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        filters.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var chips = Row(_showMemories, _showSessions, _showFiles, _showUnlinked, _staleOnly);
        chips.Spacing = 6;
        Grid.SetColumn(chips, 0);
        Grid.SetColumn(_status, 1);
        _status.Margin = new Thickness(6, 0, 0, 0);
        filters.Children.Add(chips);
        filters.Children.Add(_status);

        var stage = new Border
        {
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Hairline,
            Background = new SolidColorBrush(Color.FromArgb(10, 128, 128, 128)),
            Child = _stageGrid
        };
        _sidePane = new Border
        {
            Padding = new Thickness(16, 14, 16, 14),
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Hairline,
            Background = NexusPalette.Surface,
            Child = new ScrollViewer { Content = _side, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled }
        };

        _split = new Grid { ColumnSpacing = 12 };
        _split.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        _split.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(300) });
        Grid.SetColumn(stage, 0);
        Grid.SetColumn(_sidePane, 1);
        _split.Children.Add(stage);
        _split.Children.Add(_sidePane);
        _split.SizeChanged += (_, e) =>
        {
            _narrow = e.NewSize.Width < 720;
            UpdateSidePlacement();
        };

        var root = new Grid { RowSpacing = 8 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(top, 0);
        Grid.SetRow(filters, 1);
        Grid.SetRow(_split, 2);
        root.Children.Add(top);
        root.Children.Add(filters);
        root.Children.Add(_split);
        Content = root;

        KeyDown += Page_KeyDown;
        ActualThemeChanged += (_, _) => ApplyTheme();

        RenderSideHelp();
        Loaded += (_, _) =>
        {
            _loaded = true;
            ShellState.Changed += OnShellChanged;
            if (_builtDark != IsDark) ApplyTheme();
            UpdateSceneActive();
        };
        Unloaded += (_, _) =>
        {
            _loaded = false;
            ShellState.Changed -= OnShellChanged;
            _layoutCts?.Cancel();
            UpdateSceneActive();
        };
    }

    /// <summary>The 3D frame loop runs only while the scene is on screen.</summary>
    private void UpdateSceneActive() =>
        _scene.Active = _loaded && _is3D && Visibility == Visibility.Visible && _nodes.Count > 0;

    /// <summary>Switches between the 3D scene and the flat 2D map.</summary>
    private async Task SetModeAsync(bool threeD)
    {
        if (_mode3D.IsChecked != threeD) _mode3D.IsChecked = threeD;
        if (_is3D == threeD) return;
        _is3D = threeD;
        _spin.IsEnabled = threeD;
        _hover.Visibility = Visibility.Collapsed;
        _hovered = null;
        _scene.Visibility = threeD ? Visibility.Visible : Visibility.Collapsed;
        _host.Visibility = threeD ? Visibility.Collapsed : Visibility.Visible;
        UpdateSceneActive();
        if (_selected is not null) RenderSide(_selected); else RenderSideHelp();
        if (_nodes.Count == 0) return;
        if (threeD)
        {
            foreach (var label in _labelPool) label.Visibility = Visibility.Collapsed;
            // Free the 2D map's XAML shapes while it is hidden; it is redrawn on return.
            _nodeLayer.Children.Clear();
            _edgePath.Data = null;
            _hiEdgePath.Data = null;
            foreach (var n in _nodes) n.Shape = null;
            _drawn2D = false;
            ApplyView(fit: false);
            if (_selected is not null && _focusHops == 0) _scene.FlyTo(_selected.Index);
            return;
        }
        await Ensure2DAsync(_loadVersion);
    }

    /// <summary>Computes the 2D layout on first use, then draws it.</summary>
    private async Task Ensure2DAsync(int version)
    {
        if (!_layout2DReady)
        {
            _status.Text = "Laying out…";
            ShowEmpty("Laying out…", $"Arranging {_nodes.Count} items.", loading: true);
            var cts = new CancellationTokenSource();
            _layoutCts?.Cancel();
            _layoutCts = cts;
            var count = _nodes.Count;
            var edges = _edgeIndex;
            var kinds = _nodes.Select(n => n.KindCode).ToArray();
            var ids = _nodes.Select(n => n.Id).ToArray();
            double[] positions;
            try
            {
                positions = await Task.Run(() => GraphLayout.Layout(count, edges, kinds, ids, cts.Token), cts.Token);
            }
            catch (OperationCanceledException)
            {
                return;
            }
            await UiThread.Resume(DispatcherQueue);
            if (version != _loadVersion || cts.IsCancellationRequested) return;
            for (var i = 0; i < _nodes.Count; i++)
            {
                _nodes[i].HomeX = _nodes[i].X = positions[i * 2];
                _nodes[i].HomeY = _nodes[i].Y = positions[i * 2 + 1];
            }
            _layout2DReady = true;
            HideEmpty();
        }
        if (_is3D) return;
        if (!_drawn2D)
        {
            _drawn2D = true;
            Draw();
        }
        else
        {
            ApplyView(fit: false);
        }
        FitToView();
    }

    private void FitView()
    {
        if (_is3D) _scene.Fit(); else FitToView();
    }

    private void ResetView()
    {
        if (_is3D) _scene.ResetView(); else FitToView();
    }

    protected override void OnEnter(object? parameter) => _ = LoadAsync(force: false);

    /// <summary>Loads the graph when the project changed (or force).</summary>
    public Task LoadAsync(bool force)
    {
        var projectId = ShellProjectId();
        if (!force && projectId.Length > 0 && string.Equals(projectId, _loadedProject, StringComparison.Ordinal) && _nodes.Count > 0)
        {
            return Task.CompletedTask;
        }
        return LoadCoreAsync(projectId);
    }

    private void OnShellChanged(object? sender, EventArgs e)
    {
        DispatcherQueue?.TryEnqueue(() =>
        {
            if (Visibility == Visibility.Visible && !string.Equals(ShellProjectId(), _loadedProject, StringComparison.Ordinal))
            {
                _ = LoadAsync(force: true);
            }
        });
    }

    private async Task LoadCoreAsync(string projectId)
    {
        var version = ++_loadVersion;
        _layoutCts?.Cancel();
        // A different project: the old search and focus mean nothing here.
        if (!string.Equals(projectId, _loadedProject, StringComparison.Ordinal))
        {
            _search.Text = "";
            _search.ItemsSource = null;
        }
        _loadedProject = projectId;
        ClearGraph();
        if (projectId.Length == 0)
        {
            ShowEmpty("Choose a project", "Choose a project on the Timeline page to see its memory graph.");
            return;
        }
        if (ShellState.Current.Offline)
        {
            ShowEmpty("Offline", "The memory graph is drawn from the cloud. Reconnect to see it.", retry: true);
            return;
        }
        _status.Text = $"Loading the graph for {ShellProjectLabel()}…";
        ShowEmpty("Loading the graph…", "Gathering memories, chats and files.", loading: true);
        var response = await Call(NxMethods.ProjectsGraph, new { project_id = projectId, limit = NodeLimit });
        if (version != _loadVersion) return;
        if (!response.Ok)
        {
            _status.Text = "";
            if (IsMissingRoute(response.Error))
            {
                ShowEmpty("Graph view isn't available yet",
                    "Your Nexus server doesn't serve the memory graph yet. It will appear here once the server is updated; the Memory list keeps working meanwhile.");
            }
            else
            {
                ShowEmpty("Couldn't load the graph", response.Error ?? "Request failed.", retry: true);
            }
            return;
        }
        Parse(response.Result);
        if (_nodes.Count == 0)
        {
            _status.Text = "";
            ShowEmpty("Nothing to draw yet",
                "Memories appear here once chats are harvested. Files show up when agents edit code in a captured chat.");
            return;
        }
        HideEmpty();
        // With no links at all, unlinked nodes are the whole picture.
        if (_edges.Count == 0) _showUnlinked.IsChecked = true;
        _legend.Visibility = Visibility.Visible;
        // The 3D layout settles live in the scene (off the UI thread); the
        // 2D layout is only computed when the 2D map is first shown.
        _scene.SetGraph(_nodes.Count, _edgeIndex, _nodes.Select(n => n.KindCode).ToArray(), _degrees,
            _nodes.Select(n => n.Id).ToArray(), _nodes.Select(n => n.Short).ToArray(), _nodes.Select(n => n.Stale).ToArray());
        UpdateSceneActive();
        ApplyView(fit: false);
        if (!_is3D) await Ensure2DAsync(version);
    }

    // ---- data ----

    private void Parse(JsonElement? result)
    {
        _nodes = [];
        _edges = [];
        var byId = new Dictionary<string, GNode>(StringComparer.Ordinal);
        _truncated = false;
        if (result is not { ValueKind: JsonValueKind.Object } root) return;
        _truncated = NxJson.Text(root, "truncated") == "true";
        if (root.TryGetProperty("nodes", out var nodes) && nodes.ValueKind == JsonValueKind.Array)
        {
            foreach (var n in nodes.EnumerateArray())
            {
                var id = NxJson.Text(n, "id");
                if (id.Length == 0 || byId.ContainsKey(id)) continue;
                var node = new GNode
                {
                    Index = _nodes.Count,
                    Id = id,
                    Kind = NxJson.Text(n, "kind"),
                    Ref = NxJson.Text(n, "ref"),
                    Label = NxJson.Scrub(NxJson.Text(n, "label")),
                    Sub = NxJson.Scrub(NxJson.Text(n, "sub")),
                    UpdatedAt = NxJson.Text(n, "updated_at"),
                    Stale = NxJson.Text(n, "stale") == "true",
                    Pinned = NxJson.Text(n, "pinned") == "true"
                };
                if (node.Kind is not ("memory" or "session" or "file")) node.Kind = "memory";
                node.KindCode = GraphLayout.KindCode(node.Kind);
                var colon = id.IndexOf(':');
                if (node.Ref.Length == 0 && colon > 0) node.Ref = id[(colon + 1)..];
                node.Label = CleanLabel(node.KindCode, node.Label, node.Sub);
                if (node.KindCode != GraphLayout.File) node.Sub = ChatTitles.StripEnvelopes(node.Sub);
                if (node.Label.Length == 0) node.Label = node.Ref.Length > 0 ? node.Ref : id;
                node.Short = GraphLayout.ShortName(node.Kind, node.Label);
                byId[id] = node;
                _nodes.Add(node);
            }
        }
        var seenPairs = new HashSet<(int, int)>();
        if (root.TryGetProperty("edges", out var edges) && edges.ValueKind == JsonValueKind.Array)
        {
            foreach (var e in edges.EnumerateArray())
            {
                if (!byId.TryGetValue(NxJson.Text(e, "from"), out var from) ||
                    !byId.TryGetValue(NxJson.Text(e, "to"), out var to) || ReferenceEquals(from, to))
                {
                    continue;
                }
                var key = (Math.Min(from.Index, to.Index), Math.Max(from.Index, to.Index));
                if (!seenPairs.Add(key)) continue;
                var relation = NxJson.Text(e, "relation");
                _edges.Add(new GEdge { From = from, To = to, Relation = relation });
                from.Links.Add((to, relation));
                to.Links.Add((from, relation));
            }
        }
        _edgeIndex = new int[_edges.Count * 2];
        for (var i = 0; i < _edges.Count; i++)
        {
            _edgeIndex[i * 2] = _edges[i].From.Index;
            _edgeIndex[i * 2 + 1] = _edges[i].To.Index;
        }
        _degrees = GraphLayout.Degrees(_nodes.Count, _edgeIndex);
        foreach (var n in _nodes)
        {
            n.Degree = _degrees[n.Index];
            n.R = GraphLayout.NodeRadius(n.KindCode, n.Degree);
        }
    }

    // ---- theme ----

    private bool IsDark => ActualTheme == ElementTheme.Dark ||
        (ActualTheme == ElementTheme.Default && Application.Current.RequestedTheme == ApplicationTheme.Dark);

    private bool _builtDark;

    private void ApplyTheme()
    {
        BuildBrushes();
        _hover.Background = _hoverBg;
        _legend.Background = _hoverBg;
        BuildLegend();
        foreach (var (code, icon) in _chipIcons) icon.Fill = code >= 0 ? KindBrush(code) : code == -2 ? _staleBrush : NexusPalette.Muted;
        if (_nodes.Count > 0 && _drawn2D) Draw();
        if (_selected is not null) RenderSide(_selected); else RenderSideHelp();
    }

    private void BuildBrushes()
    {
        var dark = IsDark;
        _builtDark = dark;
        static SolidColorBrush B(byte a, byte r, byte g, byte b) => new(Color.FromArgb(a, r, g, b));
        _memBrush = dark ? B(255, 0x8E, 0xA2, 0xFF) : B(255, 0x3B, 0x5B, 0xDB);
        _chatBrush = dark ? B(255, 0x2D, 0xD4, 0xBF) : B(255, 0x0F, 0x76, 0x6E);
        _fileBrush = dark ? B(255, 0xFB, 0xBF, 0x24) : B(255, 0xB4, 0x53, 0x09);
        _edgeBrush = dark ? B(0x5C, 0xE5, 0xE5, 0xE5) : B(0x4A, 0x27, 0x27, 0x2A);
        _hiBrush = dark ? B(0xF0, 0xC7, 0xD2, 0xFF) : B(0xE6, 0x1E, 0x3A, 0xB8);
        _haloBrush = dark ? B(255, 0x0A, 0x0A, 0x0A) : B(255, 0xFF, 0xFF, 0xFF);
        _labelBg = dark ? B(0xD9, 0x14, 0x14, 0x14) : B(0xE6, 0xFF, 0xFF, 0xFF);
        _labelFg = dark ? B(255, 0xED, 0xED, 0xED) : B(255, 0x18, 0x18, 0x1B);
        _selectBrush = dark ? B(255, 0xFF, 0xFF, 0xFF) : B(255, 0x09, 0x09, 0x0B);
        _staleBrush = dark ? B(255, 0xF8, 0x71, 0x71) : B(255, 0xDC, 0x26, 0x26);
        _hoverBg = dark ? B(0xF2, 0x14, 0x14, 0x14) : B(0xF7, 0xFF, 0xFF, 0xFF);
        _edgePath.Stroke = _edgeBrush;
        _hiEdgePath.Stroke = _hiBrush;
        foreach (var label in _labelPool)
        {
            label.Background = _labelBg;
            if (label.Child is TextBlock t) t.Foreground = _labelFg;
        }
        static Color C(Brush b) => ((SolidColorBrush)b).Color;
        _scene.SetPalette(new GraphScenePalette(
            C(_memBrush), C(_chatBrush), C(_fileBrush),
            dark ? Color.FromArgb(255, 0x15, 0x16, 0x1A) : Color.FromArgb(255, 0xF6, 0xF7, 0xF9),
            dark ? Color.FromArgb(255, 0xC8, 0xCE, 0xE0) : Color.FromArgb(255, 0x3F, 0x46, 0x5A),
            C(_hiBrush) is var hi ? Color.FromArgb(255, hi.R, hi.G, hi.B) : default,
            C(_selectBrush), C(_staleBrush), _labelBg, _labelFg));
    }

    private Brush KindBrush(int code) => code switch
    {
        GraphLayout.Session => _chatBrush,
        GraphLayout.File => _fileBrush,
        _ => _memBrush
    };

    private static string KindName(string kind) => kind switch { "session" => "Chat", "file" => "File", _ => "Memory" };

    /// <summary>Circle = memory, rounded square = chat, diamond = file.</summary>
    private static Shape KindShape(int code, double size, Brush fill)
    {
        Shape shape;
        switch (code)
        {
            case GraphLayout.Session:
                var square = size * 0.88;
                shape = new Rectangle { Width = square, Height = square, RadiusX = square * 0.28, RadiusY = square * 0.28 };
                break;
            case GraphLayout.File:
                var side = size * 0.74;
                shape = new Rectangle
                {
                    Width = side,
                    Height = side,
                    RadiusX = side * 0.12,
                    RadiusY = side * 0.12,
                    RenderTransform = new RotateTransform { Angle = 45, CenterX = side / 2, CenterY = side / 2 }
                };
                break;
            default:
                shape = new Ellipse { Width = size, Height = size };
                break;
        }
        shape.Fill = fill;
        return shape;
    }

    private void BuildLegend()
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 12 };
        foreach (var (code, text) in new[] { (GraphLayout.Memory, "Memory"), (GraphLayout.Session, "Chat"), (GraphLayout.File, "File") })
        {
            var item = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 5 };
            item.Children.Add(IconCell(code, 12));
            item.Children.Add(new TextBlock { Text = text, FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center });
            row.Children.Add(item);
        }
        var stale = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 5 };
        stale.Children.Add(new Ellipse { Width = 11, Height = 11, Stroke = _staleBrush, StrokeThickness = 2, VerticalAlignment = VerticalAlignment.Center });
        stale.Children.Add(new TextBlock { Text = "Stale", FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(stale);
        _legendHint = new TextBlock
        {
            Text = "Bigger = more links",
            FontSize = 11.5,
            Opacity = 0.65,
            VerticalAlignment = VerticalAlignment.Center,
            Visibility = _narrow && _selected is not null ? Visibility.Collapsed : Visibility.Visible
        };
        row.Children.Add(_legendHint);
        _legend.Child = row;
    }

    /// <summary>A kind's shape centred in a fixed-size cell (for lists and the legend).</summary>
    private Grid IconCell(int code, double size)
    {
        var cell = new Grid { Width = size + 2, Height = size + 2, VerticalAlignment = VerticalAlignment.Center };
        var shape = KindShape(code, size, KindBrush(code));
        shape.HorizontalAlignment = HorizontalAlignment.Center;
        shape.VerticalAlignment = VerticalAlignment.Center;
        cell.Children.Add(shape);
        return cell;
    }

    // ---- drawing ----

    private void ClearGraph()
    {
        _scene.Clear();
        _layout2DReady = false;
        _drawn2D = false;
        _nodeLayer.Children.Clear();
        _edgePath.Data = null;
        _hiEdgePath.Data = null;
        foreach (var label in _labelPool) label.Visibility = Visibility.Collapsed;
        _nodes = [];
        _edges = [];
        _edgeIndex = [];
        _degrees = [];
        _selected = null;
        _hovered = null;
        _focusCenter = null;
        _selectedNeighbours = [];
        _hover.Visibility = Visibility.Collapsed;
        _legend.Visibility = Visibility.Collapsed;
        RenderSideHelp();
        UpdateSceneActive();
    }

    private void Draw()
    {
        _nodeLayer.Children.Clear();
        // Files under chats under memories; busier nodes on top within a kind.
        foreach (var n in _nodes.OrderBy(n => n.KindCode switch { GraphLayout.File => 0, GraphLayout.Session => 1, _ => 2 }).ThenBy(n => n.Degree))
        {
            n.Size = n.R * 2;
            var shape = KindShape(n.KindCode, n.Size, KindBrush(n.KindCode));
            var node = n;
            shape.PointerEntered += (_, e) => ShowHover(node, e);
            shape.PointerMoved += (_, e) => MoveHover(e);
            shape.PointerExited += (_, _) => HideHover(node);
            shape.PointerPressed += (_, e) =>
            {
                var kind = e.Pointer.PointerDeviceType;
                if (kind == PointerDeviceType.Mouse && !e.GetCurrentPoint(shape).Properties.IsLeftButtonPressed) return;
                e.Handled = true;
                Select(node);
            };
            n.Shape = shape;
            _nodeLayer.Children.Add(shape);
        }
        ApplyView(fit: false);
    }

    private void PlaceShape(GNode n)
    {
        if (n.Shape is not { } shape) return;
        Canvas.SetLeft(shape, n.X - shape.Width / 2);
        Canvas.SetTop(shape, n.Y - shape.Height / 2);
    }

    private void StyleNode(GNode n)
    {
        if (n.Shape is not { } shape) return;
        var selected = ReferenceEquals(n, _selected);
        if (selected)
        {
            shape.Stroke = _selectBrush;
            shape.StrokeThickness = 3;
        }
        else if (n.Stale)
        {
            shape.Stroke = _staleBrush;
            shape.StrokeThickness = 2.5;
        }
        else
        {
            // A thin halo in the background colour keeps nodes apart from edges.
            shape.Stroke = _haloBrush;
            shape.StrokeThickness = 1.5;
        }
        var dim = _selected is not null && _focusHops == 0 && !selected && !_selectedNeighbours.Contains(n);
        shape.Opacity = dim ? 0.22 : 1;
    }

    /// <summary>Visibility from the kind chips, unlinked toggle, stale filter and focus.</summary>
    private void ApplyView(bool fit)
    {
        if (_nodes.Count == 0) return;
        var kinds = new HashSet<int>();
        if (_showMemories.IsChecked == true) kinds.Add(GraphLayout.Memory);
        if (_showSessions.IsChecked == true) kinds.Add(GraphLayout.Session);
        if (_showFiles.IsChecked == true) kinds.Add(GraphLayout.File);
        HashSet<GNode>? staleSet = null;
        if (_staleOnly.IsChecked == true)
        {
            staleSet = [];
            foreach (var n in _nodes.Where(n => n.Stale))
            {
                staleSet.Add(n);
                foreach (var (other, _) in n.Links) staleSet.Add(other);
            }
        }
        var showUnlinked = _showUnlinked.IsChecked == true;

        // Focus: centre on the selection and lay its neighbourhood out in rings.
        int[]? hops = null;
        _focusCenter = _focusHops > 0 ? _selected : null;
        if (_is3D)
        {
            // 3D: keep positions, show just the neighbourhood and fly to it.
            if (_focusCenter is { } c3) hops = GraphLayout.Hops(_nodes.Count, _edgeIndex, c3.Index, _focusHops);
        }
        else if (_focusCenter is { } center)
        {
            var radial = GraphLayout.Radial(_nodes.Count, _edgeIndex, _nodes.Select(n => n.KindCode).ToArray(),
                _nodes.Select(n => n.Id).ToArray(), _nodes.Select(n => n.R).ToArray(), center.Index, _focusHops, out hops);
            foreach (var n in _nodes)
            {
                n.X = radial[n.Index * 2];
                n.Y = radial[n.Index * 2 + 1];
            }
        }
        else
        {
            foreach (var n in _nodes)
            {
                n.X = n.HomeX;
                n.Y = n.HomeY;
            }
        }

        var shown = 0;
        foreach (var n in _nodes)
        {
            bool v;
            if (hops is not null)
            {
                v = hops[n.Index] >= 0 && (ReferenceEquals(n, _focusCenter) || kinds.Contains(n.KindCode));
            }
            else
            {
                v = kinds.Contains(n.KindCode) && (showUnlinked || n.Degree > 0) && (staleSet is null || staleSet.Contains(n));
            }
            n.Visible = v;
            if (v) shown++;
            if (n.Shape is { } shape)
            {
                shape.Visibility = v ? Visibility.Visible : Visibility.Collapsed;
                PlaceShape(n);
            }
        }
        if (_selected is not null && !_selected.Visible)
        {
            SetSelected(null);
        }
        if (_is3D)
        {
            _scene.SetShown(_nodes.Select(n => n.Visible).ToArray());
            _scene.SetSelection(_selected?.Index ?? -1, _focusHops == 0);
        }
        else
        {
            foreach (var n in _nodes) StyleNode(n);
            RebuildEdges();
        }

        if (shown == 0)
        {
            var unlinkedOnly = !showUnlinked && _nodes.Any(n => n.Degree == 0 && kinds.Contains(n.KindCode));
            ShowEmpty(_staleOnly.IsChecked == true ? "No stale memories" : "Everything is filtered out",
                _staleOnly.IsChecked == true ? "Every memory in view still matches its code."
                : unlinkedOnly ? "Only unlinked items match. Turn on Unlinked to see them."
                : "Turn on Memories, Chats or Files above.");
        }
        else
        {
            HideEmpty();
        }
        UpdateStatus(shown);
        if (fit) FitView(); else QueueLabels();
    }

    private void RebuildEdges()
    {
        var normal = new PathGeometry();
        var high = new PathGeometry();
        foreach (var e in _edges)
        {
            if (!e.From.Visible || !e.To.Visible) continue;
            var touches = _selected is not null && (ReferenceEquals(e.From, _selected) || ReferenceEquals(e.To, _selected));
            var figure = new PathFigure { StartPoint = new Point(e.From.X, e.From.Y), IsClosed = false, IsFilled = false };
            figure.Segments.Add(new LineSegment { Point = new Point(e.To.X, e.To.Y) });
            (touches ? high : normal).Figures.Add(figure);
        }
        _edgePath.Data = normal;
        _hiEdgePath.Data = high;
        _edgePath.Opacity = _selected is not null && _focusHops == 0 ? 0.45 : 1;
        UpdateStrokes();
    }

    /// <summary>Keeps edges about the same on-screen width at any zoom.</summary>
    private void UpdateStrokes()
    {
        var s = Math.Max(0.05, _transform.ScaleX);
        _edgePath.StrokeThickness = Math.Clamp(1.25 / s, 0.5, 6);
        _hiEdgePath.StrokeThickness = Math.Clamp(2.2 / s, 1, 9);
    }

    private void UpdateStatus(int shown)
    {
        if (_nodes.Count == 0) return;
        var m = _nodes.Count(n => n.KindCode == GraphLayout.Memory);
        var s = _nodes.Count(n => n.KindCode == GraphLayout.Session);
        var f = _nodes.Count(n => n.KindCode == GraphLayout.File);
        var stale = _nodes.Count(n => n.Stale);
        var unlinked = _nodes.Count(n => n.Degree == 0);
        var text = $"{m} memories · {s} chats · {f} files · {_edges.Count} links";
        if (stale > 0) text += $" · {stale} stale";
        if (_focusHops > 0 && _focusCenter is null) text += " · select an item to focus";
        else if (shown != _nodes.Count) text += $" · {shown} shown";
        if (_focusHops == 0 && _showUnlinked.IsChecked != true && unlinked > 0) text += $" · {unlinked} unlinked hidden";
        if (_truncated) text += " · most connected only";
        _status.Text = text;
        ToolTipService.SetToolTip(_status, text);
    }

    // ---- labels (screen space) ----

    private void QueueLabels()
    {
        if (_labelsQueued) return;
        _labelsQueued = true;
        DispatcherQueue?.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low, () =>
        {
            _labelsQueued = false;
            RefreshLabels();
        });
    }

    private void RefreshLabels()
    {
        var used = 0;
        if (_nodes.Count > 0 && _empty.Visibility != Visibility.Visible && !_is3D)
        {
            var s = _transform.ScaleX;
            var tx = _transform.TranslateX;
            var ty = _transform.TranslateY;
            var w = _host.ActualWidth;
            var h = _host.ActualHeight;
            var candidates = new bool[_nodes.Count];
            foreach (var n in _nodes)
            {
                if (!n.Visible) continue;
                var sx = n.X * s + tx;
                var sy = n.Y * s + ty;
                candidates[n.Index] = sx > -40 && sx < w + 40 && sy > -20 && sy < h + 20;
            }
            var forced = new List<int>();
            if (_selected is not null) forced.Add(_selected.Index);
            if (_hovered is not null) forced.Add(_hovered.Index);
            var neighbours = _selected is null ? new List<int>() : _selected.Links.Select(l => l.Node).OrderByDescending(n => n.Degree).Select(n => n.Index).ToList();
            var priority = forced.Concat(neighbours).ToList();
            int budget;
            if (_focusCenter is not null) budget = 120;
            else budget = s < 0.45 ? 14 : s < 0.75 ? 22 : s < 1.2 ? 40 : s < 2 ? 80 : 160;
            var picked = GraphLayout.PickLabels(_degrees, candidates, priority, budget);
            if (_focusCenter is not null)
            {
                // In focus every shown item is small in number: label them all.
                foreach (var n in _nodes) if (candidates[n.Index] && !picked.Contains(n.Index)) picked.Add(n.Index);
            }
            var requests = new List<GraphLayout.LabelRequest>(picked.Count);
            var texts = new List<string>(picked.Count);
            foreach (var i in picked)
            {
                var n = _nodes[i];
                var isForced = forced.Contains(i);
                var text = GraphLayout.Truncate(n.Short, isForced ? 44 : 28);
                texts.Add(text);
                var width = Math.Min(GraphLayout.EstimateWidth(text, LabelFont) + 10, 320);
                requests.Add(new GraphLayout.LabelRequest(n.X * s + tx, n.Y * s + ty, n.R * s + 1, width, LabelHeight, isForced));
            }
            // Keep labels inside the view (and left of a floating details panel).
            var viewW = _narrow && _selected is not null ? Math.Max(w * 0.35, w - FloatingPaneWidth - 16) : w;
            var slots = GraphLayout.PlaceLabels(requests, viewW, h);
            for (var k = 0; k < slots.Length; k++)
            {
                if (!slots[k].Placed) continue;
                var node = _nodes[picked[k]];
                var label = LabelAt(used++);
                var tb = (TextBlock)label.Child;
                tb.Text = texts[k];
                tb.FontWeight = ReferenceEquals(node, _selected) ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
                label.MaxWidth = requests[k].Width;
                label.Tag = node;
                label.Opacity = _selected is not null && _focusHops == 0 && !ReferenceEquals(node, _selected) && !_selectedNeighbours.Contains(node) ? 0.55 : 1;
                Canvas.SetLeft(label, Math.Round(slots[k].X));
                Canvas.SetTop(label, Math.Round(slots[k].Y));
                label.Visibility = Visibility.Visible;
            }
        }
        for (var i = used; i < _labelPool.Count; i++) _labelPool[i].Visibility = Visibility.Collapsed;
    }

    private Border LabelAt(int index)
    {
        if (index < _labelPool.Count) return _labelPool[index];
        var label = new Border
        {
            Background = _labelBg,
            CornerRadius = new CornerRadius(4),
            Padding = new Thickness(5, 1, 5, 2),
            Height = LabelHeight,
            Child = new TextBlock
            {
                FontSize = LabelFont,
                Foreground = _labelFg,
                TextTrimming = TextTrimming.CharacterEllipsis,
                TextWrapping = TextWrapping.NoWrap,
                VerticalAlignment = VerticalAlignment.Center
            }
        };
        label.PointerPressed += (_, e) =>
        {
            if (label.Tag is not GNode node) return;
            e.Handled = true;
            Select(node);
        };
        label.PointerEntered += (_, e) =>
        {
            if (label.Tag is GNode node) ShowHoverCard(node, e);
        };
        label.PointerMoved += (_, e) => MoveHover(e);
        label.PointerExited += (_, _) => _hover.Visibility = Visibility.Collapsed;
        _labelPool.Add(label);
        _labelLayer.Children.Add(label);
        return label;
    }

    // ---- interaction ----

    private void ShowHover(GNode n, PointerRoutedEventArgs e)
    {
        _hovered = n;
        ShowHoverCard(n, e);
        QueueLabels();
    }

    private void HideHover(GNode n)
    {
        _hover.Visibility = Visibility.Collapsed;
        if (!ReferenceEquals(_hovered, n)) return;
        _hovered = null;
        QueueLabels();
    }

    private void ShowHoverCard(GNode n, PointerRoutedEventArgs e) => ShowHoverCardAt(n, e.GetCurrentPoint(_stageGrid).Position);

    private void ShowHoverCardAt(GNode n, Point at)
    {
        _hoverTitle.Text = n.Label;
        var parts = new List<string> { n.Stale ? KindName(n.Kind) + " · stale" : KindName(n.Kind) };
        if (n.Degree > 0) parts.Add(n.Degree == 1 ? "1 link" : $"{n.Degree} links");
        if (FriendlyWhen(n.UpdatedAt) is { Length: > 0 } when) parts.Add(when);
        var sub = string.Join(" · ", parts);
        if (n.Sub.Length > 0 && n.KindCode != GraphLayout.File) sub += "\n" + GraphLayout.Truncate(n.Sub, 180);
        _hoverSub.Text = sub;
        _hover.Visibility = Visibility.Visible;
        MoveHoverTo(at);
    }

    private void MoveHover(PointerRoutedEventArgs e) => MoveHoverTo(e.GetCurrentPoint(_stageGrid).Position);

    private void MoveHoverTo(Point p)
    {
        if (_hover.Visibility != Visibility.Visible) return;
        var x = p.X + 16;
        var y = p.Y + 16;
        if (x + 340 > _stageGrid.ActualWidth) x = Math.Max(4, p.X - 354);
        if (y + 110 > _stageGrid.ActualHeight) y = Math.Max(4, p.Y - 120);
        _hover.Margin = new Thickness(x, y, 0, 0);
    }

    private void Host_PointerPressed(object sender, PointerRoutedEventArgs e)
    {
        // Touch pans and pinches through manipulation; mouse and pen pan here,
        // with manipulation off so it never cancels the mouse drag.
        var touch = e.Pointer.PointerDeviceType == PointerDeviceType.Touch;
        _host.ManipulationMode = touch ? ManipulationModes.TranslateX | ManipulationModes.TranslateY | ManipulationModes.Scale : ManipulationModes.None;
        if (touch) return;
        var point = e.GetCurrentPoint(_host);
        if (!point.Properties.IsLeftButtonPressed && !point.Properties.IsMiddleButtonPressed) return;
        _panning = true;
        _panStart = point.Position;
        _panX = _transform.TranslateX;
        _panY = _transform.TranslateY;
        _host.CapturePointer(e.Pointer);
    }

    private void Host_PointerMoved(object sender, PointerRoutedEventArgs e)
    {
        if (!_panning) return;
        var p = e.GetCurrentPoint(_host).Position;
        _transform.TranslateX = _panX + (p.X - _panStart.X);
        _transform.TranslateY = _panY + (p.Y - _panStart.Y);
        QueueLabels();
    }

    private void Host_PointerReleased(object sender, PointerRoutedEventArgs e)
    {
        if (!_panning) return;
        _panning = false;
        _host.ReleasePointerCapture(e.Pointer);
        var p = e.GetCurrentPoint(_host).Position;
        // A click on empty space (no drag) clears the selection.
        if (Math.Abs(p.X - _panStart.X) < 3 && Math.Abs(p.Y - _panStart.Y) < 3 && _selected is not null && _focusHops == 0)
        {
            Select(null);
        }
    }

    private void Host_ManipulationDelta(object sender, ManipulationDeltaRoutedEventArgs e)
    {
        if (e.PointerDeviceType != PointerDeviceType.Touch) return;
        e.Handled = true;
        _transform.TranslateX += e.Delta.Translation.X;
        _transform.TranslateY += e.Delta.Translation.Y;
        if (Math.Abs(e.Delta.Scale - 1) > 0.0001) ZoomAround(e.Position, _transform.ScaleX * e.Delta.Scale);
        else QueueLabels();
    }

    private void Host_PointerWheelChanged(object sender, PointerRoutedEventArgs e)
    {
        var point = e.GetCurrentPoint(_host);
        var delta = point.Properties.MouseWheelDelta;
        if (delta == 0) return;
        e.Handled = true;
        // Ctrl + wheel (and touchpad pinch, which arrives as Ctrl + wheel) zooms.
        var ctrl = (e.KeyModifiers & Windows.System.VirtualKeyModifiers.Control) != 0;
        if (!ctrl)
        {
            // Plain wheel scrolls; Shift scrolls sideways.
            if ((e.KeyModifiers & Windows.System.VirtualKeyModifiers.Shift) != 0 || point.Properties.IsHorizontalMouseWheel)
            {
                _transform.TranslateX += delta * 0.5;
            }
            else
            {
                _transform.TranslateY += delta * 0.5;
            }
            QueueLabels();
            return;
        }
        ZoomAround(point.Position, _transform.ScaleX * Math.Pow(1.0015, delta));
    }

    private void ZoomBy(double factor)
    {
        if (_is3D) _scene.ZoomBy(factor);
        else ZoomAround(new Point(_host.ActualWidth / 2, _host.ActualHeight / 2), _transform.ScaleX * factor);
    }

    private void ZoomAround(Point p, double scale)
    {
        scale = Math.Clamp(scale, MinScale, MaxScale);
        var s = _transform.ScaleX;
        var wx = (p.X - _transform.TranslateX) / s;
        var wy = (p.Y - _transform.TranslateY) / s;
        _transform.ScaleX = scale;
        _transform.ScaleY = scale;
        _transform.TranslateX = p.X - wx * scale;
        _transform.TranslateY = p.Y - wy * scale;
        UpdateStrokes();
        QueueLabels();
    }

    private void FitToView()
    {
        var shown = _nodes.Where(n => n.Visible).ToList();
        if (shown.Count == 0) return;
        var w = _host.ActualWidth;
        var h = _host.ActualHeight;
        if (w < 10 || h < 10)
        {
            // Not measured yet: fit once it is.
            void Once(object s, SizeChangedEventArgs e)
            {
                _host.SizeChanged -= Once;
                FitToView();
            }
            _host.SizeChanged += Once;
            return;
        }
        var minX = shown.Min(n => n.X - n.R);
        var maxX = shown.Max(n => n.X + n.R);
        var minY = shown.Min(n => n.Y - n.R);
        var maxY = shown.Max(n => n.Y + n.R);
        var gw = Math.Max(1, maxX - minX);
        var gh = Math.Max(1, maxY - minY);
        // Leave room for labels at the edges and the legend at the bottom.
        var scale = Math.Clamp(Math.Min((w - 140) / gw, (h - 90) / gh), MinScale, 1.5);
        _transform.ScaleX = scale;
        _transform.ScaleY = scale;
        _transform.TranslateX = w / 2 - (minX + gw / 2) * scale;
        _transform.TranslateY = (h - 30) / 2 - (minY + gh / 2) * scale;
        UpdateStrokes();
        QueueLabels();
    }

    private void SetSelected(GNode? n)
    {
        _selected = n;
        _selectedNeighbours = n is null ? [] : n.Links.Select(l => l.Node).ToHashSet();
    }

    private void Select(GNode? n)
    {
        SetSelected(n);
        UpdateSidePlacement();
        if (n is null) RenderSideHelp(); else RenderSide(n);
        if (_focusHops > 0)
        {
            ApplyView(fit: true);
            return;
        }
        if (_is3D)
        {
            _scene.SetSelection(n?.Index ?? -1, true);
            return;
        }
        foreach (var node in _nodes) StyleNode(node);
        RebuildEdges();
        QueueLabels();
    }

    /// <summary>Selects a node, makes sure it is shown, and centres it.</summary>
    private void FocusNode(GNode n)
    {
        var changed = false;
        ToggleButton chip = n.KindCode switch { GraphLayout.Session => _showSessions, GraphLayout.File => _showFiles, _ => _showMemories };
        if (chip.IsChecked != true) { chip.IsChecked = true; changed = true; }
        if (n.Degree == 0 && _showUnlinked.IsChecked != true) { _showUnlinked.IsChecked = true; changed = true; }
        if (_staleOnly.IsChecked == true && !n.Stale) { _staleOnly.IsChecked = false; changed = true; }
        if (changed) ApplyView(fit: false);
        Select(n);
        if (_is3D)
        {
            if (_focusHops == 0) _scene.FlyTo(n.Index);
            return;
        }
        var w = _host.ActualWidth;
        var h = _host.ActualHeight;
        if (w < 10 || h < 10) return;
        if (_focusHops > 0) return; // ApplyView already fitted the neighbourhood
        var s = Math.Max(_transform.ScaleX, 0.9);
        _transform.ScaleX = s;
        _transform.ScaleY = s;
        _transform.TranslateX = w / 2 - n.X * s;
        _transform.TranslateY = h / 2 - n.Y * s;
        UpdateStrokes();
        QueueLabels();
    }

    private void Page_KeyDown(object sender, KeyRoutedEventArgs e)
    {
        var ctrl = Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(Windows.System.VirtualKey.Control)
            .HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);
        if (e.Key == Windows.System.VirtualKey.Escape && _selected is not null)
        {
            e.Handled = true;
            Select(null);
        }
        else if (ctrl && e.Key == Windows.System.VirtualKey.F)
        {
            e.Handled = true;
            _search.Focus(FocusState.Keyboard);
        }
        else if (ctrl && e.Key == Windows.System.VirtualKey.Number0)
        {
            e.Handled = true;
            FitView();
        }
    }

    // ---- search ----

    private void Search_TextChanged(AutoSuggestBox sender, AutoSuggestBoxTextChangedEventArgs args)
    {
        if (args.Reason != AutoSuggestionBoxTextChangeReason.UserInput) return;
        sender.ItemsSource = SearchHits(sender.Text, 12);
        // An emptied query clears a stale "Nothing in the graph matches" note.
        if (sender.Text.Trim().Length == 0) UpdateStatus(_nodes.Count(n => n.Visible));
    }

    private List<SearchItem> SearchHits(string query, int max) =>
        GraphLayout.Search(_nodes.Select(n => n.Label).ToList(), _nodes.Select(n => n.Ref).ToList(), _degrees, query, max)
            .Select(i => new SearchItem(_nodes[i])).ToList();

    private void Search_QuerySubmitted(AutoSuggestBox sender, AutoSuggestBoxQuerySubmittedEventArgs args)
    {
        var item = args.ChosenSuggestion as SearchItem ?? SearchHits(args.QueryText, 1).FirstOrDefault();
        if (item is null)
        {
            if (args.QueryText.Trim().Length > 0) _status.Text = $"Nothing in the graph matches \"{args.QueryText.Trim()}\".";
            return;
        }
        sender.Text = item.Node.Label;
        FocusNode(item.Node);
    }

    // ---- side panel ----

    private const double FloatingPaneWidth = 290;

    private void UpdateSidePlacement()
    {
        var floating = _narrow && _selected is not null;
        if (_narrow)
        {
            // Too narrow for a column: the details float over the graph,
            // only while something is selected.
            _split.ColumnDefinitions[1].Width = new GridLength(0);
            Grid.SetColumn(_sidePane, 0);
            _sidePane.HorizontalAlignment = HorizontalAlignment.Right;
            _sidePane.Width = FloatingPaneWidth;
            _sidePane.Margin = new Thickness(0, 8, 8, 8);
            _sidePane.Visibility = floating ? Visibility.Visible : Visibility.Collapsed;
        }
        else
        {
            _split.ColumnDefinitions[1].Width = new GridLength(300);
            Grid.SetColumn(_sidePane, 1);
            _sidePane.HorizontalAlignment = HorizontalAlignment.Stretch;
            _sidePane.Width = double.NaN;
            _sidePane.Margin = new Thickness(0);
            _sidePane.Visibility = Visibility.Visible;
        }
        // Keep the legend clear of a floating panel (shorter, and never under it),
        // and tell the scene which part of the view is covered.
        var covered = floating ? FloatingPaneWidth + 16 : 0;
        _legendHint.Visibility = floating ? Visibility.Collapsed : Visibility.Visible;
        _legend.Margin = new Thickness(10, 10, 10 + covered, 10);
        _legend.HorizontalAlignment = HorizontalAlignment.Left;
        _scene.SetInsets(0, 0, covered, 52);
    }

    /// <summary>
    /// A readable node name: chat titles and memory texts lose harness
    /// envelopes (&lt;timestamp&gt; headers and the like) and placeholder
    /// titles fall back to the chat's first message.
    /// </summary>
    private static string CleanLabel(int kind, string label, string sub)
    {
        if (kind == GraphLayout.File) return label;
        // Harness notices ("[SYSTEM NOTIFICATION - NOT USER INPUT]", task and
        // agent notifications) and envelopes go first; then the first real line.
        string Clean(string text)
        {
            var clean = ChatTitles.Clean(GraphLayout.StripNotices(ChatTitles.StripEnvelopes(text)));
            if (clean.Length > 0) return GraphLayout.StripNotices(clean);
            // An unterminated envelope header: keep what follows its first line.
            var nl = text.IndexOf('\n');
            return nl > 0 ? GraphLayout.StripNotices(ChatTitles.Clean(GraphLayout.StripNotices(text[(nl + 1)..]))) : "";
        }
        if (kind == GraphLayout.Session)
        {
            var stripped = Clean(label);
            var title = ChatTitles.Title(stripped, Clean(sub), "");
            return title != "Untitled chat" ? GraphLayout.StripNotices(title) : stripped;
        }
        var result = Clean(label);
        return result.Length > 0 ? result : Clean(sub);
    }

    private void RenderSideHelp()
    {
        _side.Children.Clear();
        _side.Children.Add(SectionHeader("Memory graph"));
        _side.Children.Add(Muted("Memories, the chats they came from, and the files they touch. Bigger shapes have more links."));
        _side.Children.Add(LegendRow(GraphLayout.Memory, "Memory: a decision, convention or fact"));
        _side.Children.Add(LegendRow(GraphLayout.Session, "Chat"));
        _side.Children.Add(LegendRow(GraphLayout.File, "Code file"));
        var stale = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        stale.Children.Add(new Ellipse { Width = 12, Height = 12, Stroke = _staleBrush, StrokeThickness = 2, VerticalAlignment = VerticalAlignment.Center });
        stale.Children.Add(new TextBlock { Text = "Stale: its code changed since", FontSize = 12, Opacity = 0.8, VerticalAlignment = VerticalAlignment.Center });
        _side.Children.Add(stale);
        _side.Children.Add(SectionHeader("Moving around"));
        if (_is3D)
        {
            _side.Children.Add(Muted("Drag to turn the graph, right-drag or Shift+drag to pan, wheel or pinch to zoom toward the pointer. Drag an item to move it."));
            _side.Children.Add(Muted("Click an item to fly to it, double-click to focus on its neighbours. Ctrl+F finds, Ctrl+0 fits, Esc clears."));
        }
        else
        {
            _side.Children.Add(Muted("Click an item for its details and links. Drag to pan, Ctrl + wheel or pinch to zoom, Ctrl+F to find, Esc to clear."));
            _side.Children.Add(Muted("Focus (top bar) shows just the selected item and its neighbours."));
        }
        if (_nodes.Count > 0)
        {
            var top = _nodes.Where(n => n.Degree > 0).OrderByDescending(n => n.Degree).Take(8).ToList();
            if (top.Count > 0)
            {
                _side.Children.Add(SectionHeader("Most connected"));
                foreach (var n in top) _side.Children.Add(NodeLink(n, $"{n.Degree} links"));
            }
        }
    }

    private StackPanel LegendRow(int code, string text)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        row.Children.Add(IconCell(code, 12));
        row.Children.Add(new TextBlock { Text = text, FontSize = 12, Opacity = 0.8, VerticalAlignment = VerticalAlignment.Center, TextWrapping = TextWrapping.Wrap });
        return row;
    }

    private void RenderSide(GNode n)
    {
        _side.Children.Clear();
        var head = new Grid { ColumnSpacing = 8 };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var icon = IconCell(n.KindCode, 12);
        var kindText = new TextBlock
        {
            Text = KindName(n.Kind) + (n.Pinned ? " · pinned" : "") + (n.Degree > 0 ? $" · {n.Degree} link{(n.Degree == 1 ? "" : "s")}" : " · no links"),
            FontSize = 12,
            Opacity = 0.75,
            VerticalAlignment = VerticalAlignment.Center
        };
        var close = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 10 },
            Padding = new Thickness(6),
            MinWidth = 0,
            MinHeight = 0,
            Background = new SolidColorBrush(Colors.Transparent),
            BorderThickness = new Thickness(0)
        };
        ToolTipService.SetToolTip(close, "Clear selection (Esc)");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(close, "Close details");
        close.Click += (_, _) => Select(null);
        Grid.SetColumn(kindText, 1);
        Grid.SetColumn(close, 2);
        head.Children.Add(icon);
        head.Children.Add(kindText);
        head.Children.Add(close);
        _side.Children.Add(head);

        var title = n.KindCode == GraphLayout.File ? Monospace(n.Label) : Line(n.Label, title: true);
        title.IsTextSelectionEnabled = true;
        _side.Children.Add(title);
        if (n.Stale)
        {
            _side.Children.Add(new TextBlock
            {
                Text = n.KindCode == GraphLayout.File ? "Changed since a memory about it was written" : "Stale: code it describes has changed",
                FontSize = 12,
                Foreground = _staleBrush,
                TextWrapping = TextWrapping.Wrap
            });
        }
        if (FriendlyWhen(n.UpdatedAt) is { Length: > 0 } when)
        {
            _side.Children.Add(Muted((n.KindCode == GraphLayout.Session ? "Active " : "Updated ") + when));
        }
        if (n.Sub.Length > 0)
        {
            var snippet = n.KindCode == GraphLayout.File ? Muted(n.Sub) : Line(GraphLayout.Truncate(n.Sub, 600));
            snippet.IsTextSelectionEnabled = true;
            _side.Children.Add(snippet);
        }

        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        switch (n.KindCode)
        {
            case GraphLayout.Memory:
                actions.Children.Add(Action("Open", () => OpenMemoryRequested?.Invoke(n.Ref, n.Label)));
                break;
            case GraphLayout.Session:
                actions.Children.Add(Action("Open chat", () => MainWindow.Current?.OpenSession(n.Ref)));
                break;
            case GraphLayout.File:
                actions.Children.Add(Action("Open", () =>
                {
                    if (XamlRoot is { } xr) _ = FilePreviewPanel.ShowDialogAsync(xr, FileRefs.Parse(n.Ref) ?? new FileRef(n.Ref), "");
                }));
                actions.Children.Add(Action("Copy path", () =>
                {
                    var data = new DataPackage();
                    data.SetText(n.Ref);
                    Clipboard.SetContent(data);
                }));
                break;
        }
        if (n.Degree > 0)
        {
            var inFocus = _focusHops > 0;
            actions.Children.Add(Action(inFocus ? "Whole graph" : "Focus", () => _view.SelectedIndex = inFocus ? 0 : 1));
        }
        _side.Children.Add(actions);

        if (n.Links.Count == 0) return;
        foreach (var group in n.Links.GroupBy(x => x.Node.KindCode).OrderBy(g => g.Key))
        {
            var title2 = group.Key switch { GraphLayout.Session => "Chats", GraphLayout.File => "Files", _ => "Memories" };
            var items = group.OrderByDescending(x => x.Node.Degree).ThenBy(x => x.Node.Label, StringComparer.OrdinalIgnoreCase).ToList();
            _side.Children.Add(SectionHeader($"{title2} ({items.Count})"));
            foreach (var (other, relation) in items.Take(40))
            {
                _side.Children.Add(NodeLink(other, RelationLabel(relation)));
            }
            if (items.Count > 40) _side.Children.Add(Muted($"+{items.Count - 40} more"));
        }
    }

    private HyperlinkButton NodeLink(GNode other, string note)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 7 };
        row.Children.Add(IconCell(other.KindCode, 9));
        var text = new TextBlock { FontSize = 12, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 230, VerticalAlignment = VerticalAlignment.Center };
        text.Text = other.Short + (note.Length > 0 ? $"  · {note}" : "");
        if (other.Stale) text.Foreground = _staleBrush;
        row.Children.Add(text);
        var link = new HyperlinkButton
        {
            Content = row,
            Padding = new Thickness(2, 1, 2, 1),
            MinHeight = 0,
            HorizontalAlignment = HorizontalAlignment.Left
        };
        ToolTipService.SetToolTip(link, other.Sub.Length > 0 ? $"{other.Label}\n{GraphLayout.Truncate(other.Sub, 200)}" : other.Label);
        link.Click += (_, _) => FocusNode(other);
        return link;
    }

    private static string RelationLabel(string relation) => relation switch
    {
        "from" => "came from",
        "cites" => "cited",
        "touched" => "edited",
        "decided_about" => "decided",
        "mentions" => "mentions",
        _ => relation
    };

    // ---- chrome ----

    /// <summary>code ≥ 0: a kind (shape icon); −1: unlinked; −2: stale.</summary>
    private ToggleButton FilterChip(string label, int code, bool on)
    {
        var content = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        Shape icon;
        if (code >= 0)
        {
            icon = KindShape(code, 10, KindBrush(code));
            icon.VerticalAlignment = VerticalAlignment.Center;
            content.Children.Add(new Grid { Width = 12, Height = 12, VerticalAlignment = VerticalAlignment.Center, Children = { icon } });
            icon.HorizontalAlignment = HorizontalAlignment.Center;
        }
        else
        {
            icon = new Ellipse { Width = 9, Height = 9, Fill = code == -2 ? _staleBrush : NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center };
            content.Children.Add(icon);
        }
        _chipIcons[code] = icon;
        content.Children.Add(new TextBlock { Text = label, FontSize = 12, VerticalAlignment = VerticalAlignment.Center });
        var chip = new ToggleButton
        {
            Content = content,
            IsChecked = on,
            MinHeight = 28,
            Padding = new Thickness(10, 2, 10, 3)
        };
        chip.Click += (_, _) => ApplyView(fit: true);
        return chip;
    }

    private static Button ToolButton(string glyph, string tip, Action click)
    {
        var button = new Button
        {
            Content = new FontIcon { Glyph = glyph, FontSize = 13 },
            MinHeight = 30,
            MinWidth = 34,
            Padding = new Thickness(8, 4, 8, 4)
        };
        ToolTipService.SetToolTip(button, tip);
        // Icon-only: screen readers need the name spelled out.
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, tip);
        button.Click += (_, _) => click();
        return button;
    }

    private void ShowEmpty(string title, string body, bool loading = false, bool retry = false)
    {
        _empty.Children.Clear();
        if (loading) _empty.Children.Add(new ProgressRing { IsActive = true, Width = 28, Height = 28, HorizontalAlignment = HorizontalAlignment.Center });
        _empty.Children.Add(new TextBlock { Text = title, FontSize = 15, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, HorizontalAlignment = HorizontalAlignment.Center, TextWrapping = TextWrapping.Wrap, TextAlignment = TextAlignment.Center });
        _empty.Children.Add(new TextBlock { Text = body, FontSize = 12.5, Opacity = 0.65, HorizontalAlignment = HorizontalAlignment.Center, TextWrapping = TextWrapping.Wrap, TextAlignment = TextAlignment.Center });
        if (retry)
        {
            var again = new Button { Content = "Try again", HorizontalAlignment = HorizontalAlignment.Center, Margin = new Thickness(0, 4, 0, 0) };
            again.Click += (_, _) => _ = LoadAsync(force: true);
            _empty.Children.Add(again);
        }
        _empty.Visibility = Visibility.Visible;
        foreach (var label in _labelPool) label.Visibility = Visibility.Collapsed;
    }

    private void HideEmpty()
    {
        // Drop the content too: a collapsed but active ProgressRing can keep animating.
        foreach (var child in _empty.Children) if (child is ProgressRing ring) ring.IsActive = false;
        _empty.Children.Clear();
        _empty.Visibility = Visibility.Collapsed;
    }

    private static bool IsMissingRoute(string? error)
    {
        var e = error ?? "";
        return e.Contains("404", StringComparison.Ordinal) ||
               e.Contains("405", StringComparison.Ordinal) ||
               e.Contains("page not found", StringComparison.OrdinalIgnoreCase) ||
               e.Contains("method not allowed", StringComparison.OrdinalIgnoreCase) ||
               e.Contains("unknown method", StringComparison.OrdinalIgnoreCase);
    }

    private static string FriendlyWhen(string raw)
    {
        if (!DateTimeOffset.TryParse(raw, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var at)) return "";
        var local = at.ToLocalTime();
        var age = DateTimeOffset.Now - local;
        if (age.TotalMinutes < 1) return "just now";
        if (age.TotalHours < 1) return $"{(int)age.TotalMinutes} min ago";
        if (age.TotalDays < 1) return $"{(int)age.TotalHours} h ago";
        if (age.TotalDays < 7) return $"{(int)age.TotalDays} d ago";
        return local.ToString(local.Year == DateTimeOffset.Now.Year ? "MMM d" : "MMM d, yyyy", CultureInfo.CurrentCulture);
    }
}
