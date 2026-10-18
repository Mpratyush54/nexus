using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class TeleportPage : NexusPage
{
    private readonly StackPanel _list = new();
    private readonly StackPanel _detail = new() { Spacing = 8 };
    private readonly TextBlock _preview = new() { TextWrapping = TextWrapping.Wrap };
    private string _selected = "";
    private string _sessionId = "";
    private Button _revoke = null!;
    private Button _prepare = null!;
    private Button _apply = null!;
    private Button _open = null!;
    private bool _sent;

    public TeleportPage()
    {
        var inbox = new Button { Content = "Inbox" };
        inbox.Click += async (_, _) => await LoadAsync(NxMethods.TeleportInbox);
        var sent = new Button { Content = "Sent" };
        sent.Click += async (_, _) => await LoadAsync(NxMethods.TeleportSent);
        _revoke = new Button { Content = "Revoke" };
        _revoke.Click += async (_, _) => await Run(NxMethods.TeleportRevoke);
        _prepare = new Button { Content = "Review restore plan" };
        _prepare.Click += async (_, _) => await PrepareAsync();
        _apply = new Button { Content = "Accept transfer" };
        _apply.Click += async (_, _) => await Run(NxMethods.TeleportApply);
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
        _detail.Children.Add(Line("Preview", title: true));
        _detail.Children.Add(_preview);
        _detail.Children.Add(Row(_prepare, _apply, _open, _revoke));
        var detail = new ScrollViewer { Content = _detail };
        Grid.SetColumn(detail, 1);
        columns.Children.Add(list);
        columns.Children.Add(detail);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var tabs = Row(inbox, sent);
        Grid.SetRow(tabs, 1);
        Grid.SetRow(columns, 2);
        root.Children.Add(Line("Teleport", title: true));
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
            var open = Action("Preview", () => Select(id, sessionId, preview));
            _list.Children.Add(Card(Line(preview.Length > 0 ? preview : id, title: true), Line(id), open));
            if (id.Length > 0 && id == requestedID)
            {
                Select(id, sessionId, preview);
            }
        }
        if (items.Count == 0)
        {
            _list.Children.Add(Card(Line(_sent ? "No sent teleports." : "Nothing in your inbox.")));
        }
    }

    private void Select(string id, string sessionId, string preview)
    {
        _selected = id;
        _sessionId = sessionId;
        _preview.Text = NxJson.Scrub(preview);
        UpdateActions();
    }

    private void UpdateActions()
    {
        _prepare.IsEnabled = _selected.Length > 0 && !_sent;
        _apply.IsEnabled = _selected.Length > 0 && !_sent && !ShellState.Current.Offline;
        _revoke.IsEnabled = _selected.Length > 0 && _sent && !ShellState.Current.Offline;
        _open.IsEnabled = _sessionId.Length > 0;
    }

    private void HandleShellChanged(object? sender, EventArgs e) => DispatcherQueue?.TryEnqueue(UpdateActions);

    private async Task PrepareAsync()
    {
		if (_selected.Length == 0 || _sessionId.Length == 0)
		{
			_preview.Text = "Select a teleport first.";
			return;
		}
        var response = await Call(NxMethods.TeleportPrepare, new { session_id = _sessionId, id = _selected, text = _preview.Text });
        if (!response.Ok || response.Result is not { } plan)
        {
            _preview.Text = response.Error ?? "Could not prepare this transfer.";
            return;
        }
        var repo = plan.TryGetProperty("repo", out var repoValue) ? repoValue : default;
        var commit = plan.TryGetProperty("commit", out var commitValue) ? commitValue : default;
        var agent = plan.TryGetProperty("agent", out var agentValue) ? agentValue : default;
        _preview.Text = "Dry-run restore plan\n" +
            "Workspace: " + NxJson.Text(repo, "path", "note") + "\n" +
            "Commit: " + NxJson.Text(commit, "action", "note") + "\n" +
            "Agent: " + NxJson.Text(agent, "name", "note") + "\n" +
            "Accepting a transfer does not modify your checkout or open a terminal.";
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
            ? "Transfer accepted. View its session, then use the resume command when ready. No checkout was changed."
            : response.Ok ? "Done" : response.Error ?? "Request failed";
        if (response.Ok && method == NxMethods.TeleportRevoke)
        {
            await LoadAsync(NxMethods.TeleportSent);
        }
    }
}
