using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class SearchPage : NexusPage
{
    private readonly StackPanel _cards = new();
    private string _query = "";

    public SearchPage()
    {
        var root = new Grid { Padding = new Thickness(20, 16, 20, 16), RowSpacing = 10 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var scroller = new ScrollViewer { Content = _cards };
        Grid.SetRow(scroller, 1);
        root.Children.Add(PageHeading("Search"));
        root.Children.Add(scroller);
        Content = root;
    }

    protected override void OnEnter(object? parameter)
    {
        _query = parameter as string ?? "";
        _ = LoadAsync();
    }

    private async Task LoadAsync()
    {
        _cards.Children.Clear();
        _cards.Children.Add(Line("Results for " + _query, title: true));
        await Section("Sessions", NxMethods.TimelineList, new { q = _query }, true);
        await Section("Memories", NxMethods.MemorySearch, new { q = _query }, false);
        await Section("Projects", NxMethods.ProjectsList, new { q = _query }, false);
    }

    private async Task Section(string title, string method, object args, bool openSession)
    {
        _cards.Children.Add(Line(title, title: true));
        var response = await Call(method, args);
        if (!response.Ok)
        {
            _cards.Children.Add(Line(response.Error ?? "Unavailable"));
            return;
        }
        var count = 0;
        foreach (var item in NxJson.Items(response.Result))
        {
            count++;
            var id = NxJson.Text(item, "id", "session_id", "project_id");
            var text = NxJson.Text(item, "title", "summary", "text", "content", "name", "path");
            var row = new StackPanel { Spacing = 4 };
            row.Children.Add(Line(text.Length > 0 ? text : id));
            if (openSession && id.Length > 0)
            {
                row.Children.Add(Action("Open", () => MainWindow.Current?.OpenSession(id)));
            }
            else if (title == "Memories")
            {
                row.Children.Add(Line(NxJson.PublicStatus(NxJson.Text(item, "status"))));
            }
            _cards.Children.Add(Card(row));
        }
        if (count == 0)
        {
            _cards.Children.Add(Line("No " + title.ToLowerInvariant() + "."));
        }
    }
}
