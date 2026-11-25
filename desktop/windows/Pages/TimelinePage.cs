using System.Globalization;
using System.Text.Json;
using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed record TimelineAgentFilter(string Agent);

public sealed class TimelinePage : NexusPage
{
    private readonly ComboBox _agentFilter = new()
    {
        PlaceholderText = "All agents",
        Width = 180,
        HorizontalAlignment = HorizontalAlignment.Stretch
    };
    private readonly CheckBox _includeSubagents = new()
    {
        Content = "Include sub-agents",
        VerticalAlignment = VerticalAlignment.Center
    };
    private readonly TextBox _search = new() { PlaceholderText = "Search chats and summaries" };
    private readonly StackPanel _agentChip = new() { Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
    private readonly ListView _list = new()
    {
        HorizontalAlignment = HorizontalAlignment.Stretch,
        VerticalAlignment = VerticalAlignment.Stretch,
        SelectionMode = ListViewSelectionMode.None,
        IsItemClickEnabled = false,
        SingleSelectionFollowsFocus = false
    };
    private const int PageSize = 20;
    private readonly Button _prevPage = new() { Content = "Previous", MinWidth = 96, IsEnabled = false };
    private readonly Button _nextPage = new() { Content = "Next", MinWidth = 96, IsEnabled = false };
    private readonly TextBlock _pageInfo = new() { VerticalAlignment = VerticalAlignment.Center, Opacity = 0.85, TextWrapping = TextWrapping.Wrap };
    private readonly TextBlock _count = new() { Text = "", Opacity = 0.7, TextWrapping = TextWrapping.Wrap };
    private string _query = "";
    private readonly List<string> _pageCursors = [""];
    private readonly List<List<JsonElement>> _pageStartCarries = [[]];
    private int _pageIndex;
    private bool _hasNextLogicalPage;
    private bool _loading;
    private int _loadedCount;
    private int _primarySessionCount;
    private string? _lastDayKey;
    private string _pendingAgentFilter = "";
    private bool _agentFilterSync;
    private readonly ProgressRing _loadingRing = new() { Width = 22, Height = 22, IsActive = false, Visibility = Visibility.Collapsed };
    private DispatcherQueueTimer? _filterDebounce;
    private string _loadedProjectId = "";
    private int _loadVersion;

    public TimelinePage(string agent = "", bool hubChrome = false)
    {
        _pendingAgentFilter = agent;
        _agentFilter.Items.Add(new ComboBoxItem { Content = "All agents", Tag = "" });
        _agentFilter.SelectedIndex = 0;
        _agentFilter.SelectionChanged += (_, _) =>
        {
            if (_agentFilterSync) return;
            ScheduleFilterReload();
        };
        _includeSubagents.Checked += (_, _) => ScheduleFilterReload();
        _includeSubagents.Unchecked += (_, _) => ScheduleFilterReload();
        _search.TextChanged += (_, _) => ScheduleFilterReload();
        var refresh = new Button { Content = "Refresh", MinWidth = 88 };
        refresh.Click += async (_, _) =>
        {
            await RefreshAgentFilterAsync();
            await LoadPageAsync(reset: true);
        };
        _prevPage.Click += async (_, _) => await GoToPageAsync(_pageIndex - 1);
        _nextPage.Click += async (_, _) => await GoToPageAsync(_pageIndex + 1);
        _search.KeyDown += async (_, e) =>
        {
            if (e.Key == Windows.System.VirtualKey.Enter)
            {
                await RefreshAgentFilterAsync();
                await LoadPageAsync(reset: true);
            }
        };
        var filters = new Grid { ColumnSpacing = 8, RowSpacing = 8 };
        filters.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        filters.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(180) });
        filters.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        filters.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        _search.HorizontalAlignment = HorizontalAlignment.Stretch;
        Grid.SetColumn(_search, 0);
        Grid.SetColumn(_agentFilter, 1);
        Grid.SetColumn(_includeSubagents, 2);
        filters.Children.Add(_search);
        filters.Children.Add(_agentFilter);
        filters.Children.Add(_includeSubagents);
        var filterActions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        filterActions.Children.Add(_loadingRing);
        filterActions.Children.Add(refresh);
        Grid.SetColumn(filterActions, 3);
        filters.Children.Add(filterActions);
        _list.SetValue(FrameworkElement.MinHeightProperty, 0.0);
        var root = new Grid
        {
            Padding = hubChrome ? new Thickness(0) : new Thickness(32, 24, 32, 24),
            RowSpacing = 16,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        var row = 0;
        if (!hubChrome)
        {
            root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            var header = PageHeader(
                "Timeline",
                "Browse captured chats for the project selected in the shell. Open a session to continue in its harness or pick another locally.");
            Grid.SetRow(header, row++);
            root.Children.Add(header);
        }
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 0 });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });

        Grid.SetRow(filters, row++);
        root.Children.Add(filters);
        var meta = new StackPanel { Spacing = 14 };
        meta.Children.Add(_agentChip);
        Grid.SetRow(meta, row++);
        root.Children.Add(meta);
        Grid.SetRow(_list, row++);
        root.Children.Add(_list);
        var pagerBar = new Grid { ColumnSpacing = 8 };
        pagerBar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        pagerBar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        pagerBar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(_prevPage, 0);
        Grid.SetColumn(_pageInfo, 1);
        Grid.SetColumn(_nextPage, 2);
        _pageInfo.HorizontalAlignment = HorizontalAlignment.Center;
        _pageInfo.TextAlignment = TextAlignment.Center;
        pagerBar.Children.Add(_prevPage);
        pagerBar.Children.Add(_pageInfo);
        pagerBar.Children.Add(_nextPage);
        var pager = new StackPanel { Spacing = 8 };
        pager.Children.Add(pagerBar);
        pager.Children.Add(_count);
        Grid.SetRow(pager, row);
        root.Children.Add(pager);
        Content = root;
        WatchShellProject(async () =>
        {
            InvalidateForProjectChange();
            await RefreshAgentFilterAsync();
            await LoadPageAsync(reset: true);
        });
    }

    internal void InvalidateForProjectChange()
    {
        _loadedProjectId = "";
        _loadVersion++;
    }

    protected override void OnEnter(object? parameter)
    {
        ApplyEnterParameter(parameter);
        var projectId = ShellProjectId();
        var needsProjectRefresh = PageCache.TimelineNeedsRefresh(projectId);
        var pendingAgentFilter = _pendingAgentFilter.Length > 0;
        if (!needsProjectRefresh &&
            !pendingAgentFilter &&
            string.Equals(_loadedProjectId, projectId, StringComparison.OrdinalIgnoreCase) &&
            _list.Items.Count > 0 &&
            !IsPlaceholderList())
        {
            return;
        }
        _ = EnterAsync(forceReload: needsProjectRefresh || pendingAgentFilter);
    }

    private void ApplyEnterParameter(object? parameter)
    {
        if (parameter is TimelineAgentFilter filter)
        {
            _pendingAgentFilter = filter.Agent;
            _query = "";
            if (_search.Text.Length > 0)
            {
                _search.Text = "";
            }
            return;
        }
        if (parameter is string query &&
            query.Length > 0 &&
            !query.Equals(HomeHubPage.SegmentSessions, StringComparison.OrdinalIgnoreCase))
        {
            _query = query;
            if (!string.Equals(_search.Text, query, StringComparison.Ordinal))
            {
                _search.Text = query;
            }
        }
    }

    private bool IsPlaceholderList()
    {
        if (_list.Items.Count != 1)
        {
            return false;
        }
        return _list.Items[0] is Border;
    }

    private async Task EnterAsync(bool forceReload)
    {
        await RefreshAgentFilterAsync();
        await LoadPageAsync(reset: forceReload || PageCache.TimelineNeedsRefresh(ShellProjectId()));
    }

    private void ScheduleFilterReload()
    {
        _filterDebounce ??= DispatcherQueue.GetForCurrentThread().CreateTimer();
        _filterDebounce.Interval = TimeSpan.FromMilliseconds(400);
        _filterDebounce.IsRepeating = false;
        _filterDebounce.Stop();
        _filterDebounce.Tick -= FilterDebounce_Tick;
        _filterDebounce.Tick += FilterDebounce_Tick;
        _filterDebounce.Start();
    }

    private async void FilterDebounce_Tick(DispatcherQueueTimer sender, object args)
    {
        sender.Tick -= FilterDebounce_Tick;
        sender.Stop();
        await RefreshAgentFilterAsync();
        await LoadPageAsync(reset: true);
    }

    private async Task RefreshAgentFilterAsync()
    {
        _agentFilterSync = true;
        try
        {
            var selected = _pendingAgentFilter.Length > 0
                ? _pendingAgentFilter
                : AgentFilterValue();
            _agentFilter.Items.Clear();
            _agentFilter.Items.Add(new ComboBoxItem { Content = "All agents", Tag = "" });

            var projectId = ShellProjectId();
            if (projectId.Length > 0)
            {
                var response = await Call(NxMethods.AgentsList, new { project_id = projectId });
                if (response.Ok)
                {
                    foreach (var agent in NxJson.Items(response.Result))
                    {
                        var countText = NxJson.Text(agent, "captured_count", "count");
                        if (countText.Length > 0 && int.TryParse(countText, out var count) && count <= 0)
                        {
                            continue;
                        }
                        var harness = NxJson.Text(agent, "harness", "name", "agent");
                        if (harness.Length > 0)
                        {
                            _agentFilter.Items.Add(new ComboBoxItem { Content = harness, Tag = harness });
                        }
                    }
                }
            }

            SelectAgentFilter(selected);
            _pendingAgentFilter = "";
        }
        finally
        {
            _agentFilterSync = false;
        }
    }

    private void SelectAgentFilter(string agent)
    {
        if (agent.Length == 0)
        {
            _agentFilter.SelectedIndex = 0;
            return;
        }
        for (var i = 0; i < _agentFilter.Items.Count; i++)
        {
            if ((_agentFilter.Items[i] as ComboBoxItem)?.Tag as string == agent ||
                string.Equals((_agentFilter.Items[i] as ComboBoxItem)?.Content?.ToString(), agent, StringComparison.OrdinalIgnoreCase))
            {
                _agentFilter.SelectedIndex = i;
                return;
            }
        }
        _agentFilter.Items.Add(new ComboBoxItem { Content = agent, Tag = agent });
        _agentFilter.SelectedIndex = _agentFilter.Items.Count - 1;
    }

    private string AgentFilterValue()
    {
        if (_agentFilter.SelectedItem is ComboBoxItem item)
        {
            var tag = item.Tag as string;
            if (tag is { Length: > 0 })
            {
                return tag;
            }
            // Sentinel rows (e.g. "All agents") use an empty Tag — no server filter.
            return "";
        }
        return "";
    }

    private async Task GoToPageAsync(int targetIndex)
    {
        if (_loading || targetIndex < 0) return;
        if (targetIndex > _pageIndex)
        {
            if (!_hasNextLogicalPage || targetIndex >= _pageCursors.Count) return;
            _pageIndex = targetIndex;
        }
        else if (targetIndex < _pageIndex)
        {
            if (targetIndex >= _pageCursors.Count) return;
            _pageCursors.RemoveRange(targetIndex + 1, _pageCursors.Count - targetIndex - 1);
            _pageStartCarries.RemoveRange(targetIndex + 1, _pageStartCarries.Count - targetIndex - 1);
            _pageIndex = targetIndex;
        }
        else
        {
            return;
        }
        await LoadPageAsync(reset: false);
    }

    private async Task LoadPageAsync(bool reset)
    {
        var version = ++_loadVersion;
        var projectId = ShellProjectId();
        if (projectId.Length == 0)
        {
            _loadedProjectId = "";
            _loading = false;
            _loadingRing.IsActive = false;
            _loadingRing.Visibility = Visibility.Collapsed;
            _list.Items.Clear();
            _lastDayKey = null;
            _count.Text = "";
            _pageInfo.Text = "";
            _prevPage.IsEnabled = false;
            _nextPage.IsEnabled = false;
            _list.Items.Add(EmptyStateCard(
                "Choose a project",
                "Pick a project in the nav pane to scope sessions and agents."));
            return;
        }
        if (reset)
        {
            _pageCursors.Clear();
            _pageCursors.Add("");
            _pageStartCarries.Clear();
            _pageStartCarries.Add([]);
            _pageIndex = 0;
            _hasNextLogicalPage = false;
        }
        var apiCursor = _pageIndex < _pageCursors.Count ? _pageCursors[_pageIndex] : "";
        var pageCarry = _pageIndex < _pageStartCarries.Count
            ? _pageStartCarries[_pageIndex]
            : [];
        _loading = true;
        _loadingRing.IsActive = true;
        _loadingRing.Visibility = Visibility.Visible;
        _prevPage.IsEnabled = false;
        _nextPage.IsEnabled = false;
        UpdateAgentChip();
        _loadedCount = 0;
        _primarySessionCount = 0;
        _lastDayKey = null;
        _list.Items.Clear();
        _count.Text = $"Loading sessions for {ShellProjectLabel()}…";
        _pageInfo.Text = "";

        var includeSubagents = _includeSubagents.IsChecked == true;
        var projectsResponse = await Call(NxMethods.ProjectsList);
        if (version != _loadVersion || projectId != ShellProjectId()) return;
        var projects = projectsResponse.Ok ? projectsResponse.Result : null;
        var fill = await FillVisiblePageAsync(projectId, apiCursor, pageCarry, includeSubagents, projects, version);
        if (version != _loadVersion || projectId != ShellProjectId()) return;
        _loading = false;
        _loadingRing.IsActive = false;
        _loadingRing.Visibility = Visibility.Collapsed;
        if (fill.Error is not null)
        {
            _loadedProjectId = "";
            ShowListError(fill.Error);
            UpdatePaginationUi();
            return;
        }
        _loadedProjectId = projectId;
        PageCache.MarkTimelineLoaded(projectId);
        _primarySessionCount = fill.VisibleCount;
        _loadedCount = fill.RawFetched;
        _hasNextLogicalPage = fill.HasMore;
        if (fill.HasMore)
        {
            while (_pageCursors.Count <= _pageIndex + 1)
            {
                _pageCursors.Add("");
                _pageStartCarries.Add([]);
            }
            _pageCursors[_pageIndex + 1] = fill.NextApiCursor;
            _pageStartCarries[_pageIndex + 1] = fill.Carry;
        }
        else if (_pageCursors.Count > _pageIndex + 1)
        {
            _pageCursors.RemoveRange(_pageIndex + 1, _pageCursors.Count - _pageIndex - 1);
            _pageStartCarries.RemoveRange(_pageIndex + 1, _pageStartCarries.Count - _pageIndex - 1);
        }

        UpdatePaginationUi();
        if (_primarySessionCount == 0)
        {
            var agentFiltered = AgentFilterValue().Length > 0;
            var subagentsHidden = _includeSubagents.IsChecked != true && _loadedCount > 0;
            var emptyBody = agentFiltered
                ? "No sessions match this agent filter. Clear the filter or pick another harness."
                : subagentsHidden
                    ? "Sessions exist for this project but are all sub-agent runs. Enable Include sub-agents to list them."
                    : "Captured harness sessions appear here after harvest. Confirm capture is on in Settings and resume from the tray if paused.";
            _list.Items.Add(EmptyStateCard(_pageIndex == 0 ? "No sessions yet" : "No sessions on this page", emptyBody));
        }
        if (reset)
        {
            await RefreshAgentFilterAsync();
        }
    }

    private void UpdatePaginationUi()
    {
        var pageNumber = _pageIndex + 1;
        var hasNext = _hasNextLogicalPage;
        var lastPageHint = hasNext ? "" : " · last page";
        _pageInfo.Text = hasNext
            ? $"Page {pageNumber}"
            : $"Page {pageNumber}{lastPageHint}";
        _prevPage.IsEnabled = !_loading && _pageIndex > 0;
        _nextPage.IsEnabled = !_loading && hasNext;

        var agentLabel = AgentFilterValue();
        var agentScope = agentLabel.Length > 0 ? " · agent: " + agentLabel : "";
        var subScope = _includeSubagents.IsChecked == true ? " · including sub-agents" : "";
        if (_primarySessionCount > 0)
        {
            var start = _pageIndex * PageSize + 1;
            var end = _pageIndex * PageSize + _primarySessionCount;
            _count.Text = $"Showing {start}–{end} · Page {pageNumber} · {ShellProjectLabel()}" + agentScope + subScope;
        }
        else
        {
            _count.Text = $"Page {pageNumber} · {ShellProjectLabel()}" + agentScope + subScope;
        }
    }

    private sealed record VisiblePageFill(
        int VisibleCount,
        int RawFetched,
        string NextApiCursor,
        List<JsonElement> Carry,
        bool HasMore,
        NxResponse? Error);

    private async Task<VisiblePageFill> FillVisiblePageAsync(
        string projectId,
        string apiCursor,
        List<JsonElement> pageCarry,
        bool includeSubagents,
        JsonElement? projects,
        int version)
    {
        var pending = new Queue<JsonElement>(pageCarry);
        var fetchCursor = apiCursor;
        var fetchedAny = pageCarry.Count > 0;
        var rawFetched = 0;
        var visible = 0;

        while (visible < PageSize)
        {
            if (pending.Count == 0)
            {
                if (fetchedAny && fetchCursor.Length == 0)
                {
                    break;
                }

                var response = await Call(NxMethods.TimelineList, new
                {
                    project_id = projectId,
                    agent = AgentFilterValue(),
                    q = _search.Text,
                    limit = PageSize,
                    cursor = fetchCursor
                });
                if (version != _loadVersion || projectId != ShellProjectId())
                {
                    return new VisiblePageFill(visible, rawFetched, fetchCursor, pending.ToList(), false, null);
                }
                if (!response.Ok)
                {
                    return new VisiblePageFill(visible, rawFetched, fetchCursor, pending.ToList(), false, response);
                }

                fetchCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
                fetchedAny = true;
                var batch = NxJson.Items(response.Result).ToList();
                rawFetched += batch.Count;
                if (batch.Count == 0 && fetchCursor.Length == 0)
                {
                    break;
                }

                foreach (var item in batch)
                {
                    pending.Enqueue(item);
                }
            }

            while (pending.Count > 0 && visible < PageSize)
            {
                var item = pending.Dequeue();
                if (!ProjectScope.TimelineItemMatches(item, projectId, projects))
                {
                    continue;
                }
                if (!includeSubagents && SessionLineage.IsSubagentSession(item))
                {
                    continue;
                }

                AppendSessionCard(item);
                visible++;
            }
        }

        var carry = pending.ToList();
        var hasMore = carry.Count > 0 || fetchCursor.Length > 0;
        return new VisiblePageFill(visible, rawFetched, fetchCursor, carry, hasMore, null);
    }

    private void AppendSessionCard(JsonElement item)
    {
        var whenRaw = NxJson.Text(item, "last_active_at", "started_at", "updated_at", "captured_at");
        var dayKey = DayGroupKey(whenRaw);
        if (dayKey != _lastDayKey)
        {
            _lastDayKey = dayKey;
            _list.Items.Add(SectionHeader(DayGroupLabel(whenRaw)));
        }

        var id = NxJson.Text(item, "id", "session_id", "sessionId");
        var title = NxJson.Text(item, "title", "native_id", "kind");
        var summary = NxJson.Text(item, "match_preview", "summary", "preview");
        var agent = NxJson.Text(item, "agent", "harness");
        var project = NxJson.Text(item, "project", "project_name");
        if (project.Length == 0) project = ShellProjectLabel();
        var machine = NxJson.Text(item, "machine", "origin_machine_id");
        var turns = NxJson.Text(item, "turn_count", "turns");
        var versionState = NxJson.Text(item, "version_state", "state");
        var relative = HumanizeTime(whenRaw);

        var titleBlock = new TextBlock
        {
            Text = NxJson.Scrub(title.Length > 0 ? title : (id.Length > 0 ? id : "Session")),
            TextWrapping = TextWrapping.Wrap,
            FontSize = 16,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            IsTextSelectionEnabled = true
        };
        var timeBlock = new TextBlock
        {
            Text = relative,
            FontSize = 12,
            Opacity = 0.65,
            HorizontalAlignment = HorizontalAlignment.Right,
            VerticalAlignment = VerticalAlignment.Top,
            IsTextSelectionEnabled = true
        };
        var titleRow = new Grid();
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(titleBlock, 0);
        Grid.SetColumn(timeBlock, 2);
        titleRow.Children.Add(titleBlock);
        if (SessionLineage.IsForkSession(item))
        {
            var fork = new TextBlock
            {
                Text = "⑂ Fork",
                FontSize = 11,
                Opacity = 0.72,
                Margin = new Thickness(10, 2, 10, 0),
                VerticalAlignment = VerticalAlignment.Top
            };
            ToolTipService.SetToolTip(fork, "Forked from another chat");
            Grid.SetColumn(fork, 1);
            titleRow.Children.Add(fork);
        }
        titleRow.Children.Add(timeBlock);

        var metaParts = new List<string>();
        if (agent.Length > 0) metaParts.Add(agent);
        if (project.Length > 0) metaParts.Add(project);
        if (machine.Length > 0) metaParts.Add(machine);
        if (turns.Length > 0 && int.TryParse(turns, out var turnN) && turnN > 0) metaParts.Add(Nexus.Core.FriendlyTime.Count(turnN, "message"));
        else if (turns.Length > 0) metaParts.Add(turns + " turns");
        if (versionState.Equals("transcript_only", StringComparison.OrdinalIgnoreCase)) metaParts.Add("transcript only");

        var meta = Muted(string.Join(" · ", metaParts));
        var summaryBlock = summary.Length > 0
            ? new TextBlock
            {
                Text = NxJson.Scrub(summary),
                TextWrapping = TextWrapping.Wrap,
                MaxHeight = 72,
                TextTrimming = TextTrimming.CharacterEllipsis,
                IsTextSelectionEnabled = true,
                FontSize = 13,
                Opacity = 0.88
            }
            : null;

        var open = Action("Open chat", () => MainWindow.Current?.OpenSession(id, _query));
        open.IsEnabled = id.Length > 0;
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accentStyle) && accentStyle is Style accent)
        {
            open.Style = accent;
        }
        var actions = Row(open);
        var stack = new StackPanel { Spacing = 8 };
        stack.Children.Add(titleRow);
        stack.Children.Add(meta);
        if (summaryBlock is not null) stack.Children.Add(summaryBlock);
        stack.Children.Add(actions);

        _list.Items.Add(new Border
        {
            Padding = new Thickness(18, 14, 18, 14),
            Margin = new Thickness(0, 0, 0, 8),
            HorizontalAlignment = HorizontalAlignment.Stretch,
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            Background = ResourceBrush("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = ResourceBrush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255)),
            Child = stack
        });
    }

    private void ShowListError(NxResponse response)
    {
        _list.Items.Clear();
        _list.Items.Add(new TextBlock
        {
            Text = response.Error ?? "Request failed",
            TextWrapping = TextWrapping.Wrap
        });
    }

    private static Border EmptyStateCard(string title, string body) =>
        Card(Line(title, title: true), Muted(body));

    private void UpdateAgentChip()
    {
        _agentChip.Children.Clear();
        var filter = AgentFilterValue();
        if (filter.Length == 0) return;
        var clear = new Button { Content = "✕", MinHeight = 26, Padding = new Thickness(8, 2, 8, 2) };
        ToolTipService.SetToolTip(clear, "Clear the agent filter");
        clear.Click += async (_, _) =>
        {
            _agentFilter.SelectedIndex = 0;
            await LoadPageAsync(reset: true);
        };
        _agentChip.Children.Add(Row(Chip("agent: " + filter), clear));
    }

    private static string DayGroupKey(string iso)
    {
        if (!TryParseUtc(iso, out var dt)) return "unknown";
        var local = dt.ToLocalTime().Date;
        return local.ToString("yyyy-MM-dd", CultureInfo.InvariantCulture);
    }

    private static string DayGroupLabel(string iso)
    {
        if (!TryParseUtc(iso, out var dt)) return "Earlier";
        var local = dt.ToLocalTime().Date;
        var today = DateTime.Now.Date;
        if (local == today) return "Today";
        if (local == today.AddDays(-1)) return "Yesterday";
        return local.ToString("dddd, MMMM d", CultureInfo.CurrentCulture);
    }

    private static string HumanizeTime(string iso)
    {
        if (!TryParseUtc(iso, out var dt)) return iso;
        var local = dt.ToLocalTime();
        var delta = DateTime.Now - local;
        if (delta.TotalMinutes < 1) return "Just now";
        if (delta.TotalMinutes < 60) return Nexus.Core.FriendlyTime.Relative(new DateTimeOffset(dt));
        if (delta.TotalHours < 24 && local.Date == DateTime.Now.Date) return local.ToString("h:mm tt", CultureInfo.CurrentCulture);
        if (delta.TotalDays < 7) return local.ToString("ddd h:mm tt", CultureInfo.CurrentCulture);
        return local.ToString("MMM d, h:mm tt", CultureInfo.CurrentCulture);
    }

    private static bool TryParseUtc(string iso, out DateTime dt)
    {
        dt = default;
        if (iso.Length == 0) return false;
        if (DateTimeOffset.TryParse(iso, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var dto))
        {
            dt = dto.UtcDateTime;
            return true;
        }
        return DateTime.TryParse(iso, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out dt);
    }

    private static Microsoft.UI.Xaml.Media.Brush ResourceBrush(string key, Windows.UI.Color fallback)
    {
        return Application.Current?.Resources.TryGetValue(key, out var value) == true && value is Microsoft.UI.Xaml.Media.Brush brush
            ? brush
            : new Microsoft.UI.Xaml.Media.SolidColorBrush(fallback);
    }
}
