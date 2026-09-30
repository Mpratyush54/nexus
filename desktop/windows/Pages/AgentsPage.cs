using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class AgentsPage : NexusPage
{
    private readonly StackPanel _cards = new();

    public AgentsPage()
    {
        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var scroller = new ScrollViewer { Content = _cards };
        Grid.SetRow(scroller, 1);
        root.Children.Add(Line("Agents", title: true));
        root.Children.Add(scroller);
        Content = root;
        Loaded += async (_, _) => await LoadAsync();
    }

    private async Task LoadAsync()
    {
        var response = await Call(NxMethods.AgentsList);
        var reported = response.Ok
            ? NxJson.Items(response.Result).Select(item => NxJson.Text(item, "name", "agent", "harness")).ToHashSet(StringComparer.OrdinalIgnoreCase)
            : [];
        _cards.Children.Clear();
        if (!response.Ok && response.Error is { Length: > 0 })
        {
            _cards.Children.Add(Line(response.Error));
        }
        foreach (var harness in AgentCatalog.All)
        {
            var installed = reported.Contains(harness.Name);
            var configure = Action("Configure MCP", () => _ = ConfigureAsync(harness.Name));
            if (ShellState.Current.Offline)
            {
                configure.IsEnabled = false;
                ToolTipService.SetToolTip(configure, "Available when online");
            }
            var badge = harness.IdeHistory
                ? "Doesn't support resume; restores into chat history"
                : harness.Resume;
            _cards.Children.Add(Card(
                Line(harness.Name, title: true),
                Line(installed ? "Reported by the core" : "Not reported yet"),
                Line(badge),
                Line("Capture follows the project switch. There is no per-agent switch."),
                configure));
        }
    }

    private async Task ConfigureAsync(string agent)
    {
        var response = await Call(NxMethods.AgentsConfigureMcp, new { agent });
        _cards.Children.Insert(0, Line(response.Ok ? agent + " MCP config requested" : response.Error ?? "Configure failed"));
    }
}
