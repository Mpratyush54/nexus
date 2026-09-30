using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class MemoryPage : NexusPage
{
    private readonly TextBox _query = new() { PlaceholderText = "Search memory", Width = 280 };
    private readonly ComboBox _scope = new() { Width = 180 };
    private readonly StackPanel _cards = new();

    public MemoryPage()
    {
        foreach (var scope in new[] { "project", "session", "personal", "organization" })
        {
            _scope.Items.Add(scope);
        }
        _scope.SelectedIndex = 0;
        var search = new Button { Content = "Search" };
        search.Click += async (_, _) => await LoadAsync();
        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var filters = Row(_query, _scope, search);
        var scroller = new ScrollViewer { Content = _cards };
        Grid.SetRow(filters, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(Line("Memory", title: true));
        root.Children.Add(filters);
        root.Children.Add(scroller);
        Content = root;
    }

    protected override void OnNavigatedTo(Microsoft.UI.Xaml.Navigation.NavigationEventArgs e)
    {
        if (e.Parameter is string query)
        {
            _query.Text = query;
        }
        _ = LoadAsync();
    }

    private async Task LoadAsync()
    {
        var scope = _scope.SelectedItem as string ?? "project";
        var response = await Call(NxMethods.MemorySearch, new { q = _query.Text, scope });
        _cards.Children.Clear();
        if (!response.Ok)
        {
            ShowError(_cards, response);
            return;
        }
        foreach (var item in NxJson.Items(response.Result))
        {
            var id = NxJson.Text(item, "id");
            var text = NxJson.Text(item, "text", "content", "title", "summary");
            var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
            var pin = CloudAction("Pin", () => _ = Mutate(NxMethods.MemoryPin, id));
            var forget = CloudAction("Forget", () => _ = Mutate(NxMethods.MemoryForget, id));
            var project = CloudAction("Project", () => _ = Scope(id, "project"));
            var session = CloudAction("Session", () => _ = Scope(id, "session"));
            var personal = CloudAction("Personal", () => _ = Scope(id, "personal"));
            var org = CloudAction("Organization", () => _ = Scope(id, "organization"));
            _cards.Children.Add(Card(
                Line(text.Length > 0 ? text : id, title: true),
                Line(status),
                Row(pin, forget, project, session, personal, org)));
        }
        if (_cards.Children.Count == 0)
        {
            _cards.Children.Add(Line(response.Result is { } raw && raw.ValueKind != JsonValueKind.Array
                ? raw.GetRawText()
                : "No active memories."));
        }
    }

    private async Task Mutate(string method, string id)
    {
        var response = await Call(method, new { id });
        if (!response.Ok)
        {
            _cards.Children.Insert(0, Line(response.Error ?? "Memory update failed"));
            return;
        }
        await LoadAsync();
    }

    private async Task Scope(string id, string scope)
    {
        var response = await Call(NxMethods.MemoryScope, new { id, scope });
        if (!response.Ok)
        {
            _cards.Children.Insert(0, Line(response.Error ?? "Scope change failed"));
            return;
        }
        await LoadAsync();
    }

    private Button CloudAction(string label, Action click)
    {
        var button = Action(label, click);
        if (ShellState.Current.Offline)
        {
            button.IsEnabled = false;
            ToolTipService.SetToolTip(button, "Available when online");
        }
        return button;
    }
}
