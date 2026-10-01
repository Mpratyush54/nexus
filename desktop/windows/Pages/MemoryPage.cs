using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class MemoryPage : NexusPage
{
    private readonly TextBox _query = new() { PlaceholderText = "Search this project's knowledge", MinWidth = 300 };
    private readonly ComboBox _project = new() { Header = "Project", MinWidth = 220 };
    private readonly ComboBox _scope = new() { Header = "Memory level", MinWidth = 160 };
    private readonly StackPanel _cards = new();
    private readonly TextBlock _subtitle = Muted("Search the active project's retained decisions, facts, and context.");
    private bool _projectsLoaded;

    public MemoryPage()
    {
        foreach (var scope in new[] { "project", "session" })
        {
            _scope.Items.Add(scope);
        }
        _scope.SelectedIndex = 0;
        var search = new Button { Content = "Search" };
        search.Click += async (_, _) => await LoadAsync();
        _project.SelectionChanged += async (_, _) =>
        {
            if (_projectsLoaded && !string.IsNullOrWhiteSpace(_query.Text))
            {
                await LoadAsync();
            }
        };

        var root = new Grid { Padding = new Thickness(28, 24, 28, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(PageHeading("Memory & knowledge"));
        header.Children.Add(_subtitle);
        var filters = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
        filters.Children.Add(_project);
        filters.Children.Add(_query);
        filters.Children.Add(_scope);
        filters.Children.Add(search);
        var scroller = new ScrollViewer { Content = PageBody(_cards, 920) };
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
        _subtitle.Text = "Loading projects…";
        var response = await Call(NxMethods.ProjectsList);
        _project.Items.Clear();
        if (!response.Ok)
        {
            _subtitle.Text = response.Error ?? "Could not load projects";
            _cards.Children.Clear();
            ShowError(_cards, response);
            return;
        }

        foreach (var item in NxJson.Items(response.Result))
        {
            var id = NxJson.Text(item, "id", "project_id");
            if (id.Length == 0)
            {
                continue;
            }
            var label = NxJson.Text(item, "display_name", "folder_name", "name", "title", "slug", "path", "canonical_url");
            _project.Items.Add(new ProjectOption(id, label.Length > 0 ? label : id));
        }
        _projectsLoaded = true;
        if (_project.Items.Count > 0)
        {
            _project.SelectedIndex = 0;
            _subtitle.Text = "Search the selected project's retained decisions, facts, and context.";
            await LoadAsync();
            return;
        }

        _subtitle.Text = "Create or join a project in the web portal before searching memory.";
        _cards.Children.Clear();
        _cards.Children.Add(Card(
            Line("No project is available", title: true),
            Line("Memory is always scoped to a project. Select or create one in the web portal, then return here.")));
    }

    private async Task LoadAsync()
    {
        _cards.Children.Clear();
        if (_project.SelectedItem is not ProjectOption project)
        {
            _cards.Children.Add(Card(Line("Choose a project to search memory", title: true)));
            return;
        }
        if (string.IsNullOrWhiteSpace(_query.Text))
        {
            _cards.Children.Add(Card(
                Line("Search your project knowledge", title: true),
                Line("Try an architecture decision, a library name, or a feature you discussed with an agent.")));
            return;
        }

        var level = _scope.SelectedItem as string ?? "project";
        var response = await Call(NxMethods.MemorySearch, new
        {
            q = _query.Text,
            project_id = project.Id,
            level,
            limit = 50
        });
        if (!response.Ok)
        {
            if (response.Error?.Contains("not signed in", StringComparison.OrdinalIgnoreCase) == true)
            {
                _cards.Children.Add(Card(
                    Line("Sign in to search memory", title: true),
                    Line("Your personal and project memories stay private until you connect this desktop to Nexus."),
                    Action("Open Settings", () => MainWindow.Current?.ShowSettings())));
                return;
            }
            ShowError(_cards, response);
            return;
        }
        foreach (var item in NxJson.Items(response.Result))
        {
            var id = NxJson.Text(item, "id");
            var text = NxJson.Text(item, "text", "content", "title", "summary");
            var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
            var manage = new Button { Content = "Manage" };
            var menu = new MenuFlyout();
            menu.Items.Add(MenuAction("Pin", () => _ = Mutate(NxMethods.MemoryPin, id)));
            menu.Items.Add(MenuAction("Forget", () => _ = Mutate(NxMethods.MemoryForget, id)));
            menu.Items.Add(new MenuFlyoutSeparator());
            menu.Items.Add(MenuAction("Move to project", () => _ = Scope(id, "project")));
            menu.Items.Add(MenuAction("Move to session", () => _ = Scope(id, "session")));
            manage.Flyout = menu;
            if (ShellState.Current.Offline)
            {
                manage.IsEnabled = false;
                ToolTipService.SetToolTip(manage, "Available when online");
            }
            _cards.Children.Add(Card(
                Line(text.Length > 0 ? text : id, title: true),
                Line(status),
                Row(manage)));
        }
        if (_cards.Children.Count == 0)
        {
            _cards.Children.Add(Line(response.Result is { } raw && raw.ValueKind != JsonValueKind.Array
                ? raw.GetRawText()
                : "No active memories."));
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

    private async Task Scope(string id, string scope)
    {
        var response = await Call(NxMethods.MemoryScope, new { id, scope });
        if (!response.Ok)
        {
            _cards.Children.Insert(0, Line(response.Error ?? "Scope change failed"));
            return;
        }
        await LoadAsync();
    }

    private static MenuFlyoutItem MenuAction(string label, Action action)
    {
        var item = new MenuFlyoutItem { Text = label };
        item.Click += (_, _) => action();
        return item;
    }

    private sealed record ProjectOption(string Id, string Label)
    {
        public override string ToString() => Label;
    }
}
