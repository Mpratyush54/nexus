using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class SearchPage : NexusPage
{
    private readonly StackPanel _cards = new() { Spacing = 12 };
    private readonly StackPanel _sessions = new() { Spacing = 8 };
    private readonly StackPanel _memories = new() { Spacing = 8 };
    private readonly Button _moreSessions = new() { Content = "Load more sessions", Visibility = Visibility.Collapsed };
    private readonly Button _moreMemories = new() { Content = "Load more memories", Visibility = Visibility.Collapsed };
    private readonly TextBox _input = new() { PlaceholderText = "Search sessions and memories", Width = 320 };
    private string _query = "";
    private string _sessionCursor = "";
    private int _memoryOffset;
    private bool _memoryHasMore;
    private int _loadVersion;

    public SearchPage()
    {
        var root = new Grid { Padding = new Thickness(32, 24, 32, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var submit = new Button { Content = "Search" };
        submit.Click += async (_, _) => await LoadAsync();
        _input.KeyDown += async (_, e) => { if (e.Key == Windows.System.VirtualKey.Enter) await LoadAsync(); };
        _moreSessions.Click += async (_, _) => await LoadSessionsAsync();
        _moreMemories.Click += async (_, _) => await LoadMemoriesAsync();
        var scroller = new ScrollViewer { Content = _cards };
        var filters = Row(_input, submit);
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(PageHeader(
            "Search",
            "Find sessions and memories within the project selected in the shell."));
        root.Children.Add(filters);
        root.Children.Add(scroller);
        Content = root;
        WatchShellProject(() => LoadAsync());
    }

    protected override void OnEnter(object? parameter)
    {
        _query = parameter as string ?? "";
        _input.Text = _query;
        if (_query.Length > 0)
        {
            _ = LoadAsync();
        }
    }

    private async Task LoadAsync()
    {
        var version = ++_loadVersion;
        _cards.Children.Clear();
        var projectId = ShellProjectId();
        _query = _input.Text.Trim();
        if (projectId.Length == 0)
        {
            _cards.Children.Add(Card(Line("Choose a project", title: true), Line("Choose a project on the Timeline page before searching.")));
            return;
        }
        if (_query.Length == 0)
        {
            _cards.Children.Add(Line("Enter a search term."));
            return;
        }
        _cards.Children.Add(Line("Results for “" + _query + "” in " + ShellProjectLabel(), title: true));
        _sessionCursor = "";
        _memoryOffset = 0;
        _memoryHasMore = false;
        _moreSessions.Visibility = Visibility.Collapsed;
        _moreSessions.IsEnabled = true;
        _moreMemories.Visibility = Visibility.Collapsed;
        _moreMemories.IsEnabled = true;
        _sessions.Children.Clear();
        _memories.Children.Clear();
        _cards.Children.Add(SectionHeader("Sessions"));
        _cards.Children.Add(_sessions);
        _cards.Children.Add(_moreSessions);
        _cards.Children.Add(SectionHeader("Memories"));
        _cards.Children.Add(_memories);
        _cards.Children.Add(_moreMemories);
        await Task.WhenAll(LoadSessionsAsync(version), LoadMemoriesAsync(version));
    }

    private Task LoadSessionsAsync() => LoadSessionsAsync(_loadVersion);

    private async Task LoadSessionsAsync(int version)
    {
        var projectId = ShellProjectId();
        var query = _query;
        if (projectId.Length == 0 || query.Length == 0) return;
        _moreSessions.IsEnabled = false;
        var projectsResponse = await Call(NxMethods.ProjectsList);
        var response = await Call(NxMethods.TimelineList, new { q = query, project_id = projectId, limit = 30, cursor = _sessionCursor });
        if (version != _loadVersion ||
            projectId != ShellProjectId() ||
            !string.Equals(query, _query, StringComparison.Ordinal))
        {
            return;
        }
        _moreSessions.IsEnabled = true;
        if (!response.Ok)
        {
            _sessions.Children.Add(Line(response.Error ?? "Sessions unavailable"));
            return;
        }
        var projects = projectsResponse.Ok ? projectsResponse.Result : null;
        _sessionCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        _moreSessions.Visibility = _sessionCursor.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        var count = 0;
        foreach (var item in NxJson.Items(response.Result))
        {
            if (!ProjectScope.TimelineItemMatches(item, projectId, projects))
            {
                continue;
            }
            count++;
            var id = NxJson.Text(item, "id", "session_id");
            var title = NxJson.Text(item, "title", "summary", "preview");
            var open = Action("Open matching message", () => MainWindow.Current?.OpenSession(id, query));
            open.IsEnabled = id.Length > 0;
            var preview = NxJson.Text(item, "match_preview", "summary");
            _sessions.Children.Add(Card(Line(title.Length > 0 ? title : "Conversation", title: true), Muted(preview), open));
        }
        if (count == 0 && _sessions.Children.Count == 0) _sessions.Children.Add(Line("No matching sessions."));
    }

    private Task LoadMemoriesAsync() => LoadMemoriesAsync(_loadVersion);

    private async Task LoadMemoriesAsync(int version)
    {
        var projectId = ShellProjectId();
        var query = _query;
        if (projectId.Length == 0 || query.Length == 0) return;
        _moreMemories.IsEnabled = false;
        var response = await Call(NxMethods.MemorySearch, new { browse = true, q = query, project_id = projectId, limit = 30, offset = _memoryOffset });
        if (version != _loadVersion ||
            projectId != ShellProjectId() ||
            !string.Equals(query, _query, StringComparison.Ordinal))
        {
            return;
        }
        _moreMemories.IsEnabled = true;
        if (!response.Ok)
        {
            _memories.Children.Add(Line(response.Error ?? "Memories unavailable"));
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        _memoryOffset += items.Count;
        _memoryHasMore = response.Result is { } result && NxJson.Text(result, "has_more") == "true";
        _moreMemories.Visibility = _memoryHasMore ? Visibility.Visible : Visibility.Collapsed;
        foreach (var item in items)
        {
            var text = NxJson.Text(item, "content", "text", "title", "key");
            var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
            _memories.Children.Add(Card(Line(text, title: true), Line(status)));
        }
        if (items.Count == 0 && _memories.Children.Count == 0) _memories.Children.Add(Line("No matching memories."));
    }
}
