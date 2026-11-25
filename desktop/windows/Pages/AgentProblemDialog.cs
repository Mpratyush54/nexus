using System.Diagnostics;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// The one dialog for an agent that can't run here: a plain sentence, one
/// action that fixes it, a way to use another agent, and the technical
/// detail folded away under "Advanced details".
/// </summary>
internal static class AgentProblemDialog
{
    internal enum Choice { Cancel, Fix, UseAnother }

    public static async Task<Choice> ShowAsync(XamlRoot root, AgentState state, bool canUseAnother)
    {
        var body = new StackPanel { Spacing = 10, MaxWidth = 460 };
        body.Children.Add(new TextBlock { Text = state.Problem.Length > 0 ? state.Problem : $"Nexus couldn't start {state.Name} on this computer.", TextWrapping = TextWrapping.Wrap, FontSize = 14 });
        body.Children.Add(new TextBlock { Text = AgentAvailability.FixSentence(state), TextWrapping = TextWrapping.Wrap, Opacity = 0.75 });
        body.Children.Add(new TextBlock { Text = "Chats you already have stay readable either way.", TextWrapping = TextWrapping.Wrap, Opacity = 0.6, FontSize = 12 });
        if (state.Detail.Length > 0)
        {
            body.Children.Add(new Expander
            {
                Header = "Advanced details",
                HorizontalAlignment = HorizontalAlignment.Stretch,
                HorizontalContentAlignment = HorizontalAlignment.Stretch,
                Content = new TextBlock { Text = state.Detail, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, FontSize = 12, Opacity = 0.8 }
            });
        }
        var dialog = new ContentDialog
        {
            XamlRoot = root,
            Title = state.Name + " isn't ready",
            Content = body,
            PrimaryButtonText = AgentAvailability.FixLabel(state),
            SecondaryButtonText = canUseAnother ? "Use another agent" : "",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary
        };
        var result = await dialog.ShowThemedAsync();
        if (result == ContentDialogResult.Primary)
        {
            OpenGuide(state);
            return Choice.Fix;
        }
        return result == ContentDialogResult.Secondary ? Choice.UseAnother : Choice.Cancel;
    }

    /// Opens the agent's own install / sign-in guide, then re-checks agents
    /// when Nexus regains focus.
    public static void OpenGuide(AgentState state)
    {
        if (!Uri.TryCreate(state.InstallUrl, UriKind.Absolute, out var uri) || uri.Scheme != "https") return;
        try { Process.Start(new ProcessStartInfo(uri.ToString()) { UseShellExecute = true }); }
        catch (Exception ex) { NxErrors.ForException("Nexus couldn't open your browser.", ex, "agents"); }
    }
}
