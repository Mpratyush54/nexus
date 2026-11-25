using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;

namespace Nexus.Pages;

/// <summary>Primary shell hub: project timeline (sessions).</summary>
public sealed class HomeHubPage : NexusPage
{
    public const string SegmentSessions = "sessions";
    public const string SegmentTeleport = "teleport";

    private readonly Grid _segmentHost = new()
    {
        HorizontalAlignment = HorizontalAlignment.Stretch,
        VerticalAlignment = VerticalAlignment.Stretch,
        MinHeight = 0
    };
    private readonly Grid _root;
    private readonly StackPanel _header;
    private readonly TextBlock _projectChip = new()
    {
        FontSize = 12,
        Opacity = 0.78,
        TextWrapping = TextWrapping.Wrap
    };
    private TimelinePage? _sessions;
    private object? _segmentParameter;

    public HomeHubPage()
    {
        var inbox = new Button { Content = "Shared with you", MinHeight = 34, Padding = new Thickness(12, 5, 12, 5) };
        ToolTipService.SetToolTip(inbox, "Handoffs and shared sessions sent to this machine — not the same as Share in a session chat.");
        inbox.Click += (_, _) => MainWindow.Current?.OpenTeleport();

        var titleRow = new Grid { ColumnSpacing = 12 };
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        var titleStack = new StackPanel { Spacing = 4 };
        titleStack.Children.Add(Line("Chats", title: true));
        titleStack.Children.Add(Muted(
            "Browse conversations in this project. Search by topic or filter by agent, then open a chat to continue."));
        Grid.SetColumn(titleStack, 0);
        Grid.SetColumn(inbox, 1);
        titleRow.Children.Add(titleStack);
        titleRow.Children.Add(inbox);

        _header = new StackPanel { Spacing = 10 };
        _header.Children.Add(titleRow);
        _header.Children.Add(_projectChip);

        _root = new Grid
        {
            Padding = new Thickness(32, 20, 32, 24),
            RowSpacing = 12,
            VerticalAlignment = VerticalAlignment.Stretch,
            HorizontalAlignment = HorizontalAlignment.Stretch
        };
        _root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        _root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star), MinHeight = 0 });
        Grid.SetRow(_header, 0);
        Grid.SetRow(_segmentHost, 1);
        _root.Children.Add(_header);
        _root.Children.Add(_segmentHost);
        Content = _root;

        Loaded += (_, _) =>
        {
            UpdateProjectChip();
            UpdateSegmentHostHeight();
        };
        _root.SizeChanged += (_, _) => UpdateSegmentHostHeight();
        _header.SizeChanged += (_, _) => UpdateSegmentHostHeight();
        void ShellChanged(object? _, EventArgs __) => DispatcherQueue?.TryEnqueue(UpdateProjectChip);
        Loaded += (_, _) => ShellState.Changed += ShellChanged;
        Unloaded += (_, _) => ShellState.Changed -= ShellChanged;
    }

    private void UpdateProjectChip()
    {
        var label = ShellState.Current.ProjectName;
        var id = ShellState.Current.ProjectId;
        _projectChip.Text = id.Length > 0 ? "Project · " + label : "";
        _projectChip.Visibility = id.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private void UpdateSegmentHostHeight()
    {
        if (_root.ActualHeight <= 0)
        {
            return;
        }

        var pad = _root.Padding.Top + _root.Padding.Bottom;
        var rowGap = _root.RowSpacing;
        var used = _header.ActualHeight + pad + rowGap;
        var available = _root.ActualHeight - used;
        if (available < 0)
        {
            available = 0;
        }

        _segmentHost.Height = available;
        _segmentHost.MaxHeight = available;
    }

    protected override void OnEnter(object? parameter)
    {
        _segmentParameter = parameter;
        if (parameter is HubNavigation nav)
        {
            _segmentParameter = nav.Parameter;
            if (nav.Segment == SegmentTeleport)
            {
                MainWindow.Current?.OpenTeleport(nav.Parameter as string);
                _segmentParameter = null;
            }
        }
        else if (parameter is string agent && agent.Length > 0)
        {
            _segmentParameter = agent;
        }

        ShowSessions(_segmentParameter);
        UpdateProjectChip();
        UpdateSegmentHostHeight();
    }

    public void NavigateToSegment(string segment, object? parameter = null)
    {
        if (segment == SegmentTeleport)
        {
            MainWindow.Current?.OpenTeleport(parameter as string);
            return;
        }
        _segmentParameter = parameter;
        ShowSessions(parameter);
    }

    private void ShowSessions(object? parameter)
    {
        _sessions ??= new TimelinePage(hubChrome: true);
        if (!ReferenceEquals(_segmentHost.Children.FirstOrDefault(), _sessions))
        {
            _segmentHost.Children.Clear();
            _segmentHost.Children.Add(_sessions);
        }
        _sessions.Enter(parameter);
        UpdateSegmentHostHeight();
    }

    internal void InvalidateForProjectChange() => _sessions?.InvalidateForProjectChange();
}

public sealed record HubNavigation(string Segment, object? Parameter = null);
