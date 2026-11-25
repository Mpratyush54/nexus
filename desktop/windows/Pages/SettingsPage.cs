using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using System.Diagnostics;
using System.Text.Json;
using Nexus.Core;

namespace Nexus.Pages;

// Sections and their data loaders: SettingsPage.Layout.cs (shell, rows),
// SettingsPage.Sections.cs (General, Agents, Memory, Usage, Advanced, tokens),
// SettingsPage.Organizations.cs, SettingsPage.AgentFiles.cs. Account and
// Workspaces & capture live here.
public sealed partial class SettingsPage : NexusPage
{
    private readonly StackPanel _workspaces = new();
    private readonly StackPanel _projects = new();
    private readonly StackPanel _sources = new();
    private readonly TextBlock _capture = Muted("Loading monitoring status…");
    private readonly TextBlock _connection = Muted("Checking the cloud connection…");
    private readonly TextBlock _account = Muted("Checking your account…");
    private readonly InfoBar _notice = new() { IsOpen = false, IsClosable = true };
    private readonly Button _pause = new() { Content = "Pause capture", IsEnabled = false };
    private readonly Button _manage = new() { Content = "Manage workspaces", IsEnabled = false };
    private readonly Button _signIn = new() { Content = "Sign in" };
    private readonly Button _signOut = new() { Content = "Sign out", IsEnabled = false, Visibility = Visibility.Collapsed };
    private readonly List<string> _approved = new();
    private readonly List<string> _suggested = new();
    // Instruction-file rows per workspace, kept across refreshes so their
    // state shows instantly and is not re-read on every status poll.
    private readonly Dictionary<string, UIElement> _agentFileRows = new(StringComparer.OrdinalIgnoreCase);
    private bool _paused;
    private bool _loading;
    private bool _captureLoaded;
    private bool _signedIn;
    private readonly ToggleSwitch _browserExecution = new() { OnContent = "", OffContent = "", MinWidth = 0, IsEnabled = false };
    private readonly TextBlock _browserExecutionStatus = Muted("");
    private bool _browserExecutionSync;

    public SettingsPage()
    {
        ToolTipService.SetToolTip(_manage, "Loading monitoring settings…");
        _manage.Click += async (_, _) => await ManageAsync();
        _pause.Click += async (_, _) =>
        {
            var response = await Call(_paused ? NxMethods.CaptureResume : NxMethods.CapturePause);
            Notify(response.Ok ? (_paused ? "Capture resumed." : "Capture paused.") : response.Error ?? "Could not change capture.", !response.Ok);
            await RefreshCaptureAsync();
        };
        _signIn.Click += async (_, _) => await SignInAsync();
        _signOut.Click += async (_, _) => await SignOutAsync();
        _browserExecution.Toggled += async (_, _) =>
        {
            if (_browserExecutionSync) return;
            _browserExecution.IsEnabled = false;
            var response = await Call("desktop.execution.configure", new { enabled = _browserExecution.IsOn });
            if (!response.Ok) Notify(response.Error ?? "Could not change browser access.", true);
            await RefreshBrowserExecutionAsync();
        };
        AutomationProperties.SetName(_browserExecution, "Allow requests from Nexus Web");
        Content = BuildShell();
    }

    // Returning to Settings shows the last section at once and refreshes it.
    protected override void OnEnter(object? parameter) =>
        ShowSection(_section.Length > 0 ? _section : s_lastSection, forceLoad: true);

    private async Task RefreshBrowserExecutionAsync()
    {
        var response = await Call("desktop.execution.status");
        _browserExecutionSync = true;
        var stored = response.Ok && response.Result is { } value && NxJson.Text(value, "enabled") == "true";
        // An older server has no desktop channel: say so instead of a raw HTTP
        // error, and show the switch off and disabled. Nothing is written
        // (the sync flag keeps Toggled from calling configure), so the saved
        // preference is still there once the server supports it.
        var unsupported = response.Ok && response.Result is { } support && NxJson.Text(support, "server_supported") == "false";
        _browserExecution.IsOn = stored && !unsupported;
        _browserExecutionStatus.Text = unsupported
            ? "Not available yet: your Nexus server does not support browser-to-desktop execution." +
              (stored ? " Your choice to allow it is kept for when it does." : "")
            : response.Ok && response.Result is { } status
            ? $"{NxJson.Text(status, "mapped_projects")} mapped projects · Approval required for every turn. {NxJson.Text(status, "problem")}"
            : response.Error ?? "Browser execution is unavailable in this core build.";
        _browserExecution.Visibility = Visibility.Visible;
        _browserExecution.IsEnabled = response.Ok && !unsupported;
        _browserExecutionSync = false;
    }

