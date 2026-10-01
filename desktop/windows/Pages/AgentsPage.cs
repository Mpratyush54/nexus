using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class AgentsPage : NexusPage
{
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };

    public AgentsPage()
    {
        var refresh = new Button { Content = "Refresh", MinWidth = 88 };
        refresh.Click += async (_, _) => await LoadAsync();

        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(Line("Captured agents", title: true));
        header.Children.Add(Line("Only harnesses with sessions visible to this account appear here."));

        var root = new Grid { Padding = new Thickness(32, 24, 32, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var scroller = new ScrollViewer { Content = _cards, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled };
        Grid.SetRow(refresh, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(header);
        root.Children.Add(refresh);
        root.Children.Add(scroller);
        Content = root;
        Loaded += async (_, _) => await LoadAsync();
    }

    private async Task LoadAsync()
    {
        var response = await Call(NxMethods.AgentsList);
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
            var project = NxJson.Text(agent, "project_id", "project");
            var status = NxJson.Text(agent, "status");
            var resume = NxJson.Text(agent, "resume_mode");
            var count = NxJson.Text(agent, "captured_count", "count");
            var configure = Action("Configure MCP", () => _ = ConfigureAsync(harness.Length > 0 ? harness : name));
            if (ShellState.Current.Offline)
            {
                configure.IsEnabled = false;
                ToolTipService.SetToolTip(configure, "Available when online");
            }
            var detail = string.Join(" · ", new[] { status, count.Length > 0 ? count + " sessions" : "", project }.Where(x => x.Length > 0));
            _cards.Children.Add(Card(
                Line(name.Length > 0 ? name : "Unknown harness", title: true),
                Line(detail.Length > 0 ? detail : "Captured session history"),
                Line(resume.Length > 0 ? resume : "Open a session from Timeline to continue."),
                configure));
        }
        if (agents.Count == 0)
        {
            _cards.Children.Add(Card(
                Line("No captured agents yet", title: true),
                Line("Nexus adds a harness here after its first session is harvested and reaches your account."),
                Line("Make sure capture is resumed from the tray and the workspace is selected.")));
        }
    }

    private async Task ConfigureAsync(string agent)
    {
        var response = await Call(NxMethods.AgentsConfigureMcp, new { agent });
        _cards.Children.Insert(0, Line(response.Ok ? agent + " MCP configuration requested" : response.Error ?? "Configure failed"));
    }
}
