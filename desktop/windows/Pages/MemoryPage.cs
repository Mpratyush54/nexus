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
    private bool _projectsLoaded;

    public MemoryPage()
    {
        foreach (var scope in new[] { "project", "session", "personal", "organization" })
        {
            _scope.Items.Add(scope);
        }
        _scope.SelectedIndex = 0;
        _project.SelectionChanged += async (_, _) =>
        {
            if (_projectsLoaded)
            {
                await LoadAsync();
            }
        };

        var search = new Button { Content = "Search", MinWidth = 88 };
        search.Click += async (_, _) => await LoadAsync();
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
        var scroller = new ScrollViewer { Content = _cards, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled };
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(header);
        root.Children.Add(filters);
        root.Children.Add(scroller);
        Content = root;
    }

    protected override void OnNavigatedTo(Microsoft.UI.Xaml.Navigation.NavigationEventArgs e)
    {
        if (e.Parameter is string query)
        {
            _query.Text = query;
        }
        _ = LoadProjectsAsync();
    }

    private async Task LoadProjectsAsync()
    {
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
        _project.SelectedIndex = 0;
        await LoadAsync();
    }

    private string SelectedProjectId() => (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";

    private async Task LoadAsync()
    {
        var projectId = SelectedProjectId();
        if (projectId.Length == 0)
        {
            return;
        }
        var scope = _scope.SelectedItem as string ?? "project";
        var response = await Call(NxMethods.MemorySearch, new { q = _query.Text, scope, project_id = projectId, limit = 50 });
        _cards.Children.Clear();
        if (!response.Ok)
        {
            ShowError(_cards, response);
            return;
        }
        foreach (var item in NxJson.Items(response.Result))
        {
            var id = NxJson.Text(item, "id");
            var text = NxJson.Text(item, "text", "content", "title", "summary", "key");
            var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
            var pin = CloudAction("Pin", () => _ = Mutate(NxMethods.MemoryPin, id));
            var forget = CloudAction("Forget", () => _ = Mutate(NxMethods.MemoryForget, id));
            _cards.Children.Add(Card(
                Line(text.Length > 0 ? text : id, title: true),
                Line(status.Length > 0 ? status : "Retained knowledge"),
                Row(pin, forget)));
        }
        if (_cards.Children.Count == 0)
        {
            var message = string.IsNullOrWhiteSpace(_query.Text)
                ? "No retained knowledge is available for this project yet."
                : "No matching memories were found.";
            _cards.Children.Add(Card(Line(message, title: true), Line("Try a decision, library, feature, or phrase discussed with an agent.")));
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
        await LoadAsync();
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
