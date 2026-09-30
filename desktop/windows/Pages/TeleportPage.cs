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
    private Button _revoke = null!;
    private Button _prepare = null!;
    private Button _apply = null!;

    public TeleportPage()
    {
        var inbox = new Button { Content = "Inbox" };
        inbox.Click += async (_, _) => await LoadAsync(NxMethods.TeleportInbox);
        var sent = new Button { Content = "Sent" };
        sent.Click += async (_, _) => await LoadAsync(NxMethods.TeleportSent);
        _revoke = new Button { Content = "Revoke" };
        _revoke.Click += async (_, _) => await Run(NxMethods.TeleportRevoke);
        _prepare = new Button { Content = "Prepare" };
        _prepare.Click += async (_, _) => await PrepareAsync();
        _apply = new Button { Content = "Continue here" };
        _apply.Click += async (_, _) => await Run(NxMethods.TeleportApply);
        var open = new Button { Content = "Open in agent" };
        open.Click += async (_, _) =>
        {
            if (_selected.Length > 0)
            {
                MainWindow.Current?.OpenSession(_selected);
            }
        };
        CloudOnly(_revoke, _prepare, _apply);

        var columns = new Grid { ColumnSpacing = 16 };
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        columns.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var list = new ScrollViewer { Content = _list };
        _detail.Children.Add(Line("Preview", title: true));
        _detail.Children.Add(_preview);
        _detail.Children.Add(Row(_prepare, _apply, open, _revoke));
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

    protected override void OnNavigatedTo(Microsoft.UI.Xaml.Navigation.NavigationEventArgs e)
    {
        if (e.Parameter is string id)
        {
            _selected = id;
        }
        _ = LoadAsync(NxMethods.TeleportInbox);
    }

    private async Task LoadAsync(string method)
    {
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
            var open = Action("Preview", () => Select(id, preview));
            _list.Children.Add(Card(Line(preview.Length > 0 ? preview : id, title: true), Line(id), open));
            if (id.Length > 0 && id == _selected)
            {
                Select(id, preview);
            }
        }
        if (items.Count == 0)
        {
            _list.Children.Add(Line("No teleports."));
        }
    }

    private void Select(string id, string preview)
    {
        _selected = id;
        _preview.Text = NxJson.Scrub(preview);
    }

    private async Task PrepareAsync()
    {
        var response = await Call(NxMethods.TeleportPrepare, new { id = _selected, text = _preview.Text });
        _preview.Text = response.Ok
            ? NxJson.Scrub(response.Result?.ToString() ?? "")
            : response.Error ?? "Prepare failed";
    }

    private async Task Run(string method)
    {
        var response = await Call(method, new { id = _selected });
        _preview.Text = response.Ok ? "Done" : response.Error ?? "Request failed";
        if (response.Ok && method == NxMethods.TeleportRevoke)
        {
            await LoadAsync(NxMethods.TeleportInbox);
        }
    }
}
