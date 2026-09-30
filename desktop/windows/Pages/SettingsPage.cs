using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class SettingsPage : NexusPage
{
    private readonly StackPanel _body = new() { Spacing = 8 };
    private readonly ToggleSwitch _login = new() { Header = "Start at login", OnContent = "On", OffContent = "Off" };
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
        CloudOnly(retry, projects);

        var root = new Grid { Padding = new Thickness(24), RowSpacing = 12 };
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        var actions = Row(_login, _clear, retry, projects);
        var scroller = new ScrollViewer { Content = _body };
        Grid.SetRow(actions, 1);
        Grid.SetRow(scroller, 2);
        root.Children.Add(Line("Settings", title: true));
        root.Children.Add(actions);
        root.Children.Add(scroller);
        Content = root;
        Loaded += async (_, _) =>
        {
            await Run(NxMethods.StatusGet);
            await Run(NxMethods.DiagnosticsGet);
            await Run(NxMethods.AuthStatus);
            _body.Children.Add(Line("Sign-in returns through nexus://auth/callback. Tokens stay in the core."));
        };
    }

    private async Task Run(string method)
    {
        var response = await Call(method);
        _body.Children.Insert(0, Line(response.Ok
            ? method + " " + NxJson.Scrub(response.Result?.ToString() ?? "ok")
            : method + " " + (response.Error ?? "failed")));
    }
}
