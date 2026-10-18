using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class TimelinePage : NexusPage
{
    private readonly TextBox _project = new() { PlaceholderText = "Project", Width = 160 };
    private readonly TextBox _agent = new() { PlaceholderText = "Agent", Width = 160 };
    private readonly TextBox _machine = new() { PlaceholderText = "Machine", Width = 160 };
    private readonly TextBox _person = new() { PlaceholderText = "Person", Width = 160 };
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly Button _more = new() { Content = "Load more sessions", Visibility = Visibility.Collapsed };
    private readonly TextBlock _count = new() { Text = "", Opacity = 0.7 };
    private string _query = "";
    private string _nextCursor = "";
    private bool _loading;
    private int _loadedCount;

    public TimelinePage()
    {
        var refresh = new Button { Content = "Refresh" };
        refresh.Click += async (_, _) => await LoadAsync(false);
        _more.Click += async (_, _) => await LoadAsync(true);
        foreach (var filter in new[] { _project, _agent, _machine, _person })
        {
            filter.KeyDown += async (_, e) =>
            {
                if (e.Key == Windows.System.VirtualKey.Enter) await LoadAsync(false);
            };
        }
        var filters = Row(_project, _agent, _machine, _person, refresh);
        var results = new StackPanel { Spacing = 14 };
        results.Children.Add(_count);
        results.Children.Add(_cards);
        results.Children.Add(_more);
        var scroller = new ScrollViewer { Content = results };
        var root = new Grid { Padding = new Thickness(32, 24, 32, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(Line("Timeline", title: true));
        header.Children.Add(Line("Sessions you own or that were shared with you, across every project."));
        var title = header;
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(title);
        root.Children.Add(filters);
        root.Children.Add(scroller);
        Content = root;
    }

    protected override void OnEnter(object? parameter)
    {
        if (parameter is string query)
        {
            _query = query;
        }
        _ = LoadAsync(false);
    }

    private async Task LoadAsync(bool append)
    {
        if (_loading || (append && _nextCursor.Length == 0)) return;
        _loading = true;
        _more.IsEnabled = false;
        if (!append)
        {
            _nextCursor = "";
            _loadedCount = 0;
            _cards.Children.Clear();
            _count.Text = "Loading sessions…";
        }
        var response = await Call(NxMethods.TimelineList, new
        {
            project = _project.Text,
            agent = _agent.Text,
            machine = _machine.Text,
            person = _person.Text,
            q = _query,
            limit = 30,
            cursor = _nextCursor
        });
        _loading = false;
        if (!response.Ok)
        {
            if (append) _cards.Children.Add(Line(response.Error ?? "Could not load more sessions."));
            else ShowError(_cards, response);
            _count.Text = $"{_loadedCount} sessions loaded";
            _more.IsEnabled = true;
            return;
        }
        _nextCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        _more.Visibility = _nextCursor.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        _more.IsEnabled = true;
        var items = NxJson.Items(response.Result).ToList();
        _loadedCount += items.Count;
        _count.Text = $"{_loadedCount} sessions loaded" + (_nextCursor.Length > 0 ? " · more available" : "");
        if (items.Count == 0 && !append)
        {
            _cards.Children.Add(Card(Line("No sessions found", title: true), Line("Check that capture is active in Settings, then refresh. Historical and new sessions appear here together.")));
            return;
        }
        foreach (var item in items)
        {
            var id = NxJson.Text(item, "id", "session_id", "sessionId");
            var headline = NxJson.Text(item, "title", "summary", "preview", "kind");
            var meta = string.Join(" · ", new[]
            {
                NxJson.Text(item, "agent", "harness"),
                NxJson.Text(item, "project", "project_name"),
                NxJson.Text(item, "machine", "origin_machine_id"),
                NxJson.Text(item, "person", "owner")
            }.Where(part => part.Length > 0));
            var open = Action("Open session", () => MainWindow.Current?.OpenSession(id));
            open.IsEnabled = id.Length > 0;
            _cards.Children.Add(Card(Line(headline.Length > 0 ? headline : id, title: true), Line(meta), open));
        }
    }
}
