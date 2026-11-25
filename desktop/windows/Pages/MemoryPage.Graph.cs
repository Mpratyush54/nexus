using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;

namespace Nexus.Pages;

// The "Graph" toggle at the top of the Memory page: swaps the list for the
// project's memory graph (GraphPage) and back.
public sealed partial class MemoryPage
{
    private UIElement? _listView;
    private GraphPage? _graph;
    private ToggleButton? _graphToggle;
    private ToggleButton? _listToggle;

    private void AddHeaderWithGraphToggle(Grid root, FrameworkElement header, FrameworkElement list)
    {
        _listView = list;
        ToggleButton Segment(string label)
        {
            var toggle = new ToggleButton { Content = label, MinHeight = 30, Padding = new Thickness(12, 3, 12, 4), BorderThickness = new Thickness(0), CornerRadius = new CornerRadius(7) };
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(toggle, label);
            return toggle;
        }
        _listToggle = Segment("List");
        _listToggle.IsChecked = true;
        _graphToggle = Segment("Graph");
        ToolTipService.SetToolTip(_graphToggle, "See memories, the chats they came from and the files they touch as a graph");
        _listToggle.Click += (_, _) => ShowGraph(false);
        _graphToggle.Click += (_, _) => ShowGraph(true);
        var segmented = new Border
        {
            Padding = new Thickness(3), CornerRadius = new CornerRadius(10), BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Hairline, Background = SessionChatRender.SubtleFill, VerticalAlignment = VerticalAlignment.Top,
            Child = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 2, Children = { _listToggle, _graphToggle } }
        };

        var bar = new Grid { ColumnSpacing = 12 };
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(header, 0);
        Grid.SetColumn(segmented, 1);
        bar.Children.Add(header);
        bar.Children.Add(segmented);

        Grid.SetRow(bar, 0);
        Grid.SetRow(list, 1);
        root.Children.Add(bar);
        root.Children.Add(list);
        _graphHost = root;
    }

    private Grid? _graphHost;

    private void ShowGraph(bool on)
    {
        if (_graphToggle is not null) _graphToggle.IsChecked = on;
        if (_listToggle is not null) _listToggle.IsChecked = !on;
        if (_listView is not null) _listView.Visibility = on ? Visibility.Collapsed : Visibility.Visible;
        if (on && _graph is null && _graphHost is not null)
        {
            _graph = new GraphPage();
            _graph.OpenMemoryRequested += (id, key) => _ = OpenMemoryFromGraphAsync(id, key);
            Grid.SetRow(_graph, 1);
            _graphHost.Children.Add(_graph);
            // Its Loaded handler runs the first load.
            return;
        }
        if (_graph is null) return;
        _graph.Visibility = on ? Visibility.Visible : Visibility.Collapsed;
        if (on) _ = _graph.LoadAsync(force: false);
    }

    private async Task OpenMemoryFromGraphAsync(string id, string key)
    {
        ShowGraph(false);
        _reviewOnly = false;
        _categoryFilter = null;
        SetActiveScope("all", reload: false);
        _query.Text = key;
        await LoadPageAsync(reset: true);
        if (_itemsById.ContainsKey(id)) SelectMemory(id);
    }
}
