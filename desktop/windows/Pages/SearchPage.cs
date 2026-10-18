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
    private readonly ComboBox _project = new() { PlaceholderText = "Select a project", Width = 240 };
    private readonly TextBox _input = new() { PlaceholderText = "Search sessions and memories", Width = 320 };
    private string _query = "";
    private string _sessionCursor = "";
    private int _memoryOffset;
    private bool _memoryHasMore;

    public SearchPage()
    {
        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var submit = new Button { Content = "Search" };
        submit.Click += async (_, _) => await LoadAsync();
        _input.KeyDown += async (_, e) => { if (e.Key == Windows.System.VirtualKey.Enter) await LoadAsync(); };
        _project.SelectionChanged += async (_, _) => await LoadAsync();
        _moreSessions.Click += async (_, _) => await LoadSessionsAsync();
        _moreMemories.Click += async (_, _) => await LoadMemoriesAsync();
        var scroller = new ScrollViewer { Content = _cards };
        var filters = Row(_project, _input, submit);
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(Line("Search", title: true));
        root.Children.Add(filters);
        root.Children.Add(scroller);
        Content = root;
    }

    protected override void OnEnter(object? parameter)
    {
        _query = parameter as string ?? "";
        _input.Text = _query;
        _ = LoadProjectsAsync();
    }

    private async Task LoadProjectsAsync()
    {
        var status = await Call(NxMethods.StatusGet);
        var preferred = status.Result is { } result && result.TryGetProperty("capture", out var capture)
            ? NxJson.Text(capture, "project_id") : "";
        var projects = await Call(NxMethods.ProjectsList);
        _project.Items.Clear();
        if (!projects.Ok) { _cards.Children.Clear(); _cards.Children.Add(Line(projects.Error ?? "Could not load projects.")); return; }
        foreach (var project in NxJson.Items(projects.Result))
        {
            var id = NxJson.Text(project, "id", "project_id");
            if (id.Length == 0) continue;
            _project.Items.Add(new ComboBoxItem { Content = ProjectLabels.Name(project), Tag = id });
        }
        for (var i = 0; i < _project.Items.Count; i++)
            if ((_project.Items[i] as ComboBoxItem)?.Tag as string == preferred) { _project.SelectedIndex = i; return; }
        if (_project.Items.Count == 1) _project.SelectedIndex = 0;
        else { _cards.Children.Clear(); _cards.Children.Add(Line("Choose a project to search its sessions and memories.")); }
    }

    private async Task LoadAsync()
    {
        _cards.Children.Clear();
        var projectId = (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        _query = _input.Text.Trim();
        if (projectId.Length == 0) { _cards.Children.Add(Line("Choose a project first.")); return; }
        if (_query.Length == 0) { _cards.Children.Add(Line("Enter a search term.")); return; }
        _cards.Children.Add(Line("Results for “" + _query + "”", title: true));
        _sessionCursor = "";
        _memoryOffset = 0;
        _memoryHasMore = false;
        _sessions.Children.Clear();
        _memories.Children.Clear();
        _cards.Children.Add(Line("Sessions", title: true));
        _cards.Children.Add(_sessions);
        _cards.Children.Add(_moreSessions);
        _cards.Children.Add(Line("Memories", title: true));
        _cards.Children.Add(_memories);
        _cards.Children.Add(_moreMemories);
        await Task.WhenAll(LoadSessionsAsync(), LoadMemoriesAsync());
    }

    private async Task LoadSessionsAsync()
    {
        var projectId = (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        if (projectId.Length == 0 || _query.Length == 0) return;
        _moreSessions.IsEnabled = false;
        var response = await Call(NxMethods.TimelineList, new { q = _query, project_id = projectId, limit = 30, cursor = _sessionCursor });
        if (!response.Ok)
        {
            _sessions.Children.Add(Line(response.Error ?? "Sessions unavailable"));
            _moreSessions.IsEnabled = true;
            return;
        }
        _sessionCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        _moreSessions.Visibility = _sessionCursor.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        _moreSessions.IsEnabled = true;
        var count = 0;
        foreach (var item in NxJson.Items(response.Result))
        {
            count++;
            var id = NxJson.Text(item, "id", "session_id");
            var title = NxJson.Text(item, "title", "summary", "preview");
            var open = Action("Open session", () => MainWindow.Current?.OpenSession(id));
            open.IsEnabled = id.Length > 0;
            _sessions.Children.Add(Card(Line(title.Length > 0 ? title : id, title: true), open));
        }
        if (count == 0 && _sessions.Children.Count == 0) _sessions.Children.Add(Line("No matching sessions."));
    }

    private async Task LoadMemoriesAsync()
    {
        var projectId = (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        if (projectId.Length == 0 || _query.Length == 0) return;
        _moreMemories.IsEnabled = false;
        var response = await Call(NxMethods.MemorySearch, new { browse = true, q = _query, project_id = projectId, limit = 30, offset = _memoryOffset });
        if (!response.Ok)
        {
            _memories.Children.Add(Line(response.Error ?? "Memories unavailable"));
            _moreMemories.IsEnabled = true;
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        _memoryOffset += items.Count;
        _memoryHasMore = response.Result is { } result && NxJson.Text(result, "has_more") == "true";
        _moreMemories.Visibility = _memoryHasMore ? Visibility.Visible : Visibility.Collapsed;
        _moreMemories.IsEnabled = true;
        foreach (var item in items)
        {
            var text = NxJson.Text(item, "content", "text", "title", "key");
            var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
            _memories.Children.Add(Card(Line(text, title: true), Line(status)));
        }
        if (items.Count == 0 && _memories.Children.Count == 0) _memories.Children.Add(Line("No matching memories."));
    }
}
