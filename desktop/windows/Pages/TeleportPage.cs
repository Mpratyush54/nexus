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
    private readonly bool _hubChrome;
    private readonly Grid _columns = new() { ColumnSpacing = 20, RowSpacing = 16 };
    private readonly TextBlock _detailTitle = new() { Text = "Review shared chat", FontSize = 20, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextWrapping = TextWrapping.Wrap };
    private readonly TextBlock _count = new() { Opacity = 0.65, FontSize = 12 };
    private readonly Button _inboxTab = new() { Content = "Received", MinWidth = 108 };
    private readonly Button _sentTab = new() { Content = "Sent", MinWidth = 86 };
    private ScrollViewer _detailView = null!;
    private string _selectedPreview = "";
    private int _loadVersion;

    public TeleportPage(bool hubChrome = false)
    {
        _hubChrome = hubChrome;
        var inbox = _inboxTab;
        inbox.Click += async (_, _) => await LoadAsync(NxMethods.TeleportInbox);
        var sent = _sentTab;
        sent.Click += async (_, _) => await LoadAsync(NxMethods.TeleportSent);
        _revoke = new Button { Content = "Cancel transfer" };
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

        var columns = _columns;
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        columns.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        columns.RowDefinitions.Add(new RowDefinition { Height = new GridLength(0) });
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var list = new ScrollViewer
        {
            Content = _list,
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        list.SetValue(FrameworkElement.MinHeightProperty, 0.0);
        _detail.Children.Add(_detailTitle);
        _detail.Children.Add(_preview);
        _detail.Children.Add(Row(_prepare, _apply, _open));
        _detail.Children.Add(_restore);
        _detail.Children.Add(_revoke);
        var detail = new ScrollViewer
        {
            Content = Card(_detail),
            VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        _detailView = detail;
        _detailView.Visibility = Visibility.Collapsed;
        detail.SetValue(FrameworkElement.MinHeightProperty, 0.0);
        Grid.SetColumn(detail, 1);
        columns.Children.Add(list);
        columns.Children.Add(detail);
        columns.SizeChanged += (_, _) => UpdateLayoutMode();

        var root = new Grid
        {
            Padding = _hubChrome ? new Thickness(0) : new Thickness(32, 24, 32, 24),
            RowSpacing = 14,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        var row = 0;
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 0 });
        var tabs = Row(inbox, sent, _count);
        if (!_hubChrome)
        {
            var header = new StackPanel { Spacing = 4 };
            header.Children.Add(NexusTokens.PageTitle("Shared with you"));
            header.Children.Add(NexusTokens.Caption("Chats and code your teammates send you. Review one, then continue it here or bring its code into your folder."));
            Grid.SetRow(header, row++);
            root.Children.Add(header);
        }
        columns.VerticalAlignment = VerticalAlignment.Stretch;
        Grid.SetRow(tabs, row++);
        Grid.SetRow(columns, row);
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
        var version = ++_loadVersion;
        _inboxTab.Opacity = _sent ? 0.6 : 1;
        _sentTab.Opacity = _sent ? 1 : 0.6;
        _inboxTab.FontWeight = !_sent ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
        _sentTab.FontWeight = _sent ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
        _selected = "";
        _sessionId = "";
        _status = "";
        _codeFiles = 0;
        _preview.Text = "Select a transfer to review its redacted preview.";
        UpdateActions();
        UpdateLayoutMode();
        _count.Text = "";
        _list.Children.Clear();
        _list.Children.Add(Skeleton.Cards(2));
        var response = await Call(method);
        if (version != _loadVersion) return;
        _list.Children.Clear();
        if (!response.Ok)
        {
            _count.Text = "";
            _list.Children.Add(StateView.Error("Couldn't load shared chats", response.Error, () => _ = LoadAsync(method, requestedID)));
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        _count.Text = items.Count == 0 ? "" : items.Count + (items.Count == 1 ? " chat" : " chats");
        if (method == NxMethods.TeleportInbox)
        {
            MainWindow.Current?.SetInboxCount(PendingCount(items, response));
        }
        foreach (var item in items)
        {
            var id = NxJson.Text(item, "id", "teleport_id");
            var preview = NxJson.Text(item, "preview", "note", "summary");
            var sessionId = NxJson.Text(item, "session_id", "sessionId");
            var status = NxJson.Text(item, "status");
            var date = DateTimeOffset.TryParse(NxJson.Text(item, "created_at"), out var at) ? at.ToLocalTime().ToString("MMM d · h:mm tt") : "";
            var label = NxJson.Text(item, "title", "session_title");
            var contents = new StackPanel { Spacing = 8 };
            contents.Children.Add(Line(label.Length > 0 ? label : "Shared conversation", title: true));
            if (preview.Length > 0) contents.Children.Add(new TextBlock { Text = NxJson.Scrub(preview), TextWrapping = TextWrapping.Wrap, MaxLines = 3, TextTrimming = TextTrimming.CharacterEllipsis, Opacity = 0.8 });
            contents.Children.Add(Row(Chip(status.Length > 0 ? status : "new"), Muted(date)));
            var open = new Button { Content = contents, Padding = new Thickness(18), HorizontalAlignment = HorizontalAlignment.Stretch, HorizontalContentAlignment = HorizontalAlignment.Stretch, CornerRadius = new CornerRadius(12) };
            open.Click += async (_, _) => await SelectAsync(id, sessionId, preview, status);
            _list.Children.Add(open);
            if (id.Length > 0 && id == requestedID)
            {
                await SelectAsync(id, sessionId, preview, status);
            }
        }
        if (items.Count == 0)
        {
            _list.Children.Add(_sent
                ? StateView.Empty("", "Nothing sent yet", "To send a chat, open it and choose Share, then pick a teammate. They get a copy with its code.")
                : StateView.Empty("", "You're all caught up", "When a teammate sends you a chat, it shows up here. You review it before any code touches your folder."));
        }
    }

    private async Task SelectAsync(string id, string sessionId, string preview, string status)
    {
        _selected = id;
        _selectedPreview = preview;
        _detailTitle.Text = _sent ? "Sent conversation" : "Review shared conversation";
        _sessionId = sessionId;
        _status = status;
        _codeFiles = 0;
        _preview.Text = NxJson.Scrub(preview) + "\nChecking included code…";
        UpdateActions();
        UpdateLayoutMode();
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
        var closed = _status.Equals("revoked", StringComparison.OrdinalIgnoreCase) || _status.Equals("expired", StringComparison.OrdinalIgnoreCase);
        _apply.IsEnabled = _selected.Length > 0 && !_sent && !closed && !_status.Equals("accepted", StringComparison.OrdinalIgnoreCase) && !ShellState.Current.Offline;
        _restore.IsEnabled = _selected.Length > 0 && !_sent && _status.Equals("accepted", StringComparison.OrdinalIgnoreCase) && _codeFiles > 0 && !ShellState.Current.Offline;
        _revoke.IsEnabled = _selected.Length > 0 && _sent && !closed && !ShellState.Current.Offline;
        _open.IsEnabled = _sessionId.Length > 0 && (_sent || _status.Equals("accepted", StringComparison.OrdinalIgnoreCase));
        _apply.Visibility = !_sent && !closed && !_status.Equals("accepted", StringComparison.OrdinalIgnoreCase) ? Visibility.Visible : Visibility.Collapsed;
        _open.Visibility = _open.IsEnabled ? Visibility.Visible : Visibility.Collapsed;
        _restore.Visibility = _codeFiles > 0 && !_sent ? Visibility.Visible : Visibility.Collapsed;
        _revoke.Visibility = _sent ? Visibility.Visible : Visibility.Collapsed;
    }

    private void UpdateLayoutMode()
    {
        if (_detailView is null || _columns.ColumnDefinitions.Count < 2) return;
        var selected = _selected.Length > 0;
        var narrow = _columns.ActualWidth < 720;
        _detailView.Visibility = selected ? Visibility.Visible : Visibility.Collapsed;
        _columns.ColumnDefinitions[0].Width = new GridLength(1, GridUnitType.Star);
        _columns.ColumnDefinitions[1].Width = selected && !narrow ? new GridLength(1.2, GridUnitType.Star) : new GridLength(0);
        _columns.RowDefinitions[1].Height = selected && narrow ? new GridLength(1, GridUnitType.Star) : new GridLength(0);
        Grid.SetColumn(_detailView, narrow ? 0 : 1);
        Grid.SetRow(_detailView, narrow ? 1 : 0);
    }

    private void HandleShellChanged(object? sender, EventArgs e) => DispatcherQueue?.TryEnqueue(UpdateActions);

    private async Task PrepareAsync()
    {
        if (_selected.Length > 0) await SelectAsync(_selected, _sessionId, _selectedPreview, _status);
    }

    private async Task RestoreAsync()
    {
        if (_sessionId.Length == 0 || _codeFiles == 0) return;
        var parent = Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.MyDocuments), "Nexus Restores");
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

    private static int PendingCount(List<JsonElement> items, NxResponse response)
    {
        var text = response.Result is { } result ? NxJson.Text(result, "pending") : "";
        if (int.TryParse(text, out var count))
        {
            return count;
        }
        return items.Count(item => IsPending(NxJson.Text(item, "status")));
    }

    private static bool IsPending(string status) =>
        !status.Equals("accepted", StringComparison.OrdinalIgnoreCase) &&
        !status.Equals("revoked", StringComparison.OrdinalIgnoreCase) &&
        !status.Equals("expired", StringComparison.OrdinalIgnoreCase);

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
