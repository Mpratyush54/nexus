using System.Text.Json;
using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

/// <summary>
/// Agents browser: every coding agent on this computer (left), what it is set
/// up with by kind (middle) and the selected item's details and read-only
/// content (right). MCP servers show env/header names only; values are
/// masked by the core. Results are cached and shown instantly, then
/// refreshed in the background.
/// </summary>
public sealed partial class AgentSetupPage : NexusPage
{
    private const string BrowseMethod = "agents.setup.browse";
    private const string ReadMethod = "agents.setup.read";
    private const string RemoveMethod = "agents.setup.mcp_remove";

    private static readonly (string Key, string Kind, string Title, string Short, string Empty)[] Sections =
    {
        ("subagents", "subagent", "Sub-agents", "sub-agents", "No sub-agents."),
        ("skills", "skill", "Skills", "skills", "No skills."),
        ("commands", "command", "Commands", "commands", "No custom commands."),
        ("mcp_servers", "mcp", "Tools", "tools", "No tool servers (MCP) set up."),
        ("rules", "rules", "Rules", "rules", "No rules or instruction files."),
    };

    // Agents the share flow (agents.setup.share) can read from and write to.
    private static readonly HashSet<string> ShareAgents = new(StringComparer.OrdinalIgnoreCase) { "claude", "codex", "cursor", "opencode", "gemini" };

    private static readonly FontFamily MonoFont = new("Cascadia Mono, Consolas");
    private static readonly string CacheFile = System.IO.Path.Combine(
        Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "agents-browse.json");

    private readonly TextBlock _status = new() { FontSize = 11.5, Opacity = 0.55, TextWrapping = TextWrapping.NoWrap, TextTrimming = TextTrimming.CharacterEllipsis, VerticalAlignment = VerticalAlignment.Center };
    private readonly TextBox _search = new() { PlaceholderText = "Search skills, sub-agents and tools", FontSize = 12.5, MinWidth = 240, MaxWidth = 380 };
    private readonly StackPanel _agentList = new() { Spacing = 1, Padding = new Thickness(8, 6, 8, 12) };
    private readonly TextBlock _middleTitle = new() { FontSize = 13, FontWeight = FontWeights.SemiBold, Margin = new Thickness(4, 0, 0, 0) };
    private readonly StackPanel _segments = new() { Orientation = Orientation.Horizontal, Spacing = 2 };
    // The middle list is virtualized: only rows in view are built.
    private readonly ItemsRepeater _itemList = new() { Margin = new Thickness(6, 4, 8, 12), Layout = new StackLayout { Spacing = 1 } };
    private readonly List<object> _rows = new();
    private RowFactory? _rowFactory;
    private readonly StackPanel _detail = new() { Spacing = 12, Padding = new Thickness(20, 14, 20, 24) };

    // Middle-list rows: a section label, a muted note, an agent item or a sub-agent run.
    private sealed record LabelRow(string Text);
    private sealed record NoteRow(string Text);
    private sealed record ItemRowData(string Agent, JsonElement Item, bool ShowAgent);
    private sealed record RunRow(SubagentRuns.Run Run);

    // The left pane's "Sub-agent runs" entry uses this id in _selectedAgent.
    private const string RunsKey = "__subagent_runs";
    private List<SubagentRuns.Run>? _runs;
    private string _runsProject = "";
    private bool _runsLoading;

    // Resolving a project's folder can take network calls (projects.list,
    // status.get); it is done once per project per app run.
    private static readonly Dictionary<string, string> s_workspaces = new(StringComparer.OrdinalIgnoreCase);

    private readonly Dictionary<string, string> _agentNames = new(StringComparer.OrdinalIgnoreCase);
    private readonly HashSet<string> _installed = new(StringComparer.OrdinalIgnoreCase);
    private readonly Dictionary<string, JsonElement> _contents = new();
    private List<JsonElement> _agents = new();
    private string _raw = "";
    private string _workspace = "";
    private string _selectedAgent = "";
    private string _selectedSection = "subagents";
    private string _selectedItem = "";
    private bool _showSource;
    private bool _loading;
    private int _version;
    private int _detailVersion;

