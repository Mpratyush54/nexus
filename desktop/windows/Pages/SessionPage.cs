using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class SessionPage : NexusPage
{
    private string _id = "";
    private readonly TextBlock _header = new() { TextWrapping = TextWrapping.Wrap, FontSize = 16 };
    private readonly StackPanel _chat = new();
    private readonly StackPanel _files = new();
    private readonly StackPanel _commands = new();
    private readonly StackPanel _memory = new();
    private readonly StackPanel _summary = new();
    private readonly StackPanel _context = new() { Spacing = 8 };
    private readonly TextBox _message = new() { PlaceholderText = "Message the session", AcceptsReturn = true, Height = 80 };
    private readonly TextBox _people = new() { PlaceholderText = "Named people", Width = 220 };
    private readonly CheckBox _team = new() { Content = "Everyone in this project" };
    private readonly CheckBox _live = new() { Content = "Live", IsChecked = true };
    private readonly ComboBox _resume = new();
    private readonly ComboBox _seedAgent = new();
    private Button _send = null!;
    private Button _share = null!;
    private Button _teleport = null!;

    public SessionPage()
    {
        foreach (var mode in new[] { "native", "fork", "seeded" })
        {
            _resume.Items.Add(mode);
        }
        _resume.SelectedIndex = 0;
        foreach (var agent in AgentCatalog.Names)
        {
            _seedAgent.Items.Add(agent);
        }
        _seedAgent.SelectedIndex = 0;

        var back = new Button { Content = "Timeline" };
        back.Click += (_, _) =>
        {
            if (Frame.CanGoBack)
            {
                Frame.GoBack();
            }
        };
        var here = new Button { Content = "Here in Nexus" };
        here.Click += async (_, _) => await ContinueAsync("here", "native");
        var open = new Button { Content = "Open in agent" };
        open.Click += async (_, _) => await ContinueAsync("open_in_agent", Selected(_resume));
        var seeded = new Button { Content = "Continue in another agent" };
        seeded.Click += async (_, _) => await ContinueAsync("open_in_agent", "seeded", Selected(_seedAgent));
        _share = new Button { Content = "Share" };
        _share.Click += async (_, _) => await ShareAsync();
        _teleport = new Button { Content = "Send teleport" };
        _teleport.Click += async (_, _) => await SendTeleportAsync();
        _send = new Button { Content = "Send" };
        _send.Click += async (_, _) => await SendAsync();

        var tabs = new Pivot();
        tabs.Items.Add(Tab("Chat", _chat));
        tabs.Items.Add(Tab("Files", _files));
        tabs.Items.Add(Tab("Commands", _commands));
        tabs.Items.Add(Tab("Memory", _memory));
        tabs.Items.Add(Tab("Summary", _summary));

        var body = new Grid { ColumnSpacing = 16 };
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(2, GridUnitType.Star) });
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        Grid.SetColumn(tabs, 0);
        var context = new ScrollViewer { Content = _context };
        Grid.SetColumn(context, 1);
        body.Children.Add(tabs);
        body.Children.Add(context);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        var actions = Row(back, here, open, _resume, seeded, _seedAgent, _teleport, _share);
        Grid.SetRow(actions, 1);
        Grid.SetRow(body, 2);
        var composer = new StackPanel { Spacing = 8 };
        composer.Children.Add(_message);
        composer.Children.Add(Row(_send, _people, _team, _live));
        Grid.SetRow(composer, 3);
        root.Children.Add(_header);
        root.Children.Add(actions);
        root.Children.Add(body);
        root.Children.Add(composer);
        Content = root;
        CloudOnly(_send, _share, _teleport, here, open, seeded);
    }

    protected override void OnNavigatedTo(Microsoft.UI.Xaml.Navigation.NavigationEventArgs e)
    {
        _id = e.Parameter as string ?? "";
        _header.Text = _id;
        _ = LoadAsync();
    }

    private static PivotItem Tab(string title, StackPanel body)
    {
        return new PivotItem
        {
            Header = title,
            Content = new ScrollViewer { Content = body }
        };
    }

    private async Task LoadAsync()
    {
        if (_id.Length == 0)
        {
            _header.Text = "Session";
            return;
        }
        var session = await Call(NxMethods.SessionsGet, new { session_id = _id, id = _id });
        var turns = await Call(NxMethods.SessionsTurns, new { session_id = _id, id = _id });
        var files = await Call(NxMethods.SessionsFiles, new { session_id = _id, id = _id });
        var ops = await Call(NxMethods.SessionsOperations, new { session_id = _id, id = _id });
        var memories = await Call(NxMethods.SessionsMemories, new { session_id = _id, id = _id });
        var summary = await Call(NxMethods.SessionsSummary, new { session_id = _id, id = _id });
        var versions = await Call(NxMethods.SessionsVersions, new { session_id = _id, id = _id });
        FillLines(_chat, turns, "role", "text", "content", "message");
        FillLines(_files, files, "path", "name");
        FillLines(_commands, ops, "command", "cmd", "text");
        FillLines(_memory, memories, "text", "title", "content");
        _summary.Children.Clear();
        _summary.Children.Add(Line(summary.Ok ? summary.Result?.ToString() ?? "" : summary.Error ?? ""));
        _context.Children.Clear();
        _context.Children.Add(Line("Context", title: true));
        if (session.Ok && session.Result is { } body)
        {
            var title = NxJson.Text(body, "title", "summary", "preview");
            var agent = NxJson.Text(body, "agent", "harness");
            _header.Text = string.Join(" · ", new[] { agent, title, _id }.Where(part => part.Length > 0));
            _context.Children.Add(Line("Repo " + NxJson.Text(body, "repo", "project")));
            _context.Children.Add(Line("Branch " + NxJson.Text(body, "branch")));
            _context.Children.Add(Line("Visibility " + NxJson.Text(body, "visibility")));
            var legacy = NxJson.Text(body, "lineage_kind") == "legacy_snapshot";
            _teleport.IsEnabled = !legacy && !ShellState.Current.Offline;
            if (legacy)
            {
                _context.Children.Add(Line("This is an archived snapshot. It can be read, but cannot be teleported until a new captured version exists."));
            }
        }
        else if (!session.Ok)
        {
            _header.Text = session.Error ?? _id;
        }
        _context.Children.Add(Line("Versions " + NxJson.Items(versions.Result).Count()));
        _context.Children.Add(Line("Memory " + NxJson.Items(memories.Result).Count()));
        _context.Children.Add(Line("Files " + NxJson.Items(files.Result).Count()));
    }

    private static void FillLines(StackPanel panel, NxResponse response, params string[] keys)
    {
        panel.Children.Clear();
        if (!response.Ok)
        {
            panel.Children.Add(new TextBlock { Text = response.Error ?? "", TextWrapping = TextWrapping.Wrap });
            return;
        }
        var items = NxJson.Items(response.Result).ToList();
        if (items.Count == 0)
        {
            panel.Children.Add(new TextBlock { Text = NxJson.Scrub(response.Result?.ToString() ?? "Nothing here yet."), TextWrapping = TextWrapping.Wrap });
            return;
        }
        foreach (var item in items)
        {
            var text = NxJson.Text(item, keys);
            if (text.Length == 0)
            {
                text = item.ToString();
            }
            if (keys.Contains("text") || keys.Contains("content"))
            {
                var status = NxJson.PublicStatus(NxJson.Text(item, "status"));
                if (status.Length > 0 && NxJson.Text(item, "status").Length > 0)
                {
                    text = status + " · " + text;
                }
            }
            panel.Children.Add(new TextBlock { Text = NxJson.Scrub(text), TextWrapping = TextWrapping.Wrap, Margin = new Thickness(0, 0, 0, 8) });
        }
    }

    private async Task ContinueAsync(string mode, string resume, string? agent = null)
    {
        var response = await Call(NxMethods.ContinueStart, new
        {
            session_id = _id,
            mode,
            resume,
            agent = agent ?? "",
            prompt = _message.Text
        });
        _summary.Children.Clear();
        _summary.Children.Add(Line(response.Ok ? response.Result?.ToString() ?? "Started" : response.Error ?? "Continue failed"));
    }

    private async Task SendAsync()
    {
        var response = await Call(NxMethods.RunsMessage, new { session_id = _id, prompt = _message.Text });
        _chat.Children.Add(Line(response.Ok ? _message.Text : response.Error ?? "Send failed"));
        if (response.Ok)
        {
            _message.Text = "";
        }
    }

    private async Task ShareAsync()
    {
        var team = _team.IsChecked == true;
        if (team)
        {
            var confirm = new ContentDialog
            {
                Title = "Share with the project",
                Content = "All members will see this chat and its files.",
                PrimaryButtonText = "Share with everyone",
                CloseButtonText = "Cancel",
                XamlRoot = XamlRoot,
                DefaultButton = ContentDialogButton.Close
            };
            if (await confirm.ShowAsync() != ContentDialogResult.Primary)
            {
                return;
            }
        }
        var people = _people.Text.Split(',', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries);
        var response = await Call(NxMethods.SessionsShare, new
        {
            session_id = _id,
            people,
            team,
            confirm = team,
            live = _live.IsChecked == true
        });
        _context.Children.Add(Line(response.Ok ? "Share sent" : response.Error ?? "Share failed"));
    }

    private async Task SendTeleportAsync()
    {
        if (_id.Length == 0)
        {
            return;
        }
        var recipient = new TextBox { PlaceholderText = "Recipient user ID" };
        var home = new TextBox { PlaceholderText = "Recipient home folder (optional)" };
        var dialog = new ContentDialog
        {
            Title = "Send a teleport",
            Content = new StackPanel { Spacing = 10, Children = { new TextBlock { Text = "The recipient receives the latest completed, redacted session version." }, recipient, home } },
            PrimaryButtonText = "Send",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary,
            XamlRoot = XamlRoot
        };
        if (await dialog.ShowAsync() != ContentDialogResult.Primary)
        {
            return;
        }
        if (string.IsNullOrWhiteSpace(recipient.Text))
        {
            _context.Children.Add(Line("A recipient user ID is required."));
            return;
        }
        var response = await Call(NxMethods.TeleportSend, new { session_id = _id, to_user_id = recipient.Text.Trim(), home = home.Text.Trim() });
        _context.Children.Add(Line(response.Ok ? "Teleport sent" : response.Error ?? "Teleport could not be sent"));
    }

    private static string Selected(ComboBox box) => box.SelectedItem as string ?? "";
}
