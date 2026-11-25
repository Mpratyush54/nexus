using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using Nexus.Pages;

namespace Nexus;

/// <summary>Per-project numbers the switcher shows when a page has already loaded them.</summary>
internal static class ProjectStats
{
    private static readonly Dictionary<string, long> s_memories = new(StringComparer.OrdinalIgnoreCase);
    public static void SetMemories(string projectId, long count) { lock (s_memories) s_memories[projectId] = count; }
    public static long Memories(string projectId) { lock (s_memories) return s_memories.TryGetValue(projectId, out var n) ? n : -1; }
}

/// <summary>
/// The one project selector: always at the top of the sidebar (and behind the
/// rail's project button when the sidebar is collapsed). "All projects" shows
/// every project's chats and activity; pages that need a single project
/// (memory, graph, agents) keep using the last project chosen, which the
/// button names. The choice and recent projects persist in
/// %LocalAppData%\Nexus\project-switcher.json. Ctrl+P opens it.
/// </summary>
internal sealed class ProjectSwitcher : UserControl
{
    private sealed class Saved
    {
        public string Filter { get; set; } = "";
        public bool Chosen { get; set; }
        public List<string> Recent { get; set; } = new();
    }

    private static string FilePath => Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "project-switcher.json");
    private static Saved? s_saved;

    private static Saved State
    {
        get
        {
            if (s_saved is not null) return s_saved;
            try { s_saved = File.Exists(FilePath) ? JsonSerializer.Deserialize<Saved>(File.ReadAllText(FilePath)) : null; }
            catch { s_saved = null; }
            return s_saved ??= new Saved();
        }
    }

    private static void Persist()
    {
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(FilePath)!);
            File.WriteAllText(FilePath, JsonSerializer.Serialize(State));
        }
        catch { /* the choice still applies for this run */ }
    }

    /// <summary>The filter saved by the last run ("" = all projects); null when nothing was chosen yet.</summary>
    public static string? SavedFilter => State.Chosen ? State.Filter : null;

    private readonly Func<IReadOnlyList<(string Id, string Name)>> _projects;
    private readonly Func<string> _filter;
    private readonly Action<string> _select;
    private readonly Func<string, int> _chatCount;
    private readonly Button _button = new();
    private readonly TextBlock _name = new() { FontSize = 13.5, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextTrimming = TextTrimming.CharacterEllipsis };
    private readonly TextBlock _hint = new() { FontSize = 11, Opacity = 0.55, TextTrimming = TextTrimming.CharacterEllipsis };
    private readonly Border _badge = new() { Width = 22, Height = 22, CornerRadius = new CornerRadius(6), VerticalAlignment = VerticalAlignment.Center };
    private readonly Flyout _flyout = new() { Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedLeft };
    private readonly TextBox _search = new() { PlaceholderText = "Find a project", FontSize = 13 };
    private readonly StackPanel _list = new() { Spacing = 1 };
    private FrameworkElement? _anchor;

    /// <param name="projects">Projects in the account (id, name).</param>
    /// <param name="filter">The current choice: a project id, or "" for all projects.</param>
    /// <param name="select">Applies a choice ("" = all projects).</param>
    /// <param name="chatCount">Chats loaded in the sidebar for a project ("" = all).</param>
    public ProjectSwitcher(Func<IReadOnlyList<(string Id, string Name)>> projects, Func<string> filter, Action<string> select, Func<string, int> chatCount)
    {
        _projects = projects; _filter = filter; _select = select; _chatCount = chatCount;
        var grid = new Grid { ColumnSpacing = 9 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.Children.Add(_badge);
        var text = new StackPanel { Spacing = 0, VerticalAlignment = VerticalAlignment.Center };
        text.Children.Add(_name);
        text.Children.Add(_hint);
        Grid.SetColumn(text, 1);
        grid.Children.Add(text);
        var chevron = new FontIcon { Glyph = "", FontSize = 11, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center };
        Grid.SetColumn(chevron, 2);
        grid.Children.Add(chevron);
        _button.Content = grid;
        _button.HorizontalAlignment = HorizontalAlignment.Stretch;
        _button.HorizontalContentAlignment = HorizontalAlignment.Stretch;
        _button.Padding = new Thickness(8, 6, 10, 6);
        _button.CornerRadius = new CornerRadius(8);
        _button.BorderThickness = new Thickness(1);
        ToolTipService.SetToolTip(_button, "Switch project (" + Shortcuts.Label("P") + ")");
        _button.Click += (_, _) => Open(_button);
        Content = _button;

        var panel = new StackPanel { Spacing = 8, Width = 300 };
        panel.Children.Add(_search);
        panel.Children.Add(new ScrollViewer { Content = _list, MaxHeight = 420, VerticalScrollBarVisibility = ScrollBarVisibility.Auto });
        _flyout.Content = panel;
        AutomationProperties.SetName(_search, "Find a project");
        _search.TextChanged += (_, _) => RenderList();
        _search.KeyDown += (_, e) =>
        {
            if (e.Key == Windows.System.VirtualKey.Enter && _list.Children.OfType<Button>().FirstOrDefault() is { Tag: string id })
            {
                e.Handled = true;
                Choose(id);
            }
            else if (e.Key == Windows.System.VirtualKey.Down && _list.Children.OfType<Button>().FirstOrDefault() is { } first)
            {
                e.Handled = true;
                first.Focus(FocusState.Keyboard);
            }
        };
        // Esc closes the list from the search box or a row. WinUI's light
        // dismiss does this already; Uno on macOS did not.
        panel.KeyDown += (_, e) =>
        {
            if (e.Key != Windows.System.VirtualKey.Escape) return;
            e.Handled = true;
            _flyout.Hide();
        };
        _flyout.Opened += (_, _) => _search.Focus(FocusState.Programmatic);
        Refresh();
    }

    /// <summary>Opens the project list at the switcher, or at another anchor (the collapsed rail's button).</summary>
    public void Open(FrameworkElement? anchor = null)
    {
        _anchor = anchor ?? _button;
        _search.Text = "";
        RenderList();
        _flyout.ShowAt(_anchor.XamlRoot is null ? _button : _anchor);
    }

    private string NameOf(string id) => _projects().FirstOrDefault(p => p.Id == id).Name ?? "";

    /// <summary>Repaints the button from the current choice.</summary>
    public void Refresh()
    {
        var filter = _filter();
        var shell = ShellState.Current;
        var name = filter.Length == 0 ? "All projects" : NameOf(filter) is { Length: > 0 } named ? named : shell.ProjectDisplay;
        _name.Text = name;
        // Says what the list shows. Where a new chat starts is on Home's
        // project chip, not here.
        _hint.Text = filter.Length == 0 ? "Chats from every project" : "Switch project · " + Shortcuts.Label("P");
        var color = NexusPalette.BadgeInk(BadgeColor(filter.Length == 0 ? "*" : name));
        _badge.Background = new SolidColorBrush(Windows.UI.Color.FromArgb(46, color.R, color.G, color.B));
        _badge.Child = new TextBlock
        {
            Text = filter.Length == 0 ? "∗" : Initials(name), FontSize = 10, FontWeight = Microsoft.UI.Text.FontWeights.Bold,
            Foreground = new SolidColorBrush(color), HorizontalAlignment = HorizontalAlignment.Center, VerticalAlignment = VerticalAlignment.Center
        };
        AutomationProperties.SetName(_button, "Project: " + name + ". Switch project");
        _button.Background = ThemeService.Brush("NexusSurfaceBrush", Windows.UI.Color.FromArgb(255, 0x17, 0x18, 0x17));
        _button.BorderBrush = ThemeService.Brush("NexusHairlineBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255));
    }

    private void RenderList()
    {
        _list.Children.Clear();
        var query = _search.Text.Trim();
        var projects = _projects();
        var filter = _filter();
        bool Matches(string name) => query.Length == 0 || name.Contains(query, StringComparison.OrdinalIgnoreCase);
        if (Matches("All projects")) _list.Children.Add(Row("", "All projects", "Every project's chats and activity", filter.Length == 0));
        var recent = State.Recent.Where(id => projects.Any(p => p.Id == id)).Take(4).ToList();
        if (query.Length == 0 && recent.Count > 0)
        {
            _list.Children.Add(Section("Recent"));
            foreach (var id in recent) _list.Children.Add(Row(id, NameOf(id), Counts(id), filter == id));
            _list.Children.Add(Section("All projects"));
        }
        foreach (var (id, name) in projects.OrderBy(p => p.Name, StringComparer.OrdinalIgnoreCase))
            if (Matches(name) && (query.Length > 0 || !recent.Contains(id))) _list.Children.Add(Row(id, name, Counts(id), filter == id));
        if (!_list.Children.OfType<Button>().Any()) _list.Children.Add(new TextBlock { Text = "No project matches.", Opacity = 0.6, Margin = new Thickness(8, 4, 8, 4) });
    }

    private string Counts(string id)
    {
        var parts = new List<string>();
        var chats = _chatCount(id);
        if (chats > 0) parts.Add(chats + (chats == 1 ? " recent chat" : " recent chats"));
        var memories = ProjectStats.Memories(id);
        // The dashboard counts at most 500 memories.
        if (memories >= 0) parts.Add((memories >= 500 ? "500+" : memories.ToString()) + (memories == 1 ? " memory" : " memories"));
        return string.Join(" · ", parts);
    }

    private static TextBlock Section(string text) => new()
    {
        Text = text.ToUpperInvariant(), FontSize = 10, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Opacity = 0.5, Margin = new Thickness(8, 8, 0, 2)
    };

    private Button Row(string id, string name, string detail, bool selected)
    {
        var text = new StackPanel { Spacing = 1 };
        text.Children.Add(new TextBlock { Text = name, FontSize = 13, TextTrimming = TextTrimming.CharacterEllipsis });
        if (detail.Length > 0) text.Children.Add(new TextBlock { Text = detail, FontSize = 11, Opacity = 0.55, TextTrimming = TextTrimming.CharacterEllipsis });
        var grid = new Grid { ColumnSpacing = 8 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.Children.Add(text);
        if (selected)
        {
            var check = new FontIcon { Glyph = "", FontSize = 12, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center };
            Grid.SetColumn(check, 1);
            grid.Children.Add(check);
        }
        var row = new Button
        {
            Content = grid, Tag = id, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
            Background = selected ? NexusPalette.UserFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
            Padding = new Thickness(8, 6, 8, 6), CornerRadius = new CornerRadius(6)
        };
        AutomationProperties.SetName(row, name + (selected ? ", selected" : ""));
        row.Click += (_, _) => Choose(id);
        return row;
    }

    private void Choose(string id)
    {
        _flyout.Hide();
        var state = State;
        state.Filter = id;
        state.Chosen = true;
        if (id.Length > 0)
        {
            state.Recent.Remove(id);
            state.Recent.Insert(0, id);
            if (state.Recent.Count > 8) state.Recent.RemoveRange(8, state.Recent.Count - 8);
        }
        Persist();
        _select(id);
        Refresh();
    }

    private static string Initials(string name)
    {
        var words = name.Split(new[] { ' ', '-', '_', '.', '·' }, StringSplitOptions.RemoveEmptyEntries);
        var letters = words.Length switch
        {
            0 => "?",
            1 => new string(words[0].Where(char.IsLetterOrDigit).Take(2).ToArray()),
            _ => string.Concat(words.Take(2).Select(w => w[0]))
        };
        return letters.Length > 0 ? letters.ToUpperInvariant() : "?";
    }

    private static Windows.UI.Color BadgeColor(string name)
    {
        var palette = new[]
        {
            Windows.UI.Color.FromArgb(255, 0x34, 0xD3, 0x99), Windows.UI.Color.FromArgb(255, 0x8E, 0xA2, 0xFF),
            Windows.UI.Color.FromArgb(255, 0xF4, 0xA2, 0x61), Windows.UI.Color.FromArgb(255, 0xC0, 0x84, 0xFC),
            Windows.UI.Color.FromArgb(255, 0x5E, 0xC8, 0xE5), Windows.UI.Color.FromArgb(255, 0xF8, 0x71, 0x71)
        };
        var hash = 0;
        foreach (var c in name) hash = (hash * 31 + c) & 0x7fffffff;
        return palette[hash % palette.Length];
    }
}
