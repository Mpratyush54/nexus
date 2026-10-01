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
    private string _query = "";

    public TimelinePage()
    {
        var refresh = new Button { Content = "Refresh" };
        refresh.Click += async (_, _) => await LoadAsync();
        var filters = Row(_project, _agent, _machine, _person, refresh);
        var scroller = new ScrollViewer { Content = _cards };
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

    protected override void OnNavigatedTo(Microsoft.UI.Xaml.Navigation.NavigationEventArgs e)
    {
        if (e.Parameter is string query)
        {
            _query = query;
        }
        _ = LoadAsync();
    }

    private async Task LoadAsync()
    {
        var response = await Call(NxMethods.TimelineList, new
        {
            project = _project.Text,
            agent = _agent.Text,
            machine = _machine.Text,
            person = _person.Text,
            q = _query
        });
        _cards.Children.Clear();
        if (!response.Ok)
        {
            ShowError(_cards, response);
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        if (items.Count == 0)
        {
            _cards.Children.Add(Line(response.Result is { } raw && raw.ValueKind == JsonValueKind.Object
                ? raw.GetRawText()
                : "No timeline events yet."));
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
