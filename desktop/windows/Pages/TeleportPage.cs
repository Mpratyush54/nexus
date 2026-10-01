using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class TeleportPage : NexusPage
{
    private readonly StackPanel _list = new() { Spacing = 8 };
    private readonly StackPanel _detail = new() { Spacing = 10 };
    private readonly TextBlock _subtitle = Muted("Reviewed session handoffs between your Nexus devices.");
    private string _selected = "";
    private string _selectedSession = "";
    private bool _inbox = true;

    public TeleportPage()
    {
        var inbox = new Button { Content = "Inbox" };
        inbox.Click += async (_, _) => await LoadAsync(true);
        var sent = new Button { Content = "Sent" };
        sent.Click += async (_, _) => await LoadAsync(false);

        var header = new StackPanel { Spacing = 4 };
        header.Children.Add(PageHeading("Teleport"));
        header.Children.Add(_subtitle);
        var tabs = Row(inbox, sent);

        var columns = new Grid { ColumnSpacing = 20 };
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(3, GridUnitType.Star) });
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(2, GridUnitType.Star) });
        var list = new ScrollViewer { Content = _list };
        var detail = new ScrollViewer { Content = _detail };
        Grid.SetColumn(detail, 1);
        columns.Children.Add(list);
        columns.Children.Add(detail);

        var root = new Grid { Padding = new Thickness(28, 24, 28, 24), RowSpacing = 16 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(tabs, 1);
        Grid.SetRow(columns, 2);
        root.Children.Add(header);
        root.Children.Add(tabs);
        root.Children.Add(columns);
        Content = root;
        ShowNoSelection();
    }

    protected override void OnEnter(object? parameter)
    {
        if (parameter is string id)
        {
            _selected = id;
        }
        _ = LoadAsync(true);
    }

    private async Task LoadAsync(bool inbox)
    {
        _inbox = inbox;
        _subtitle.Text = inbox
            ? "Sessions another person sent to you. Review a transfer before continuing it here."
            : "Sessions you sent to another device or teammate.";
        var response = await Call(inbox ? NxMethods.TeleportInbox : NxMethods.TeleportSent);
        _list.Children.Clear();
        if (!response.Ok)
        {
            if (response.Error?.Contains("not signed in", StringComparison.OrdinalIgnoreCase) == true)
            {
                _list.Children.Add(EmptyState(
                    "Sign in to use Teleport",
                    "Teleport moves a reviewed session context between your Nexus devices.",
                    Action("Open Settings", () => MainWindow.Current?.ShowSettings())));
                return;
            }
            ShowError(_list, response);
            return;
        }

        var items = NxJson.Items(response.Result).ToList();
        if (inbox)
        {
            MainWindow.Current?.SetInboxCount(items.Count);
        }
        if (items.Count == 0)
        {
            _list.Children.Add(EmptyState(
                inbox ? "Nothing in your inbox" : "No sent transfers",
                inbox
                    ? "When someone teleports a session to you, it will appear here for review."
                    : "Choose Teleport from a session when you want to move it to another device."));
            ShowNoSelection();
            return;
        }

        foreach (var item in items)
        {
            var id = NxJson.Text(item, "id", "teleport_id");
            var session = NxJson.Text(item, "session_id", "session");
            var preview = NxJson.Text(item, "preview", "note", "summary");
            var status = NxJson.Text(item, "status");
            var review = Action("Review", () => Select(id, session, preview, status));
            _list.Children.Add(Card(
                Line(preview.Length > 0 ? preview : "Session transfer", title: true),
                Line(status.Length > 0 ? status : "Ready for review"),
                Row(review)));
            if (id.Length > 0 && id == _selected)
            {
                Select(id, session, preview, status);
            }
        }
    }

    private void Select(string id, string session, string preview, string status)
    {
        _selected = id;
        _selectedSession = session;
        _detail.Children.Clear();
        _detail.Children.Add(Card(
            Line(_inbox ? "Review incoming transfer" : "Sent transfer", title: true),
            Line(preview.Length > 0 ? preview : "No preview was provided."),
            Line(status.Length > 0 ? status : "Ready")));

        var primary = _inbox
            ? Action("Continue here", () => _ = RunAsync(NxMethods.TeleportApply))
            : Action("Revoke transfer", () => _ = RunAsync(NxMethods.TeleportRevoke));
        primary.IsEnabled = !ShellState.Current.Offline;
        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        actions.Children.Add(primary);
        if (_selectedSession.Length > 0)
        {
            actions.Children.Add(Action("Open session", () => MainWindow.Current?.OpenSession(_selectedSession)));
        }
        _detail.Children.Add(actions);
    }

    private void ShowNoSelection()
    {
        _detail.Children.Clear();
        _detail.Children.Add(EmptyState(
            "Select a transfer",
            "Its redacted preview and the available next step will appear here."));
    }

    private async Task RunAsync(string method)
    {
        if (_selected.Length == 0)
        {
            return;
        }
        var response = await Call(method, new { id = _selected });
        if (!response.Ok)
        {
            _detail.Children.Add(Line(response.Error ?? "Transfer update failed"));
            return;
        }
        await LoadAsync(_inbox);
    }
}
