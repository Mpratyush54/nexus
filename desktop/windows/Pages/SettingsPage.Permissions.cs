using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using System.Text.Json;

namespace Nexus.Pages;

// Settings > Agents: how much agents may do without asking. The default
// permission mode for chats, what "Always allow" remembered per project,
// and when Nexus notifies about chats working in the background.
public sealed partial class SettingsPage
{
    private readonly ComboBox _defaultMode = new()
    {
        MinWidth = 200,
        Items =
        {
            new ComboBoxItem { Content = "Ask every time", Tag = "ask" },
            new ComboBoxItem { Content = "Auto-approve edits", Tag = "edits" },
            new ComboBoxItem { Content = "Full access", Tag = "full" }
        }
    };
    private readonly ComboBox _agentNotifications = new()
    {
        MinWidth = 200,
        Items =
        {
            new ComboBoxItem { Content = "Finished and approvals", Tag = "all" },
            new ComboBoxItem { Content = "Only approvals", Tag = "approvals" },
            new ComboBoxItem { Content = "Off", Tag = "off" }
        }
    };
    private readonly StackPanel _ruleList = new() { Spacing = 0 };
    private readonly TextBlock _modeNote = Muted("");
    private bool _permissionsLoading;

    private UIElement BuildPermissionsGroup()
    {
        _defaultMode.SelectionChanged += async (_, _) =>
        {
            var mode = (_defaultMode.SelectedItem as ComboBoxItem)?.Tag as string ?? "edits";
            ApplyModeNote(mode);
            if (_permissionsLoading) return;
            var response = await Call("live.rules.default", new { mode });
            if (!response.Ok) { Notify(response.Error ?? "Could not save the default.", true); return; }
            LiveSessions.Current.DefaultMode = mode;
        };
        _agentNotifications.SelectionChanged += (_, _) =>
        {
            if (_permissionsLoading) return;
            AppPrefs.Data.AgentNotifications = (_agentNotifications.SelectedItem as ComboBoxItem)?.Tag as string ?? "all";
            AppPrefs.Save();
        };
        return SettingsGroup("Permissions and background work",
            SettingRow(RowText("Default permission mode", _modeNote), _defaultMode),
            SettingRow(RowText("Always-allowed in each project",
                "What you chose to always allow from a chat. Remove one and agents ask again."), null, _ruleList),
            SettingRow(RowText("Notifications",
                "When Nexus isn't the focused window: a chat that finishes, or one that needs your approval."), _agentNotifications));
    }

    private void ApplyModeNote(string mode)
    {
        _modeNote.Text = mode switch
        {
            "ask" => "Agents ask before every file edit and command. Change it per chat in the message box.",
            "full" => "Agents edit files and run commands without asking. Use only on projects you trust.",
            _ => "File edits inside the project folder run without asking; commands still ask. Change it per chat in the message box."
        };
        _modeNote.Foreground = mode == "full" ? NexusPalette.Amber : NexusPalette.Muted;
    }

    private async Task LoadPermissionsAsync()
    {
        var response = await Call("live.rules.list", new { });
        _permissionsLoading = true;
        try
        {
            var mode = response.Ok && response.Result is { } r ? NxJson.Text(r, "default_mode") : LiveSessions.Current.DefaultMode;
            if (mode.Length == 0) mode = "edits";
            Select(_defaultMode, mode);
            ApplyModeNote(mode);
            Select(_agentNotifications, AppPrefs.Data.AgentNotifications ?? "all");
        }
        finally { _permissionsLoading = false; }
        RenderRules(response.Ok && response.Result is { } result && result.TryGetProperty("projects", out var projects) ? projects : default);
    }

    private static void Select(ComboBox box, string tag)
    {
        foreach (var item in box.Items.OfType<ComboBoxItem>())
            if (item.Tag as string == tag) box.SelectedItem = item;
    }

    private void RenderRules(JsonElement projects)
    {
        _ruleList.Children.Clear();
        var any = false;
        if (projects.ValueKind == JsonValueKind.Array)
        {
            foreach (var project in projects.EnumerateArray())
            {
                var key = NxJson.Text(project, "project");
                if (!project.TryGetProperty("rules", out var rules) || rules.ValueKind != JsonValueKind.Array || rules.GetArrayLength() == 0) continue;
                any = true;
                var name = key.StartsWith("folder:", StringComparison.Ordinal) ? key["folder:".Length..] : MainWindow.Current?.ProjectDisplayName(key) is { Length: > 0 } n ? n : key;
                _ruleList.Children.Add(new TextBlock { Text = name, FontSize = 12, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Opacity = 0.7, Margin = new Thickness(0, 8, 0, 4) });
                foreach (var rule in rules.EnumerateArray())
                {
                    var tool = NxJson.Text(rule, "tool");
                    var prefix = NxJson.Text(rule, "prefix");
                    var label = NxJson.Text(rule, "label");
                    var row = new Grid { ColumnSpacing = 8 };
                    row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
                    row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                    row.Children.Add(new TextBlock { Text = label.Length > 0 ? label : tool, FontSize = 13, VerticalAlignment = VerticalAlignment.Center, TextWrapping = TextWrapping.Wrap });
                    var remove = new Button { Content = "Remove", Padding = new Thickness(10, 3, 10, 3) };
                    AutomationProperties.SetName(remove, "Remove " + (label.Length > 0 ? label : tool) + " from " + name);
                    Grid.SetColumn(remove, 1);
                    row.Children.Add(remove);
                    remove.Click += async (_, _) =>
                    {
                        remove.IsEnabled = false;
                        var done = await Call("live.rules.remove", new { project = key, tool, prefix });
                        if (!done.Ok) { Notify(done.Error ?? "Could not remove it.", true); remove.IsEnabled = true; return; }
                        await LoadPermissionsAsync();
                    };
                    _ruleList.Children.Add(new Border { Child = row, Padding = new Thickness(0, 4, 0, 4) });
                }
            }
        }
        if (!any) _ruleList.Children.Add(Muted("Nothing yet. In a chat, choose “Always allow” on an approval to add one."));
    }
}