    private void Notify(string message, bool error = false)
    {
        _notice.Message = message;
        _notice.Severity = error ? InfoBarSeverity.Error : InfoBarSeverity.Success;
        _notice.IsOpen = true;
    }

    private async Task ManageAsync()
    {
        if (!_captureLoaded)
        {
            Notify("Monitoring settings are still loading. Try again in a moment.", true);
            return;
        }
        var result = await CaptureSetup.ShowAsync(this, _approved, _suggested);
        if (result is null) return;
        Notify(result.Ok ? "Monitoring choices saved." : result.Error ?? "Could not save monitoring choices.", !result.Ok);
        await RefreshCaptureAsync();
    }

    // ---- Account

    private UIElement BuildAccountSection()
    {
        var account = SettingsGroup("Account",
            SettingRow(RowText("Nexus account", _account), Row(_signIn, _signOut)));
        var refresh = new Button { Content = "Refresh" };
        AutomationProperties.SetName(refresh, "Refresh agent tokens");
        refresh.Click += async (_, _) => await LoadTokensAsync();
        var tokens = SettingsGroup("Agent access",
            SettingRow(RowText("Connected agents", "Each agent you connect gets its own key, which you can revoke at any time. Your own sign-in is never shared with agents."), refresh),
            _tokens);
        return new StackPanel { Children = { account, tokens } };
    }

    private async Task LoadAccountSectionAsync()
    {
        await RefreshAccountAsync();
        await LoadTokensAsync();
    }

    // Called by the shell after a browser sign-in completes.
    public void RefreshAccount()
    {
        InvalidateSection("organizations");
        InvalidateSection("agents");
        _ = LoadSectionAsync("account", force: true);
    }

    private async Task RefreshAccountAsync()
    {
        var response = await Call(NxMethods.AuthStatus);
        if (!response.Ok || response.Result is not { } status)
        {
            _account.Text = response.Error ?? "Account status is unavailable.";
            SetSignedIn(false);
            ClearOrganizationState();
            return;
        }
        var signedIn = NxJson.Text(status, "signed_in") == "true";
        var name = NxJson.Text(status, "username");
        var server = Uri.TryCreate(NxJson.Text(status, "server_url"), UriKind.Absolute, out var serverUri) ? serverUri.Authority : "";
        _account.Text = signedIn
            ? (name.Length > 0 ? $"Signed in as @{name}" : "Signed in to your Nexus account") + (server.Length > 0 ? $" on {server}." : ".")
            : "Not signed in. Sign in to sync your chats and memories across your computers.";
        SetSignedIn(signedIn);
        if (!signedIn || (_orgUser.Length > 0 && _orgUser != NxJson.Text(status, "user_id"))) ClearOrganizationState();
    }

    private void SetSignedIn(bool signedIn)
    {
        _signedIn = signedIn;
        _signIn.IsEnabled = !signedIn;
        _signOut.IsEnabled = signedIn;
        _signIn.Visibility = signedIn ? Visibility.Collapsed : Visibility.Visible;
        _signOut.Visibility = signedIn ? Visibility.Visible : Visibility.Collapsed;
    }

