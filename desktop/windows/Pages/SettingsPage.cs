using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

public sealed class SettingsPage : NexusPage
{
    private readonly TextBlock _accountDetail = Muted("Checking sign-in…");
    private readonly TextBlock _statusLine = Muted("");
    private readonly TextBlock _actionFeedback = Muted("");
    private readonly ToggleSwitch _login = new()
    {
        OnContent = "On",
        OffContent = "Off",
        MinWidth = 0
    };
    private Button _signIn = null!;
    private Button _clear = null!;
    private Button _retry = null!;

    public SettingsPage()
    {
        _login.IsOn = LoginStart.IsEnabled();
        _login.Toggled += (_, _) =>
        {
            try
            {
                LoginStart.SetEnabled(_login.IsOn);
                Flash(_login.IsOn ? "Will start at login" : "Won’t start at login");
            }
            catch (Exception ex)
            {
                Flash(ex.Message);
            }
        };

        _signIn = Primary("Sign in");
        _signIn.Click += async (_, _) => await SignInAsync();
        _clear = Secondary("Clear cache");
        _clear.Click += async (_, _) => await ClearCacheAsync();
        _retry = Secondary("Retry uploads");
        _retry.Click += async (_, _) => await RetryUploadsAsync();
        CloudOnly(_retry);

        var accountLabels = new StackPanel { Spacing = 2, VerticalAlignment = VerticalAlignment.Center };
        accountLabels.Children.Add(new TextBlock
        {
            Text = "Account",
            FontSize = 13,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold
        });
        accountLabels.Children.Add(_accountDetail);

        var accountGrid = new Grid { ColumnSpacing = 16 };
        accountGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        accountGrid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(accountLabels, 0);
        Grid.SetColumn(_signIn, 1);
        _signIn.VerticalAlignment = VerticalAlignment.Center;
        accountGrid.Children.Add(accountLabels);
        accountGrid.Children.Add(_signIn);

        var panel = new Border
        {
            CornerRadius = new CornerRadius(8),
            BorderThickness = new Thickness(1),
            BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Gray) { Opacity = 0.45 },
            Child = new StackPanel
            {
                Children =
                {
                    new Border
                    {
                        Padding = new Thickness(14, 10, 14, 10),
                        BorderThickness = new Thickness(0, 0, 0, 1),
                        BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Gray) { Opacity = 0.35 },
                        Child = accountGrid
                    },
                    SettingRow("Start at login", "Open Nexus when Windows starts", _login),
                    SettingRow("Local data", "Clear cached cloud views or retry pending uploads", Row(_clear, _retry))
                }
            }
        };

        // Drop the divider on the last row
        if (panel.Child is StackPanel stack && stack.Children[^1] is Border last)
        {
            last.BorderThickness = new Thickness(0);
        }

        var root = new StackPanel
        {
            Padding = new Thickness(20, 16, 20, 16),
            Spacing = 10,
            Children =
            {
                PageHeading("Settings"),
                PageBody(panel, 560),
                _statusLine,
                _actionFeedback,
                Muted("Credentials stay on this PC only.")
            }
        };
        Content = new ScrollViewer { Content = root };
    }

    protected override void OnEnter(object? parameter) => _ = RefreshAsync();

    private async Task SignInAsync()
    {
        _accountDetail.Text = "Waiting for browser…";
        _signIn.IsEnabled = false;
        var response = await Call(NxMethods.AuthLogin);
        _signIn.IsEnabled = true;
        if (!response.Ok)
        {
            _accountDetail.Text = response.Error ?? "Sign-in failed";
            Flash(response.Error ?? "Sign-in failed");
            return;
        }
        Flash("Signed in");
        await RefreshAsync();
    }

    private async Task ClearCacheAsync()
    {
        var response = await Call(NxMethods.CacheClear);
        Flash(response.Ok ? "Cache cleared" : response.Error ?? "Couldn’t clear cache");
        await RefreshAsync();
    }

    private async Task RetryUploadsAsync()
    {
        var response = await Call(NxMethods.UploadsRetry);
        Flash(response.Ok ? "Upload retry started" : response.Error ?? "Couldn’t retry uploads");
        await RefreshAsync();
    }

    private async Task RefreshAsync()
    {
        var auth = await Call(NxMethods.AuthStatus);
        var status = await Call(NxMethods.StatusGet);

        var signedIn = auth.Ok && BoolProp(auth.Result, "signed_in");
        if (signedIn)
        {
            _accountDetail.Text = "Signed in to Nexus cloud";
            _signIn.Content = "Sign in again";
        }
        else
        {
            _accountDetail.Text = "Not signed in — cloud features need a session";
            _signIn.Content = "Sign in";
        }

        var online = status.Ok && BoolProp(status.Result, "online");
        var pending = status.Ok ? IntProp(status.Result, "pending_uploads") : 0;
        var uploads = pending == 0 ? "no uploads waiting" : $"{pending} upload{(pending == 1 ? "" : "s")} waiting";
        _statusLine.Text = $"{(online ? "Online" : "Offline")} · {uploads}";
    }

    private void Flash(string message)
    {
        _actionFeedback.Text = string.IsNullOrWhiteSpace(message) ? "" : message;
    }

    private static bool BoolProp(JsonElement? result, string name)
    {
        return result is { } el &&
               el.ValueKind == JsonValueKind.Object &&
               el.TryGetProperty(name, out var value) &&
               value.ValueKind == JsonValueKind.True;
    }

    private static int IntProp(JsonElement? result, string name)
    {
        if (result is { } el &&
            el.ValueKind == JsonValueKind.Object &&
            el.TryGetProperty(name, out var value) &&
            value.TryGetInt32(out var n))
        {
            return n;
        }
        return 0;
    }

    private static Button Primary(string label) => new()
    {
        Content = label,
        Padding = new Thickness(12, 4, 12, 4),
        Style = Application.Current.Resources["AccentButtonStyle"] as Style
    };

    private static Button Secondary(string label) => new()
    {
        Content = label,
        Padding = new Thickness(12, 4, 12, 4)
    };
}
