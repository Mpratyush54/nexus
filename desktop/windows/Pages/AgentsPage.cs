using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class AgentsPage : NexusPage
{
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly ComboBox _project = new() { PlaceholderText = "Select a project", Width = 240 };
    private bool _projectsLoaded;

    public AgentsPage()
    {
        var refresh = new Button { Content = "Refresh", MinWidth = 88 };
        refresh.Click += async (_, _) => await LoadAsync();
        _project.SelectionChanged += async (_, _) => { if (_projectsLoaded) await LoadAsync(); };

        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(Line("Captured agents", title: true));
        header.Children.Add(Line("Agents with captured sessions in the selected project appear here."));

        var root = new Grid { Padding = new Thickness(32, 24, 32, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var scroller = new ScrollViewer { Content = _cards, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled };
        var filters = Row(_project, refresh);
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(header);
        root.Children.Add(filters);
        root.Children.Add(scroller);
        Content = root;
        Loaded += async (_, _) => await LoadProjectsAsync();
    }

    private async Task LoadProjectsAsync()
    {
        var status = await Call(NxMethods.StatusGet);
        var preferred = status.Result is { } result && result.TryGetProperty("capture", out var capture)
            ? NxJson.Text(capture, "project_id") : "";
        var projects = await Call(NxMethods.ProjectsList);
        _project.Items.Clear();
        _projectsLoaded = false;
        _cards.Children.Clear();
        if (!projects.Ok) { ShowError(_cards, projects); return; }
        foreach (var project in NxJson.Items(projects.Result))
        {
            var id = NxJson.Text(project, "id", "project_id");
            if (id.Length == 0) continue;
            _project.Items.Add(new ComboBoxItem { Content = ProjectLabels.Name(project), Tag = id });
        }
        _projectsLoaded = true;
        for (var i = 0; i < _project.Items.Count; i++)
            if ((_project.Items[i] as ComboBoxItem)?.Tag as string == preferred) { _project.SelectedIndex = i; return; }
        if (_project.Items.Count == 1) _project.SelectedIndex = 0;
        else _cards.Children.Add(Line(_project.Items.Count == 0 ? "No accessible projects. Connect a workspace in Settings." : "Choose a project to see its captured agents."));
    }

    private async Task LoadAsync()
    {
        var projectId = (_project.SelectedItem as ComboBoxItem)?.Tag as string ?? "";
        if (projectId.Length == 0) return;
        var response = await Call(NxMethods.AgentsList, new { project_id = projectId });
        _cards.Children.Clear();
        if (!response.Ok)
        {
            ShowError(_cards, response);
            return;
        }
        var agents = NxJson.Items(response.Result).ToList();
        foreach (var agent in agents)
        {
            var name = NxJson.Text(agent, "name", "agent", "harness");
            var harness = NxJson.Text(agent, "harness");
            var project = (_project.SelectedItem as ComboBoxItem)?.Content?.ToString() ?? "";
            var status = NxJson.Text(agent, "status");
            var resume = NxJson.Text(agent, "resume_mode");
            var count = NxJson.Text(agent, "captured_count", "count");
            var view = Action("View sessions", () => MainWindow.Current?.ShowAgentSessions(harness.Length > 0 ? harness : name, projectId));
            var detail = string.Join(" · ", new[] { status, count.Length > 0 ? count + " sessions" : "", project }.Where(x => x.Length > 0));
            _cards.Children.Add(Card(
                Line(name.Length > 0 ? name : "Unknown harness", title: true),
                Line(detail.Length > 0 ? detail : "Captured session history"),
                Line(resume.Length > 0 ? resume : "Open a session from Timeline to continue."),
                view));
        }
        if (agents.Count == 0)
        {
            _cards.Children.Add(Card(
                Line("No captured agents yet", title: true),
                Line("Nexus adds a harness here after its first session is harvested and reaches your account."),
                Line("Make sure capture is resumed from the tray and the workspace is selected.")));
        }
    }

}
