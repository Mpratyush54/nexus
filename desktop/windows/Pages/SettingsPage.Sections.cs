using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using System.Diagnostics;
using System.Reflection;
using System.Text.Json;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

public sealed partial class SettingsPage
{
    // ---- General

    private readonly ToggleSwitch _loginStart = new() { OnContent = "", OffContent = "", MinWidth = 0 };
    private bool _loginStartSync;

    private UIElement BuildGeneralSection()
    {
        var theme = new ComboBox { MinWidth = 160 };
        foreach (var (label, value) in new[] { (PlatformText.SystemThemeChoice, ThemeService.System), ("Light", ThemeService.Light), ("Dark", ThemeService.Dark) })
            theme.Items.Add(new ComboBoxItem { Content = label, Tag = value });
        theme.SelectedItem = theme.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (string)i.Tag == ThemeService.Preference) ?? theme.Items[0];
        AutomationProperties.SetName(theme, "Theme");
        theme.SelectionChanged += (_, _) =>
        {
            if ((theme.SelectedItem as ComboBoxItem)?.Tag is string chosen && chosen != ThemeService.Preference && MainWindow.Current is { } window)
                ThemeService.Set(window, chosen);
        };
        var appearance = SettingsGroup("Appearance",
            SettingRow(RowText("Theme", PlatformText.IsMac ? "Light, dark, or the same as macOS." : "Light, dark, or the same as Windows. High contrast always follows Windows."), theme));