    private async Task SignInAsync()
    {
        var response = await Call(NxMethods.AuthLogin);
        if (!response.Ok || response.Result is not { } result)
        {
            Notify(response.Error ?? "Could not start sign-in.", true);
            return;
        }
        var url = NxJson.Text(result, "url");
        if (url.Length == 0)
        {
            Notify("Sign-in did not return a browser URL.", true);
            return;
        }
        try
        {
            Process.Start(new ProcessStartInfo(url) { UseShellExecute = true });
            Notify("Complete sign-in in your browser, then return to Nexus.");
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("auth", "could not open the sign-in page", ex);
            Notify(NxErrors.ForException("Nexus couldn't open your browser. Try again.", ex, "settings"), true);
        }
    }

    private async Task SignOutAsync()
    {
        var confirm = new ContentDialog
        {
            Title = "Sign out of Nexus?",
            Content = new TextBlock
            {
                Text = "This device stops syncing. Your cloud sessions, memories, and captured history stay in your account.",
                TextWrapping = TextWrapping.Wrap
            },
            PrimaryButtonText = "Sign out",
            CloseButtonText = "Cancel",
            XamlRoot = XamlRoot,
            DefaultButton = ContentDialogButton.Close
        };
        if (await confirm.ShowThemedAsync() != ContentDialogResult.Primary) return;
        var response = await Call(NxMethods.AuthLogout);
        if (!response.Ok)
        {
            Notify(response.Error ?? "Could not sign out.", true);
            return;
        }
        await Task.Run(() => App.InitializeCore());
        if (MainWindow.Current is { } window) await window.RefreshBannerAsync();
        await LoadAccountSectionAsync();
        InvalidateSection("organizations");
        InvalidateSection("agents");
        Notify("Signed out. Local capture remains available.");
    }

    // ---- Workspaces & capture

    private UIElement BuildWorkspacesSection()
    {
        var refresh = new Button { Content = "Refresh" };
        AutomationProperties.SetName(refresh, "Refresh monitoring status");
        refresh.Click += async (_, _) => await LoadSectionAsync("workspaces", force: true);
        var monitoring = SettingsGroup("Monitoring & privacy",
            SettingRow(RowText("Approved folders", "Nexus captures agent history only for workspaces you approve. Add or remove folders, and review the sources found in each."), _manage),
            SettingRow(RowText("Capture", _capture), Row(_pause, refresh)));
        _workspaces.Children.Add(NoteRow("Loading workspaces…"));
        var workspaces = SettingsGroup("Approved workspaces", _workspaces);
        var device = SettingsGroup("This device",
            Disclosure("Agent history sources on this device", "Folders where installed agents keep their chat history.", _sources),
            Disclosure("Sync per project", "Choose, for each project, whether its chats upload to your account and feed its memories. The folders above still decide what Nexus reads.", _projects, LoadProjectsAsync));
        return new StackPanel { Children = { monitoring, workspaces, device } };
    }

    private async Task RefreshCaptureAsync()
    {
        if (_loading) return;
        _loading = true;
        try
        {
            var response = await Call(NxMethods.StatusGet);
            if (!response.Ok || response.Result is not { } status) { Notify(response.Error ?? "Status unavailable.", true); return; }
            _connection.Text = NxJson.Text(status, "online") == "true" ? "Connected to your cloud account." : "Cloud sync is offline. Sign in or check your connection.";
            var capture = status.TryGetProperty("capture", out var item) ? item : default;
            _approved.Clear();
            _suggested.Clear();
            ReadPaths(capture, "approved_workspaces", _approved);
            ReadPaths(capture, "suggested_workspaces", _suggested);
            CaptureSetup.ReadDiscovered(capture);
            _captureLoaded = true;
            _manage.IsEnabled = true;
            ToolTipService.SetToolTip(_manage, null);
            _paused = NxJson.Text(capture, "paused") == "true";
            _pause.Content = _paused ? "Resume capture" : "Pause capture";
            _pause.IsEnabled = _approved.Count > 0;
            _capture.Text = _approved.Count == 0
                ? "Capture is off. Choose a folder to get started."
                : CaptureSummary(capture);
            _workspaces.Children.Clear();
            _sources.Children.Clear();
            if (capture.ValueKind == JsonValueKind.Object && capture.TryGetProperty("sources", out var sourceItems) && sourceItems.ValueKind == JsonValueKind.Array)
                foreach (var source in sourceItems.EnumerateArray())
                    _sources.Children.Add(SettingRow(RowText(NxJson.Text(source, "agent"), NxJson.Text(source, "path"))));
            if (_sources.Children.Count == 0) _sources.Children.Add(NoteRow("No local agent history directories were detected. Install a supported agent, then refresh."));
            var shown = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
            if (capture.ValueKind == JsonValueKind.Object && capture.TryGetProperty("workspace_details", out var details) && details.ValueKind == JsonValueKind.Array)
            {
                foreach (var workspace in details.EnumerateArray())
                {
                    var path = NxJson.Text(workspace, "path");
                    shown.Add(path);
                    _workspaces.Children.Add(WorkspaceRow(workspace, path));
                }
            }
            foreach (var stale in _agentFileRows.Keys.Where(k => !shown.Contains(k)).ToList()) _agentFileRows.Remove(stale);
            if (_workspaces.Children.Count == 0)
                _workspaces.Children.Add(NoteRow(_approved.Count == 0
                    ? "No folders approved yet. Choose Manage workspaces to add one."
                    : "Waiting for the first scan of your approved folders…"));
        }
        finally { _loading = false; }
    }

