using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Shows agents that have actually connected to this desktop. It intentionally
/// does not present the complete supported-harness catalogue as a task list.
/// </summary>
public sealed class AgentsPage : NexusPage
{
    private readonly StackPanel _connections = new() { Spacing = 8 };
    private readonly TextBlock _status = Muted("Checking connected agents…");

    public AgentsPage()
    {
        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(PageHeading("Captured agents"));
        header.Children.Add(Muted("This shows harnesses with activity in your account, not a catalogue of agents Nexus merely supports."));

        var summary = Card(Line("Account capture", title: true), _status);

        var root = new Grid { Padding = new Thickness(28, 24, 28, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(summary, 1);
        var scroller = new ScrollViewer { Content = PageBody(_connections, 920) };
        Grid.SetRow(scroller, 2);
        root.Children.Add(header);
        root.Children.Add(summary);
        root.Children.Add(scroller);
        Content = root;
    }

    protected override void OnEnter(object? parameter) => _ = LoadAsync();

    private async Task LoadAsync()
    {
        var response = await Call(NxMethods.AgentsList);
        _connections.Children.Clear();
        if (!response.Ok)
        {
            _status.Text = response.Error ?? "Could not reach the local core";
            _connections.Children.Add(EmptyState(
                "Agent status is unavailable",
                "Nexus could not read local capture activity. Your existing cloud data is unchanged."));
            return;
        }

        var agents = NxJson.Items(response.Result).ToList();
        _status.Text = agents.Count == 0
            ? "No captured agent activity yet"
            : $"{agents.Count} captured agent{(agents.Count == 1 ? "" : "s")}";

        if (agents.Count == 0)
        {
            _connections.Children.Add(EmptyState(
                "No captured agents yet",
                "Open a supported coding agent in a project. Once Nexus captures a session, it appears here and in Timeline."));
            return;
        }

        foreach (var item in agents)
        {
            var name = NxJson.Text(item, "name", "agent", "harness", "id");
            var project = NxJson.Text(item, "project", "project_name", "folder_name");
            var state = NxJson.Text(item, "status", "capture_status");
            var resume = NxJson.Text(item, "resume", "resume_mode");
            var captured = NxJson.Text(item, "captured_count");
            var detail = string.Join(" · ", new[] { project, state, captured.Length > 0 ? captured + " captures" : "", resume }.Where(x => x.Length > 0));
            _connections.Children.Add(Card(
                Line(name.Length > 0 ? name : "Connected agent", title: true),
                Line(detail.Length > 0 ? detail : "Activity captured by Nexus")));
        }
    }
}
