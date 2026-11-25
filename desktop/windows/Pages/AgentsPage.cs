using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class AgentsPage : NexusPage
{
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly TextBlock _summary = new() { Opacity = 0.7, TextWrapping = TextWrapping.Wrap };
    private bool _loading;
    private readonly bool _hubChrome;

    public AgentsPage(bool hubChrome = false)
    {
        _hubChrome = hubChrome;
        var refresh = new Button { Content = "Refresh", MinWidth = 88 };
        refresh.Click += async (_, _) => await LoadAsync();
        var toolbar = Row(refresh);

        var root = new Grid
        {
            Padding = _hubChrome ? new Thickness(0) : new Thickness(32, 24, 32, 24),
            RowSpacing = 16,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 0 });
        var scroller = new ScrollViewer
        {
            Content = _cards,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        scroller.SetValue(FrameworkElement.MinHeightProperty, 0.0);
        var row = 0;
        if (!_hubChrome)
        {
            var header = PageHeader(
                "Agents & harnesses",
                "Capture status and session history for the project selected in the shell. Resume behavior follows each harness.");
            Grid.SetRow(header, row++);
            root.Children.Add(header);
        }
        Grid.SetRow(toolbar, row++);
        Grid.SetRow(_summary, row++);
        Grid.SetRow(scroller, row);
        root.Children.Add(toolbar);
        root.Children.Add(_summary);
        root.Children.Add(scroller);
        Content = root;
        WatchShellProject(() => LoadAsync());
    }

    protected override void OnEnter(object? parameter)
    {
        _ = LoadAsync();
    }

    private async Task LoadAsync()
    {
        if (_loading) return;
        var projectId = ShellProjectId();
        _cards.Children.Clear();
        if (projectId.Length == 0)
        {
            _summary.Text = "";
            _cards.Children.Add(Card(
                Line("Choose a project", title: true),
                Line("Choose a project in the nav pane to see which agent harnesses have captured sessions."),
                Line("After the first harvest completes, harnesses appear here with session counts and resume guidance.")));
            return;
        }

        _loading = true;
        _summary.Text = $"Loading agents for {ShellProjectLabel()}…";
        var status = await Call(NxMethods.StatusGet);
        var captureRunning = false;
        var capturePaused = false;
        if (status.Result is { } st && st.TryGetProperty("capture", out var capture))
        {
            captureRunning = NxJson.Text(capture, "running") == "true";
            capturePaused = NxJson.Text(capture, "paused") == "true";
        }

        var response = await Call(NxMethods.AgentsList, new { project_id = projectId });
        _loading = false;
        _cards.Children.Clear();
        if (!response.Ok)
        {
            _summary.Text = "";
            ShowError(_cards, response);
            return;
        }

        var agents = NxJson.Items(response.Result).ToList();
        var captureChip = capturePaused ? "Capture paused" :
            captureRunning ? "Capture active" : "Capture idle";
        _summary.Text = $"{Nexus.Core.FriendlyTime.Count(agents.Count, "agent")} with chats · {ShellProjectLabel()} · {captureChip}";

        if (agents.Count == 0)
        {
            _cards.Children.Add(Card(
                Line("No captured agents yet", title: true),
                Line("Nexus lists a harness here after its first session is harvested for this project."),
                Line("Confirm capture is on in Settings (per project) and resumed from the tray, then run a session in Cursor, Codex, Claude Code, or another supported harness.")));
            _cards.Children.Add(SectionHeader("Supported harnesses"));
            foreach (var harness in AgentCatalog.All.Take(8))
            {
                _cards.Children.Add(ListItem(
                    Line(harness.Name, title: true),
                    Muted("Waiting for first captured session"),
                    Muted("Resume: " + harness.Resume)));
            }
            await AppendSpawnedSessionsAsync();
            return;
        }

        foreach (var agent in agents
                     .OrderByDescending(CapturedCount)
                     .ThenBy(a => NxJson.Text(a, "name", "agent", "harness"), StringComparer.OrdinalIgnoreCase))
        {
            var name = NxJson.Text(agent, "name", "agent", "harness");
            var harness = NxJson.Text(agent, "harness");
            if (harness.Length == 0) harness = name;
            var catalog = FindCatalog(harness, name);
            var statusText = NxJson.Text(agent, "status");
            var resume = NxJson.Text(agent, "resume_mode");
            if (resume.Length == 0 && catalog.HasValue)
            {
                resume = catalog.Value.Resume;
            }
            var count = NxJson.Text(agent, "captured_count", "count");
            var last = FormatLastActivity(NxJson.Text(agent, "last_activity", "last_seen", "updated_at"));
            var view = Action("View sessions", () => MainWindow.Current?.ShowAgentSessions(harness, projectId));
            var chips = Row(
                Chip(statusText.Length > 0 ? statusText : "Captured"),
                Chip(count.Length > 0 ? count + " sessions" : "Sessions available"));
            if (last.Length > 0)
            {
                chips.Children.Add(Chip("Last activity · " + last));
            }
            _cards.Children.Add(Card(
                Line(name.Length > 0 ? name : harness, title: true),
                chips,
                Line(resume.Length > 0 ? "Resume: " + resume : "Open Timeline to continue a session."),
                view));
        }
        await AppendSpawnedSessionsAsync();
    }

    private async Task AppendSpawnedSessionsAsync()
    {
        var response = await Call(NxMethods.TimelineList, new { project_id = ShellProjectId(), limit = 50, include_subagents = true });
        if (!response.Ok)
        {
            return;
        }
        var spawned = NxJson.Items(response.Result).Where(SessionLineage.IsSubagentSession).Take(24).ToList();
        if (spawned.Count == 0)
        {
            return;
        }
        _cards.Children.Add(SectionHeader("Sessions spawned by agents"));
        _cards.Children.Add(Muted("Sub-agent runs from Cursor and other harnesses — not shown in the main Sessions feed."));
        var projectId = ShellProjectId();
        foreach (var item in spawned)
        {
            if (!ProjectScope.TimelineItemMatches(item, projectId))
            {
                continue;
            }
            var id = NxJson.Text(item, "session_id", "id", "sessionId");
            var title = NxJson.Text(item, "title", "native_id", "summary");
            var harness = NxJson.Text(item, "harness", "agent");
            var when = NxJson.Text(item, "last_active_at");
            var open = Action("Open session", () => MainWindow.Current?.OpenSession(id));
            open.IsEnabled = id.Length > 0;
            _cards.Children.Add(ListItem(
                Line(title.Length > 0 ? title : id, title: true),
                Muted(string.Join(" · ", new[] { harness, when }.Where(p => p.Length > 0))),
                open));
        }
    }

    private static int CapturedCount(System.Text.Json.JsonElement agent)
    {
        var text = NxJson.Text(agent, "captured_count", "count");
        return int.TryParse(text, out var count) ? count : 0;
    }

    private static string FormatLastActivity(string raw) =>
        DateTimeOffset.TryParse(raw, out var at) ? at.ToLocalTime().ToString("MMM d, h:mm tt") : raw;

    private static AgentCatalog.Harness? FindCatalog(string harness, string name)
    {
        foreach (var item in AgentCatalog.All)
        {
            if (item.Name.Equals(harness, StringComparison.OrdinalIgnoreCase) ||
                item.Name.Equals(name, StringComparison.OrdinalIgnoreCase))
            {
                return item;
            }
        }
        return null;
    }
}