    // One wording for capture everywhere (this section, Advanced, Home).
    internal static string CaptureSummary(JsonElement capture)
    {
        var folders = capture.ValueKind == JsonValueKind.Object && capture.TryGetProperty("approved_workspaces", out var list) && list.ValueKind == JsonValueKind.Array
            ? list.GetArrayLength() : 0;
        if (folders == 0) return "Capture is off (no folders chosen)";
        return NxJson.Text(capture, "paused") == "true"
            ? $"Capture is paused · {Nexus.Core.FriendlyTime.Count(folders, "folder")}"
            : $"Capturing {Nexus.Core.FriendlyTime.Count(folders, "folder")}";
    }

    private Border WorkspaceRow(JsonElement workspace, string path)
    {
        var name = Path.GetFileName(path.TrimEnd('\\', '/'));
        if (name.Length == 0) name = "Workspace";
        var state = NxJson.Text(workspace, "state");
        var sources = new List<string>();
        if (workspace.TryGetProperty("agent_files", out var agents) && agents.ValueKind == JsonValueKind.Object)
            foreach (var agent in agents.EnumerateObject())
                if (agent.Value.TryGetInt32(out var count) && count > 0) sources.Add(agent.Name + " · " + count + " history files");
        var last = NxJson.Text(workspace, "last_scan_at");
        if (DateTimeOffset.TryParse(last, out var scanned) && scanned.Year > 1) last = scanned.ToLocalTime().ToString("MMM d · h:mm:ss tt");

        var title = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        title.Children.Add(RowLabel(name));
        if (state.Length > 0) title.Children.Add(Chip(state));
        var text = new StackPanel { Spacing = 2 };
        text.Children.Add(title);
        text.Children.Add(Muted(path));
        text.Children.Add(new TextBlock
        {
            Text = NxJson.Scrub(sources.Count > 0 ? string.Join("   /   ", sources) : "Looking for agent history in this folder…"),
            FontSize = 12, Opacity = 0.85, TextWrapping = TextWrapping.Wrap
        });
        text.Children.Add(Muted(last.Length > 0 && !last.StartsWith("0001") ? "Last scan · " + last
            : sources.Count > 0 ? "Importing agent history… the first import can take a while; progress is kept if you close Nexus."
            : "First scan in progress."));
        if (NxJson.Text(workspace, "error") is { Length: > 0 } problem)
            text.Children.Add(new TextBlock { Text = NxJson.Scrub(problem), FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });

        var remove = new Button { Content = "Stop monitoring" };
        AutomationProperties.SetName(remove, "Stop monitoring " + name);
        remove.Click += async (_, _) => await RemoveAsync(path);
        return SettingRow(text, remove, AgentFilesFor(path));
    }

    private UIElement AgentFilesFor(string path)
    {
        if (_agentFileRows.TryGetValue(path, out var row))
        {
            if (row is FrameworkElement { Parent: Panel parent }) parent.Children.Remove(row);
            return row;
        }
        row = AgentFilesRow(path);
        _agentFileRows[path] = row;
        return row;
    }

