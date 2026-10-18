using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class MemoryPage : NexusPage
{
    private readonly ComboBox _project = new() { Width = 240, PlaceholderText = "Select a project" };
    private readonly TextBox _query = new() { PlaceholderText = "Search this project's knowledge", Width = 320 };
    private readonly ComboBox _scope = new() { Width = 150 };
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly Button _more = new() { Content = "Load more memories", Visibility = Visibility.Collapsed };
    private readonly TextBlock _count = new() { Opacity = 0.7 };
    private bool _projectsLoaded;
    private bool _loading;
    private bool _hasMore;
    private int _offset;
    private int _shown;

    public MemoryPage()
    {
        foreach (var scope in new[] { "all", "project", "session", "personal", "organization" })
        {
            _scope.Items.Add(scope);
        }
        _scope.SelectedIndex = 0;
        _project.SelectionChanged += async (_, _) =>
        {
            if (_projectsLoaded)
            {
                await LoadAsync(false);
            }
        };
        _scope.SelectionChanged += async (_, _) =>
        {
            if (_projectsLoaded) await LoadAsync(false);
        };

        var search = new Button { Content = "Search", MinWidth = 88 };
        search.Click += async (_, _) => await LoadAsync(false);
        _query.KeyDown += async (_, e) =>
        {
            if (e.Key == Windows.System.VirtualKey.Enter) await LoadAsync(false);
        };
        _more.Click += async (_, _) => await LoadAsync(true);
        var refresh = new Button { Content = "Refresh projects" };
        refresh.Click += async (_, _) => await LoadProjectsAsync();

        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(Line("Memory & knowledge", title: true));
        header.Children.Add(Line("Browse the selected project's retained decisions, facts, and context."));

        var root = new Grid { Padding = new Thickness(32, 24, 32, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var filters = Row(_project, _query, _scope, search, refresh);
        var results = new StackPanel { Spacing = 14 };
        results.Children.Add(_count);
        results.Children.Add(_cards);
        results.Children.Add(_more);
        var scroller = new ScrollViewer { Content = results, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled };
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
            _query.Text = query;
        }
        _ = LoadProjectsAsync();
    }

    private async Task LoadProjectsAsync()
    {
        var previous = SelectedProjectId();
        var status = await Call(NxMethods.StatusGet);
        var preferred = status.Result is { } state && state.TryGetProperty("capture", out var capture)
            ? NxJson.Text(capture, "project_id") : "";
        var response = await Call(NxMethods.ProjectsList);
        _project.Items.Clear();
        _projectsLoaded = false;
        if (!response.Ok)
        {
            _cards.Children.Clear();
            ShowError(_cards, response);
            return;
        }
        foreach (var project in NxJson.Items(response.Result))
        {
            var id = NxJson.Text(project, "id", "project_id");
            var name = NxJson.Text(project, "name", "slug", "title");
            if (id.Length > 0)
            {
                _project.Items.Add(new ComboBoxItem { Content = name.Length > 0 ? name : id, Tag = id });
            }
        }
        _projectsLoaded = true;
        if (_project.Items.Count == 0)
        {
            _cards.Children.Clear();
            _cards.Children.Add(Card(
                Line("No accessible projects", title: true),
                Line("Sign in, then select or create a project before searching shared knowledge.")));
            return;
        }
        var wanted = previous.Length > 0 ? previous : preferred;
        var chosen = -1;
        for (var i = 0; i < _project.Items.Count; i++)
            if ((_project.Items[i] as ComboBoxItem)?.Tag as string == wanted) { chosen = i; break; }
        if (chosen >= 0 || _project.Items.Count == 1) _project.SelectedIndex = chosen >= 0 ? chosen : 0;
        else
        {
            _cards.Children.Clear();
            _cards.Children.Add(Card(Line("Choose a project", title: true), Line("Memory is scoped to one project at a time.")));
        }
    }

    private string SelectedProjectId() => (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";

    private async Task LoadAsync(bool append)
    {
        if (_loading || (append && !_hasMore)) return;
        var projectId = SelectedProjectId();
        if (projectId.Length == 0)
        {
            return;
        }
        _loading = true;
        _more.IsEnabled = false;
        if (!append)
        {
            _offset = 0;
            _shown = 0;
            _cards.Children.Clear();
            _count.Text = "Loading memories…";
        }
        var scope = _scope.SelectedItem as string ?? "all";
        var response = await Call(NxMethods.MemorySearch, new { browse = true, q = _query.Text, project_id = projectId, limit = 50, offset = _offset });
        _loading = false;
        if (!response.Ok)
        {
            if (append) _cards.Children.Add(Line(response.Error ?? "Could not load more memories."));
            else ShowError(_cards, response);
            _more.IsEnabled = true;
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        _offset += items.Count;
        _hasMore = response.Result is { } result && NxJson.Text(result, "has_more") == "true";
        _more.Visibility = _hasMore ? Visibility.Visible : Visibility.Collapsed;
        _more.IsEnabled = true;
        foreach (var item in items)
        {
            var level = NxJson.Text(item, "level");
            if (scope != "all" && !string.Equals(scope, level, StringComparison.OrdinalIgnoreCase)) continue;
            var id = NxJson.Text(item, "id");
            var text = NxJson.Text(item, "text", "content", "title", "summary", "key");
            var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
            var pin = CloudAction("Pin", () => _ = Mutate(NxMethods.MemoryPin, id));
            var forget = CloudAction("Forget", () => _ = ConfirmForgetAsync(id));
            _cards.Children.Add(Card(
                Line(text.Length > 0 ? text : id, title: true),
                Line(status.Length > 0 ? status : "Retained knowledge"),
                Row(pin, forget)));
            _shown++;
        }
        _count.Text = $"{_shown} memories shown" + (_hasMore ? " · more available" : "");
        if (_cards.Children.Count == 0 && !append)
        {
            var message = string.IsNullOrWhiteSpace(_query.Text)
                ? "No memories at this level on this page."
                : "No matching memories were found.";
            _cards.Children.Add(Card(Line(message, title: true), Line(_hasMore ? "Load more to continue browsing this project." : "Try another memory level, project, or search term.")));
        }
    }

    private async Task Mutate(string method, string id)
    {
        var response = await Call(method, new { id });
        if (!response.Ok)
        {
            _cards.Children.Insert(0, Line(response.Error ?? "Memory update failed"));
            return;
        }
        await LoadAsync(false);
    }

    private async Task ConfirmForgetAsync(string id)
    {
        var dialog = new ContentDialog
        {
            Title = "Forget this memory?",
            Content = "This will remove it from active knowledge.",
            PrimaryButtonText = "Forget",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = XamlRoot
        };
        if (await dialog.ShowAsync() == ContentDialogResult.Primary) await Mutate(NxMethods.MemoryForget, id);
    }

    private Button CloudAction(string label, Action click)
    {
        var button = Action(label, click);
        if (ShellState.Current.Offline)
        {
            button.IsEnabled = false;
            ToolTipService.SetToolTip(button, "Available when online");
        }
        return button;
    }
}
