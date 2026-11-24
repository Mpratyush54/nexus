using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Automation.Peers;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

// Settings shell: a section list on the left (a dropdown on narrow widths)
// and one section on the right. A section is built the first time it is
// opened and keeps its controls, so the last values show instantly; its data
// reloads in the background each time it is opened (at most every 20s unless
// forced by Refresh or by navigating back to Settings).
public sealed partial class SettingsPage
{
    private sealed record SettingsSection(string Key, string Title, string Glyph, string Subtitle);

    private static readonly SettingsSection[] Sections =
    [
        new("general", "General", "", "Appearance, startup, and how the Nexus window behaves."),
        new("account", "Account", "", "Your Nexus identity and the tokens your agents use."),
        new("workspaces", "Workspaces & capture", "", "Choose which folders Nexus monitors and how capture runs."),
        new("agents", "Agents", "", "Connect your coding agents to Nexus memory."),
        new("memory", "Memory", "", "Project briefs and who can see what Nexus captures."),
        new("organizations", "Organizations", "", "Teams, invitations, and member roles."),
        new("usage", "Usage", "", "How much Nexus you and your agents use."),
        new("advanced", "Advanced", "", "Sync, local cache, diagnostics, and version details.")
    ];

    private static readonly TimeSpan SectionFreshFor = TimeSpan.FromSeconds(20);
    private const double NarrowWidth = 720;
    private const double NavWidth = 220;

    private static string s_lastSection = "general";
    private readonly ListView _nav = new() { SelectionMode = ListViewSelectionMode.Single };
    private readonly ComboBox _navCompact = new() { HorizontalAlignment = HorizontalAlignment.Stretch, Visibility = Visibility.Collapsed, Margin = new Thickness(16, 12, 16, 4) };
    private readonly StackPanel _sectionHost = new();
    private readonly Dictionary<string, UIElement> _sectionBodies = new();
    private readonly Dictionary<string, DateTimeOffset> _sectionLoadedAt = new();
    private readonly HashSet<string> _sectionLoading = new();
    private ScrollViewer? _sectionScroller;
    private Border? _sectionPadding;
    private Border? _navPane;
    private ColumnDefinition? _navColumn;
    private bool? _narrow;
    private string _section = "";
    private bool _navSync;

    private UIElement BuildShell()
    {
        foreach (var section in Sections)
        {
            var label = new TextBlock { Text = section.Title, FontSize = 13.5, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis };
            var content = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 10 };
            content.Children.Add(new FontIcon { Glyph = section.Glyph, FontSize = 14, Opacity = 0.75, VerticalAlignment = VerticalAlignment.Center });
            content.Children.Add(label);
            var item = new ListViewItem { Content = content, Tag = section.Key, MinHeight = 34, Padding = new Thickness(10, 0, 10, 0) };
            AutomationProperties.SetName(item, section.Title);
            _nav.Items.Add(item);
            var choice = new ComboBoxItem { Content = section.Title, Tag = section.Key };
            AutomationProperties.SetName(choice, section.Title);
            _navCompact.Items.Add(choice);
        }
        AutomationProperties.SetName(_nav, "Settings sections");
        AutomationProperties.SetName(_navCompact, "Settings section");
        _nav.SelectionChanged += (_, _) =>
        {
            if (!_navSync && _nav.SelectedItem is ListViewItem { Tag: string key }) ShowSection(key);
        };
        _navCompact.SelectionChanged += (_, _) =>
        {
            if (!_navSync && _navCompact.SelectedItem is ComboBoxItem { Tag: string key }) ShowSection(key);
        };

        var title = new TextBlock
        {
            Text = "Settings", FontSize = 15, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            Margin = new Thickness(10, 0, 0, 6)
        };
        AutomationProperties.SetHeadingLevel(title, AutomationHeadingLevel.Level1);
        var navStack = new StackPanel { Spacing = 4 };
        navStack.Children.Add(title);
        navStack.Children.Add(_nav);
        _navPane = new Border
        {
            Padding = new Thickness(10, 24, 8, 24),
            BorderThickness = new Thickness(0, 0, 1, 0),
            BorderBrush = NexusPalette.Hairline,
            Child = new ScrollViewer { Content = navStack, VerticalScrollBarVisibility = ScrollBarVisibility.Auto }
        };

