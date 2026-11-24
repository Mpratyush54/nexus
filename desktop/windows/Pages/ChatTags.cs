using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Tags on chats. The server has no tag field on chats (agent sessions):
/// only memories carry tags. Chat tags are therefore kept on this device, in
/// %LocalAppData%\Nexus\chat-tags.json, keyed by chat id; the dialog says so.
/// </summary>
internal static class ChatTags
{
    private static readonly object Gate = new();
    private static Dictionary<string, List<string>>? s_tags;
    public static event EventHandler? Changed;

    private static string FilePath => Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "chat-tags.json");

    private static Dictionary<string, List<string>> Load()
    {
        if (s_tags is not null) return s_tags;
        var tags = new Dictionary<string, List<string>>(StringComparer.OrdinalIgnoreCase);
        try
        {
            if (File.Exists(FilePath))
            {
                var raw = JsonSerializer.Deserialize<Dictionary<string, List<string>>>(File.ReadAllText(FilePath));
                if (raw is not null)
                    foreach (var (id, list) in raw)
                        if (list?.Select(TagText.Normalize).Where(t => t.Length > 0).Distinct().ToList() is { Count: > 0 } clean) tags[id] = clean;
            }
        }
        catch { /* an unreadable file only loses the tags */ }
        return s_tags = tags;
    }

    public static IReadOnlyList<string> For(string chatId)
    {
        lock (Gate) return Load().TryGetValue(chatId, out var list) ? list.ToList() : Array.Empty<string>();
    }

    /// <summary>Every tag in use, most used first.</summary>
    public static IReadOnlyList<string> All()
    {
        lock (Gate)
            return Load().Values.SelectMany(l => l).GroupBy(t => t).OrderByDescending(g => g.Count()).ThenBy(g => g.Key).Select(g => g.Key).ToList();
    }

    public static bool Has(string chatId, string tag)
    {
        lock (Gate) return Load().TryGetValue(chatId, out var list) && list.Contains(tag);
    }

    public static void Set(string chatId, IEnumerable<string> tags)
    {
        lock (Gate)
        {
            var map = Load();
            var clean = tags.Select(TagText.Normalize).Where(t => t.Length > 0).Distinct().ToList();
            if (clean.Count == 0) map.Remove(chatId); else map[chatId] = clean;
            try
            {
                Directory.CreateDirectory(Path.GetDirectoryName(FilePath)!);
                var temp = FilePath + ".tmp";
                File.WriteAllText(temp, JsonSerializer.Serialize(map));
                File.Move(temp, FilePath, overwrite: true);
            }
            catch { /* kept in memory for this run */ }
        }
        Changed?.Invoke(null, EventArgs.Empty);
    }

    /// <summary>Small editor: current tags as removable chips, a box to add more, and tags already in use to pick from.</summary>
    public static Task EditAsync(XamlRoot root, string chatId, string title) =>
        TagEditor.ShowAsync(root, "Tags for " + (title.Length > 48 ? title[..45] + "…" : title), For(chatId), All(),
            "Chat tags are saved on this computer.", tags => { Set(chatId, tags); return Task.FromResult<string?>(null); });
}

/// <summary>The tag editor dialog shared by chats and memories.</summary>
internal static class TagEditor
{
    /// <summary>save returns an error to show, or null when saved.</summary>
    public static async Task ShowAsync(XamlRoot root, string title, IReadOnlyList<string> current, IReadOnlyList<string> known, string note, Func<List<string>, Task<string?>> save)
    {
        var tags = current.ToList();
        var chips = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        var chipScroll = new ScrollViewer { Content = chips, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled };
        var input = new TextBox { PlaceholderText = "Add a tag (Enter). Separate several with commas.", FontSize = 13 };
        var suggestions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        var error = new TextBlock { Foreground = NexusPalette.Amber, FontSize = 12, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };

        void Render()
        {
            chips.Children.Clear();
            if (tags.Count == 0) chips.Children.Add(new TextBlock { Text = "No tags yet.", Opacity = 0.6, FontSize = 12 });
            foreach (var tag in tags)
            {
                var remove = new Button { Content = tag + "  ✕", FontSize = 12, Padding = new Thickness(8, 2, 8, 3), CornerRadius = new CornerRadius(10) };
                ToolTipService.SetToolTip(remove, "Remove " + tag);
                Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(remove, "Remove tag " + tag);
                var captured = tag;
                remove.Click += (_, _) => { tags.Remove(captured); Render(); };
                chips.Children.Add(remove);
            }
            suggestions.Children.Clear();
            foreach (var tag in known.Where(t => !tags.Contains(t)).Take(8))
            {
                var add = new HyperlinkButton { Content = "+ " + tag, FontSize = 12, Padding = new Thickness(4, 0, 4, 0), MinHeight = 0 };
                var captured = tag;
                add.Click += (_, _) => { if (!tags.Contains(captured)) tags.Add(captured); Render(); };
                suggestions.Children.Add(add);
            }
        }

        void AddTyped()
        {
            foreach (var tag in TagText.Parse(input.Text))
                if (!tags.Contains(tag)) tags.Add(tag);
            input.Text = "";
            Render();
        }
        input.KeyDown += (_, e) =>
        {
            if (e.Key != Windows.System.VirtualKey.Enter) return;
            e.Handled = true;
            AddTyped();
        };
        Render();
        var panel = new StackPanel { Spacing = 10, MinWidth = 380, MaxWidth = 480 };
        panel.Children.Add(chipScroll);
        panel.Children.Add(input);
        if (known.Count > 0)
            panel.Children.Add(new ScrollViewer { Content = suggestions, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled });
        panel.Children.Add(new TextBlock { Text = note, FontSize = 11.5, Opacity = 0.6, TextWrapping = TextWrapping.Wrap });
        panel.Children.Add(error);
        var dialog = new ContentDialog
        {
            XamlRoot = root, Title = title, Content = panel,
            PrimaryButtonText = "Save", CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Primary
        };
        dialog.PrimaryButtonClick += async (_, args) =>
        {
            var deferral = args.GetDeferral();
            try
            {
                if (input.Text.Trim().Length > 0) AddTyped();
                var failed = await save(tags.ToList());
                if (failed is not null)
                {
                    error.Text = failed;
                    error.Visibility = Visibility.Visible;
                    args.Cancel = true;
                }
            }
            finally { deferral.Complete(); }
        };
        await dialog.ShowThemedAsync();
    }
}
