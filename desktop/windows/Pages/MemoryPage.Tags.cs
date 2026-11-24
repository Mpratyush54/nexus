using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

// Memory tags: stored on the server (memory_items.tags). Nexus adds its own
// provenance tags (session:…, source:…, brief-…); only the person's tags are
// shown, filtered on and edited, and the others are kept on every save.
public sealed partial class MemoryPage
{
    private string? _tagFilter;
    private readonly HashSet<string> _seenTags = new(StringComparer.Ordinal);

    private static List<string> AllTags(JsonElement item)
    {
        var tags = new List<string>();
        if (item.TryGetProperty("tags", out var raw) && raw.ValueKind == JsonValueKind.Array)
            foreach (var tag in raw.EnumerateArray())
                if (tag.ValueKind == JsonValueKind.String && tag.GetString() is { Length: > 0 } text) tags.Add(text);
        return tags;
    }

    private static List<string> UserTags(JsonElement item) => AllTags(item).Where(t => !TagText.IsSystemMemoryTag(t)).ToList();

    private void NoteSeenTags(JsonElement item)
    {
        foreach (var tag in UserTags(item)) _seenTags.Add(tag);
    }

    private bool PassesTagFilter(JsonElement item) => _tagFilter is not { Length: > 0 } tag || AllTags(item).Contains(tag, StringComparer.Ordinal);

    private UIElement TagRow(JsonElement item, string id)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        foreach (var tag in UserTags(item))
        {
            var chip = new Button
            {
                Content = "#" + tag, FontSize = 11.5, MinHeight = 0, Padding = new Thickness(8, 1, 8, 2), CornerRadius = new CornerRadius(10),
                Foreground = NexusPalette.Accent, Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent)
            };
            ToolTipService.SetToolTip(chip, "Show only memories tagged #" + tag);
            var captured = tag;
            chip.Click += async (_, _) => { _tagFilter = captured; await LoadPageAsync(reset: true); };
            row.Children.Add(chip);
        }
        var edit = new HyperlinkButton { Content = row.Children.Count > 0 ? "Edit tags" : "+ Tag", FontSize = 11.5, Padding = new Thickness(2, 0, 2, 0), MinHeight = 0 };
        edit.Click += async (_, _) => await EditTagsAsync(item, id);
        row.Children.Add(edit);
        return row;
    }

    private async Task EditTagsAsync(JsonElement item, string id)
    {
        var current = AllTags(item);
        JsonElement? saved = null;
        await TagEditor.ShowAsync(XamlRoot, "Memory tags", UserTags(item), _seenTags.OrderBy(t => t).ToList(),
            "Tags are saved with the memory, for everyone who can see it.", async tags =>
            {
                var response = await Call(NxMethods.MemoryUpdate, new Dictionary<string, object?> { ["id"] = id, ["tags"] = TagText.WithUserTags(current, tags) });
                if (!response.Ok) return FriendlyEditError(response.Error);
                saved = response.Result;
                return null;
            });
        if (saved is { ValueKind: JsonValueKind.Object } updated)
        {
            NoteSeenTags(updated);
            ReplaceCard(id, updated);
        }
    }

    private void AddTagPills()
    {
        var tags = new SortedSet<string>(_seenTags, StringComparer.Ordinal);
        if (_tagFilter is { Length: > 0 } active) tags.Add(active);
        if (tags.Count == 0) return;
        _categoryBar.Children.Add(Muted("Tag:"));
        Button Pill(string label, string? tag)
        {
            var button = new Button { Content = label, MinHeight = 28, Padding = new Thickness(10, 2, 10, 3), FontSize = 12 };
            if (string.Equals(_tagFilter, tag, StringComparison.Ordinal)) button.Background = new SolidColorBrush(Windows.UI.Color.FromArgb(48, 245, 158, 11));
            button.Click += async (_, _) => { _tagFilter = tag; await LoadPageAsync(reset: true); };
            return button;
        }
        _categoryBar.Children.Add(Pill("Any", null));
        foreach (var tag in tags.Take(15)) _categoryBar.Children.Add(Pill("#" + tag, tag));
    }
}