    internal static void ReadPaths(JsonElement value, string key, List<string> destination)
    {
        if (value.ValueKind == JsonValueKind.Object && value.TryGetProperty(key, out var array) && array.ValueKind == JsonValueKind.Array)
            foreach (var path in array.EnumerateArray())
                if (path.ValueKind == JsonValueKind.String && path.GetString() is { Length: > 0 } text && !destination.Contains(text, StringComparer.OrdinalIgnoreCase)) destination.Add(text);
    }

    private async Task RemoveAsync(string path)
    {
        var confirm = new ContentDialog
        {
            Title = "Stop monitoring this workspace?",
            Content = new TextBlock { Text = path + "\n\nFuture capture stops for this folder. Existing chats and memories stay in your account.", TextWrapping = TextWrapping.Wrap },
            PrimaryButtonText = "Stop monitoring", CloseButtonText = "Cancel", XamlRoot = XamlRoot,
            DefaultButton = ContentDialogButton.Close
        };
        if (await confirm.ShowThemedAsync() != ContentDialogResult.Primary) return;
        var result = await Call(NxMethods.CaptureConfigure, new { workspaces = _approved.Where(p => !p.Equals(path, StringComparison.OrdinalIgnoreCase)).ToArray(), confirm = true });
        Notify(result.Ok ? "Workspace monitoring stopped." : result.Error ?? "Could not stop monitoring.", !result.Ok);
        await RefreshCaptureAsync();
    }

    private async Task LoadProjectsAsync()
    {
        _projects.Children.Clear();
        _projects.Children.Add(NoteRow("Loading project policies…"));
        var response = await Call(NxMethods.ProjectsList);
        _projects.Children.Clear();
        _connection.Text = response.Ok ? "Cloud API reachable. Captured sessions sync to your signed-in account." : response.Error ?? "Cloud sync is unavailable. Check your connection and sign-in.";
        if (!response.Ok) { _projects.Children.Add(NoteRow(response.Error ?? "Project policies unavailable.")); return; }
        using var limit = new SemaphoreSlim(4);
        var pending = NxJson.Items(response.Result).Select(async project =>
        {
            await limit.WaitAsync();
            try
            {
                var policy = await Core.CallAsync(NxMethods.ProjectsCaptureGet, new { project_id = NxJson.Text(project, "id", "project_id") });
                return (Project: project, Policy: policy);
            }
            finally { limit.Release(); }
        }).ToArray();
        var policies = await Task.WhenAll(pending);
        await UiThread.Resume(DispatcherQueue);
        foreach (var (project, policy) in policies)
        {
            var id = NxJson.Text(project, "id", "project_id");
            if (id.Length == 0) continue;
            var label = NxJson.Text(project, "display_name", "folder_name", "name");
            if (label.Length == 0) label = "Project";
            var enabled = policy.Ok && policy.Result is { } p && NxJson.Text(p, "enabled") == "true";
            var toggle = new ToggleSwitch { IsOn = enabled, IsEnabled = policy.Ok, OnContent = "Capture allowed", OffContent = "Capture blocked" };
            AutomationProperties.SetName(toggle, "Cloud capture for " + label);
            if (!policy.Ok) ToolTipService.SetToolTip(toggle, policy.Error ?? "Policy unavailable");
            var suppress = false;
            toggle.Toggled += async (_, _) =>
            {
                if (suppress) return;
                toggle.IsEnabled = false;
                var result = await Call(NxMethods.ProjectsCaptureSet, new { project_id = id, enabled = toggle.IsOn });
                if (!result.Ok)
                {
                    suppress = true; toggle.IsOn = !toggle.IsOn; suppress = false;
                    Notify(result.Error ?? "Could not update policy.", true);
                }
                toggle.IsEnabled = true;
            };
            _projects.Children.Add(SettingRow(RowText(label), toggle));
        }
        if (_projects.Children.Count == 0) _projects.Children.Add(NoteRow("No cloud projects yet."));
    }
}
