using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using System.Text.Json;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class SettingsPage : NexusPage
{
    private readonly StackPanel _body = new() { Spacing = 8 };
    private readonly ToggleSwitch _login = new() { Header = "Start at login", OnContent = "On", OffContent = "Off" };
    private readonly TextBox _workspace = new() { PlaceholderText = @"D:\path\to\project", MinWidth = 380 };
    private readonly TextBlock _captureStatus = new() { Text = "Checking capture…", TextWrapping = TextWrapping.Wrap };
    private Button _clear = null!;

    public SettingsPage()
    {
        _login.IsOn = LoginStart.IsEnabled();
        _login.Toggled += (_, _) =>
        {
            try
            {
                LoginStart.SetEnabled(_login.IsOn);
            }
            catch (Exception ex)
            {
                _body.Children.Insert(0, Line(ex.Message));
            }
        };
        _clear = new Button { Content = "Clear cache" };
        _clear.Click += async (_, _) => await Run(NxMethods.CacheClear);
        var retry = new Button { Content = "Retry uploads" };
        retry.Click += async (_, _) => await Run(NxMethods.UploadsRetry);
        var projects = new Button { Content = "Refresh projects" };
        projects.Click += async (_, _) => await Run(NxMethods.ProjectsList);
        var connect = new Button { Content = "Use workspace and reconnect" };
        connect.Click += async (_, _) => await ReconnectAsync();
        CloudOnly(retry, projects);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var actions = Row(_login, _clear, retry, projects);
        var capture = new StackPanel { Spacing = 8 };
        capture.Children.Add(Line("Capture workspace", title: true));
        capture.Children.Add(Line("Choose your main project folder. Nexus also maps local Codex chats to their existing Git workspaces and captures them under the right project."));
        capture.Children.Add(Row(_workspace, connect));
        capture.Children.Add(_captureStatus);
        var scroller = new ScrollViewer { Content = _body };
        var settings = new StackPanel { Spacing = 14 };
        settings.Children.Add(capture);
        settings.Children.Add(actions);
        Grid.SetRow(settings, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(Line("Settings", title: true));
        root.Children.Add(settings);
        root.Children.Add(scroller);
        Content = root;
        Loaded += async (_, _) =>
        {
            await RefreshStatusAsync();
            await Run(NxMethods.DiagnosticsGet);
            await Run(NxMethods.AuthStatus);
            _body.Children.Add(Line("Sign-in returns through nexus://auth/callback. Tokens stay in the core."));
        };
    }

    private async Task RefreshStatusAsync()
    {
        var response = await Call(NxMethods.StatusGet);
        if (!response.Ok || response.Result is not { } result)
        {
            _captureStatus.Text = response.Error ?? "Capture status unavailable";
            return;
        }
        var root = NxJson.Text(result, "workspace_root");
        if (_workspace.Text.Length == 0) _workspace.Text = root;
        var capture = result.TryGetProperty("capture", out var value) ? value : default;
        var configured = NxJson.Text(capture, "configured") == "true";
        var running = NxJson.Text(capture, "running") == "true";
        var paused = NxJson.Text(capture, "paused") == "true";
        var online = NxJson.Text(result, "online") == "true";
        _captureStatus.Text = root.Length == 0
            ? "No workspace selected. Local chats cannot be captured until you choose a project folder."
            : $"{(running ? "Capturing" : configured ? "Not running" : "Not configured")} · {(paused ? "paused" : "active")} · {(online ? "cloud connected" : "cloud offline")}\n{root}\n" +
              $"Workspaces: {NxJson.Text(capture, "workspaces")} · last scan: {NxJson.Text(capture, "last_scan_at")} · matched files: {NxJson.Text(capture, "matched_files")} · new turns: {NxJson.Text(capture, "new_turns_last_scan")}" +
              (NxJson.Text(capture, "error", "last_scan_error") is { Length: > 0 } issue ? $"\nNeeds attention: {issue}" : "");
        if (capture.ValueKind == JsonValueKind.Object && capture.TryGetProperty("workspace_errors", out var errors) && errors.ValueKind == JsonValueKind.Object)
        {
            foreach (var workspace in errors.EnumerateObject())
            {
                _captureStatus.Text += $"\n{workspace.Name}: {workspace.Value.GetString()}";
            }
        }
    }

    private async Task ReconnectAsync()
    {
        var root = _workspace.Text.Trim();
        if (root.Length == 0 || !Directory.Exists(root))
        {
            _captureStatus.Text = "Select an existing project folder first.";
            return;
        }
        _captureStatus.Text = "Connecting and starting capture…";
        var config = JsonSerializer.Serialize(new { online = true, workspace_root = root });
        var connected = await Task.Run(() => App.Core.Init(config));
        await UiThread.Resume(DispatcherQueue);
        _captureStatus.Text = connected ? "Workspace saved. Checking capture…" : "Could not initialize Nexus core.";
        if (connected)
        {
            await RefreshStatusAsync();
            if (MainWindow.Current is { } window) await window.RefreshBannerAsync();
        }
    }

    private async Task Run(string method)
    {
        var response = await Call(method);
        _body.Children.Insert(0, Line(response.Ok
            ? method + " " + NxJson.Scrub(response.Result?.ToString() ?? "ok")
            : method + " " + (response.Error ?? "failed")));
    }
}
