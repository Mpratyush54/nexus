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
    private static readonly (string Id, string Label)[] Scopes =
    [
        ("all", "All"),
        ("organization", "Team"),
        ("project", "Project"),
        ("personal", "Personal"),
        ("session", "Session"),
        ("ephemeral", "Ephemeral")
    ];

    private const int PageSize = 20;
    private const int FetchBatchSize = 50;

    private readonly TextBox _query = new()
    {
        PlaceholderText = "Refine: redis, jwt, docker, auth…",
        HorizontalAlignment = HorizontalAlignment.Stretch
    };
    private readonly StackPanel _scopeBar = new() { Orientation = Orientation.Horizontal, Spacing = 4 };
    private readonly StackPanel _categoryBar = new() { Orientation = Orientation.Horizontal, Spacing = 6 };
    private readonly StackPanel _cards = new() { Spacing = 10, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly StackPanel _detail = new() { Spacing = 10 };
    private readonly Border _detailPane;
    private readonly TextBlock _count = new() { Opacity = 0.7, TextWrapping = TextWrapping.Wrap };
    private readonly Button _prevPage = new() { Content = "Previous", MinWidth = 96, IsEnabled = false };
    private readonly Button _nextPage = new() { Content = "Next", MinWidth = 96, IsEnabled = false };
    private readonly TextBlock _pageInfo = new() { VerticalAlignment = VerticalAlignment.Center, Opacity = 0.85, TextWrapping = TextWrapping.Wrap };
    private readonly Grid _split;
    private readonly Dictionary<string, ToggleButton> _scopeButtons = new(StringComparer.OrdinalIgnoreCase);
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
        var refresh = new Button { Content = "Refresh" };
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
        searchRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(_query, 0);
        Grid.SetColumn(refresh, 1);
        searchRow.Children.Add(_query);
        searchRow.Children.Add(refresh);

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
        list.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        list.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        list.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(_briefCard, 0);
        Grid.SetRow(_scopeBar, 1);
        Grid.SetRow(searchRow, 2);
        Grid.SetRow(_categoryBar, 3);
        Grid.SetRow(_split, 4);
        list.Children.Add(_briefCard);
        list.Children.Add(_scopeBar);
        list.Children.Add(searchRow);
        list.Children.Add(_categoryBar);
        list.Children.Add(_split);

        var root = new Grid { Padding = new Thickness(32, 24, 32, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        AddHeaderWithGraphToggle(root, PageHeader(
            "Memory & knowledge",
            "Browse retained decisions and facts for the project selected on Timeline. Search refines the list."), list);
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
        _scopeBar.Children.Clear();
        _scopeButtons.Clear();
        foreach (var (id, label) in Scopes)
        {
            var button = new ToggleButton
            {
                Content = label,
                MinHeight = 32,
                Padding = new Thickness(12, 4, 12, 4),
                Tag = id
            };
            button.Click += ScopeButton_Click;
            _scopeButtons[id] = button;
            _scopeBar.Children.Add(button);
        }
        SetActiveScope("all", reload: false);
    }

    private void ScopeButton_Click(object sender, RoutedEventArgs e)
    {
        if (sender is ToggleButton button && button.Tag is string id)
        {
            SetActiveScope(id, reload: true);
        }
    }

    private void SetActiveScope(string id, bool reload)
    {
        _activeScope = id;
        foreach (var (scopeId, button) in _scopeButtons)
        {
            button.IsChecked = string.Equals(scopeId, id, StringComparison.OrdinalIgnoreCase);
        }
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
            _detailPane.Visibility = Visibility.Collapsed;
            _selectedId = null;
            _briefProjectId = "";
            _briefCard.Visibility = Visibility.Collapsed;
            _cards.Children.Add(EmptyCard(
                "Choose a project",
                "Choose a project on the Timeline page to browse its shared knowledge."));
            return;
        }
        if (!string.Equals(_briefProjectId, projectId, StringComparison.Ordinal))
        {
            _briefProjectId = projectId;
            _ = LoadBriefAsync(projectId);
        }
        var browseKey = $"{projectId}\n{_activeScope}\n{_query.Text.Trim()}";
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
        _count.Text = $"Loading memories for {ShellProjectLabel()}…";
        _pageInfo.Text = "";

        var fill = await FillVisiblePageAsync(projectId, apiOffset, pageCarry);
        _loading = false;
        if (fill.Error is not null)
        {
            ShowError(_cards, fill.Error);
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
                _cards.Children.Add(EmptyCard(
                    "Nothing left to review",
                    "Every memory here has been confirmed or rejected. Clear the Review filter to see them all."));
            }
            else
            {
                _cards.Children.Add(EmptyCard(
                    string.IsNullOrWhiteSpace(_query.Text) ? "No memories in this scope" : "No matching memories",
                    string.IsNullOrWhiteSpace(_query.Text)
                        ? "Try another scope tab or go to the next page if available."
                        : "Clear the search box or try a different scope."));
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

        var scope = ScopeLabel(_activeScope) + (_reviewOnly ? " · not reviewed" : "");
        if (_visibleCount > 0)
        {
            var start = _pageIndex * PageSize + 1;
            var end = _pageIndex * PageSize + _visibleCount;
            _count.Text = $"Showing {start}–{end} · Page {pageNumber} · {scope} · {ShellProjectLabel()}";
        }
        else
        {
            _count.Text = $"Page {pageNumber} · {scope} · {ShellProjectLabel()}";
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
        return true;
    }

    private UIElement BuildMemoryCard(JsonElement item, string id)
    {
        var key = NxJson.Text(item, "key");
        var text = NxJson.Text(item, "text", "content", "title", "summary");
        var level = NxJson.Text(item, "level");
        var proposed = IsProposed(item);
        var status = proposed ? "" : NxJson.PublicStatus(NxJson.Text(item, "status"));
        var category = FormatCategory(NxJson.Text(item, "category"));
        var title = key.Length > 0 ? key : (text.Length > 0 ? Truncate(text, 120) : id);
        var preview = text.Length > 0 && key.Length > 0 ? Truncate(text, 160) : "";

        var meta = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        if (proposed) meta.Children.Add(AccentChip("Not reviewed", NexusPalette.Amber));
        if (level.Length > 0) meta.Children.Add(Chip(level));
        if (category.Length > 0) meta.Children.Add(Chip(category));
        if (status.Length > 0) meta.Children.Add(Chip(status));
        if (DatesLine(item) is { Length: > 0 } dates) meta.Children.Add(Chip(dates));

        var body = new StackPanel { Spacing = 6 };
        body.Children.Add(Line(title, title: true));
        if (preview.Length > 0) body.Children.Add(Muted(preview));
        if (StaleBadge(item) is { } stale) body.Children.Add(stale);
        body.Children.Add(meta);
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
            border.BorderBrush = new SolidColorBrush(Windows.UI.Color.FromArgb(180, 245, 158, 11));
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
                ? new SolidColorBrush(Windows.UI.Color.FromArgb(180, 245, 158, 11))
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
        _detail.Children.Add(Line(text.Length > 0 ? text : key, title: true));
        var chips = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        if (proposed) chips.Children.Add(AccentChip("Not reviewed", NexusPalette.Amber));
        if (level.Length > 0) chips.Children.Add(Chip(level));
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
        if (ordered.Count == 0) return;
        _categoryBar.Children.Add(Muted("Category:"));
        _categoryBar.Children.Add(CategoryPill("All", null));
        foreach (var cat in ordered)
        {
            _categoryBar.Children.Add(CategoryPill(FormatCategory(cat), cat));
        }
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
        if (active)
        {
            button.Background = new SolidColorBrush(Windows.UI.Color.FromArgb(48, 245, 158, 11));
        }
        button.Click += async (_, _) =>
        {
            _categoryFilter = categoryId;
            await LoadPageAsync(reset: true);
        };
        return button;
    }

    private static UIElement EmptyCard(string title, string body) =>
        Card(Line(title, title: true), Muted(body));

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
        if (await dialog.ShowAsync() == ContentDialogResult.Primary) await Mutate(NxMethods.MemoryForget, id);
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
        return Application.Current?.Resources.TryGetValue(key, out var value) == true && value is Brush brush
            ? brush
            : new SolidColorBrush(fallback);
    }
}
