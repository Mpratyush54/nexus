using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

public sealed partial class MemoryPage : NexusPage
{
    // Who a memory is for, in plain words. The last two are rarely needed
    // and sit under "More".
    private static readonly (string Id, string Label, bool Advanced)[] Scopes =
    [
        ("all", "All", false),
        ("organization", "Team", false),
        ("project", "This project", false),
        ("personal", "Only me", false),
        ("session", "A single chat", true),
        ("ephemeral", "Short-lived", true)
    ];
    // One filter row: search, who it's for, topic and tag pickers, review, refresh.
    private readonly ComboBox _scopePicker = new() { MinWidth = 150, MinHeight = 32 };
    private readonly ComboBox _topicPicker = new() { MinWidth = 140, MinHeight = 32, Visibility = Visibility.Collapsed };
    private readonly ComboBox _tagPicker = new() { MinWidth = 120, MinHeight = 32, Visibility = Visibility.Collapsed };
    private bool _pickerSync;

    private const int PageSize = 20;
    private const int FetchBatchSize = 50;

    private readonly TextBox _query = new()
    {
        PlaceholderText = "Search memories",
        HorizontalAlignment = HorizontalAlignment.Stretch
    };
    private readonly StackPanel _categoryBar = new() { Orientation = Orientation.Horizontal, Spacing = 6 };
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly StackPanel _detail = new() { Spacing = 10 };
    private readonly Border _detailPane;
    private readonly TextBlock _count = new() { Opacity = 0.7, TextWrapping = TextWrapping.Wrap };
    private readonly Button _prevPage = new() { Content = "Previous", MinWidth = 96, IsEnabled = false };
    private readonly Button _nextPage = new() { Content = "Next", MinWidth = 96, IsEnabled = false };
    // The Previous / page / Next row. Referenced directly: Parent of a
    // control in a row that isn't on screen can be null.
    private readonly Grid _pagerBar = new() { ColumnSpacing = 8, Visibility = Visibility.Collapsed };
    private readonly TextBlock _pageInfo = new() { VerticalAlignment = VerticalAlignment.Center, Opacity = 0.85, TextWrapping = TextWrapping.Wrap };
    private readonly Grid _split;
    private readonly Dictionary<string, JsonElement> _itemsById = new(StringComparer.Ordinal);

    private readonly List<int> _pageOffsets = [0];
    private readonly List<List<JsonElement>> _pageStartCarries = [[]];
    private int _pageIndex;
    private bool _hasNextLogicalPage;
    private string _activeScope = "all";
    private string? _categoryFilter;
    private string? _selectedId;
    private bool _loading;
    private int _visibleCount;

    // Review state spans every batch fetched for the current project, scope
    // and search, so "Review N new" and the category chips do not shrink to
    // whatever the visible page happens to hold.
    private readonly Dictionary<string, bool> _seenProposed = new(StringComparer.Ordinal);
    private readonly HashSet<string> _seenCategories = new(StringComparer.OrdinalIgnoreCase);
    private string _browseKey = "";
    private bool _reviewOnly;
    private bool _moreUnscanned;

    public MemoryPage()
    {
        BuildScopeBar();
        _query.KeyDown += async (_, e) =>
        {
            if (e.Key == Windows.System.VirtualKey.Enter) await LoadPageAsync(reset: true);
        };
        _prevPage.Click += async (_, _) => await GoToPageAsync(_pageIndex - 1);
        _nextPage.Click += async (_, _) => await GoToPageAsync(_pageIndex + 1);
        var refresh = new Button { Content = new FontIcon { Glyph = "\uE72C", FontSize = 14 }, MinWidth = 34, MinHeight = 32, Padding = new Thickness(8) };
        ToolTipService.SetToolTip(refresh, "Refresh");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(refresh, "Refresh memories");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_query, "Search memories");
        refresh.Click += async (_, _) =>
        {
            _briefProjectId = "";
            await LoadPageAsync(reset: true);
        };
        _briefCard = BuildBriefCard();

