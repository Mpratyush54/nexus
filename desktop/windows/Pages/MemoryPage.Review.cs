using System.Globalization;
using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

// Correcting and trusting memories: review (confirm / reject), edit,
// version history, source chats, staleness and the project brief.
public sealed partial class MemoryPage
{
    private static readonly (string Id, string Label)[] Kinds =
    [
        ("fact", "Fact"),
        ("decision", "Decision"),
        ("preference", "Preference"),
        ("constraint", "Constraint"),
        ("pattern", "Pattern"),
        ("episode_summary", "Episode summary")
    ];

    private const string ErrorTag = "memory-error";

    private readonly Border _briefCard;
    private readonly StackPanel _briefContent = new() { Spacing = 6 };
    private readonly StackPanel _briefSources = new() { Orientation = Orientation.Horizontal, Spacing = 10 };
    private readonly TextBlock _briefUpdated = new() { FontSize = 12, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center };
    private readonly TextBlock _briefError = new() { FontSize = 12, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
    private readonly Button _briefRefresh = new() { Content = "Refresh", MinHeight = 28, Padding = new Thickness(10, 2, 10, 3), FontSize = 12 };
    private string _briefProjectId = "";
    private int _briefVersion;
    private readonly Dictionary<string, Task<string>> _chatTitles = new(StringComparer.Ordinal);

    // ── Project brief ────────────────────────────────────────────────────

    private Border BuildBriefCard()
    {
        var title = new TextBlock
        {
            Text = "Project brief",
            FontSize = 13,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            VerticalAlignment = VerticalAlignment.Center
        };
        var header = new Grid { ColumnSpacing = 10 };
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        header.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(_briefUpdated, 1);
        Grid.SetColumn(_briefRefresh, 2);
        header.Children.Add(title);
        header.Children.Add(_briefUpdated);
        header.Children.Add(_briefRefresh);
        ToolTipService.SetToolTip(_briefRefresh, "Rebuild the brief from this project's memories now (project owners and admins)");
        _briefRefresh.Click += async (_, _) => await RefreshBriefAsync();
        _briefError.Foreground = NexusPalette.Amber;

        var body = new ScrollViewer
        {
            Content = _briefContent,
            MaxHeight = 180,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto
        };
        var card = Card(header, body, _briefSources, _briefError);
        card.MinHeight = 0;
        card.Padding = new Thickness(16, 12, 16, 12);
        card.Visibility = Visibility.Collapsed;
        return card;
    }

    private async Task LoadBriefAsync(string projectId)
    {
        var version = ++_briefVersion;
        var response = await Call(NxMethods.ProjectsBrief, new { project_id = projectId });
        if (version != _briefVersion || !string.Equals(projectId, ShellProjectId(), StringComparison.Ordinal)) return;
        if (!response.Ok)
        {
            // The brief route ships separately from this app: a server
            // without it (404/405) or no connection simply hides the card.
            if (IsMissingRoute(response.Error) || ShellState.Current.Offline)
            {
                _briefCard.Visibility = Visibility.Collapsed;
                return;
            }
            RenderBrief(null);
            ShowBriefError("Couldn't load the project brief: " + (response.Error ?? "request failed"));
            return;
        }
        RenderBrief(response.Result);
    }

    private async Task RefreshBriefAsync()
    {
        var projectId = ShellProjectId();
        if (projectId.Length == 0) return;
        _briefRefresh.IsEnabled = false;
        _briefRefresh.Content = "Refreshing…";
        _briefError.Visibility = Visibility.Collapsed;
        var version = ++_briefVersion;
        var response = await Call(NxMethods.ProjectsBriefRefresh, new { project_id = projectId });
        _briefRefresh.IsEnabled = true;
        _briefRefresh.Content = "Refresh";
        if (version != _briefVersion) return;
        if (!response.Ok)
        {
            ShowBriefError(FriendlyBriefError(response.Error));
            return;
        }
        RenderBrief(response.Result);
    }

    private void RenderBrief(JsonElement? result)
    {
        _briefCard.Visibility = Visibility.Visible;
        _briefContent.Children.Clear();
        _briefSources.Children.Clear();
        _briefError.Visibility = Visibility.Collapsed;
        var content = result is { ValueKind: JsonValueKind.Object } body ? NxJson.Text(body, "content").Trim() : "";
        var updated = result is { ValueKind: JsonValueKind.Object } b2 ? FriendlyDate(NxJson.Text(b2, "updated_at")) : "";
        _briefUpdated.Text = updated.Length > 0 ? $"Updated {updated}" : "";
        if (content.Length == 0)
        {
            _briefContent.Children.Add(Muted(
                "No brief yet. Nexus writes one from this project's memories so every agent starts with the same context."));
            _briefSources.Visibility = Visibility.Collapsed;
            return;
        }
        var document = SessionPage.FormattedMessage(content);
        document.FontSize = 13;
        _briefContent.Children.Add(document);

        var sources = new List<string>();
        if (result is { ValueKind: JsonValueKind.Object } b3 &&
            b3.TryGetProperty("sources", out var list) && list.ValueKind == JsonValueKind.Array)
        {
            foreach (var source in list.EnumerateArray())
            {
                var id = source.ValueKind == JsonValueKind.String
                    ? source.GetString() ?? ""
                    : NxJson.Text(source, "session_id", "id");
                if (id.Length > 0 && !sources.Contains(id)) sources.Add(id);
            }
        }
        _briefSources.Visibility = sources.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        if (sources.Count == 0) return;
        _briefSources.Children.Add(new TextBlock { Text = "From chats:", FontSize = 12, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center });
        const int shown = 4;
        foreach (var id in sources.Take(shown)) _briefSources.Children.Add(ChatLink(id, ""));
        if (sources.Count > shown)
        {
            _briefSources.Children.Add(new TextBlock { Text = $"+{sources.Count - shown} more", FontSize = 12, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center });
        }
    }

    private void ShowBriefError(string message)
    {
        _briefError.Text = message;
        _briefError.Visibility = Visibility.Visible;
    }

    private static bool IsMissingRoute(string? error)
    {
        var e = error ?? "";
        return e.Contains("404", StringComparison.Ordinal) ||
               e.Contains("405", StringComparison.Ordinal) ||
               e.Contains("page not found", StringComparison.OrdinalIgnoreCase) ||
               e.Contains("method not allowed", StringComparison.OrdinalIgnoreCase);
    }

    private static string FriendlyBriefError(string? error)
    {
        var e = error ?? "";
        if (e.Contains("owner or admin", StringComparison.OrdinalIgnoreCase) || e.Contains("403", StringComparison.Ordinal) || e.Contains("forbidden", StringComparison.OrdinalIgnoreCase))
            return "Only project owners and admins can refresh the brief.";
        if (e.Contains("no project memories", StringComparison.OrdinalIgnoreCase))
            return "There are no project memories to build a brief from yet.";
        if (e.Contains("timeout", StringComparison.OrdinalIgnoreCase) || e.Contains("deadline", StringComparison.OrdinalIgnoreCase))
            return "The brief is still rebuilding. Check back in a minute.";
        if (IsMissingRoute(e))
            return "This server doesn't support project briefs yet.";
        if (ShellState.Current.Offline)
            return "Refreshing the brief needs a connection.";
        return "Couldn't refresh the brief: " + (e.Length > 0 ? e : "request failed");
    }

    // ── Card pieces ──────────────────────────────────────────────────────

    private void NoteSeen(JsonElement item)
    {
        var id = NxJson.Text(item, "id");
        if (id.Length == 0) return;
        _seenProposed[id] = IsProposed(item);
        _seenCategories.Add(NormalizeCategory(NxJson.Text(item, "category")));
        NoteSeenTags(item);
    }

    private static bool IsProposed(JsonElement item) =>
        string.Equals(NxJson.Text(item, "status").Trim(), "PROPOSED", StringComparison.OrdinalIgnoreCase);

    private static bool IsRetired(JsonElement item) =>
        NxJson.Text(item, "status").Trim().ToUpperInvariant() is "REJECTED" or "SUPERSEDED" or "FORGOTTEN";

    private static Border AccentChip(string text, Brush brush) => new()
    {
        Padding = new Thickness(0, 0, 10, 0),
        VerticalAlignment = VerticalAlignment.Center,
        Child = new TextBlock { Text = text, FontSize = 12, Foreground = brush }
    };

    private static UIElement? StaleBadge(JsonElement item)
    {
        var raw = NxJson.Text(item, "stale_since");
        if (raw.Length == 0) return null;
        var when = FriendlyDate(raw);
        var badge = new Border
        {
            HorizontalAlignment = HorizontalAlignment.Left,
            Padding = new Thickness(7, 1, 7, 2),
            CornerRadius = new CornerRadius(4),
            BorderThickness = new Thickness(1),
            BorderBrush = NexusPalette.Amber,
            Child = new TextBlock { Text = "Files changed since: check this", FontSize = 11.5, Foreground = NexusPalette.Amber }
        };
        ToolTipService.SetToolTip(badge,
            $"Files this memory refers to changed{(when.Length > 0 ? " " + when : "")}. Check it is still true, then edit or confirm it.");
        return badge;
    }

    private static string DatesLine(JsonElement item)
    {
        var created = FriendlyDate(NxJson.Text(item, "created_at"));
        var used = FriendlyDate(NxJson.Text(item, "last_used_at"));
        var parts = new List<string>();
        if (created.Length > 0) parts.Add("Added " + created);
        parts.Add(used.Length > 0 ? "used " + used : "never used");
        return string.Join(" · ", parts);
    }

    private static string LastUsedText(JsonElement item)
    {
        var used = FriendlyDate(NxJson.Text(item, "last_used_at"));
        var count = NxJson.Text(item, "use_count");
        if (used.Length == 0) return "Not used by an agent yet";
        return count is { Length: > 0 } && count != "0" ? $"Last used {used} · {count} uses" : $"Last used {used}";
    }

    internal static string FriendlyDate(string raw)
    {
        if (!DateTimeOffset.TryParse(raw, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var at) || at.Year < 2000)
        {
            return "";
        }
        var local = at.ToLocalTime();
        var age = DateTimeOffset.Now - local;
        if (age.TotalMinutes < 1) return "just now";
        if (age.TotalHours < 1) return $"{(int)age.TotalMinutes} min ago";
        if (age.TotalDays < 1) return $"{(int)age.TotalHours} h ago";
        if (age.TotalDays < 7) return $"{(int)age.TotalDays} d ago";
        return local.ToString(local.Year == DateTimeOffset.Now.Year ? "MMM d" : "MMM d, yyyy", CultureInfo.CurrentCulture);
    }

    private static string SourceSessionId(JsonElement item)
    {
        if (item.TryGetProperty("tags", out var tags) && tags.ValueKind == JsonValueKind.Array)
        {
            foreach (var tag in tags.EnumerateArray())
            {
                var text = tag.ValueKind == JsonValueKind.String ? tag.GetString() ?? "" : "";
                if (text.StartsWith("session:", StringComparison.OrdinalIgnoreCase) && text.Length > 8)
                {
                    return text[8..].Trim();
                }
            }
        }
        return NxJson.Text(item, "session_id");
    }

    private HyperlinkButton ChatLink(string sessionId, string prefix = "From chat: ")
    {
        var label = new TextBlock { FontSize = 12, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 360 };
        var link = new HyperlinkButton
        {
            Content = label,
            Padding = new Thickness(0),
            MinHeight = 0,
            HorizontalAlignment = HorizontalAlignment.Left,
            VerticalAlignment = VerticalAlignment.Center
        };
        ToolTipService.SetToolTip(link, "Open the chat this came from");
        link.Click += (_, _) => MainWindow.Current?.OpenSession(sessionId);
        var listed = MainWindow.Current?.ChatTitleFor(sessionId) ?? "";
        label.Text = prefix + (listed.Length > 0 ? Truncate(listed, 60) : "chat " + Truncate(sessionId, 8));
        if (listed.Length == 0) _ = FillTitleAsync();
        return link;

        async Task FillTitleAsync()
        {
            var title = await ChatTitleAsync(sessionId);
            if (title.Length > 0) label.Text = prefix + Truncate(title, 60);
        }
    }

    private Task<string> ChatTitleAsync(string sessionId)
    {
        if (!_chatTitles.TryGetValue(sessionId, out var lookup))
        {
            lookup = FetchChatTitleAsync(sessionId);
            _chatTitles[sessionId] = lookup;
        }
        return lookup;
    }

    private async Task<string> FetchChatTitleAsync(string sessionId)
    {
        var response = await Call(NxMethods.SessionsGet, new { session_id = sessionId });
        if (!response.Ok || response.Result is not { ValueKind: JsonValueKind.Object } body) return "";
        return ChatTitles.Title(
            NxJson.Text(body, "title"),
            NxJson.Text(body, "summary", "preview"),
            NxJson.Text(body, "agent", "harness"));
    }

    private UIElement MemoryActions(JsonElement item, string id, bool proposed, bool compact = false)
    {
        var review = new List<UIElement>();
        if (proposed)
        {
            var confirm = CloudAction("Confirm", () => _ = ReviewAsync(NxMethods.MemoryConfirm, id));
            ToolTipService.SetToolTip(confirm, "This is right: agents can rely on it");
            var reject = CloudAction("Reject", () => _ = ReviewAsync(NxMethods.MemoryReject, id));
            ToolTipService.SetToolTip(reject, "Wrong or not useful: agents stop using it");
            review.Add(confirm);
            review.Add(reject);
        }
        var manage = new List<UIElement>
        {
            CloudAction("Edit", () => _ = ShowEditDialogAsync(id)),
            CloudAction("History", () => _ = ShowHistoryDialogAsync(id)),
            CloudAction("Pin", () => _ = Mutate(NxMethods.MemoryPin, id)),
            CloudAction("Forget", () => _ = ConfirmForgetAsync(id))
        };
        if (!compact) return Row([.. review, .. manage]);
        var stack = new StackPanel { Spacing = 8 };
        if (review.Count > 0) stack.Children.Add(Row([.. review]));
        stack.Children.Add(Row([.. manage.Take(2)]));
        stack.Children.Add(Row([.. manage.Skip(2)]));
        return stack;
    }

    // ── Mutations ────────────────────────────────────────────────────────

    private async Task ReviewAsync(string method, string id)
    {
        var response = await Call(method, new { id });
        if (!response.Ok)
        {
            ShowListError(response.Error ?? "Couldn't update this memory.");
            return;
        }
        _seenProposed[id] = false;
        if (response.Result is { ValueKind: JsonValueKind.Object } updated && NxJson.Text(updated, "content", "key").Length > 0)
        {
            ReplaceCard(id, updated);
        }
        else if (method == NxMethods.MemoryReject || _reviewOnly)
        {
            RemoveCard(id);
        }
        else
        {
            await LoadPageAsync(reset: false);
        }
    }

    private void ReplaceCard(string id, JsonElement updated)
    {
        NoteSeen(updated);
        if (IsRetired(updated) || (_reviewOnly && !IsProposed(updated)) || !PassesClientFilters(updated))
        {
            RemoveCard(id);
            return;
        }
        _itemsById[id] = updated;
        var index = CardIndex(id);
        if (index >= 0) _cards.Children[index] = BuildMemoryCard(updated, id);
        if (string.Equals(_selectedId, id, StringComparison.Ordinal)) RenderDetail(updated, id);
        RebuildCategoryBar();
    }

    private void RemoveCard(string id)
    {
        _itemsById.Remove(id);
        var index = CardIndex(id);
        if (index >= 0) _cards.Children.RemoveAt(index);
        if (string.Equals(_selectedId, id, StringComparison.Ordinal))
        {
            _selectedId = null;
            _detail.Children.Clear();
            UpdateDetailPaneLayout();
        }
        RebuildCategoryBar();
    }

    private int CardIndex(string id)
    {
        for (var i = 0; i < _cards.Children.Count; i++)
        {
            if (_cards.Children[i] is Border border && string.Equals(border.Tag as string, id, StringComparison.Ordinal)) return i;
        }
        return -1;
    }

    private void ShowListError(string message)
    {
        if (_cards.Children.FirstOrDefault(c => c is TextBlock t && Equals(t.Tag, ErrorTag)) is UIElement old)
        {
            _cards.Children.Remove(old);
        }
        _cards.Children.Insert(0, new TextBlock
        {
            Text = message,
            Tag = ErrorTag,
            FontSize = 12.5,
            TextWrapping = TextWrapping.Wrap,
            Foreground = NexusPalette.Amber
        });
    }

    // ── Edit ─────────────────────────────────────────────────────────────

    private async Task ShowEditDialogAsync(string id)
    {
        if (!_itemsById.TryGetValue(id, out var item)) return;
        var originalContent = NxJson.Text(item, "content", "text").Trim();
        var originalKind = NxJson.Text(item, "scope").Trim().ToLowerInvariant();

        var content = new TextBox
        {
            Header = "What Nexus should remember",
            Text = originalContent,
            AcceptsReturn = true,
            TextWrapping = TextWrapping.Wrap,
            MinHeight = 120,
            MaxHeight = 260,
            FontSize = 13
        };
        ScrollViewer.SetVerticalScrollBarVisibility(content, ScrollBarVisibility.Auto);
        var kind = new ComboBox { Header = "Kind", MinWidth = 200, FontSize = 13 };
        foreach (var (value, label) in Kinds)
        {
            var option = new ComboBoxItem { Content = label, Tag = value };
            kind.Items.Add(option);
            if (value == originalKind) kind.SelectedItem = option;
        }
        var category = FormatCategory(NxJson.Text(item, "category"));
        var error = new TextBlock { FontSize = 12, TextWrapping = TextWrapping.Wrap, Foreground = NexusPalette.Amber, Visibility = Visibility.Collapsed };

        var panel = new StackPanel { Spacing = 12, Width = 460 };
        panel.Children.Add(content);
        panel.Children.Add(kind);
        panel.Children.Add(Muted($"Category: {category}. Nexus sets the category from the content; it can't be changed here yet."));
        panel.Children.Add(error);

        JsonElement? saved = null;
        var dialog = new ContentDialog
        {
            Title = "Edit memory",
            Content = panel,
            PrimaryButtonText = "Save",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = XamlRoot
        };
        dialog.PrimaryButtonClick += async (_, args) =>
        {
            var deferral = args.GetDeferral();
            try
            {
                // PUT /memory/{id} accepts only key, content, tags,
                // context_snippet, level and scope; send just what changed.
                var body = new Dictionary<string, object?> { ["id"] = id };
                var newContent = content.Text.Replace("\r\n", "\n").Replace('\r', '\n').Trim();
                if (!string.Equals(newContent, originalContent.Replace("\r\n", "\n"), StringComparison.Ordinal)) body["content"] = newContent;
                if ((kind.SelectedItem as ComboBoxItem)?.Tag is string newKind && newKind != originalKind) body["scope"] = newKind;
                if (body.Count == 1) return;
                var response = await Call(NxMethods.MemoryUpdate, body);
                if (!response.Ok)
                {
                    error.Text = FriendlyEditError(response.Error);
                    error.Visibility = Visibility.Visible;
                    args.Cancel = true;
                    return;
                }
                saved = response.Result;
            }
            finally
            {
                deferral.Complete();
            }
        };
        if (await dialog.ShowAsync() == ContentDialogResult.Primary && saved is { ValueKind: JsonValueKind.Object } updated)
        {
            ReplaceCard(id, updated);
        }
    }

    private static string FriendlyEditError(string? error)
    {
        var e = error ?? "";
        if (e.Contains("outside 20-2000", StringComparison.OrdinalIgnoreCase))
            return "A memory needs between 20 and 2,000 characters.";
        if (e.Contains("403", StringComparison.Ordinal) || e.Contains("forbidden", StringComparison.OrdinalIgnoreCase) || e.Contains("permission", StringComparison.OrdinalIgnoreCase))
            return "You don't have permission to edit this memory.";
        if (ShellState.Current.Offline)
            return "Editing needs a connection.";
        return e.Length > 0 ? e : "Couldn't save this memory.";
    }

    // ── History ──────────────────────────────────────────────────────────

    private async Task ShowHistoryDialogAsync(string id)
    {
        var list = new StackPanel { Spacing = 10 };
        list.Children.Add(Muted("Loading history…"));
        var dialog = new ContentDialog
        {
            Title = "Memory history",
            Content = new ScrollViewer
            {
                Content = list,
                Width = 480,
                MaxHeight = 440,
                HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
                VerticalScrollBarVisibility = ScrollBarVisibility.Auto
            },
            CloseButtonText = "Close",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = XamlRoot
        };
        JsonElement? reverted = null;
        var shown = dialog.ShowAsync();

        var response = await Call(NxMethods.MemoryHistory, new { id });
        list.Children.Clear();
        if (!response.Ok)
        {
            list.Children.Add(Line(response.Error ?? "Couldn't load this memory's history."));
        }
        else
        {
            if (response.Result is { ValueKind: JsonValueKind.Object } result &&
                result.TryGetProperty("current", out var current) && current.ValueKind == JsonValueKind.Object)
            {
                list.Children.Add(VersionRow(current, "Current", null));
            }
            var versions = NxJson.Items(response.Result)
                .OrderByDescending(v => int.TryParse(NxJson.Text(v, "version"), out var n) ? n : 0)
                .ToList();
            if (versions.Count == 0)
            {
                list.Children.Add(Muted("No earlier versions: this memory hasn't been edited."));
            }
            foreach (var version in versions)
            {
                var number = int.TryParse(NxJson.Text(version, "version"), out var n) ? n : 0;
                list.Children.Add(VersionRow(version, number > 0 ? $"Version {number}" : "Earlier version", number > 0 ? number : null));
            }
        }
        await shown;
        if (reverted is { ValueKind: JsonValueKind.Object } item) ReplaceCard(id, item);
        return;

        UIElement VersionRow(JsonElement version, string label, int? number)
        {
            var meta = new List<string> { label };
            if (FriendlyDate(NxJson.Text(version, "created_at")) is { Length: > 0 } at) meta.Add(at);
            if (NxJson.Text(version, "edited_by") is { Length: > 0 } by) meta.Add("by " + Truncate(by, 32));
            var row = new StackPanel { Spacing = 4 };
            row.Children.Add(new TextBlock
            {
                Text = string.Join(" · ", meta),
                FontSize = 12,
                FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                Opacity = 0.75
            });
            var text = NxJson.Text(version, "content", "key");
            row.Children.Add(new TextBlock { Text = text, FontSize = 13, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
            if (number is int target)
            {
                var revert = new Button { Content = "Revert to this", MinHeight = 28, Padding = new Thickness(10, 2, 10, 3), FontSize = 12 };
                var armed = false;
                revert.Click += async (_, _) =>
                {
                    // Two clicks: the first arms, the second reverts.
                    if (!armed)
                    {
                        armed = true;
                        revert.Content = "Click again to revert";
                        return;
                    }
                    revert.IsEnabled = false;
                    var done = await Call(NxMethods.MemoryRevert, new { id, version = target });
                    if (!done.Ok)
                    {
                        revert.IsEnabled = true;
                        armed = false;
                        revert.Content = "Revert to this";
                        row.Children.Add(new TextBlock { Text = done.Error ?? "Couldn't revert.", FontSize = 12, Foreground = NexusPalette.Amber, TextWrapping = TextWrapping.Wrap });
                        return;
                    }
                    reverted = done.Result;
                    dialog.Hide();
                };
                if (ShellState.Current.Offline) revert.IsEnabled = false;
                row.Children.Add(revert);
            }
            return new Border
            {
                Padding = new Thickness(0, 0, 0, 10),
                BorderThickness = new Thickness(0, 0, 0, 1),
                BorderBrush = NexusPalette.Hairline,
                Child = row
            };
        }
    }
}