        _loginStartSync = true;
        _loginStart.IsOn = LoginStart.IsEnabled();
        _loginStartSync = false;
        _loginStart.Toggled += (_, _) =>
        {
            if (_loginStartSync) return;
            try { LoginStart.SetEnabled(_loginStart.IsOn); }
            catch (Exception ex) { Notify(NxErrors.ForException($"{PlatformText.Os} didn't let Nexus change whether it starts when you sign in.", ex, "settings"), true); }
        };
        var startup = SettingsGroup("Startup & window",
            SettingRow(RowText("Start Nexus when I sign in", $"Starts Nexus when you sign in to {PlatformText.Os} so capture keeps running in the background."), _loginStart),
            SettingRow(RowText("Closing the window", PlatformText.IsMac
                ? "Closing hides Nexus to the menu bar and capture keeps running. Quit with ⌘Q or from the Nexus icon's menu."
                : "Closing hides Nexus to the system tray and capture keeps running. Quit from the tray icon's menu.")));
        var rerun = new Button { Content = "Run setup" };
        rerun.Click += (_, _) => MainWindow.Current?.ShowSetup();
        var setup = SettingsGroup("Setup",
            SettingRow(RowText("Setup guide", "Sign in, connect your agents, choose workspaces and startup options again."), rerun),
            SettingRow(RowText("Updates", _updateStatus), _updateButton));
        _updateButton.Click += async (_, _) => await UpdateAsync();
        RenderUpdateState();
        return new StackPanel { Children = { appearance, startup, setup } };
    }

    // ---- General: updates (installed copies only; see UpdateService)

    private readonly TextBlock _updateStatus = new() { FontSize = NexusTokens.FontCaption, Opacity = 0.65, TextWrapping = TextWrapping.Wrap };
    private readonly Button _updateButton = new() { Content = "Check for updates" };
    private bool _updateBusy;

    private void RenderUpdateState()
    {
        var updates = UpdateService.Shared;
        var staged = updates.IsAvailable ? updates.StagedVersion : "";
        _updateStatus.Text = !updates.IsAvailable
            ? updates.UnavailableText
            : staged.Length > 0 ? $"Version {staged} is ready. Restart Nexus to finish updating."
            : $"Version {updates.CurrentVersion}. Nexus checks for updates in the background and installs them when it restarts.";
        _updateButton.Visibility = updates.IsAvailable ? Visibility.Visible : Visibility.Collapsed;
        _updateButton.Content = staged.Length > 0 ? "Restart to update" : "Check for updates";
        AutomationProperties.SetName(_updateButton, _updateButton.Content as string ?? "Check for updates");
    }

    private async Task UpdateAsync()
    {
        if (_updateBusy) return;
        var updates = UpdateService.Shared;
        if (updates.StagedVersion.Length > 0)
        {
            await Task.Run(() => App.Core.Shutdown());
            updates.ApplyAndRestart();
            return;
        }
        _updateBusy = true;
        _updateButton.IsEnabled = false;
        _updateStatus.Text = "Checking for updates…";
        try
        {
            var found = await updates.CheckForUpdatesAsync();
            await UiThread.Resume(DispatcherQueue);
            RenderUpdateState();
            if (found.Length == 0) _updateStatus.Text = $"Version {updates.CurrentVersion} is the latest.";
        }
        catch (Exception ex)
        {
            // A feed that is down or not published yet (HTTP 403/404) is
            // not something the person can fix: one calm line, details in
            // the log under a reference id.
            var reference = NxErrors.NewRef();
            Nexus.Core.Diagnostics.DiagLog.Warn("update", $"update check failed (ref {reference})", ex);
            await UiThread.Resume(DispatcherQueue);
            _updateStatus.Text = $"Couldn't check for updates right now. Nexus tries again on its own later. (ref {reference})";
        }
        finally
        {
            _updateBusy = false;
            _updateButton.IsEnabled = true;
        }
    }

    private Task LoadGeneralAsync()
    {
        _loginStartSync = true;
        try { _loginStart.IsOn = LoginStart.IsEnabled(); }
        finally { _loginStartSync = false; }
        return Task.CompletedTask;
    }

    // ---- Account: agent tokens
    //
    // The one-click agent install mints scoped nxs_ tokens named
    // "Nexus MCP · <harness>" (POST /auth/tokens). The server lists them at
    // GET /auth/tokens ({items:[{id,name,token_prefix,agent_id,created_at,
    // last_used_at,revoked_at}]}) and revokes at DELETE /auth/tokens/{id}.
    // These nx_call names are what the core needs to route to those; older
    // cores answer "unknown nx_call method" and the list says so.

    private const string TokensListMethod = "auth.tokens.list";
    private const string TokensRevokeMethod = "auth.tokens.revoke";
    private readonly StackPanel _tokens = new();

    private async Task LoadTokensAsync()
    {
        if (!_signedIn)
        {
            _tokens.Children.Clear();
            _tokens.Children.Add(NoteRow("Sign in to see the tokens your agents use."));
            return;
        }
        if (_tokens.Children.Count == 0) _tokens.Children.Add(NoteRow("Loading tokens…"));
        var response = await Call(TokensListMethod);
        _tokens.Children.Clear();
        if (!response.Ok)
        {
            var unsupported = NxErrors.IsUnsupported(response.RawError);
            _tokens.Children.Add(NoteRow(unsupported
                ? "Listing and revoking agent tokens needs a newer Nexus core. Uninstalling Nexus from an agent removes its config entry; its token stays valid until revoked."
                : response.Error ?? "Tokens are unavailable."));
            return;
        }
        var items = NxJson.Items(response.Result)
            .Where(t => NxJson.Text(t, "revoked_at").Length == 0)
            .OrderByDescending(t => NxJson.Text(t, "name").StartsWith("Nexus MCP", StringComparison.Ordinal))
            .ToList();
        if (items.Count == 0)
        {
            _tokens.Children.Add(NoteRow("No agents connected yet. Connect them from the Agents page; each gets its own key."));
            return;
        }
        foreach (var token in items)
        {
            var id = NxJson.Text(token, "id");
            var name = NxJson.Text(token, "name");
            if (name.Length == 0) name = "API token";
            // "Nexus MCP · cursor (maya-desktop)" reads as "Cursor on maya-desktop".
            var match = System.Text.RegularExpressions.Regex.Match(name, @"^Nexus MCP · (?<agent>[^(]+?)\s*(?:\((?<host>[^)]+)\))?$");
            if (match.Success)
                name = ChatTitles.AgentName(match.Groups["agent"].Value.Trim()) + (match.Groups["host"].Success ? " on " + match.Groups["host"].Value : "");
            var details = new List<string>();
            var prefix = NxJson.Text(token, "token_prefix", "prefix");
            if (FriendlyWhen(NxJson.Text(token, "created_at")) is { Length: > 0 } created) details.Add("created " + created);
            var used = FriendlyWhen(NxJson.Text(token, "last_used_at"));
            details.Add(used.Length > 0 ? "last used " + used : "never used");
            var revoke = new Button { Content = "Revoke", IsEnabled = id.Length > 0 };
            AutomationProperties.SetName(revoke, "Revoke " + name);
            var label = name;
            revoke.Click += async (_, _) =>
            {
                if (!await ConfirmOrgAsync("Revoke this token?", $"{label} stops working immediately. An agent using it loses access to Nexus memory until you connect Nexus to it again.", "Revoke")) return;
                revoke.IsEnabled = false;
                var result = await Call(TokensRevokeMethod, new { token_id = id });
                Notify(result.Ok ? "Token revoked." : result.Error ?? "Could not revoke token.", !result.Ok);
                await LoadTokensAsync();
            };
            var row = SettingRow(RowText(name, string.Join(" · ", details)), revoke);
            // The key's prefix identifies it for support; it is detail, not a headline.
            if (prefix.Length > 0) ToolTipService.SetToolTip(row, "Key starts with " + prefix + "…");
            _tokens.Children.Add(row);
        }
    }

    // ---- Agents

    private readonly TextBlock _mcpStatus = Muted("Checking your agents…");
    private Button? _mcpConnect;

    private UIElement BuildAgentsSection()
    {
        var install = _mcpConnect = new Button { Content = "Connect Nexus to your agents" };
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accent) && accent is Style style) install.Style = style;
        AutomationProperties.SetName(install, "Connect Nexus to your agents (opens the Agents page)");
        install.Click += (_, _) => OpenShellDestination("FooterAgents", "Agents");
        var open = new Button { Content = "Open Agents" };
        open.Click += (_, _) => OpenShellDestination("FooterAgents", "Agents");
        var mcp = SettingsGroup("Connected agents",
            SettingRow(RowText("Nexus memory server", _mcpStatus), install),
            SettingRow(RowText("Skills, sub-agents and tools", "See each agent's setup and share it between agents on the Agents page."), open));
        var web = SettingsGroup("Run agents from the browser",
            SettingRow(RowText("Allow requests from Nexus Web",
                "Off by default. When on, you can start an agent on this computer from Nexus in your browser. Nexus asks you here before every message, and only folders you chose can be used. Turning this off stops any work started that way."),
                _browserExecution, _browserExecutionStatus));
        return new StackPanel { Children = { mcp, web } };
    }

    private async Task LoadAgentsSectionAsync()
    {
        await LoadMcpStatusAsync();
        await RefreshBrowserExecutionAsync();
    }

    // A dry-run uninstall is read-only and reports, per agent, whether a
    // Nexus entry exists ("removed" = would be removed = installed).
    private async Task LoadMcpStatusAsync()
    {
        var response = await Call("agents.uninstall_all", new { dry_run = true, scope = "user" });
        if (!response.Ok || response.Result is not { } result)
        {
            _mcpStatus.Text = response.Error ?? "Could not check your agents.";
            return;
        }
        var connected = new List<string>();
        var missing = new List<string>();
        if (result.TryGetProperty("harnesses", out var harnesses) && harnesses.ValueKind == JsonValueKind.Array)
        {
            foreach (var harness in harnesses.EnumerateArray())
            {
                var name = NxJson.Text(harness, "name");
                var action = NxJson.Text(harness, "action");
                if (action == "removed") connected.Add(name);
                else if (!(action == "skipped" && NxJson.Text(harness, "reason") == "not installed")) missing.Add(name);
            }
        }
        if (connected.Count == 0 && missing.Count == 0)
        {
            _mcpStatus.Text = "No supported agents were found on this computer.";
            return;
        }
        // Nothing left to connect: no call to action for a finished job.
        if (_mcpConnect is not null) _mcpConnect.Visibility = missing.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
        var text = connected.Count > 0 ? "Connected in " + string.Join(", ", connected) + "." : "Not connected to any agent yet.";
        if (missing.Count > 0) text += " Not yet in " + string.Join(", ", missing) + ".";
        _mcpStatus.Text = text + " Each agent gets its own revocable token (see Account).";
    }

    // ---- Memory

    private readonly TextBlock _briefStatus = Muted("Loading the project brief…");
    private readonly Button _briefRefresh = new() { Content = "Refresh brief", IsEnabled = false };

    private UIElement BuildMemorySection()
    {
        _briefRefresh.Click += async (_, _) => await RefreshBriefAsync();
        var openMemory = new Button { Content = "Open Memory" };
        openMemory.Click += (_, _) => OpenShellDestination("FooterMemory", "Memory");
        var openWorkspaces = new Button { Content = "Workspaces" };
        AutomationProperties.SetName(openWorkspaces, "Go to Workspaces & capture");
        openWorkspaces.Click += (_, _) => ShowSection("workspaces");
        var brief = SettingsGroup("Project memory",
            SettingRow(RowText("Project brief", _briefStatus), _briefRefresh),
            SettingRow(RowText("Memories", "Review, pin, correct, or forget what Nexus has learned for the current project."), openMemory),
            SettingRow(RowText("Agent instruction files", "Keep a Nexus-marked section of CLAUDE.md, AGENTS.md and GEMINI.md in sync with project memory. Set per workspace."), openWorkspaces));
        var visibility = SettingsGroup("Who can see it",
            SettingRow(RowText("Private by default", "Captured chats are visible only to you until you share them.")),
            SettingRow(RowText("Sharing a chat", "Share with the project team for a live view, or with named people. Private snapshot shares stay limited to the version you shared.")),
            SettingRow(RowText("Organizations", "Organization membership does not automatically reveal private chats. Members see only chats shared with the team.")));
        return new StackPanel { Children = { brief, visibility } };
    }

    private async Task LoadMemorySectionAsync()
    {
        var projectId = ShellProjectId();
        if (projectId.Length == 0)
        {
            _briefRefresh.IsEnabled = false;
            _briefStatus.Text = "Choose a project in the sidebar to see and refresh its brief.";
            return;
        }
        var label = ShellProjectLabel();
        _briefRefresh.IsEnabled = true;
        var response = await Call(NxMethods.ProjectsBrief, new { project_id = projectId });
        if (projectId != ShellProjectId()) return;
        var updated = response.Ok && response.Result is { ValueKind: JsonValueKind.Object } brief ? FriendlyWhen(NxJson.Text(brief, "updated_at")) : "";
        _briefStatus.Text = $"The summary agents read first for {label}." +
            (!response.Ok ? " " + (response.Error ?? "The brief is unavailable.") : updated.Length > 0 ? $" Updated {updated}." : "");
    }

    private async Task RefreshBriefAsync()
    {
        var projectId = ShellProjectId();
        if (projectId.Length == 0) return;
        _briefRefresh.IsEnabled = false;
        _briefRefresh.Content = "Refreshing…";
        try
        {
            var response = await Call(NxMethods.ProjectsBriefRefresh, new { project_id = projectId });
            Notify(response.Ok ? "Project brief refreshed." : response.Error ?? "Could not refresh the project brief.", !response.Ok);
        }
        finally
        {
            _briefRefresh.Content = "Refresh brief";
            _briefRefresh.IsEnabled = true;
        }
        await LoadMemorySectionAsync();
    }

    // ---- Usage
    //
    // SettingsPage.Usage.cs (owned separately) exposes BuildUsageSection().
    // Wire it by implementing this hook there:
    //     partial void BuildUsageSectionHook(ref UIElement? section) => section = BuildUsageSection();
    // Until then the section shows a placeholder and nothing else changes.
    partial void BuildUsageSectionHook(ref UIElement? section);

    private UIElement BuildUsageSectionSlot()
    {
        UIElement? section = null;
        BuildUsageSectionHook(ref section);
        return section ?? SettingsGroup("Usage",
            SettingRow(RowText("Usage details", "Usage for your account and agents will appear here.")));
    }

    // ---- Advanced

    private readonly TextBlock _uploads = Muted("Checking uploads…");
    private readonly TextBlock _diagnostics = Muted("Loading diagnostics…");
    private readonly Button _copyDiagnostics = new() { Content = "Copy diagnostics", IsEnabled = false };
    private string _diagnosticsJson = "";

    private UIElement BuildAdvancedSection()
    {
        var refresh = new Button { Content = "Refresh" };
        AutomationProperties.SetName(refresh, "Refresh connection status");
        refresh.Click += async (_, _) => await LoadSectionAsync("advanced", force: true);
        var retry = new Button { Content = "Retry uploads" };
        retry.Click += async (_, _) =>
        {
            var result = await Call(NxMethods.UploadsRetry);
            Notify(result.Ok ? "Upload retry requested." : result.Error ?? "Retry failed.", !result.Ok);
            await LoadUploadsAsync();
        };
        var clear = new Button { Content = "Clear local cache" };
        clear.Click += async (_, _) =>
        {
            var result = await Call(NxMethods.CacheClear);
            Notify(result.Ok ? "Local cache cleared. Your captured history is unchanged." : result.Error ?? "Could not clear cache.", !result.Ok);
        };
        _copyDiagnostics.Click += (_, _) => CopyText(_diagnosticsJson, "Diagnostics copied.");
        var version = VersionText();
        var copyVersion = new Button { Content = "Copy" };
        AutomationProperties.SetName(copyVersion, "Copy version");
        copyVersion.Click += (_, _) => CopyText(version, "Version copied.");

        var sync = SettingsGroup("Connection & sync",
            SettingRow(RowText("Cloud connection", _connection), refresh),
            SettingRow(RowText("Uploads", _uploads), retry));
        var local = SettingsGroup("This device",
            SettingRow(RowText("Local cache", "Clears cached lists and previews on this device. Your captured history is unchanged."), clear));
        var about = SettingsGroup("Diagnostics",
            SettingRow(RowText("Diagnostics", _diagnostics), _copyDiagnostics),
            SettingRow(RowText("Version", version), copyVersion));
        return new StackPanel { Children = { sync, local, about } };
    }

    private async Task LoadAdvancedAsync()
    {
        var diag = await Call(NxMethods.DiagnosticsGet);
        if (diag.Ok && diag.Result is { } status)
        {
            var online = NxJson.Text(status, "online") == "true";
            _connection.Text = online ? "Connected to your cloud account." : "Cloud sync is offline. Sign in or check your connection.";
            var capture = status.TryGetProperty("capture", out var c) ? c : default;
            var pending = long.TryParse(NxJson.Text(status, "pending_uploads"), out var waiting) ? waiting : 0;
            var parts = new List<string> { online ? "Online" : "Offline", pending == 0 ? "nothing waiting to upload" : Nexus.Core.FriendlyTime.Count(pending, "item") + " waiting to upload" };
            if (capture.ValueKind == JsonValueKind.Object) parts.Add(CaptureSummary(capture));
            if (NxJson.Text(status, "workspace_root") is { Length: > 0 } root) parts.Add(root);
            _diagnostics.Text = string.Join(" · ", parts);
            _diagnosticsJson = JsonSerializer.Serialize(status, new JsonSerializerOptions { WriteIndented = true });
            _copyDiagnostics.IsEnabled = true;
        }
        else
        {
            _diagnostics.Text = diag.Error ?? "Diagnostics are unavailable.";
        }
        await LoadUploadsAsync();
        var cloud = await Call(NxMethods.ProjectsList);
        _connection.Text = cloud.Ok ? "Connected. Your chats sync to your Nexus account." : cloud.Error ?? "Cloud sync is unavailable. Check your connection and sign-in.";
    }

    private async Task LoadUploadsAsync()
    {
        var response = await Call(NxMethods.UploadsStatus);
        if (!response.Ok || response.Result is not { } result)
        {
            _uploads.Text = response.Error ?? "Upload status is unavailable.";
            return;
        }
        var pending = NxJson.Text(result, "pending");
        _uploads.Text = pending is "" or "0"
            ? "Everything captured on this device has been uploaded."
            : $"{pending} item{(pending == "1" ? "" : "s")} waiting to upload. Retry if they are stuck.";
    }

    private static string VersionText()
    {
        var assembly = typeof(App).Assembly;
        var info = assembly.GetCustomAttribute<AssemblyInformationalVersionAttribute>()?.InformationalVersion
            ?? assembly.GetName().Version?.ToString() ?? "unknown";
        var plus = info.IndexOf('+');
        var version = plus > 0 ? info[..plus] : info;
        var commit = plus > 0 ? info[(plus + 1)..] : "";
        return "Nexus " + version + (commit.Length > 0 ? " · commit " + commit[..Math.Min(10, commit.Length)] : "") + " · .NET " + Environment.Version;
    }

    private void CopyText(string text, string done)
    {
        if (text.Length == 0) return;
        try
        {
            var data = new DataPackage();
            data.SetText(text);
            Clipboard.SetContent(data);
            Notify(done);
        }
        catch (Exception ex) { Notify(NxErrors.ForException("Nexus couldn't copy that. Try again.", ex, "settings"), true); }
    }
}