        // Left-aligned reading column capped at 760px.
        var column = new Grid();
        column.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star), MaxWidth = 760 });
        column.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var body = new StackPanel { Spacing = 0 };
        _notice.Margin = new Thickness(0, 0, 0, 16);
        body.Children.Add(_notice);
        body.Children.Add(_sectionHost);
        column.Children.Add(body);
        _sectionPadding = new Border { Padding = new Thickness(32, 24, 32, 40), Child = column };
        _sectionScroller = new ScrollViewer
        {
            Content = _sectionPadding,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled
        };

        var root = new Grid();
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        _navColumn = new ColumnDefinition { Width = new GridLength(NavWidth) };
        root.ColumnDefinitions.Add(_navColumn);
        root.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        Grid.SetColumnSpan(_navCompact, 2);
        root.Children.Add(_navCompact);
        Grid.SetRow(_navPane, 1);
        root.Children.Add(_navPane);
        Grid.SetRow(_sectionScroller, 1);
        Grid.SetColumn(_sectionScroller, 1);
        root.Children.Add(_sectionScroller);
        root.SizeChanged += (_, e) => ApplyNarrow(e.NewSize.Width < NarrowWidth);
        return root;
    }

    private void ApplyNarrow(bool narrow)
    {
        if (_narrow == narrow || _navPane is null || _navColumn is null || _sectionScroller is null || _sectionPadding is null) return;
        _narrow = narrow;
        _navPane.Visibility = narrow ? Visibility.Collapsed : Visibility.Visible;
        _navCompact.Visibility = narrow ? Visibility.Visible : Visibility.Collapsed;
        _navColumn.Width = new GridLength(narrow ? 0 : NavWidth);
        Grid.SetColumn(_sectionScroller, narrow ? 0 : 1);
        Grid.SetColumnSpan(_sectionScroller, narrow ? 2 : 1);
        _sectionPadding.Padding = narrow ? new Thickness(16, 12, 16, 32) : new Thickness(32, 24, 32, 40);
    }

    private void ShowSection(string key, bool forceLoad = false)
    {
        var section = Sections.FirstOrDefault(s => s.Key == key) ?? Sections[0];
        var changed = _section != section.Key || _sectionHost.Children.Count == 0;
        _section = section.Key;
        s_lastSection = section.Key;
        _navSync = true;
        _nav.SelectedItem = _nav.Items.OfType<ListViewItem>().FirstOrDefault(i => i.Tag as string == section.Key);
        _navCompact.SelectedItem = _navCompact.Items.OfType<ComboBoxItem>().FirstOrDefault(i => i.Tag as string == section.Key);
        _navSync = false;
        if (changed)
        {
            if (!_sectionBodies.TryGetValue(section.Key, out var body))
            {
                body = BuildSectionBody(section.Key);
                _sectionBodies[section.Key] = body;
            }
            _sectionHost.Children.Clear();
            _sectionHost.Children.Add(SectionTitle(section));
            _sectionHost.Children.Add(body);
            _notice.IsOpen = false;
            _sectionScroller?.ChangeView(null, 0, null, true);
        }
        _ = LoadSectionAsync(section.Key, forceLoad);
    }

    private async Task LoadSectionAsync(string key, bool force)
    {
        if (_sectionLoading.Contains(key)) return;
        if (!force && _sectionLoadedAt.TryGetValue(key, out var at) && DateTimeOffset.UtcNow - at < SectionFreshFor) return;
        _sectionLoading.Add(key);
        try
        {
            await LoadSectionDataAsync(key);
            _sectionLoadedAt[key] = DateTimeOffset.UtcNow;
        }
        catch (Exception ex) { Notify(ex.Message, true); }
        finally { _sectionLoading.Remove(key); }
    }

    private void InvalidateSection(string key) => _sectionLoadedAt.Remove(key);

    private Task LoadSectionDataAsync(string key) => key switch
    {
        "general" => LoadGeneralAsync(),
        "account" => LoadAccountSectionAsync(),
        "workspaces" => RefreshCaptureAsync(),
        "agents" => LoadAgentsSectionAsync(),
        "memory" => LoadMemorySectionAsync(),
        "organizations" => RefreshOrganizationsAsync(),
        "advanced" => LoadAdvancedAsync(),
        _ => Task.CompletedTask
    };

    private UIElement BuildSectionBody(string key) => key switch
    {
        "general" => BuildGeneralSection(),
        "account" => BuildAccountSection(),
        "workspaces" => BuildWorkspacesSection(),
        "agents" => BuildAgentsSection(),
        "memory" => BuildMemorySection(),
        "organizations" => BuildOrganizationSection(),
        "usage" => BuildUsageSectionSlot(),
        "advanced" => BuildAdvancedSection(),
        _ => new StackPanel()
    };

    private static StackPanel SectionTitle(SettingsSection section)
    {
        var title = NexusTokens.PageTitle(section.Title);
        AutomationProperties.SetHeadingLevel(title, AutomationHeadingLevel.Level2);
        var header = new StackPanel { Spacing = 4, Margin = new Thickness(0, 0, 0, 24) };
        header.Children.Add(title);
        header.Children.Add(Muted(section.Subtitle));
        return header;
    }

    // ---- Row vocabulary: label + description left, control right, hairlines.

    private static TextBlock RowLabel(string text)
    {
        return new TextBlock
        {
            Text = NxJson.Scrub(text), FontSize = 13.5, TextWrapping = TextWrapping.Wrap,
            FontWeight = Microsoft.UI.Text.FontWeights.Medium
        };
    }

    private static StackPanel RowText(string label, string description = "") =>
        RowText(label, description.Length > 0 ? Muted(description) : null);

    private static StackPanel RowText(string label, TextBlock? description)
    {
        var stack = new StackPanel { Spacing = 2, VerticalAlignment = VerticalAlignment.Center };
        stack.Children.Add(RowLabel(label));
        if (description is not null) stack.Children.Add(description);
        return stack;
    }

    private static Border SettingRow(UIElement text, FrameworkElement? control = null, UIElement? below = null)
    {
        var grid = new Grid { ColumnSpacing = 24 };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        grid.Children.Add(text);
        if (control is not null)
        {
            control.VerticalAlignment = VerticalAlignment.Center;
            control.HorizontalAlignment = HorizontalAlignment.Right;
            // Switches and pickers carry no visible label of their own.
            if (control is ToggleSwitch or ComboBox && string.IsNullOrEmpty(AutomationProperties.GetName(control)) &&
                text is StackPanel { Children.Count: > 0 } stack && stack.Children[0] is TextBlock label)
                AutomationProperties.SetName(control, label.Text);
            Grid.SetColumn(control, 1);
            grid.Children.Add(control);
        }
        if (below is FrameworkElement extra)
        {
            grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
            extra.Margin = new Thickness(extra.Margin.Left, Math.Max(extra.Margin.Top, 10), extra.Margin.Right, extra.Margin.Bottom);
            Grid.SetRow(extra, 1);
            Grid.SetColumnSpan(extra, 2);
            grid.Children.Add(extra);
        }
        return new Border
        {
            Padding = new Thickness(0, 12, 0, 12),
            BorderThickness = new Thickness(0, 0, 0, 1),
            BorderBrush = NexusPalette.Hairline,
            Child = grid
        };
    }

    private static Border NoteRow(string text) => SettingRow(Muted(text));

    private static Border GroupHeader(string title, double top = 0)
    {
        var text = new TextBlock
        {
            Text = title, FontSize = 12, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Opacity = 0.6,
            TextWrapping = TextWrapping.Wrap
        };
        AutomationProperties.SetHeadingLevel(text, AutomationHeadingLevel.Level3);
        return new Border
        {
            Padding = new Thickness(0, 0, 0, 8),
            Margin = new Thickness(0, top, 0, 0),
            BorderThickness = new Thickness(0, 0, 0, 1),
            BorderBrush = NexusPalette.Hairline,
            Child = text
        };
    }

    private static StackPanel SettingsGroup(string title, params UIElement[] rows)
    {
        var group = new StackPanel { Margin = new Thickness(0, 0, 0, 28) };
        group.Children.Add(GroupHeader(title));
        foreach (var row in rows) group.Children.Add(row);
        return group;
    }

    // A row whose details open beneath it; onOpen runs on every open.
    private static Border Disclosure(string label, string description, Panel content, Func<Task>? onOpen = null)
    {
        content.Visibility = Visibility.Collapsed;
        content.Margin = new Thickness(16, 0, 0, 0);
        var toggle = new Button { Content = "Show" };
        AutomationProperties.SetName(toggle, "Show " + label);
        toggle.Click += async (_, _) =>
        {
            var open = content.Visibility != Visibility.Visible;
            content.Visibility = open ? Visibility.Visible : Visibility.Collapsed;
            toggle.Content = open ? "Hide" : "Show";
            AutomationProperties.SetName(toggle, (open ? "Hide " : "Show ") + label);
            if (open && onOpen is not null) await onOpen();
        };
        return SettingRow(RowText(label, description), toggle, content);
    }

    private static ToggleSwitch CompactSwitch() => new() { OnContent = "", OffContent = "", MinWidth = 0 };

    // Opens another shell destination exactly as its sidebar button does.
    private void OpenShellDestination(string buttonName, string label)
    {
        try
        {
            if (MainWindow.Current?.Content is FrameworkElement root && root.FindName(buttonName) is Button button)
            {
                new ButtonAutomationPeer(button).Invoke();
                return;
            }
        }
        catch (Exception ex)
        {
            Notify(ex.Message, true);
            return;
        }
        Notify($"Open {label} from the sidebar.", true);
    }

    private static string FriendlyWhen(string raw)
    {
        if (!DateTimeOffset.TryParse(raw, out var when) || when.Year <= 1) return "";
        var local = when.ToLocalTime();
        var age = DateTimeOffset.Now - local;
        if (age < TimeSpan.FromMinutes(1)) return "just now";
        if (age < TimeSpan.FromHours(1)) return $"{(int)age.TotalMinutes} min ago";
        if (age < TimeSpan.FromDays(1)) return $"{(int)age.TotalHours} h ago";
        return local.ToString(local.Year == DateTimeOffset.Now.Year ? "MMM d" : "MMM d, yyyy");
    }
}