    public AgentSetupPage()
    {
        var title = NexusTokens.PageTitle("Agents");
        title.VerticalAlignment = VerticalAlignment.Center;
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_search, "Search agents");
        // One primary action (connect); refresh and removal live under "More".
        var refresh = new Button { Content = new FontIcon { Glyph = "\uE72C", FontSize = 14 }, Padding = new Thickness(8), MinWidth = 34, Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0) };
        ToolTipService.SetToolTip(refresh, "Scan agent folders again");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(refresh, "Refresh agents");
        refresh.Click += async (_, _) =>
        {
            _contents.Clear();
            if (_selectedAgent == RunsKey) { _runs = null; RenderMiddle(); }
            await LoadAsync();
        };
        _search.TextChanged += (_, _) => RenderMiddle();
        _itemList.ItemTemplate = _rowFactory = new RowFactory(BuildRow);
        _itemList.ItemsSource = new List<object>();

        var top = new Grid { ColumnSpacing = 12 };
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        top.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        top.Children.Add(title);
        Grid.SetColumn(_search, 1);
        _search.HorizontalAlignment = HorizontalAlignment.Left;
        top.Children.Add(_search);
        Grid.SetColumn(refresh, 2);
        top.Children.Add(refresh);
        var install = InstallBar();
        Grid.SetColumn((FrameworkElement)install, 3);
        ((FrameworkElement)install).Margin = new Thickness(0);
        top.Children.Add(install);

        var header = new StackPanel { Spacing = 6, Padding = new Thickness(20, 14, 20, 10) };
        header.Children.Add(top);
        header.Children.Add(_status);

        var middleHead = new StackPanel { Spacing = 8, Padding = new Thickness(10, 10, 10, 6) };
        middleHead.Children.Add(_middleTitle);
        middleHead.Children.Add(new ScrollViewer
        {
            Content = _segments, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto,
            VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled
        });
        var middle = new Grid();
        middle.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        middle.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        middle.Children.Add(middleHead);
        var itemScroll = Scroll(_itemList);
        Grid.SetRow(itemScroll, 1);
        middle.Children.Add(itemScroll);

        var body = new Grid();
        var left = Pane(Scroll(_agentList));
        var mid = Pane(middle);
        var right = Scroll(_detail);
        body.Children.Add(left);
        body.Children.Add(mid);
        body.Children.Add(right);
        // Responsive: three columns when wide, agents+items stacked beside the
        // detail when medium, everything stacked when narrow.
        var bodyMode = -1;
        body.SizeChanged += (_, e) =>
        {
            var w = e.NewSize.Width;
            var mode = w >= 940 ? 3 : w >= 600 ? 2 : 1;
            if (mode == 3)
            {
                // Lists take a share of the width within bounds; the detail keeps the rest.
                var leftW = Math.Clamp(w * 0.2, 180, 260);
                var midW = Math.Clamp(w * 0.3, 260, 400);
                if (mode == bodyMode && body.ColumnDefinitions.Count == 3)
                {
                    body.ColumnDefinitions[0].Width = new GridLength(leftW);
                    body.ColumnDefinitions[1].Width = new GridLength(midW);
                    return;
                }
                bodyMode = mode;
                body.RowDefinitions.Clear();
                body.ColumnDefinitions.Clear();
                body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(leftW) });
                body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(midW) });
                body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star), MinWidth = 320 });
                Place(left, 0, 0); Place(mid, 0, 1); Place(right, 0, 2);
                left.BorderThickness = mid.BorderThickness = new Thickness(0, 0, 1, 0);
                return;
            }
            if (mode == bodyMode) return;
            bodyMode = mode;
            body.RowDefinitions.Clear();
            body.ColumnDefinitions.Clear();
            if (mode == 2)
            {
                body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(2, GridUnitType.Star), MinWidth = 240, MaxWidth = 400 });
                body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(3, GridUnitType.Star), MinWidth = 300 });
                body.RowDefinitions.Add(new RowDefinition { Height = new GridLength(2, GridUnitType.Star) });
                body.RowDefinitions.Add(new RowDefinition { Height = new GridLength(3, GridUnitType.Star) });
                Place(left, 0, 0); Place(mid, 1, 0); Place(right, 0, 1);
                Grid.SetRowSpan(right, 2);
                left.BorderThickness = new Thickness(0, 0, 1, 1);
                mid.BorderThickness = new Thickness(0, 0, 1, 0);
            }
            else
            {
                body.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 120 });
                body.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 140 });
                body.RowDefinitions.Add(new RowDefinition { Height = new GridLength(2, GridUnitType.Star), MinHeight = 160 });
                Place(left, 0, 0); Place(mid, 1, 0); Place(right, 2, 0);
                left.BorderThickness = mid.BorderThickness = new Thickness(0, 0, 0, 1);
            }
        };

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.Children.Add(header);
        var bodyFrame = new Border { BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 1, 0, 0), Child = body };
        Grid.SetRow(bodyFrame, 1);
        root.Children.Add(bodyFrame);
        Content = root;
        WatchShellProject(() => LoadAsync());
        Loaded += (_, _) => { AgentAvailabilityService.Changed += AgentsChanged; AgentAvailabilityService.Refresh(); };
        Unloaded += (_, _) => AgentAvailabilityService.Changed -= AgentsChanged;
    }

    private void AgentsChanged() => DispatcherQueue.TryEnqueue(() => { if (_agents.Count > 0) RenderAgents(); });

    protected override void OnEnter(object? parameter)
    {
        var watch = System.Diagnostics.Stopwatch.StartNew();
        if (_agents.Count == 0) LoadDiskCache();
        System.Diagnostics.Trace.WriteLine($"[perf] agents: enter-to-cached-view {watch.ElapsedMilliseconds} ms ({_agents.Count} agents)");
        _ = LoadAsync();
    }

    // ── Loading & cache ────────────────────────────────────────────────

    private void LoadDiskCache()
    {
        try
        {
            if (!System.IO.File.Exists(CacheFile)) return;
            using var doc = JsonDocument.Parse(System.IO.File.ReadAllText(CacheFile));
            Apply(doc.RootElement.Clone());
            _status.Text = "Showing the last scan · refreshing…";
        }
        catch { /* a bad cache just means no instant view */ }
    }

    private async Task<string> WorkspaceAsync()
    {
        var project = ShellProjectId();
        if (project.Length == 0) return "";
        lock (s_workspaces)
            if (s_workspaces.TryGetValue(project, out var known)) return known;
        var folder = await ProjectFolders.ResolveAsync(Core, project, ShellProjectLabel());
        lock (s_workspaces) s_workspaces[project] = folder;
        return folder;
    }

    private async Task LoadAsync()
    {
        var version = ++_version;
        var watch = System.Diagnostics.Stopwatch.StartNew();
        _loading = true;
        if (_agents.Count == 0) _status.Text = "Reading agent setup…";
        _workspace = await WorkspaceAsync();
        var resolved = watch.ElapsedMilliseconds;
        var response = await Core.CallAsync(BrowseMethod, new { workspace = _workspace });
        await UiThread.Resume(DispatcherQueue);
        var browsed = watch.ElapsedMilliseconds;
        if (version != _version) return;
        _loading = false;
        if (!response.Ok || response.Result is not { } inv)
        {
            _status.Text = response.Error ?? "Could not read agent setup.";
            return;
        }
        var raw = inv.GetRawText();
        var changed = raw != _raw;
        if (changed)
        {
            _contents.Clear();
            Apply(inv.Clone());
            _ = Task.Run(() =>
            {
                try
                {
                    System.IO.Directory.CreateDirectory(System.IO.Path.GetDirectoryName(CacheFile)!);
                    System.IO.File.WriteAllText(CacheFile, raw);
                }
                catch { }
            });
        }
        var connected = _agents.Count(a => Flag(a, "installed") && Flag(a, "nexus_mcp"));
        _status.Text = _installed.Count == 0 ? "No coding agents found on this computer yet."
            : connected == _installed.Count ? $"All {_installed.Count} agents on this computer are connected to Nexus."
            : $"{connected} of {_installed.Count} agents on this computer are connected to Nexus.";
        ToolTipService.SetToolTip(_status, (_workspace.Length > 0 ? "Project folder: " + _workspace : "Settings for your user account (pick a project to include its folder)") +
            "\nChecked at " + DateTime.Now.ToString("t"));
        System.Diagnostics.Trace.WriteLine($"[perf] agents: workspace {resolved} ms, browse {browsed - resolved} ms, render {watch.ElapsedMilliseconds - browsed} ms (changed={changed}, {raw.Length / 1024} KB)");
    }

    private void Apply(JsonElement inventory)
    {
        _raw = inventory.GetRawText();
        _agentNames.Clear();
        _installed.Clear();
        var agents = Arr(inventory, "agents").ToList();
        foreach (var agent in agents)
        {
            var id = NxJson.Text(agent, "agent");
            _agentNames[id] = NxJson.Text(agent, "name");
            if (Flag(agent, "installed")) _installed.Add(id);
        }
        // Installed first; otherwise keep the core's order.
        _agents = agents.Select((a, i) => (a, i)).OrderBy(x => Flag(x.a, "installed") ? 0 : 1).ThenBy(x => x.i).Select(x => x.a).ToList();
        if (_selectedAgent.Length == 0 || (_selectedAgent != RunsKey && !_agentNames.ContainsKey(_selectedAgent)))
            _selectedAgent = _agents.Count > 0 ? NxJson.Text(_agents[0], "agent") : "";
        RenderAgents();
        RenderMiddle();
        // The lists paint first; the selected item's content follows.
        if (DispatcherQueue is { } queue) queue.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low, RenderDetail);
        else RenderDetail();
    }

    // ── Left: agents ───────────────────────────────────────────────────

    private void RenderAgents()
    {
        _agentList.Children.Clear();
        _agentList.Children.Add(SectionLabel("Coding agents"));
        foreach (var agent in _agents)
        {
            var id = NxJson.Text(agent, "agent");
            var installed = Flag(agent, "installed");
            var nexus = Flag(agent, "nexus_mcp");
            var version = NxJson.Text(agent, "version");

            var text = new StackPanel { Spacing = 1 };
            var nameRow = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
            nameRow.Children.Add(new TextBlock { Text = _agentNames.GetValueOrDefault(id, id), FontSize = 12.5, FontWeight = id == _selectedAgent ? FontWeights.SemiBold : FontWeights.Normal, Opacity = installed ? 1 : 0.55 });
            if (version.Length > 0) nameRow.Children.Add(new TextBlock { Text = version, FontSize = 10.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center });
            text.Children.Add(nameRow);

            var state = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 5 };
            state.Children.Add(Dot(installed ? NexusPalette.Teal : NexusPalette.Muted));
            state.Children.Add(Tiny(installed ? "Installed" : "Not installed"));
            if (nexus)
            {
                state.Children.Add(Tiny("·"));
                state.Children.Add(new TextBlock { Text = "Connected", FontSize = 10.5, Foreground = NexusPalette.Accent });
            }
            else if (installed) state.Children.Add(Tiny("· Not connected"));
            text.Children.Add(state);
            // Agents that can't run chats here are left out of the chat
            // pickers; this is where they get set up.
            if (AgentAvailabilityService.For(id) is { Usable: false } chat)
            {
                var setup = new HyperlinkButton { Content = "Set up for chats", Padding = new Thickness(0), FontSize = 11, MinHeight = 0 };
                ToolTipService.SetToolTip(setup, chat.Problem);
                Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(setup, "Set up " + chat.Name + " for chats");
                setup.Click += async (_, _) =>
                {
                    if (await AgentProblemDialog.ShowAsync(XamlRoot, chat, canUseAnother: false) == AgentProblemDialog.Choice.Fix)
                        _status.Text = AgentAvailability.FixSentence(chat);
                };
                var line = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 5 };
                line.Children.Add(Tiny(chat.State == "signed_out" ? "Not signed in ·" : "Can't run chats yet ·"));
                line.Children.Add(setup);
                text.Children.Add(line);
            }

            var counts = Sections.Select(s => (s.Short, N: Arr(agent, s.Key).Count())).Where(c => c.N > 0).Select(c => c.N + " " + c.Short).ToList();
            text.Children.Add(Tiny(counts.Count > 0 ? string.Join(" · ", counts) : "No skills or tools yet"));

            var button = RowButton(text, id == _selectedAgent);
            button.Click += (_, _) =>
            {
                _selectedAgent = id;
                _selectedItem = "";
                if (_search.Text.Length > 0) _search.Text = "";
                RenderAgents();
                RenderMiddle();
                RenderDetail();
            };
            _agentList.Children.Add(button);
        }

        // Sub-agent runs: chats an agent started from another chat. They are
        // hidden from the chat list, so they are reachable from here.
        _agentList.Children.Add(SectionLabel("Activity", new Thickness(6, 14, 0, 4)));
        var runsText = new StackPanel { Spacing = 1 };
        runsText.Children.Add(new TextBlock { Text = "Sub-agent runs", FontSize = 12.5, FontWeight = _selectedAgent == RunsKey ? FontWeights.SemiBold : FontWeights.Normal });
        runsText.Children.Add(Tiny(_runs is { } runs ? runs.Count + " recent chats started by agents" : "Chats your agents started from a chat"));
        var runsButton = RowButton(runsText, _selectedAgent == RunsKey);
        runsButton.Click += (_, _) =>
        {
            _selectedAgent = RunsKey;
            _selectedItem = "";
            if (_search.Text.Length > 0) _search.Text = "";
            RenderAgents();
            RenderMiddle();
            RenderDetail();
        };
        _agentList.Children.Add(runsButton);
    }

    // ── Middle: items ──────────────────────────────────────────────────

    private JsonElement? Agent(string id) => _agents.Where(a => NxJson.Text(a, "agent") == id).Select(a => (JsonElement?)a).FirstOrDefault();

    private void RenderMiddle()
    {
        _segments.Children.Clear();
        _rows.Clear();
        var query = _search.Text.Trim();
        if (query.Length > 0)
        {
            RenderSearch(query);
        }
        else if (_selectedAgent == RunsKey)
        {
            RenderRuns();
        }
        else if (Agent(_selectedAgent) is not { } agent)
        {
            _middleTitle.Text = _loading ? "Loading…" : "No agents found";
        }
        else
        {
            _middleTitle.Text = _agentNames.GetValueOrDefault(_selectedAgent, _selectedAgent);
            foreach (var (key, _, title, _, _) in Sections)
            {
                var n = Arr(agent, key).Count();
                var seg = new Button
                {
                    Content = new TextBlock { Text = n > 0 ? title + "  " + n : title, FontSize = 11.5, Opacity = key == _selectedSection ? 1 : 0.7 },
                    Padding = new Thickness(9, 3, 9, 4), MinHeight = 24, CornerRadius = new CornerRadius(5), BorderThickness = new Thickness(0),
                    Background = key == _selectedSection ? SessionChatRender.SubtleFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent)
                };
                seg.Click += (_, _) => { _selectedSection = key; _selectedItem = ""; RenderMiddle(); RenderDetail(); };
                _segments.Children.Add(seg);
            }
            var section = Sections.First(s => s.Key == _selectedSection);
            var items = Arr(agent, section.Key).ToList();
            if (items.Count == 0)
            {
                _rows.Add(new NoteRow(section.Key == "subagents" ? NoSubagents(_selectedAgent) : section.Empty));
                if (!Flag(agent, "installed")) _rows.Add(new NoteRow("This agent is not installed on this computer."));
            }
            else
            {
                if (_selectedItem.Length == 0) _selectedItem = ItemKey(_selectedAgent, items[0]);
                foreach (var item in items) _rows.Add(new ItemRowData(_selectedAgent, item, false));
            }
        }
        RefreshRows();
    }

    // Where each agent keeps sub-agent definitions, for the empty state.
    private string NoSubagents(string agent)
    {
        var name = _agentNames.GetValueOrDefault(agent, agent);
        return agent switch
        {
            "claude" => "No sub-agents defined for Claude Code. Sub-agents live in ~/.claude/agents/*.md (or .claude/agents/*.md in a project), or come with an installed Claude Code plugin. Claude Code's built-in sub-agents (general-purpose, Explore, Plan) need no file.",
            "opencode" => "No sub-agents defined for OpenCode. They live in ~/.config/opencode/agent/*.md, a project's .opencode/agent/*.md, or the \"agent\" section of opencode.json.",
            "copilot" => "No custom agents defined for GitHub Copilot CLI. They live in ~/.copilot/agents/*.md or a project's .github/agents/*.agent.md.",
            _ => "Nexus does not read sub-agent definitions for " + name + ". Chats an agent started are under Activity → Sub-agent runs."
        };
    }

    private void RefreshRows()
    {
        // A fresh list instance makes the repeater re-read every row.
        _itemList.ItemsSource = _rows.ToList();
    }

    private void RenderSearch(string query)
    {
        _middleTitle.Text = "Search: " + query;
        var hits = 0;
        foreach (var (key, _, title, _, _) in Sections)
        {
            var matches = new List<(string Agent, JsonElement Item)>();
            foreach (var agent in _agents)
            {
                var id = NxJson.Text(agent, "agent");
                foreach (var item in Arr(agent, key))
                    if (Matches(item, query) || _agentNames.GetValueOrDefault(id, id).Contains(query, StringComparison.OrdinalIgnoreCase))
                        matches.Add((id, item));
            }
            if (matches.Count == 0) continue;
            _rows.Add(new LabelRow(title + " · " + matches.Count));
            foreach (var (id, item) in matches) _rows.Add(new ItemRowData(id, item, true));
            hits += matches.Count;
        }
        if (hits == 0) _rows.Add(new NoteRow("Nothing matches."));
    }

    private static bool Matches(JsonElement item, string q)
    {
        foreach (var field in new[] { "name", "description", "path", "command", "url", "scope", "kind" })
            if (NxJson.Text(item, field).Contains(q, StringComparison.OrdinalIgnoreCase)) return true;
        foreach (var field in new[] { "args", "env_keys", "header_keys" })
            if (Arr(item, field).Any(v => (v.GetString() ?? "").Contains(q, StringComparison.OrdinalIgnoreCase))) return true;
        return false;
    }

    private static string ItemKey(string agent, JsonElement item) =>
        string.Join("|", agent, NxJson.Text(item, "kind"), NxJson.Text(item, "name"), NxJson.Text(item, "scope"), NxJson.Text(item, "path"));

    // Builds one realized row of the virtualized middle list.
    private UIElement BuildRow(object data) => data switch
    {
        LabelRow label => SectionLabel(label.Text),
        NoteRow note => Tiny(note.Text, new Thickness(6, 8, 6, 0)),
        ItemRowData row => ItemRow(row.Agent, row.Item, row.ShowAgent),
        RunRow run => RunRowView(run.Run),
        _ => new Border()
    };

    private void HighlightRows(string key)
    {
        foreach (var host in _rowFactory?.Live ?? Enumerable.Empty<ContentControl>())
            if (host.Content is Button { Tag: string tag } button)
                button.Background = tag == key ? SessionChatRender.SubtleFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
    }

    private UIElement ItemRow(string agent, JsonElement item, bool showAgent)
    {
        var key = ItemKey(agent, item);
        var kind = NxJson.Text(item, "kind");
        var text = new StackPanel { Spacing = 1 };
        var nameRow = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        nameRow.Children.Add(new TextBlock { Text = NxJson.Text(item, "name"), FontSize = 12.5, FontWeight = key == _selectedItem ? FontWeights.SemiBold : FontWeights.Normal, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 250 });
        if (kind == "mcp" && NxJson.Text(item, "name") == "nexus") nameRow.Children.Add(new TextBlock { Text = "Nexus", FontSize = 10.5, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center });
        text.Children.Add(nameRow);
        var desc = kind == "mcp" ? McpSummary(item) : Flatten(NxJson.Text(item, "description"));
        if (desc.Length > 0) text.Children.Add(new TextBlock { Text = desc, FontSize = 11.5, Foreground = NexusPalette.Muted, MaxLines = 1, TextTrimming = TextTrimming.CharacterEllipsis });
        var shared = Arr(item, "shared_with").Count();
        var meta = new List<string>();
        if (showAgent) meta.Add(_agentNames.GetValueOrDefault(agent, agent));
        meta.Add(NxJson.Text(item, "scope"));
        if (shared > 0) meta.Add("shared to " + shared + (shared == 1 ? " agent" : " agents"));
        meta.Add(System.IO.Path.GetFileName(NxJson.Text(item, "path")));
        text.Children.Add(new TextBlock { Text = string.Join("  ·  ", meta.Where(m => m.Length > 0)), FontSize = 10.5, Opacity = 0.5, TextTrimming = TextTrimming.CharacterEllipsis });

        var button = RowButton(text, key == _selectedItem);
        button.Tag = key;
        var copy = item.Clone();
        button.Click += (_, _) =>
        {
            _selectedItem = key;
            if (showAgent) _selectedAgent = agent;
            HighlightRows(key);
            RenderDetail(agent, copy);
        };
        return button;
    }

    // ── Sub-agent runs ─────────────────────────────────────────────────

    private void RenderRuns()
    {
        _middleTitle.Text = "Sub-agent runs";
        var project = ShellProjectId();
        if (_runs is null || _runsProject != project)
        {
            _rows.Add(new NoteRow("Finding sub-agent runs…"));
            _ = LoadRunsAsync(project);
            return;
        }
        if (_runs.Count == 0)
        {
            _rows.Add(new NoteRow("No sub-agent runs captured" + (project.Length > 0 ? " for this project" : "") + " yet. When an agent starts a sub-agent (Claude Code's Task/Agent tool, Cursor sub-agents) and capture is on, its chat shows up here."));
            return;
        }
        _rows.Add(new NoteRow("Chats an agent started from another chat. They are hidden from the chat list; open one here, or from the parent chat's work log."));
        foreach (var run in _runs) _rows.Add(new RunRow(run));
    }

    private async Task LoadRunsAsync(string project)
    {
        if (_runsLoading) return;
        _runsLoading = true;
        List<SubagentRuns.Run> runs;
        try { runs = await SubagentRuns.RecentAsync(Core, project); }
        catch { runs = new List<SubagentRuns.Run>(); }
        await UiThread.Resume(DispatcherQueue);
        _runsLoading = false;
        _runs = runs;
        _runsProject = project;
        RenderAgents();
        if (_selectedAgent == RunsKey && _search.Text.Trim().Length == 0)
        {
            RenderMiddle();
            RenderDetail();
        }
    }

    private UIElement RunRowView(SubagentRuns.Run run)
    {
        var key = "run|" + run.Id;
        var text = new StackPanel { Spacing = 1 };
        text.Children.Add(new TextBlock { Text = run.Title.Length > 0 ? run.Title : "Sub-agent run", FontSize = 12.5, FontWeight = key == _selectedItem ? FontWeights.SemiBold : FontWeights.Normal, TextTrimming = TextTrimming.CharacterEllipsis });
        if (run.ParentTitle.Length > 0)
            text.Children.Add(new TextBlock { Text = "from " + run.ParentTitle, FontSize = 11.5, Foreground = NexusPalette.Muted, MaxLines = 1, TextTrimming = TextTrimming.CharacterEllipsis });
        var meta = new[] { run.AgentType, _agentNames.GetValueOrDefault(run.Agent, run.Agent), run.When }.Where(m => m.Length > 0);
        text.Children.Add(new TextBlock { Text = string.Join("  ·  ", meta), FontSize = 10.5, Opacity = 0.5, TextTrimming = TextTrimming.CharacterEllipsis });
        var button = RowButton(text, key == _selectedItem);
        button.Tag = key;
        button.Click += (_, _) =>
        {
            _selectedItem = key;
            HighlightRows(key);
            RenderRunDetail(run);
        };
        button.DoubleTapped += (_, _) => MainWindow.Current?.OpenSession(run.Id);
        return button;
    }

    private void RenderRunDetail(SubagentRuns.Run run)
    {
        ++_detailVersion;
        _detail.Children.Clear();
        var head = new StackPanel { Spacing = 3 };
        head.Children.Add(new TextBlock { Text = run.Title.Length > 0 ? run.Title : "Sub-agent run", FontSize = 16, FontWeight = FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        head.Children.Add(Tiny(string.Join(" · ", new[] { "Sub-agent run", run.AgentType, _agentNames.GetValueOrDefault(run.Agent, run.Agent), run.When }.Where(m => m.Length > 0))));
        head.Children.Add(Tiny(run.ParentTitle.Length > 0 ? "Started from: " + run.ParentTitle
            : run.ParentNativeId.Length > 0 ? "Started from a chat that is not captured in this project (" + run.ParentNativeId + ")."
            : "The parent chat is not known for this run.", new Thickness(0, 4, 0, 0)));
        _detail.Children.Add(head);
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4 };
        var open = FlatButton("Open sub-agent chat");
        open.Click += (_, _) => MainWindow.Current?.OpenSession(run.Id);
        row.Children.Add(open);
        if (run.ParentId.Length > 0)
        {
            var parent = FlatButton("Open parent chat");
            parent.Click += (_, _) => MainWindow.Current?.OpenSession(run.ParentId);
            row.Children.Add(parent);
        }
        _detail.Children.Add(row);
    }

    // ── Right: details ─────────────────────────────────────────────────

    private void RenderDetail()
    {
        if (_search.Text.Trim().Length == 0 && _selectedAgent == RunsKey)
        {
            if (_runs?.FirstOrDefault(r => "run|" + r.Id == _selectedItem) is { } run) { RenderRunDetail(run); return; }
            _detail.Children.Clear();
            _detail.Children.Add(Tiny(_runs is { Count: > 0 } ? "Select a run to open its chat or the chat that started it." : "", new Thickness(0, 6, 0, 0)));
            return;
        }
        if (_search.Text.Trim().Length == 0 && Agent(_selectedAgent) is { } agent)
        {
            var section = Sections.First(s => s.Key == _selectedSection);
            foreach (var item in Arr(agent, section.Key))
                if (ItemKey(_selectedAgent, item) == _selectedItem) { RenderDetail(_selectedAgent, item.Clone()); return; }
        }
        _detail.Children.Clear();
        _detail.Children.Add(Tiny(_agents.Count == 0 ? "" : "Pick an item to see what it does and the file behind it.", new Thickness(0, 6, 0, 0)));
    }

    private void RenderDetail(string agent, JsonElement item)
    {
        var version = ++_detailVersion;
        _detail.Children.Clear();
        var kind = NxJson.Text(item, "kind");
        var name = NxJson.Text(item, "name");
        var path = NxJson.Text(item, "path");
        var scope = NxJson.Text(item, "scope");
        var kindTitle = Sections.FirstOrDefault(s => s.Kind == kind).Title ?? kind;

        var head = new StackPanel { Spacing = 3 };
        head.Children.Add(new TextBlock { Text = name, FontSize = 16, FontWeight = FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        var scopeText = scope switch { "project" => "project scope", "plugin" => "from a Claude Code plugin", _ => "user scope" };
        head.Children.Add(Tiny(kindTitle + " · " + scopeText + " · " + _agentNames.GetValueOrDefault(agent, agent)));
        var desc = NxJson.Text(item, "description");
        if (desc.Length > 0 && kind != "mcp") head.Children.Add(new TextBlock { Text = desc, FontSize = 12.5, Opacity = 0.8, TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 4, 0, 0) });
        var shared = Arr(item, "shared_with").Select(s => _agentNames.GetValueOrDefault(s.GetString() ?? "", s.GetString() ?? "")).ToList();
        head.Children.Add(Tiny(shared.Count > 0 ? "Also in " + string.Join(", ", shared) : "Only in this agent", new Thickness(0, 4, 0, 0)));
        _detail.Children.Add(head);

        _detail.Children.Add(new TextBlock { Text = path, FontFamily = MonoFont, FontSize = 11, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        _detail.Children.Add(Actions(agent, item, kind, name, path));

        if (kind == "mcp") _detail.Children.Add(McpFacts(item));

        var contentHost = new StackPanel { Spacing = 8 };
        _detail.Children.Add(contentHost);
        _ = LoadContentAsync(version, contentHost, agent, item);
    }

    private UIElement Actions(string agent, JsonElement item, string kind, string name, string path)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4 };
        var open = FlatButton("Open file location");
        open.Click += (_, _) => OpenLocation(path);
        row.Children.Add(open);
        var copyPath = FlatButton("Copy path");
        copyPath.Click += (_, _) =>
        {
            var data = new DataPackage(); data.SetText(path); Clipboard.SetContent(data);
            _status.Text = "Copied " + path;
        };
        row.Children.Add(copyPath);

        var holders = Arr(item, "shared_with").Select(s => s.GetString() ?? "").Append(agent).ToHashSet(StringComparer.OrdinalIgnoreCase);
        // Plugin items belong to their plugin; the share flow copies only user or project items.
        var plugin = NxJson.Text(item, "scope") == "plugin";
        var targets = ShareAgents.Contains(agent) && !plugin
            ? _installed.Where(id => !holders.Contains(id) && CanReceive(agent, kind, id)).ToList()
            : new List<string>();
        var copy = FlatButton("Copy to other agents…");
        if (targets.Count == 0)
        {
            copy.IsEnabled = false;
            ToolTipService.SetToolTip(copy, plugin ? "Plugin items are managed by their plugin" : ShareAgents.Contains(agent) ? "Every installed agent that can take this already has it" : "Copying from this agent is not supported yet");
        }
        else
        {
            var menu = new MenuFlyout();
            foreach (var target in targets)
            {
                var entry = new MenuFlyoutItem { Text = _agentNames.GetValueOrDefault(target, target) };
                entry.Click += async (_, _) => await ShareAsync(agent, item, new[] { target });
                menu.Items.Add(entry);
            }
            if (targets.Count > 1)
            {
                menu.Items.Add(new MenuFlyoutSeparator());
                var all = new MenuFlyoutItem { Text = "All of them" };
                all.Click += async (_, _) => await ShareAsync(agent, item, targets);
                menu.Items.Add(all);
            }
            copy.Flyout = menu;
        }
        row.Children.Add(copy);

        if (kind == "mcp")
        {
            var remove = FlatButton("Remove from this agent…");
            remove.Foreground = NexusPalette.Danger;
            remove.Click += async (_, _) => await RemoveMcpAsync(agent, name, path);
            row.Children.Add(remove);
        }
        return new ScrollViewer { Content = row, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, VerticalScrollMode = ScrollMode.Disabled };
    }

    // Only offer targets the share flow can actually convert to.
    private static bool CanReceive(string from, string kind, string to) => ShareAgents.Contains(to) && kind switch
    {
        "skill" => to is "claude" or "codex",
        "subagent" => to is "claude" or "opencode",
        "command" => from != "gemini",
        _ => true
    };

    private static UIElement McpFacts(JsonElement item)
    {
        var grid = new Grid { ColumnSpacing = 14, RowSpacing = 6 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(78) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var rows = new List<(string, string)> { ("Transport", NxJson.Text(item, "transport")) };
        var command = NxJson.Text(item, "command");
        if (command.Length > 0)
            rows.Add(("Command", string.Join(" ", new[] { command }.Concat(Arr(item, "args").Select(a => Quote(a.GetString() ?? ""))))));
        if (NxJson.Text(item, "url") is { Length: > 0 } url) rows.Add(("URL", url));
        var env = Arr(item, "env_keys").Select(e => e.GetString() ?? "").ToList();
        if (env.Count > 0) rows.Add(("Env", string.Join(", ", env)));
        var headers = Arr(item, "header_keys").Select(e => e.GetString() ?? "").ToList();
        if (headers.Count > 0) rows.Add(("Headers", string.Join(", ", headers)));
        for (var i = 0; i < rows.Count; i++)
        {
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            var label = new TextBlock { Text = rows[i].Item1, FontSize = 11.5, Foreground = NexusPalette.Muted };
            var value = new TextBlock { Text = rows[i].Item2, FontSize = 11.5, FontFamily = MonoFont, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true };
            Grid.SetRow(label, i); Grid.SetRow(value, i); Grid.SetColumn(value, 1);
            grid.Children.Add(label); grid.Children.Add(value);
        }
        var panel = new StackPanel { Spacing = 6 };
        panel.Children.Add(grid);
        if (env.Count + headers.Count > 0)
            panel.Children.Add(Tiny("Only names are shown. Env and header values, tokens and keys stay hidden."));
        return new Border { Child = panel, Padding = new Thickness(12, 10, 12, 10), CornerRadius = new CornerRadius(6), Background = NexusPalette.Surface, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1) };
    }

    private static string Quote(string arg) => arg.Contains(' ') ? "\"" + arg + "\"" : arg;

    private async Task LoadContentAsync(int version, StackPanel host, string agent, JsonElement item)
    {
        var key = ItemKey(agent, item);
        if (!_contents.TryGetValue(key, out var content))
        {
            host.Children.Add(Tiny("Loading content…"));
            var response = await Core.CallAsync(ReadMethod, new
            {
                agent, kind = NxJson.Text(item, "kind"), name = NxJson.Text(item, "name"), path = NxJson.Text(item, "path"), workspace = _workspace
            });
            await UiThread.Resume(DispatcherQueue);
            if (version != _detailVersion) return;
            host.Children.Clear();
            if (!response.Ok || response.Result is not { } result)
            {
                host.Children.Add(Tiny(response.Error ?? "Could not read this file."));
                return;
            }
            content = result.Clone();
            _contents[key] = content;
        }
        if (version != _detailVersion) return;
        RenderContent(host, content);
    }

    private void RenderContent(StackPanel host, JsonElement content)
    {
        host.Children.Clear();
        var format = NxJson.Text(content, "format");
        var text = NxJson.Text(content, "content");
        var masked = content.TryGetProperty("masked", out var m) && m.TryGetInt32(out var mi) ? mi : 0;

        var bar = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        bar.Children.Add(SectionLabel(format == "json" && NxJson.Text(content, "path").Length > 0 ? "Entry (read-only)" : "Content (read-only)", new Thickness(0)));
        if (masked > 0) bar.Children.Add(new TextBlock { Text = masked + " secret value" + (masked == 1 ? "" : "s") + " hidden", FontSize = 11, Foreground = NexusPalette.Amber, VerticalAlignment = VerticalAlignment.Center });
        if (format == "markdown")
        {
            var toggle = FlatButton(_showSource ? "Rendered" : "Source");
            toggle.Click += (_, _) => { _showSource = !_showSource; RenderContent(host, content); };
            bar.Children.Add(toggle);
        }
        host.Children.Add(bar);
        if (text.Trim().Length == 0) { host.Children.Add(Tiny("This file is empty.")); return; }
        host.Children.Add(format == "markdown" && !_showSource ? Markdown(text) : MonoBlock(text));
        if (content.TryGetProperty("truncated", out var t) && t.ValueKind == JsonValueKind.True)
            host.Children.Add(Tiny("Showing the first 256 KB. Open the file to see the rest."));
    }

    private static UIElement MonoBlock(string text)
    {
        var lines = text.Split('\n');
        var gutter = new TextBlock
        {
            Text = string.Join("\n", Enumerable.Range(1, lines.Length)), FontFamily = MonoFont, FontSize = 11.5, LineHeight = 18,
            Opacity = 0.3, TextAlignment = TextAlignment.Right, Margin = new Thickness(0, 0, 12, 0)
        };
        var code = new TextBlock { Text = text, FontFamily = MonoFont, FontSize = 11.5, LineHeight = 18, TextWrapping = TextWrapping.NoWrap, IsTextSelectionEnabled = true };
        var grid = new Grid();
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(gutter);
        Grid.SetColumn(code, 1);
        grid.Children.Add(code);
        return new Border
        {
            Child = new ScrollViewer { Content = grid, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled },
            Padding = new Thickness(12, 10, 12, 10), CornerRadius = new CornerRadius(6), Background = NexusPalette.Surface, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
        };
    }

    // A small, safe markdown view: frontmatter, headings, lists, fenced
    // code, quotes and inline `code` / **bold**. Anything else is plain text.
    private static UIElement Markdown(string text)
    {
        var panel = new StackPanel { Spacing = 6 };
        var lines = text.Split('\n');
        if (lines.Length > 3000) return MonoBlock(text);
        var i = 0;
        if (lines.Length > 0 && lines[0].Trim() == "---")
        {
            var end = Array.FindIndex(lines, 1, l => l.Trim() == "---");
            if (end > 0)
            {
                panel.Children.Add(MonoBlock(string.Join("\n", lines[1..end])));
                i = end + 1;
            }
        }
        var para = new List<string>();
        void Flush()
        {
            if (para.Count == 0) return;
            panel.Children.Add(MdText(string.Join(" ", para.Select(p => p.Trim())), 12.5, FontWeights.Normal));
            para.Clear();
        }
        for (; i < lines.Length; i++)
        {
            var line = lines[i];
            var t = line.Trim();
            if (t.StartsWith("```"))
            {
                Flush();
                var code = new List<string>();
                for (i++; i < lines.Length && !lines[i].Trim().StartsWith("```"); i++) code.Add(lines[i]);
                panel.Children.Add(MonoBlock(string.Join("\n", code)));
                continue;
            }
            if (t.Length == 0) { Flush(); continue; }
            if (t.StartsWith('#'))
            {
                Flush();
                var level = t.TakeWhile(c => c == '#').Count();
                var size = level switch { 1 => 16.0, 2 => 14.5, 3 => 13.5, _ => 12.5 };
                var h = MdText(t.TrimStart('#').Trim(), size, FontWeights.SemiBold);
                h.Margin = new Thickness(0, level <= 2 ? 8 : 4, 0, 0);
                panel.Children.Add(h);
                continue;
            }
            var indent = (line.Length - line.TrimStart().Length) / 2;
            var bullet = t.StartsWith("- ") || t.StartsWith("* ") || t.StartsWith("+ ");
            var number = !bullet ? System.Text.RegularExpressions.Regex.Match(t, @"^(\d+)[.)]\s+") : System.Text.RegularExpressions.Match.Empty;
            if (bullet || number.Success)
            {
                Flush();
                var body = bullet ? t[2..] : t[number.Length..];
                var marker = bullet ? "•" : number.Groups[1].Value + ".";
                var row = new Grid { ColumnSpacing = 6, Margin = new Thickness(6 + 14 * Math.Min(indent, 4), 0, 0, 0) };
                row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(16) });
                row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
                row.Children.Add(new TextBlock { Text = marker, FontSize = 12.5, Opacity = 0.6 });
                var content = MdText(body, 12.5, FontWeights.Normal);
                Grid.SetColumn(content, 1);
                row.Children.Add(content);
                panel.Children.Add(row);
                continue;
            }
            if (t.StartsWith('>'))
            {
                Flush();
                var q = MdText(t.TrimStart('>').Trim(), 12.5, FontWeights.Normal);
                q.Opacity = 0.75;
                panel.Children.Add(new Border { Child = q, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(2, 0, 0, 0), Padding = new Thickness(10, 0, 0, 0) });
                continue;
            }
            if (t is "---" or "***" or "___")
            {
                Flush();
                panel.Children.Add(new Border { Height = 1, Background = NexusPalette.Hairline, Margin = new Thickness(0, 4, 0, 4) });
                continue;
            }
            para.Add(line);
        }
        Flush();
        return panel;
    }

    private static TextBlock MdText(string text, double size, Windows.UI.Text.FontWeight weight)
    {
        var block = new TextBlock { FontSize = size, FontWeight = weight, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, LineHeight = size * 1.5 };
        var parts = text.Split('`');
        for (var p = 0; p < parts.Length; p++)
        {
            if (p % 2 == 1 && p < parts.Length - 1)
            {
                block.Inlines.Add(new Run { Text = parts[p], FontFamily = MonoFont, FontSize = size - 1, Foreground = NexusPalette.Accent });
                continue;
            }
            var seg = p % 2 == 1 ? "`" + parts[p] : parts[p];
            var bold = seg.Split("**");
            for (var b = 0; b < bold.Length; b++)
            {
                if (bold[b].Length == 0) continue;
                var run = new Run { Text = bold[b] };
                if (b % 2 == 1 && b < bold.Length - 1) run.FontWeight = FontWeights.SemiBold;
                block.Inlines.Add(run);
            }
        }
        return block;
    }

    // ── Actions ────────────────────────────────────────────────────────

    private void OpenLocation(string path)
    {
        try
        {
            if (System.IO.File.Exists(path))
                PlatformText.Reveal(path);
            else if (System.IO.Path.GetDirectoryName(path) is { } dir && System.IO.Directory.Exists(dir))
                PlatformText.Reveal(dir);
            else _status.Text = "That file no longer exists.";
        }
        catch (Exception e) { _status.Text = NxErrors.ForException("Nexus couldn't open that folder.", e, "agents"); }
    }

    private async Task RemoveMcpAsync(string agent, string name, string path)
    {
        var agentName = _agentNames.GetValueOrDefault(agent, agent);
        var check = await Core.CallAsync(RemoveMethod, new { agent, name, path, workspace = _workspace, dry_run = true });
        await UiThread.Resume(DispatcherQueue);
        if (!check.Ok) { _status.Text = check.Error ?? "Could not check that MCP server."; return; }

        var nexus = name == "nexus";
        var body = new StackPanel { Spacing = 10, MaxWidth = 480 };
        body.Children.Add(new TextBlock
        {
            Text = nexus
                ? $"Removes the Nexus memory server from {agentName}. Other MCP servers and settings stay."
                : $"\"{name}\" was not added by Nexus. Removing it means {agentName} can no longer use this server, and you would need to add it back yourself (or restore the backup).",
            FontSize = 12.5, TextWrapping = TextWrapping.Wrap
        });
        body.Children.Add(new TextBlock { Text = path, FontFamily = MonoFont, FontSize = 11, Opacity = 0.65, TextWrapping = TextWrapping.Wrap });
        body.Children.Add(Tiny("The file is backed up next to the original first (.nexus-backup-…). Restart the agent to apply."));
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = (nexus ? "Remove Nexus from " : "Remove " + name + " from ") + agentName + "?",
            Content = body, PrimaryButtonText = "Remove", CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Close
        };
        if (!nexus)
        {
            var understand = new CheckBox { Content = "I understand this is not a Nexus entry", FontSize = 12.5 };
            dialog.IsPrimaryButtonEnabled = false;
            understand.Checked += (_, _) => dialog.IsPrimaryButtonEnabled = true;
            understand.Unchecked += (_, _) => dialog.IsPrimaryButtonEnabled = false;
            body.Children.Add(understand);
        }
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;

        var done = await Core.CallAsync(RemoveMethod, new { agent, name, path, workspace = _workspace, dry_run = false });
        await UiThread.Resume(DispatcherQueue);
        _status.Text = done.Ok ? $"Removed {name} from {agentName}. Backup: {NxJson.Text(done.Result ?? default, "backup")}" : done.Error ?? "Remove failed.";
        _selectedItem = "";
        await LoadAsync();
    }

    private async Task ShareAsync(string from, JsonElement item, IReadOnlyList<string> targets)
    {
        var scope = NxJson.Text(item, "scope");
        if (scope.Length == 0) scope = "user";
        var args = new { item = JsonSerializer.Deserialize<object>(item.GetRawText()), from, to = targets, scope, workspace = _workspace, dry_run = true };
        var plan = await Core.CallAsync(NxMethods.AgentsSetupShare, args);
        await UiThread.Resume(DispatcherQueue);
        if (!plan.Ok || plan.Result is not { } result) { _status.Text = plan.Error ?? "Could not plan the copy."; return; }

        var body = new StackPanel { Spacing = 8, MaxWidth = 520 };
        var writes = Arr(result, "writes").ToList();
        var skipped = Arr(result, "skipped").Select(x => x.GetString() ?? "").ToList();
        if (writes.Count > 0)
        {
            body.Children.Add(new TextBlock { Text = "Nexus will write:", FontSize = 13, FontWeight = FontWeights.Medium });
            foreach (var write in writes)
            {
                var action = NxJson.Text(write, "Action", "action");
                var line = (action == "update" ? "Update " : "Create ") + NxJson.Text(write, "Path", "path");
                body.Children.Add(new TextBlock { Text = line, FontSize = 12, TextWrapping = TextWrapping.Wrap, FontFamily = MonoFont });
            }
            if (writes.Any(x => NxJson.Text(x, "Action", "action") == "update"))
                body.Children.Add(new TextBlock { Text = "Changed files are backed up next to the original first (.nexus-backup-…).", FontSize = 12, Opacity = 0.6, TextWrapping = TextWrapping.Wrap });
        }
        foreach (var reason in skipped.Where(r => r.Length > 0))
            body.Children.Add(new TextBlock { Text = "Skipped: " + reason, FontSize = 12, Opacity = 0.7, TextWrapping = TextWrapping.Wrap });
        if (writes.Count == 0 && skipped.Count == 0)
            body.Children.Add(new TextBlock { Text = "Nothing to do.", FontSize = 12.5 });

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Copy " + NxJson.Text(item, "name"),
            Content = new ScrollViewer { Content = body, MaxHeight = 420 },
            PrimaryButtonText = writes.Count > 0 ? "Copy" : "", CloseButtonText = writes.Count > 0 ? "Cancel" : "Close",
            DefaultButton = writes.Count > 0 ? ContentDialogButton.Primary : ContentDialogButton.Close
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;

        var done = await Core.CallAsync(NxMethods.AgentsSetupShare, new { item = JsonSerializer.Deserialize<object>(item.GetRawText()), from, to = targets, scope, workspace = _workspace, dry_run = false });
        await UiThread.Resume(DispatcherQueue);
        _status.Text = done.Ok ? "Copied " + NxJson.Text(item, "name") + " to " + string.Join(", ", targets.Select(t => _agentNames.GetValueOrDefault(t, t))) + "." : done.Error ?? "Copy failed.";
        await LoadAsync();
    }

    // ── Small building blocks ──────────────────────────────────────────

    private static IEnumerable<JsonElement> Arr(JsonElement obj, string key) =>
        obj.ValueKind == JsonValueKind.Object && obj.TryGetProperty(key, out var a) && a.ValueKind == JsonValueKind.Array ? a.EnumerateArray() : Enumerable.Empty<JsonElement>();

    private static bool Flag(JsonElement obj, string key) =>
        obj.ValueKind == JsonValueKind.Object && obj.TryGetProperty(key, out var v) && v.ValueKind == JsonValueKind.True;

    private static string Flatten(string s) => string.Join(" ", s.Split((char[])null!, StringSplitOptions.RemoveEmptyEntries));

    private static string McpSummary(JsonElement item)
    {
        var transport = NxJson.Text(item, "transport");
        var target = NxJson.Text(item, "url") is { Length: > 0 } url ? url : NxJson.Text(item, "command");
        return (transport.Length > 0 ? transport + " · " : "") + target;
    }

    private static TextBlock Tiny(string text, Thickness? margin = null) =>
        new() { Text = text, FontSize = 11, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, Margin = margin ?? new Thickness(0) };

    private static TextBlock SectionLabel(string text, Thickness? margin = null) =>
        new() { Text = text.ToUpperInvariant(), FontSize = 10.5, FontWeight = FontWeights.SemiBold, CharacterSpacing = 40, Foreground = NexusPalette.Muted, Margin = margin ?? new Thickness(6, 8, 0, 4), VerticalAlignment = VerticalAlignment.Center };

    private static Border Dot(Brush fill) => new() { Width = 6, Height = 6, CornerRadius = new CornerRadius(3), Background = fill, VerticalAlignment = VerticalAlignment.Center };

    private static Button FlatButton(string text) => new()
    {
        Content = new TextBlock { Text = text, FontSize = 12 }, Padding = new Thickness(9, 4, 9, 5), MinHeight = 26,
        Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1),
        CornerRadius = new CornerRadius(5), VerticalAlignment = VerticalAlignment.Center
    };

    private static Button RowButton(UIElement content, bool selected) => new()
    {
        Content = content, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
        Padding = new Thickness(10, 6, 10, 7), BorderThickness = new Thickness(0), CornerRadius = new CornerRadius(6),
        Background = selected ? SessionChatRender.SubtleFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent)
    };

    private static ScrollViewer Scroll(UIElement content) => new()
    {
        Content = content, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled, VerticalScrollBarVisibility = ScrollBarVisibility.Auto
    };

    private static void Place(FrameworkElement element, int row, int column)
    {
        Grid.SetRow(element, row);
        Grid.SetColumn(element, column);
        Grid.SetRowSpan(element, 1);
        Grid.SetColumnSpan(element, 1);
    }

    private static Border Pane(UIElement child) => new() { Child = child, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(0, 0, 1, 0) };

    // ItemsRepeater element factory: realizes rows on demand and recycles
    // their hosts, so a long list (or a broad search) builds only what is in view.
    private sealed partial class RowFactory : IElementFactory
    {
        private readonly Func<object, UIElement> _build;
        private readonly Stack<ContentControl> _pool = new();
        private readonly HashSet<ContentControl> _live = new();

        // Rows currently realized (in or near view).
        public IEnumerable<ContentControl> Live => _live;

        public RowFactory(Func<object, UIElement> build) => _build = build;

        public UIElement GetElement(Microsoft.UI.Xaml.ElementFactoryGetArgs args) => Realize(args.Data);

        public void RecycleElement(Microsoft.UI.Xaml.ElementFactoryRecycleArgs args) => Release(args.Element);

        private UIElement Realize(object data)
        {
            // A recycled host stays a (hidden) child of the repeater, as with
            // the framework's own recycle pool, and is handed back here.
            var host = _pool.Count > 0 ? _pool.Pop() : new ContentControl
            {
                HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch, IsTabStop = false
            };
            host.Content = _build(data);
            _live.Add(host);
            return host;
        }

        private void Release(UIElement element)
        {
            if (element is not ContentControl host) return;
            host.Content = null;
            _live.Remove(host);
            _pool.Push(host);
        }
    }

#if HAS_UNO
    // Uno's ItemsRepeater takes custom factories only through its
    // IElementFactoryShim (Microsoft.UI.Xaml.Controls); same behaviour.
    private sealed partial class RowFactory : Microsoft.UI.Xaml.Controls.IElementFactoryShim
    {
        public UIElement GetElement(Microsoft.UI.Xaml.Controls.ElementFactoryGetArgs args) => Realize(args.Data);
        public void RecycleElement(Microsoft.UI.Xaml.Controls.ElementFactoryRecycleArgs args) => Release(args.Element);
    }
#endif
}
