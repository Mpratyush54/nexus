using System.IO;
using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class TeleportPage : NexusPage
{
    private readonly StackPanel _list = new() { Spacing = 6 };
    private readonly StackPanel _detail = new() { Spacing = 8 };
    private readonly TextBlock _preview = new() { TextWrapping = TextWrapping.Wrap };
    private string _selected = "";
    private string _sessionId = "";
    private string _status = "";
    private int _codeFiles;
    private Button _revoke = null!;
    private Button _prepare = null!;
    private Button _apply = null!;
    private Button _open = null!;
    private Button _restore = null!;
    private bool _sent;

    public TeleportPage()
    {
        var inbox = new Button { Content = "Inbox" };
        inbox.Click += async (_, _) => await LoadAsync(NxMethods.TeleportInbox);
        var sent = new Button { Content = "Sent" };
        sent.Click += async (_, _) => await LoadAsync(NxMethods.TeleportSent);
        _revoke = new Button { Content = "Revoke" };
        _revoke.Click += async (_, _) => await Run(NxMethods.TeleportRevoke);
        _prepare = new Button { Content = "Review contents" };
        _prepare.Click += async (_, _) => await PrepareAsync();
        _apply = new Button { Content = "Accept transfer" };
        _apply.Click += async (_, _) => await Run(NxMethods.TeleportApply);
        _restore = new Button { Content = "Restore code to new folder" };
        _restore.Click += async (_, _) => await RestoreAsync();
        _open = new Button { Content = "View session" };
        _open.Click += (_, _) =>
        {
            if (_sessionId.Length > 0)
            {
                MainWindow.Current?.OpenSession(_sessionId);
            }
        };
        Loaded += (_, _) => ShellState.Changed += HandleShellChanged;
        Unloaded += (_, _) => ShellState.Changed -= HandleShellChanged;
        UpdateActions();

        var columns = new Grid { ColumnSpacing = 16 };
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var list = new ScrollViewer { Content = _list };
        _detail.Children.Add(Line("Selected transfer", title: true));
        _detail.Children.Add(_preview);
        _detail.Children.Add(Row(_prepare, _apply, _restore, _open, _revoke));
        var detail = new ScrollViewer { Content = Card(_detail) };
        Grid.SetColumn(detail, 1);
        columns.Children.Add(list);
        columns.Children.Add(detail);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 14 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var tabs = Row(inbox, sent);
        Grid.SetRow(tabs, 1);
        Grid.SetRow(columns, 2);
        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(Line("Teleport", title: true));
        header.Children.Add(Muted("Send a chat with its code; receive it as a verified copy. Existing workspaces are never overwritten."));
        root.Children.Add(header);
        root.Children.Add(tabs);
        root.Children.Add(columns);
        Content = root;
    }

    protected override void OnEnter(object? parameter)
    {
        _ = LoadAsync(NxMethods.TeleportInbox, parameter as string ?? "");
    }

    private async Task LoadAsync(string method, string requestedID = "")
    {
        _sent = method == NxMethods.TeleportSent;
        _selected = "";
        _sessionId = "";
        _status = "";
        _codeFiles = 0;
        _preview.Text = "Select a transfer to review its redacted preview.";
        UpdateActions();
        var response = await Call(method);
        _list.Children.Clear();
        if (!response.Ok)
        {
            ShowError(_list, response);
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        if (method == NxMethods.TeleportInbox)
        {
            MainWindow.Current?.SetInboxCount(items.Count);
        }
        foreach (var item in items)
        {
            var id = NxJson.Text(item, "id", "teleport_id");
            var preview = NxJson.Text(item, "preview", "note", "summary");
            var sessionId = NxJson.Text(item, "session_id", "sessionId");
            var status = NxJson.Text(item, "status");
            var open = Action("Review", () => _ = SelectAsync(id, sessionId, preview, status));
            _list.Children.Add(Card(Line(preview.Length > 0 ? preview : "Transferred chat", title: true), Chip(status.Length > 0 ? status : "new"), open));
            if (id.Length > 0 && id == requestedID)
            {
                await SelectAsync(id, sessionId, preview, status);
            }
        }
        if (items.Count == 0)
        {
            _list.Children.Add(Card(Line(_sent ? "No sent teleports" : "Nothing in your inbox", title: true),
                Muted(_sent ? "Open a chat and choose Send teleport to package it with code." : "Transfers sent to your account will appear here for review.")));
        }
    }

    private async Task SelectAsync(string id, string sessionId, string preview, string status)
    {
        _selected = id;
        _sessionId = sessionId;
        _status = status;
        _codeFiles = 0;
        _preview.Text = NxJson.Scrub(preview) + "\nChecking included code…";
        UpdateActions();
        if (sessionId.Length == 0) return;
        if (!_sent && !status.Equals("accepted", StringComparison.OrdinalIgnoreCase))
        {
            _preview.Text = NxJson.Scrub(preview) + "\n\nAccept this transfer to inspect its chat and code package. Accepting does not write files or run commands.";
            return;
        }
        var response = await Call(NxMethods.SessionsVersions, new { session_id = sessionId });
        if (_selected != id) return;
        if (!response.Ok)
        {
            _preview.Text = NxJson.Scrub(preview) + "\nCould not inspect package: " + response.Error;
            return;
        }
        var versions = NxJson.Items(response.Result)
            .Where(v => NxJson.Text(v, "state") is "complete" or "transcript_only")
            .OrderByDescending(v => int.TryParse(NxJson.Text(v, "version"), out var number) ? number : 0)
            .ToList();
        foreach (var version in versions)
        {
            if (version.ValueKind == JsonValueKind.Object && version.TryGetProperty("manifest", out var manifest) && manifest.ValueKind == JsonValueKind.Object
                && manifest.TryGetProperty("files", out var files) && files.ValueKind == JsonValueKind.Array && files.GetArrayLength() > 0)
            {
                _codeFiles = files.GetArrayLength();
                break;
            }
        }
        _preview.Text = NxJson.Scrub(preview) + "\n\n" + (_codeFiles > 0
            ? $"Includes {_codeFiles} code files. Known credential paths and generated dependencies may be excluded. Accept, then restore a verified copy into a new folder."
            : "Transcript only. No code package is attached, so this cannot restore a checkout.");
        UpdateActions();
    }

    private void UpdateActions()
    {
        _prepare.IsEnabled = _selected.Length > 0 && !_sent;
        _apply.IsEnabled = _selected.Length > 0 && !_sent && !_status.Equals("accepted", StringComparison.OrdinalIgnoreCase) && !ShellState.Current.Offline;
        _restore.IsEnabled = _selected.Length > 0 && !_sent && _status.Equals("accepted", StringComparison.OrdinalIgnoreCase) && _codeFiles > 0 && !ShellState.Current.Offline;
        _revoke.IsEnabled = _selected.Length > 0 && _sent && !ShellState.Current.Offline;
        _open.IsEnabled = _sessionId.Length > 0 && (_sent || _status.Equals("accepted", StringComparison.OrdinalIgnoreCase));
    }

    private void HandleShellChanged(object? sender, EventArgs e) => DispatcherQueue?.TryEnqueue(UpdateActions);

    private async Task PrepareAsync()
    {
        if (_selected.Length > 0) await SelectAsync(_selected, _sessionId, _preview.Text.Split('\n')[0], _status);
    }

    private async Task RestoreAsync()
    {
        if (_sessionId.Length == 0 || _codeFiles == 0) return;
        var parent = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.MyDocuments), "Nexus Restores");
        var folder = _sessionId[..Math.Min(8, _sessionId.Length)] + "-" + DateTime.UtcNow.ToString("yyyyMMdd-HHmmss-fff");
        var destination = Path.Combine(parent, folder);
        var dialog = new ContentDialog
        {
            Title = "Restore code copy?",
            Content = new TextBlock { Text = $"{_codeFiles} verified files will be copied to:\n{destination}\n\nNo existing files are overwritten and no commands are run.", TextWrapping = TextWrapping.Wrap },
            PrimaryButtonText = "Restore copy", CloseButtonText = "Cancel", XamlRoot = XamlRoot
        };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return;
        _preview.Text = "Downloading and verifying code…";
        var response = await Call(NxMethods.TeleportRestoreCode, new { session_id = _sessionId, dest = destination });
        _preview.Text = response.Ok ? "Code restored to " + destination + ". The chat can be viewed here. To run an agent against this copy, select it as your Nexus workspace first." : response.Error ?? "Restore failed.";
    }

    private async Task Run(string method)
    {
		if (_selected.Length == 0)
		{
			_preview.Text = "Select a teleport first.";
			return;
		}
        var response = await Call(method, new { id = _selected });
        _preview.Text = response.Ok && method == NxMethods.TeleportApply
            ? "Transfer accepted. View the chat here or restore its code to a new folder. No checkout was changed."
            : response.Ok ? "Done" : response.Error ?? "Request failed";
        if (response.Ok && method == NxMethods.TeleportApply) await SelectAsync(_selected, _sessionId, "Transfer accepted", "accepted");
        if (response.Ok && method == NxMethods.TeleportRevoke)
        {
            await LoadAsync(NxMethods.TeleportSent);
        }
    }
}
