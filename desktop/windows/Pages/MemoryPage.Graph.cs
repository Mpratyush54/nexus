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

    private void AddHeaderWithGraphToggle(Grid root, FrameworkElement header, FrameworkElement list)
    {
        _listView = list;
        _graphToggle = new ToggleButton
        {
            Content = "Graph",
            MinHeight = 32,
            Padding = new Thickness(14, 4, 14, 4),
            VerticalAlignment = VerticalAlignment.Top
        };
        ToolTipService.SetToolTip(_graphToggle, "See memories, the chats they came from and the files they touch as a graph");
        _graphToggle.Click += (_, _) => ShowGraph(_graphToggle.IsChecked == true);

        var bar = new Grid { ColumnSpacing = 12 };
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        bar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(header, 0);
        Grid.SetColumn(_graphToggle, 1);
        bar.Children.Add(header);
        bar.Children.Add(_graphToggle);

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