        _detailPane = new Border
        {
            Padding = new Thickness(18, 16, 18, 16),
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            Background = ResourceBrush("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = ResourceBrush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255)),
            Child = _detail,
            Visibility = Visibility.Collapsed
        };

        var searchRow = new Grid { ColumnSpacing = 8 };
        searchRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        for (var c = 0; c < 5; c++) searchRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(_query, 0);
        Grid.SetColumn(_scopePicker, 1);
        Grid.SetColumn(_topicPicker, 2);
        Grid.SetColumn(_tagPicker, 3);
        Grid.SetColumn(_categoryBar, 4);
        Grid.SetColumn(refresh, 5);
        searchRow.Children.Add(_query);
        searchRow.Children.Add(_scopePicker);
        searchRow.Children.Add(_topicPicker);
        searchRow.Children.Add(_tagPicker);
        searchRow.Children.Add(_categoryBar);
        searchRow.Children.Add(refresh);
        _categoryBar.VerticalAlignment = VerticalAlignment.Center;
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_scopePicker, "Who the memories are for");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_topicPicker, "Topic");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_tagPicker, "Tag");
        ToolTipService.SetToolTip(_scopePicker, "Who the memories are for");

        var pagerBar = _pagerBar;
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

        var listPanel = new StackPanel { Spacing = 14 };
        listPanel.Children.Add(_count);
        listPanel.Children.Add(_cards);
        listPanel.Children.Add(pagerBar);
        var listScroller = new ScrollViewer
        {
            Content = listPanel,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled
        };

        _split = new Grid { ColumnSpacing = 16 };
        _split.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        _split.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(340) });
        Grid.SetColumn(listScroller, 0);
        Grid.SetColumn(_detailPane, 1);
        _split.Children.Add(listScroller);
        _split.Children.Add(_detailPane);
        _split.SizeChanged += (_, _) => UpdateDetailPaneLayout();

        // The list view; the "Graph" toggle in the header swaps it for the
        // memory graph (MemoryPage.Graph.cs).
        var list = new Grid { RowSpacing = 16 };
        list.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        list.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        list.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(_briefCard, 0);
        Grid.SetRow(searchRow, 1);
        Grid.SetRow(_split, 2);
        list.Children.Add(_briefCard);
        list.Children.Add(searchRow);
        list.Children.Add(_split);

        var root = new Grid { Padding = NexusTokens.PagePadding, RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        AddHeaderWithGraphToggle(root, PageHeader(
            "Memory",
            "What your agents have learned: decisions, conventions and facts they reuse in every chat."), list);
        Content = root;
        WatchShellProject(() => LoadPageAsync(reset: true));
    }

    protected override void OnEnter(object? parameter)
    {
        if (parameter is string query)
        {
            _query.Text = query;
        }
        ApplySavedScope(ShellState.Current.MemoryScope);
        _briefProjectId = "";
        _ = LoadPageAsync(reset: true);
    }

    private void BuildScopeBar()
    {
        _scopePicker.Items.Clear();
        foreach (var (id, label, advanced) in Scopes)
        {
            var item = new ComboBoxItem { Content = label, Tag = id };
            if (advanced) ToolTipService.SetToolTip(item, id == "session" ? "Memories kept for a single chat" : "Short-lived notes that expire");
            _scopePicker.Items.Add(item);
        }
        _scopePicker.SelectionChanged += (_, _) =>
        {
            if (_pickerSync || _scopePicker.SelectedItem is not ComboBoxItem { Tag: string id }) return;
            SetActiveScope(id, reload: true);
        };
        _topicPicker.SelectionChanged += async (_, _) =>
        {
            if (_pickerSync || _topicPicker.SelectedItem is not ComboBoxItem item) return;
            _categoryFilter = item.Tag as string;
            await LoadPageAsync(reset: true);
        };
        _tagPicker.SelectionChanged += async (_, _) =>
        {
            if (_pickerSync || _tagPicker.SelectedItem is not ComboBoxItem item) return;
            _tagFilter = item.Tag as string;
            await LoadPageAsync(reset: true);
        };
        SetActiveScope("all", reload: false);
    }

    private void SetActiveScope(string id, bool reload)
    {
        _activeScope = id;
        _pickerSync = true;
        try { _scopePicker.SelectedItem = _scopePicker.Items.OfType<ComboBoxItem>().FirstOrDefault(i => string.Equals(i.Tag as string, id, StringComparison.OrdinalIgnoreCase)); }
        finally { _pickerSync = false; }
        ShellState.Current.MemoryScope = id;
        if (reload)
        {
            _ = LoadPageAsync(reset: true);
        }
    }

    private void ApplySavedScope(string saved)
    {
        var match = Scopes.FirstOrDefault(s => string.Equals(s.Id, saved, StringComparison.OrdinalIgnoreCase));
        var id = match.Id;
        if (string.IsNullOrEmpty(id)) id = "all";
        SetActiveScope(id, reload: false);
    }

    private async Task GoToPageAsync(int targetIndex)
    {
        if (_loading || targetIndex < 0) return;
        if (targetIndex > _pageIndex)
        {
            if (!_hasNextLogicalPage || targetIndex >= _pageOffsets.Count) return;
            _pageIndex = targetIndex;
        }
        else if (targetIndex < _pageIndex)
        {
            if (targetIndex >= _pageOffsets.Count) return;
            _pageOffsets.RemoveRange(targetIndex + 1, _pageOffsets.Count - targetIndex - 1);
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
        if (_loading) return;
        var projectId = ShellProjectId();
        if (projectId.Length == 0)
        {
            _cards.Children.Clear();
            _categoryBar.Children.Clear();
            _count.Text = "";
            _pageInfo.Text = "";
            _prevPage.IsEnabled = false;
            _nextPage.IsEnabled = false;
            _pagerBar.Visibility = Visibility.Collapsed;
            _detailPane.Visibility = Visibility.Collapsed;
            _selectedId = null;
            _briefProjectId = "";
            _briefCard.Visibility = Visibility.Collapsed;
            _cards.Children.Add(StateView.Empty("\uE8B7", "Pick a project",
                "Memories belong to a project. Choose one at the top of the sidebar (" + Shortcuts.Label("P") + ")."));
            return;
        }
        if (!string.Equals(_briefProjectId, projectId, StringComparison.Ordinal))
        {
            _briefProjectId = projectId;
            _ = LoadBriefAsync(projectId);
        }
        var browseKey = $"{projectId}\n{_activeScope}\n{_query.Text.Trim()}\n{_tagFilter}";
        if (!string.Equals(browseKey, _browseKey, StringComparison.Ordinal))
        {
            _browseKey = browseKey;
            _seenProposed.Clear();
            _seenCategories.Clear();
        }
        if (reset)
        {
            _pageOffsets.Clear();
            _pageOffsets.Add(0);
            _pageStartCarries.Clear();
            _pageStartCarries.Add([]);
            _pageIndex = 0;
            _hasNextLogicalPage = false;
        }

        var apiOffset = _pageIndex < _pageOffsets.Count ? _pageOffsets[_pageIndex] : 0;
        var pageCarry = _pageIndex < _pageStartCarries.Count
            ? _pageStartCarries[_pageIndex]
            : [];

        _loading = true;
        _prevPage.IsEnabled = false;
        _nextPage.IsEnabled = false;
        _visibleCount = 0;
        _itemsById.Clear();
        _selectedId = null;
        _detailPane.Visibility = Visibility.Collapsed;
        _detail.Children.Clear();
        _cards.Children.Clear();
        _categoryBar.Children.Clear();
        _count.Text = "";
        _pageInfo.Text = "";
        // Shown again by UpdatePaginationUi when there is more than one page;
        // an empty, offline or failed list has nothing to page.
        _pagerBar.Visibility = Visibility.Collapsed;
        _cards.Children.Add(Skeleton.Cards(3));

        var fill = await FillVisiblePageAsync(projectId, apiOffset, pageCarry);
        _loading = false;
        _cards.Children.Clear();
        if (fill.Error is not null)
        {
            _cards.Children.Add(StateView.Error("Couldn't load memories", fill.Error.Error, () => _ = LoadPageAsync(reset: true), "Your memories are taking a moment…"));
            UpdatePaginationUi();
            return;
        }

        _visibleCount = fill.VisibleItems.Count;
        _hasNextLogicalPage = fill.HasMore;
        if (fill.HasMore)
        {
            while (_pageOffsets.Count <= _pageIndex + 1)
            {
                _pageOffsets.Add(0);
                _pageStartCarries.Add([]);
            }
            _pageOffsets[_pageIndex + 1] = fill.NextApiOffset;
            _pageStartCarries[_pageIndex + 1] = fill.Carry;
        }
        else if (_pageOffsets.Count > _pageIndex + 1)
        {
            _pageOffsets.RemoveRange(_pageIndex + 1, _pageOffsets.Count - _pageIndex - 1);
            _pageStartCarries.RemoveRange(_pageIndex + 1, _pageStartCarries.Count - _pageIndex - 1);
        }

        foreach (var item in fill.VisibleItems)
        {
            var id = NxJson.Text(item, "id");
            if (id.Length == 0) continue;
            _itemsById[id] = item;
            _cards.Children.Add(BuildMemoryCard(item, id));
        }
        RebuildCategoryBar();
        UpdatePaginationUi();

        if (_cards.Children.Count == 0)
        {
            if (_reviewOnly)
            {
                _cards.Children.Add(StateView.Empty("\uE73E", "All caught up",
                    "Every memory here has been confirmed or rejected.", "Show all memories", () => { _reviewOnly = false; _ = LoadPageAsync(reset: true); }));
            }
            else
            {
                var searching = !string.IsNullOrWhiteSpace(_query.Text);
                _cards.Children.Add(searching
                    ? StateView.Empty("\uE721", "No memories match", "Try other words, or clear the search.", "Clear search", () => { _query.Text = ""; _ = LoadPageAsync(reset: true); })
                    : _activeScope != "all"
                        ? StateView.Empty("\uE8F1", "Nothing here yet", "No memories of this kind in " + ShellProjectLabel() + ".", "Show all", () => SetActiveScope("all", reload: true))
                        : StateView.Empty("\uE8F1", "No memories yet",
                            "As you chat, agents save decisions and conventions here so the next chat starts with them."));
            }
        }
        UpdateDetailPaneLayout();
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
        // One page needs no pager.
        var paged = _pageIndex > 0 || hasNext;
        _pagerBar.Visibility = paged ? Visibility.Visible : Visibility.Collapsed;

        if (_visibleCount > 0)
        {
            var start = _pageIndex * PageSize + 1;
            var end = _pageIndex * PageSize + _visibleCount;
            var noun = _reviewOnly ? "to review" : _visibleCount == 1 && !paged ? "memory" : "memories";
            _count.Text = paged ? $"{start}–{end} {noun}" : $"{_visibleCount} {noun}";
        }
        else
        {
            _count.Text = "";
        }
    }

    private sealed record VisiblePageFill(
        List<JsonElement> VisibleItems,
        int RawFetched,
        int NextApiOffset,
        List<JsonElement> Carry,
        bool HasMore,
        NxResponse? Error);

    private async Task<VisiblePageFill> FillVisiblePageAsync(
        string projectId,
        int apiOffset,
        List<JsonElement> pageCarry)
    {
        var pending = new Queue<JsonElement>(pageCarry);
        var fetchOffset = apiOffset;
        var fetchedAny = pageCarry.Count > 0;
        var rawFetched = 0;
        var apiHasMore = true;
        var visibleItems = new List<JsonElement>();

        while (visibleItems.Count < PageSize)
        {
            if (pending.Count == 0)
            {
                if (fetchedAny && !apiHasMore)
                {
                    break;
                }

                var body = new Dictionary<string, object?>
                {
                    ["browse"] = true,
                    ["q"] = _query.Text,
                    ["project_id"] = projectId,
                    ["limit"] = FetchBatchSize,
                    ["offset"] = fetchOffset
                };
                if (_activeScope != "all")
                {
                    body["level"] = _activeScope;
                }
                // The server filters by tag across the whole project.
                if (_tagFilter is { Length: > 0 } tag) body["tags"] = tag;
                var response = await Call(NxMethods.MemorySearch, body);
                if (!response.Ok)
                {
                    return new VisiblePageFill(visibleItems, rawFetched, fetchOffset, pending.ToList(), false, response);
                }

                fetchedAny = true;
                var batch = NxJson.Items(response.Result).ToList();
                rawFetched += batch.Count;
                apiHasMore = response.Result is { } result && NxJson.Text(result, "has_more") == "true";
                fetchOffset += batch.Count;
                if (batch.Count == 0 && !apiHasMore)
                {
                    break;
                }

                foreach (var item in batch)
                {
                    NoteSeen(item);
                    pending.Enqueue(item);
                }
            }

            while (pending.Count > 0 && visibleItems.Count < PageSize)
            {
                var item = pending.Dequeue();
                if (!PassesClientFilters(item))
                {
                    continue;
                }
                var id = NxJson.Text(item, "id");
                if (id.Length == 0)
                {
                    continue;
                }
                visibleItems.Add(item);
            }
        }

        var carry = pending.ToList();
        var hasMore = carry.Count > 0 || apiHasMore;
        _moreUnscanned = apiHasMore;
        return new VisiblePageFill(visibleItems, rawFetched, fetchOffset, carry, hasMore, null);
    }

    private bool PassesClientFilters(JsonElement item)
    {
        if (_activeScope != "all")
        {
            var level = NxJson.Text(item, "level");
            if (!string.Equals(level, _activeScope, StringComparison.OrdinalIgnoreCase))
            {
                return false;
            }
        }
        // The search route has no category or status parameter, so both are
        // applied here; FillVisiblePageAsync keeps fetching batches until a
        // full page matches, so the filter covers the whole project.
        if (_categoryFilter is { Length: > 0 } cat &&
            !string.Equals(NormalizeCategory(NxJson.Text(item, "category")), cat, StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }
        if (_reviewOnly && !IsProposed(item))
        {
            return false;
        }
        // The project brief has its own card above the list; showing its
        // memory again as "project/brief" said the same thing twice.
        if (NxJson.Text(item, "key").Equals("project/brief", StringComparison.OrdinalIgnoreCase) && _query.Text.Trim().Length == 0)
        {
            return false;
        }
        return PassesTagFilter(item);
    }

    // Who a memory is for, in the same words as the filter ("Team", not "organization").
    private static string LevelLabel(string level) =>
        Scopes.FirstOrDefault(s => s.Id != "all" && string.Equals(s.Id, level, StringComparison.OrdinalIgnoreCase)).Label ?? level;

    // Memory keys are slugs ("webhooks/signature-tolerance"); show them as
    // words ("Webhooks: signature tolerance").
    internal static string HumanKey(string key)
    {
        var parts = key.Split('/', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries)
            .Select(p => p.Replace('-', ' ').Replace('_', ' ')).ToList();
        if (parts.Count == 0) return key;
        if (parts.Count == 1 && parts[0] == "brief") return "Project brief";
        if (parts.Count == 2 && parts[0] == "project" && parts[1] == "brief") return "Project brief";
        var head = char.ToUpperInvariant(parts[0][0]) + parts[0][1..];
        return parts.Count == 1 ? head : head + ": " + string.Join(" · ", parts.Skip(1));
    }

    private UIElement BuildMemoryCard(JsonElement item, string id)
    {
        var key = NxJson.Text(item, "key");
        var text = NxJson.Text(item, "text", "content", "title", "summary");
        var level = NxJson.Text(item, "level");
        var proposed = IsProposed(item);
        var status = proposed ? "" : NxJson.PublicStatus(NxJson.Text(item, "status"));
        var category = FormatCategory(NxJson.Text(item, "category"));
        // Cards preview one plain line: a brief stored as markdown would
        // otherwise show "# Project brief ## Key dec…". The detail pane
        // renders the markdown.
        var plain = MarkdownText.Plain(text);
        var title = key.Length > 0 ? HumanKey(key) : (plain.Length > 0 ? Truncate(plain, 120) : id);
        var preview = plain.Length > 0 && key.Length > 0 ? Truncate(plain, 160) : "";

        var meta = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        if (proposed) meta.Children.Add(AccentChip("Not reviewed", NexusPalette.Amber));
        if (level.Length > 0) meta.Children.Add(Chip(LevelLabel(level)));
        if (category.Length > 0) meta.Children.Add(Chip(category));
        if (status.Length > 0) meta.Children.Add(Chip(status));
        if (DatesLine(item) is { Length: > 0 } dates) meta.Children.Add(Chip(dates));

        var body = new StackPanel { Spacing = 6 };
        body.Children.Add(Line(title, title: true));
        if (preview.Length > 0) body.Children.Add(Muted(preview));
        if (StaleBadge(item) is { } stale) body.Children.Add(stale);
        body.Children.Add(meta);
        body.Children.Add(TagRow(item, id));
        if (SourceSessionId(item) is { Length: > 0 } sessionId) body.Children.Add(ChatLink(sessionId));
        body.Children.Add(MemoryActions(item, id, proposed));

        var border = Card(body);
        border.Tag = id;
        border.PointerPressed += (_, e) =>
        {
            if (e.GetCurrentPoint(border).Properties.IsLeftButtonPressed)
            {
                SelectMemory(id);
            }
        };
        if (string.Equals(_selectedId, id, StringComparison.Ordinal))
        {
            border.BorderBrush = NexusPalette.Accent;
        }
        return border;
    }

    private void SelectMemory(string id)
    {
        _selectedId = id;
        foreach (var child in _cards.Children.OfType<Border>())
        {
            var selected = string.Equals(child.Tag as string, id, StringComparison.Ordinal);
            child.BorderBrush = selected
                ? NexusPalette.Accent
                : ResourceBrush("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255));
        }
        if (!_itemsById.TryGetValue(id, out var item))
        {
            _detail.Children.Clear();
            _detailPane.Visibility = Visibility.Collapsed;
            return;
        }
        RenderDetail(item, id);
        UpdateDetailPaneLayout();
    }

    private void RenderDetail(JsonElement item, string id)
    {
        _detail.Children.Clear();
        var key = NxJson.Text(item, "key");
        var text = NxJson.Text(item, "text", "content", "title", "summary");
        var level = NxJson.Text(item, "level");
        var proposed = IsProposed(item);
        var status = proposed ? "" : NxJson.PublicStatus(NxJson.Text(item, "status"));
        var source = NxJson.Text(item, "source", "proposed_by");

        _detail.Children.Add(SectionHeader("Detail"));
        if (key.Length > 0) _detail.Children.Add(Monospace(key));
        if (MarkdownText.LooksLikeMarkdown(text))
        {
            var document = SessionPage.FormattedMessage(text);
            document.FontSize = 13;
            _detail.Children.Add(document);
        }
        else
        {
            _detail.Children.Add(Line(text.Length > 0 ? text : key, title: true));
        }
        var chips = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        if (proposed) chips.Children.Add(AccentChip("Not reviewed", NexusPalette.Amber));
        if (level.Length > 0) chips.Children.Add(Chip(LevelLabel(level)));
        if (status.Length > 0) chips.Children.Add(Chip(status));
        _detail.Children.Add(chips);
        if (StaleBadge(item) is { } stale) _detail.Children.Add(stale);
        if (source.Length > 0) _detail.Children.Add(Muted($"Source: {source}"));
        if (FriendlyDate(NxJson.Text(item, "created_at")) is { Length: > 0 } created) _detail.Children.Add(Muted($"Created {created}"));
        if (FriendlyDate(NxJson.Text(item, "updated_at")) is { Length: > 0 } updated) _detail.Children.Add(Muted($"Updated {updated}"));
        _detail.Children.Add(Muted(LastUsedText(item)));
        if (SourceSessionId(item) is { Length: > 0 } sessionId) _detail.Children.Add(ChatLink(sessionId));
        _detail.Children.Add(Muted($"ID: {id}"));
        _detail.Children.Add(MemoryActions(item, id, proposed, compact: true));
    }

    private void UpdateDetailPaneLayout()
    {
        var wide = _split.ActualWidth >= 900 && _selectedId is { Length: > 0 };
        _detailPane.Visibility = wide ? Visibility.Visible : Visibility.Collapsed;
        _split.ColumnDefinitions[1].Width = wide ? new GridLength(340) : new GridLength(0);
    }

    private void RebuildCategoryBar()
    {
        _categoryBar.Children.Clear();
        var proposed = _seenProposed.Count(pair => pair.Value);
        if (proposed > 0 || _reviewOnly)
        {
            var label = proposed == 0
                ? "Review: none left"
                : $"Review {proposed}{(_moreUnscanned ? "+" : "")} new";
            var review = new Button
            {
                Content = label,
                MinHeight = 28,
                Padding = new Thickness(10, 2, 10, 3),
                FontSize = 12,
                Foreground = NexusPalette.Amber
            };
            ToolTipService.SetToolTip(review, _reviewOnly
                ? "Showing only memories nobody has reviewed. Click to show everything."
                : "Show only memories nobody has confirmed or rejected yet.");
            if (_reviewOnly) review.Background = new SolidColorBrush(Windows.UI.Color.FromArgb(48, 245, 158, 11));
            review.Click += async (_, _) =>
            {
                _reviewOnly = !_reviewOnly;
                await LoadPageAsync(reset: true);
            };
            _categoryBar.Children.Add(review);
        }
        var categories = new HashSet<string>(_seenCategories, StringComparer.OrdinalIgnoreCase);
        if (_categoryFilter is { Length: > 0 } active) categories.Add(active);
        var ordered = categories.OrderBy(c => c, StringComparer.OrdinalIgnoreCase).ToList();
        _pickerSync = true;
        try
        {
            _topicPicker.Items.Clear();
            _topicPicker.Items.Add(new ComboBoxItem { Content = "Any topic", Tag = null });
            foreach (var cat in ordered) _topicPicker.Items.Add(new ComboBoxItem { Content = FormatCategory(cat), Tag = cat });
            _topicPicker.SelectedItem = _topicPicker.Items.OfType<ComboBoxItem>().FirstOrDefault(i => string.Equals(i.Tag as string, _categoryFilter, StringComparison.OrdinalIgnoreCase)) ?? _topicPicker.Items[0];
            _topicPicker.Visibility = ordered.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        }
        finally { _pickerSync = false; }
        AddTagPills();
    }

    private Button CategoryPill(string label, string? categoryId)
    {
        var active = string.Equals(_categoryFilter, categoryId, StringComparison.OrdinalIgnoreCase) ||
                     (_categoryFilter is null && categoryId is null);
        var button = new Button
        {
            Content = label,
            MinHeight = 28,
            Padding = new Thickness(10, 2, 10, 3),
            FontSize = 12
        };
        if (active) MarkActivePill(button);
        button.Click += async (_, _) =>
        {
            _categoryFilter = categoryId;
            await LoadPageAsync(reset: true);
        };
        return button;
    }

    // Selected filter pill: the brand tint, same as the scope toggles' family.
    internal static void MarkActivePill(Button button)
    {
        button.Background = NexusPalette.AccentFill;
        button.BorderBrush = NexusPalette.Accent;
        button.Foreground = NexusPalette.Accent;
    }

    private async Task Mutate(string method, string id)
    {
        var response = await Call(method, new { id });
        if (!response.Ok)
        {
            ShowListError(response.Error ?? "Memory update failed");
            return;
        }
        await LoadPageAsync(reset: true);
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
        if (await dialog.ShowThemedAsync() == ContentDialogResult.Primary) await Mutate(NxMethods.MemoryForget, id);
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

    private static string ScopeLabel(string scope) =>
        Scopes.FirstOrDefault(s => string.Equals(s.Id, scope, StringComparison.OrdinalIgnoreCase)).Label is { Length: > 0 } label
            ? label
            : "All";

    private static string NormalizeCategory(string category)
    {
        var c = category.Trim().ToLowerInvariant();
        return c.Length > 0 ? c : "general";
    }

    private static string FormatCategory(string category)
    {
        var c = NormalizeCategory(category);
        return c switch
        {
            "general" => "General",
            "architecture" => "Architecture",
            "infrastructure" => "Infrastructure",
            "auth" => "Authentication",
            "api" => "API",
            "conventions" => "Conventions",
            "dependencies" => "Dependencies",
            _ => char.ToUpperInvariant(c[0]) + c[1..]
        };
    }

    private static string Truncate(string text, int max)
    {
        var trimmed = text.Trim();
        return trimmed.Length <= max ? trimmed : trimmed[..max].TrimEnd() + "…";
    }

    private static Brush ResourceBrush(string key, Windows.UI.Color fallback)
    {
        return ThemeService.Brush(key, fallback);
    }
}
