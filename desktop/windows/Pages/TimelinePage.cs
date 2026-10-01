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
    private readonly StackPanel _cards = new();
    private string _query = "";

    public TimelinePage()
    {
        var refresh = new Button { Content = "Refresh" };
        refresh.Click += async (_, _) => await LoadAsync();
        var filters = Row(_project, _agent, _machine, _person);
        var header = new Grid { ColumnSpacing = 16 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var heading = new StackPanel { Spacing = 4 };
        heading.Children.Add(PageHeading("Timeline"));
        heading.Children.Add(Muted("Sessions you own or that were shared with you, across every project."));
        Grid.SetColumn(heading, 0);
        Grid.SetColumn(refresh, 1);
        header.Children.Add(heading);
        header.Children.Add(refresh);

        var scroller = new ScrollViewer { Content = PageBody(_cards, 980) };
        var root = new Grid { Padding = new Thickness(28, 24, 28, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(header);
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
            if (response.Error?.Contains("not signed in", StringComparison.OrdinalIgnoreCase) == true)
            {
                _cards.Children.Add(Card(
                    Line("Your timeline is ready after sign-in", title: true),
                    Line("Nexus keeps the desktop shell available offline, but timeline data is private cloud data."),
                    Action("Open Settings", () => MainWindow.Current?.ShowSettings())));
                return;
            }
            ShowError(_cards, response);
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        if (items.Count == 0)
        {
            if (response.Result is { } raw && raw.ValueKind == JsonValueKind.Object &&
                raw.TryGetProperty("note", out var note) && note.ValueKind == JsonValueKind.String)
            {
                _cards.Children.Add(EmptyState(
                    "Timeline service needs an update",
                    note.GetString() ?? "The cloud timeline is not available yet.",
                    Action("Try again", () => _ = LoadAsync())));
            }
            else
            {
                _cards.Children.Add(EmptyState(
                    "No sessions yet",
                    "Captured harness sessions will appear here once they reach the cloud."));
            }
            return;
        }
        foreach (var item in items)
        {
            var id = NxJson.Text(item, "id", "session_id", "sessionId");
            var headline = NxJson.Text(item, "title", "summary", "preview", "kind");
            var legacySnapshot = string.Equals(NxJson.Text(item, "version_state"), "legacy_snapshot", StringComparison.OrdinalIgnoreCase);
            var meta = string.Join(" · ", new[]
            {
                NxJson.Text(item, "agent", "harness"),
                NxJson.Text(item, "project", "project_name"),
                NxJson.Text(item, "machine", "origin_machine_id"),
                NxJson.Text(item, "person", "owner"),
                legacySnapshot ? "archived snapshot" : ""
            }.Where(part => part.Length > 0));
            var open = Action(legacySnapshot ? "View snapshot" : "Open", () => MainWindow.Current?.OpenSession(id));
            var cont = Action("Continue", () => MainWindow.Current?.OpenSession(id));
            open.IsEnabled = id.Length > 0;
            cont.IsEnabled = id.Length > 0 && !legacySnapshot;
            _cards.Children.Add(Card(Line(headline.Length > 0 ? headline : id, title: true), Line(meta), Row(open, cont)));
        }
    }
}
