using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// One-click "Install Nexus into all agents" (agents.install_all): adds the
/// Nexus memory MCP server to every installed agent, optionally shares the
/// agent setup and turns on the managed memory section. Always previewed as
/// a dry run first; "Uninstall" removes only the Nexus MCP entries.
/// </summary>
public sealed partial class AgentSetupPage
{
    private const string InstallAllMethod = "agents.install_all";
    private const string UninstallAllMethod = "agents.uninstall_all";

    private Button? _installButton;

    private UIElement InstallBar()
    {
        // Hidden once every installed agent is connected (see LoadAsync);
        // "Disconnect" stays in the ... menu.
        var install = _installButton = new Button { Content = "Connect Nexus to your agents", Padding = new Thickness(14, 6, 14, 7) };
        ToolTipService.SetToolTip(install, "Adds Nexus to every coding agent on this computer so they share memory and chats. You review the changes first.");
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accent) && accent is Style style)
            install.Style = style;
        install.Click += async (_, _) => await InstallAllAsync();
        var uninstall = new MenuFlyoutItem { Text = "Disconnect Nexus from all agents", Icon = new FontIcon { Glyph = "\uE711" } };
        ToolTipService.SetToolTip(uninstall, "Removes only the Nexus entry from each agent's settings");
        uninstall.Click += async (_, _) => await UninstallAllAsync();
        var more = new Button
        {
            Content = new FontIcon { Glyph = "\uE712", FontSize = 14 }, Padding = new Thickness(8), MinWidth = 34,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0),
            Flyout = new MenuFlyout { Items = { uninstall }, Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedRight }
        };
        ToolTipService.SetToolTip(more, "More");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(more, "More agent actions");
        var bar = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 4, Margin = new Thickness(0, 6, 0, 2) };
        bar.Children.Add(install);
        bar.Children.Add(more);
        return bar;
    }

    private object InstallArgs(bool dryRun, bool share, bool memory, bool projectOnly) => new
    {
        dry_run = dryRun,
        include_setup_share = share,
        include_memory_files = memory,
        scope = projectOnly ? "project" : "user",
        project_id = projectOnly ? ShellProjectId() : "",
        workspace_root = _workspace,
        // Antigravity runs the nexus-mcp-proxy.exe shipped next to Nexus.exe.
        app_dir = AppContext.BaseDirectory
    };

    private async Task InstallAllAsync()
    {
        var hasFolder = _workspace.Length > 0;
        var everywhere = new RadioButton { Content = "All projects (each agent's user settings)", GroupName = "nexus-install-scope", IsChecked = true };
        var projectOnly = new RadioButton { Content = hasFolder ? "Only this project folder" : "Only this project folder (choose a project first)", GroupName = "nexus-install-scope", IsEnabled = hasFolder };
        var share = new CheckBox { Content = "Share skills, sub-agents, commands and rules between agents" };
        var memory = new CheckBox { Content = "Keep CLAUDE.md, AGENTS.md and GEMINI.md updated with project memory", IsEnabled = hasFolder };
        var preview = new StackPanel { Spacing = 6 };

        var body = new StackPanel { Spacing = 8, MaxWidth = 560 };
        body.Children.Add(Small("Adds the Nexus memory server (memory_search, project_knowledge, related_to, file_history and the rest) to every agent installed on this computer. Each agent gets its own revocable token, never your sign-in. Changed files are backed up first.", 0.7));
        body.Children.Add(everywhere);
        body.Children.Add(projectOnly);
        body.Children.Add(share);
        body.Children.Add(memory);
        body.Children.Add(new TextBlock { Text = "What will change", FontSize = 13, FontWeight = Microsoft.UI.Text.FontWeights.Medium, Margin = new Thickness(0, 8, 0, 0) });
        body.Children.Add(preview);

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Connect Nexus to your agents",
            Content = new ScrollViewer { Content = body, MaxHeight = 480 },
            PrimaryButtonText = "Install", CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary, IsPrimaryButtonEnabled = false
        };

        var version = 0;
        async Task RefreshAsync()
        {
            var mine = ++version;
            dialog.IsPrimaryButtonEnabled = false;
            preview.Children.Clear();
            preview.Children.Add(Small("Checking your agents…", 0.6));
            var response = await Core.CallAsync(InstallAllMethod, InstallArgs(true, share.IsChecked == true, memory.IsChecked == true, projectOnly.IsChecked == true));
            await UiThread.Resume(DispatcherQueue);
            if (mine != version) return;
            preview.Children.Clear();
            if (!response.Ok || response.Result is not { } result)
            {
                preview.Children.Add(Small(response.Error ?? "Could not check your agents.", 0.85));
                return;
            }
            dialog.IsPrimaryButtonEnabled = RenderInstallReport(preview, result, true) > 0;
        }
        foreach (var box in new ToggleButton[] { share, memory, everywhere, projectOnly })
        {
            box.Checked += async (_, _) => await RefreshAsync();
            box.Unchecked += async (_, _) => await RefreshAsync();
        }
        _ = RefreshAsync();
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;

        _status.Text = "Connecting Nexus to your agents…";
        var done = await Core.CallAsync(InstallAllMethod, InstallArgs(false, share.IsChecked == true, memory.IsChecked == true, projectOnly.IsChecked == true));
        await UiThread.Resume(DispatcherQueue);
        if (!done.Ok || done.Result is not { } report)
        {
            _status.Text = done.Error ?? "Couldn't connect Nexus to your agents.";
            return;
        }
        var panel = new StackPanel { Spacing = 6, MaxWidth = 560 };
        RenderInstallReport(panel, report, false);
        _status.Text = "Nexus is connected. Restart any agent that is open so it picks Nexus up.";
        await new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Nexus is connected",
            Content = new ScrollViewer { Content = panel, MaxHeight = 480 },
            CloseButtonText = "Done", DefaultButton = ContentDialogButton.Close
        }.ShowThemedAsync();
        await LoadAsync();
    }

    private async Task UninstallAllAsync()
    {
        var scopes = _workspace.Length > 0 ? new[] { "user", "project" } : new[] { "user" };
        var plans = new List<JsonElement>();
        foreach (var scope in scopes)
        {
            var response = await Core.CallAsync(UninstallAllMethod, new { dry_run = true, scope, workspace_root = _workspace });
            await UiThread.Resume(DispatcherQueue);
            if (!response.Ok || response.Result is not { } result)
            {
                _status.Text = response.Error ?? "Could not check your agents.";
                return;
            }
            plans.Add(result);
        }
        var body = new StackPanel { Spacing = 6, MaxWidth = 560 };
        var changes = 0;
        foreach (var plan in plans) changes += RenderInstallReport(body, plan, true, onlyChanges: true);
        if (changes == 0) body.Children.Add(Small("No agent is connected to Nexus.", 0.8));
        else body.Children.Add(Small("Only Nexus is removed; the agent's other tools and settings stay. Files are backed up first. Agent keys stay valid until you revoke them in Settings.", 0.6));
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Disconnect Nexus from your agents",
            Content = new ScrollViewer { Content = body, MaxHeight = 420 },
            PrimaryButtonText = changes > 0 ? "Disconnect" : "", CloseButtonText = changes > 0 ? "Cancel" : "Close",
            DefaultButton = changes > 0 ? ContentDialogButton.Primary : ContentDialogButton.Close
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;

        var removed = 0;
        var problems = new List<string>();
        foreach (var scope in scopes)
        {
            var response = await Core.CallAsync(UninstallAllMethod, new { dry_run = false, scope, workspace_root = _workspace });
            await UiThread.Resume(DispatcherQueue);
            if (!response.Ok || response.Result is not { } result)
            {
                problems.Add(response.Error ?? "Uninstall failed.");
                continue;
            }
            foreach (var h in ArrayOf(result, "harnesses"))
            {
                var action = NxJson.Text(h, "action");
                if (action == "removed") removed++;
                else if (action == "error") problems.Add(NxJson.Text(h, "name") + ": " + PlainReason(NxJson.Text(h, "reason")));
            }
        }
        _status.Text = (removed > 0 ? $"Removed Nexus from {removed} agent config{(removed == 1 ? "" : "s")}." : "Nothing removed.") +
            (problems.Count > 0 ? " " + string.Join(" · ", problems) : "");
        await LoadAsync();
    }

    /// <summary>
    /// A setup reason from the engine as a sentence people can act on. Its
    /// " [detail: …]" part (file paths, parser errors) goes to the log only.
    /// </summary>
    internal static string PlainReason(string reason)
    {
        reason = reason.Trim();
        if (reason.Length == 0) return "";
        var (sentence, detail) = NxErrors.SplitDetail(reason);
        var (message, reference) = NxErrors.Friendly(sentence.Length > 0 ? sentence : reason);
        if (detail.Length > 0 || reference.Length > 0)
            Nexus.Core.Diagnostics.DiagLog.Warn("agents", "setup: " + reason + (reference.Length > 0 ? " (ref " + reference + ")" : ""));
        return message.Length > 0 ? message : sentence;
    }

    /// <summary>
    /// Renders a per-harness report; returns how many things change.
    /// foldFiles keeps the changed settings files behind one expander, for
    /// people who only need to know which agents change (the setup guide).
    /// </summary>
    internal static int RenderInstallReport(Panel into, JsonElement result, bool preview, bool onlyChanges = false, bool foldFiles = false)
    {
        var changes = 0;
        var notInstalled = new List<string>();
        var folded = new List<string>();
        foreach (var h in ArrayOf(result, "harnesses"))
        {
            var name = NxJson.Text(h, "name");
            var action = NxJson.Text(h, "action");
            var rawReason = NxJson.Text(h, "reason");
            if (action == "skipped" && rawReason == "not installed") { notInstalled.Add(name); continue; }
            var reason = PlainReason(rawReason);
            var changed = action is "added" or "updated" or "removed";
            if (changed) changes++;
            if (onlyChanges && !changed) continue;
            var label = action switch
            {
                "added" => preview ? "Will add Nexus" : "Added Nexus",
                "updated" => preview ? "Will update the Nexus entry" : "Updated the Nexus entry",
                "removed" => preview ? "Will remove Nexus" : "Removed Nexus",
                "unchanged" => reason.Length > 0 ? reason : "Already set up",
                "skipped" => "Skipped · " + reason,
                "error" => "Problem · " + reason,
                _ => action
            };
            var row = new StackPanel { Spacing = 1 };
            row.Children.Add(new TextBlock { Text = name + " — " + label, FontSize = 12.5, TextWrapping = TextWrapping.Wrap, Opacity = action is "unchanged" or "skipped" ? 0.6 : 1 });
            if (changed)
                foreach (var file in ArrayOf(h, "changed_files"))
                {
                    if (foldFiles) folded.Add(file.GetString() ?? "");
                    else row.Children.Add(Mono(file.GetString() ?? ""));
                }
            into.Children.Add(row);
        }
        if (folded.Count > 0)
        {
            var files = new StackPanel { Spacing = 2 };
            foreach (var file in folded) files.Children.Add(Mono(file));
            into.Children.Add(new Expander
            {
                Header = preview ? "Settings files Nexus will change" : "Settings files Nexus changed",
                Content = files, HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch,
                Margin = new Thickness(0, 4, 0, 0)
            });
        }
        if (notInstalled.Count > 0 && !onlyChanges)
            into.Children.Add(Small("Not installed: " + string.Join(", ", notInstalled), 0.5));

        if (result.TryGetProperty("setup_share", out var share) && share.ValueKind == JsonValueKind.Object)
        {
            var writes = ArrayOf(share, "writes").ToList();
            changes += writes.Count;
            var head = writes.Count == 0 ? "Agent setup: nothing new to share" :
                (preview ? "Agent setup: will copy " : "Agent setup: copied ") + writes.Count + " file" + (writes.Count == 1 ? "" : "s");
            into.Children.Add(new TextBlock { Text = head, FontSize = 12.5, Margin = new Thickness(0, 6, 0, 0), TextWrapping = TextWrapping.Wrap });
            foreach (var w in writes.Take(12))
                into.Children.Add(Mono((NxJson.Text(w, "Action", "action") == "update" ? "Update " : "Create ") + NxJson.Text(w, "Path", "path")));
            if (writes.Count > 12) into.Children.Add(Small($"…and {writes.Count - 12} more", 0.6));
            var skipped = ArrayOf(share, "skipped").Select(s => s.GetString() ?? "").Where(s => s.Length > 0).ToList();
            if (skipped.Count > 0) into.Children.Add(Small($"{skipped.Count} item{(skipped.Count == 1 ? "" : "s")} left alone (already different or not convertible).", 0.6));
            if (NxJson.Text(share, "error") is { Length: > 0 } shareError) into.Children.Add(Small("Problem: " + PlainReason(shareError), 0.85));
        }

        if (result.TryGetProperty("memory_files", out var memory) && memory.ValueKind == JsonValueKind.Object)
        {
            var action = NxJson.Text(memory, "action");
            if (action == "updated") changes++;
            var text = action switch
            {
                "updated" => preview ? "CLAUDE.md / AGENTS.md / GEMINI.md: will keep a project-memory section updated" : "CLAUDE.md / AGENTS.md / GEMINI.md: project-memory section turned on",
                "unchanged" => "CLAUDE.md / AGENTS.md / GEMINI.md: already kept updated",
                _ => "CLAUDE.md / AGENTS.md / GEMINI.md: " + NxJson.Text(memory, "reason")
            };
            into.Children.Add(new TextBlock { Text = text, FontSize = 12.5, Margin = new Thickness(0, 6, 0, 0), TextWrapping = TextWrapping.Wrap });
        }

        if (preview && changes > 0 && !onlyChanges)
            into.Children.Add(Small("Existing files are backed up next to the original first (.nexus-backup-…).", 0.6));
        if (!preview && changes == 0)
            into.Children.Add(Small("Everything was already set up.", 0.7));
        return changes;
    }

    private static IEnumerable<JsonElement> ArrayOf(JsonElement obj, string key) =>
        obj.TryGetProperty(key, out var a) && a.ValueKind == JsonValueKind.Array ? a.EnumerateArray().ToList() : Enumerable.Empty<JsonElement>();

    private static TextBlock Small(string text, double opacity) =>
        new() { Text = text, FontSize = 12, Opacity = opacity, TextWrapping = TextWrapping.Wrap };

    private static TextBlock Mono(string text) =>
        new() { Text = text, FontSize = 11.5, Opacity = 0.65, TextWrapping = TextWrapping.Wrap, FontFamily = new FontFamily("Cascadia Mono, Consolas") };
}
