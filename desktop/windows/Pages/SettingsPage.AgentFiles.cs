using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using System.Text.Json;
using Nexus.Core;

namespace Nexus.Pages;

// Per-workspace opt-in that keeps a Nexus-marked section of CLAUDE.md /
// AGENTS.md / GEMINI.md in sync with the project's memory. Routed by the core
// as agents.memory_files.get / .set (set runs a forced sync when enabled).
public sealed partial class SettingsPage
{
    private static readonly (string Id, string Name)[] AgentFileIds =
    [
        ("claude", "CLAUDE.md"),
        ("agents", "AGENTS.md"),
        ("gemini", "GEMINI.md")
    ];

    private UIElement AgentFilesRow(string workspaceRoot)
    {
        var toggle = new ToggleSwitch { OnContent = "", OffContent = "", MinWidth = 0, IsEnabled = false };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(toggle,
            "Keep CLAUDE.md / AGENTS.md / GEMINI.md updated with project memory for " + Path.GetFileName(workspaceRoot.TrimEnd('\\', '/')));
        var boxes = AgentFileIds.Select(file => (file.Id, Box: new CheckBox { Content = file.Name, FontSize = 12.5, MinWidth = 0, IsChecked = true })).ToArray();
        var createMissing = new CheckBox { Content = "Create missing files", FontSize = 12.5, MinWidth = 0, IsChecked = false };
        var options = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 14, Visibility = Visibility.Collapsed };
        foreach (var (_, box) in boxes) options.Children.Add(box);
        options.Children.Add(createMissing);
        var status = Muted("");
        status.Visibility = Visibility.Collapsed;

        var suppress = true;
        var version = 0;
        void SetBusy(bool busy)
        {
            toggle.IsEnabled = !busy;
            foreach (var (_, box) in boxes) box.IsEnabled = !busy;
            createMissing.IsEnabled = !busy;
        }
        void Apply(JsonElement settings)
        {
            suppress = true;
            toggle.IsOn = NxJson.Text(settings, "enabled") == "true";
            var files = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            if (settings.TryGetProperty("files", out var list) && list.ValueKind == JsonValueKind.Array)
                foreach (var item in list.EnumerateArray())
                    if (item.ValueKind == JsonValueKind.String && item.GetString() is { } id) files.Add(id);
            foreach (var (id, box) in boxes) box.IsChecked = files.Contains(id);
            createMissing.IsChecked = NxJson.Text(settings, "create_missing") == "true";
            options.Visibility = toggle.IsOn ? Visibility.Visible : Visibility.Collapsed;
            suppress = false;
        }
        void ShowStatus(string text)
        {
            status.Text = text;
            status.Visibility = text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        }

        async Task SaveAsync()
        {
            if (suppress) return;
            var current = ++version;
            options.Visibility = toggle.IsOn ? Visibility.Visible : Visibility.Collapsed;
            SetBusy(true);
            ShowStatus(toggle.IsOn ? "Saving and updating files…" : "Saving…");
            var response = await Call("agents.memory_files.set", new
            {
                workspace_root = workspaceRoot,
                enabled = toggle.IsOn,
                files = boxes.Where(b => b.Box.IsChecked == true).Select(b => b.Id).ToArray(),
                create_missing = createMissing.IsChecked == true
            });
            if (current != version) return;
            SetBusy(false);
            if (!response.Ok || response.Result is not { } result)
            {
                ShowStatus("Could not save: " + (response.Error ?? "the core did not answer."));
                var reload = await Call("agents.memory_files.get", new { workspace_root = workspaceRoot });
                if (current == version && reload.Ok && reload.Result is { } saved && saved.TryGetProperty("settings", out var savedSettings)) Apply(savedSettings);
                return;
            }
            if (result.TryGetProperty("settings", out var settings)) Apply(settings);
            ShowStatus(toggle.IsOn ? AgentFilesReport(result) : "Off. Nexus will not touch these files.");
        }

        toggle.Toggled += async (_, _) => await SaveAsync();
        foreach (var (_, box) in boxes) box.Click += async (_, _) => await SaveAsync();
        createMissing.Click += async (_, _) => await SaveAsync();

        _ = LoadAsync();
        async Task LoadAsync()
        {
            var response = await Call("agents.memory_files.get", new { workspace_root = workspaceRoot });
            if (version != 0) return;
            if (response.Ok && response.Result is { } result && result.TryGetProperty("settings", out var settings))
            {
                Apply(settings);
                SetBusy(false);
            }
            else
            {
                ShowStatus("Unavailable: " + (response.Error ?? "this core build does not support instruction files."));
            }
        }

        // Nested row inside the workspace row: label + description left,
        // switch right; file choices and the last sync result beneath.
        var head = new Grid { ColumnSpacing = 24 };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.Children.Add(RowText("Agent instruction files",
            "Keep CLAUDE.md / AGENTS.md / GEMINI.md updated with project memory. Nexus edits only its own marked section; your text is never changed."));
        toggle.VerticalAlignment = VerticalAlignment.Center;
        Grid.SetColumn(toggle, 1);
        head.Children.Add(toggle);
        options.Margin = new Thickness(0, 2, 0, 0);
        return new Border
        {
            BorderThickness = new Thickness(0, 1, 0, 0),
            BorderBrush = NexusPalette.Hairline,
            Padding = new Thickness(0, 10, 0, 0),
            Child = new StackPanel { Spacing = 6, Children = { head, options, status } }
        };
    }

    // "CLAUDE.md updated · AGENTS.md unchanged · GEMINI.md skipped (missing)"
    private static string AgentFilesReport(JsonElement result)
    {
        var error = NxJson.Text(result, "error");
        if (!result.TryGetProperty("report", out var report) || report.ValueKind != JsonValueKind.Object)
            return error.Length > 0 ? "Sync failed: " + error : "Saved.";
        var parts = new List<string>();
        if (report.TryGetProperty("files", out var files) && files.ValueKind == JsonValueKind.Array)
        {
            foreach (var file in files.EnumerateArray())
            {
                var id = NxJson.Text(file, "file");
                var known = AgentFileIds.Where(f => f.Id.Equals(id, StringComparison.OrdinalIgnoreCase)).Select(f => f.Name).FirstOrDefault();
                var name = known is { Length: > 0 } ? known : Path.GetFileName(NxJson.Text(file, "path"));
                var reason = NxJson.Text(file, "reason");
                parts.Add(NxJson.Text(file, "action") switch
                {
                    "created" => name + " created",
                    "updated" => name + " updated",
                    "unchanged" => name + " unchanged",
                    "skipped" => name + " skipped (" + (reason.StartsWith("file does not exist", StringComparison.Ordinal) ? "missing" : reason.Length > 0 ? reason : "skipped") + ")",
                    "error" => name + " failed (" + (reason.Length > 0 ? reason : "error") + ")",
                    var other => name + " " + other
                });
            }
        }
        if (parts.Count > 0)
            return string.Join(" · ", parts) + (error.Length > 0 ? " · " + error : "");
        var skipped = NxJson.Text(report, "skipped");
        var why = skipped switch
        {
            "not connected" => "Saved. Files update once this workspace is connected to your cloud project (sign in and wait for monitoring to connect).",
            "brief fetch failed" => "Saved, but the project brief could not be loaded.",
            "memory fetch failed" => "Saved, but project memory could not be loaded.",
            "settings unreadable" => "Saved, but the settings file could not be read.",
            "no workspace" => "Saved. Files update once monitoring for this workspace has started.",
            "" => "Saved. No files selected.",
            _ => "Saved (" + skipped + ")."
        };
        return error.Length > 0 ? why + " " + error : why;
    }
}
