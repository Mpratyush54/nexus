using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using System.Diagnostics;
using System.Text.Json;

namespace Nexus.Pages;

public sealed partial class SessionPage
{
    private readonly Button _refreshModels = new() { Content = new FontIcon { Glyph = "\uE72C", FontSize = 13 }, MinWidth = 30, MinHeight = 30, Padding = new Thickness(4), Visibility = Visibility.Collapsed };
    // Shared by every chat page (UI thread only) so switching chats shows the
    // last list instantly. The core also caches per harness on disk (24h).
    private static readonly Dictionary<string, ModelPickerCatalog> _modelCatalogs = new(StringComparer.OrdinalIgnoreCase);
    private static readonly Dictionary<string, DateTime> _modelCatalogLoadedAt = new(StringComparer.OrdinalIgnoreCase);
    private static readonly Dictionary<string, string> _modelLoadErrors = new(StringComparer.OrdinalIgnoreCase);
    private static readonly TimeSpan ModelCatalogRecheck = TimeSpan.FromMinutes(10);
    private readonly HashSet<string> _loadingModelAgents = new(StringComparer.OrdinalIgnoreCase);
    private bool _configuringModel;
    private readonly TextBlock _modelCatalogNotice = new() { FontSize = 11, Opacity = 0.72, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, Visibility = Visibility.Collapsed };
    private bool _preparingHarness;

    private async Task<JsonElement?> HarnessStatusAsync(string agent)
    {
        var response = await Call(NxMethods.HarnessStatus, new { agent });
        if (!response.Ok || response.Result is not { } status) { SetComposerStatus(response.Error ?? "Could not check the local agent installation."); return null; }
        if (NxJson.Text(status, "installed") == "true") return status;
        var url = NxJson.Text(status, "install_url");
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Install " + NxJson.Text(status, "name"),
            Content = new TextBlock
            {
                Text = NxJson.Text(status, "problem") + "\n\nCaptured chats can be read without the CLI. To continue or resume, install the agent CLI and sign in. Installing its desktop app alone may not add the CLI to PATH.",
                TextWrapping = TextWrapping.Wrap
            },
            PrimaryButtonText = url.Length > 0 ? "Installation guide" : "OK", CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowAsync() == ContentDialogResult.Primary && Uri.TryCreate(url, UriKind.Absolute, out var uri) && uri.Scheme == "https")
        {
            try { Process.Start(new ProcessStartInfo(uri.ToString()) { UseShellExecute = true }); }
            catch (Exception ex) { SetComposerStatus("Could not open the installation guide: " + ex.Message); }
        }
        return null;
    }

    private async Task RefreshHarnessModelsAsync()
    {
        await LoadHarnessModelsAsync(true, true);
    }

    private async Task LoadHarnessModelsAsync(bool refresh, bool openPicker)
    {
        if (_sendInFlight || _preparingHarness) return;
        var agent = SelectedHarness();
        if (agent.Length == 0) return;
        // A list loaded recently is already on screen; the core refreshes its
        // own cache in the background, so only re-ask it now and then.
        if (!refresh && _modelCatalogs.ContainsKey(agent) && _modelCatalogLoadedAt.TryGetValue(agent, out var loadedAt) && DateTime.UtcNow - loadedAt < ModelCatalogRecheck) return;
        if (!_loadingModelAgents.Add(agent)) return;
        var hadCatalog = _modelCatalogs.ContainsKey(agent);
        _refreshModels.IsEnabled = false;
        if (!hadCatalog) _model.PlaceholderText = "Loading models…";
        if (openPicker) SetComposerStatus("Loading models from " + agent + "…");
        var session = _id;
        try
        {
            // Listing is read-only and runs off the UI thread (NxClient uses
            // the thread pool). Do not show installation/login dialogs merely
            // because the user opened the editable model picker.
            var response = await Call(NxMethods.HarnessModels, new { agent, refresh });
            if (!response.Ok || response.Result is not { } result)
            {
                var reason = (response.Error is { Length: > 0 } e ? e : "the harness did not answer").Trim().TrimEnd('.');
                _modelLoadErrors[agent] = hadCatalog
                    ? "Couldn't refresh models: " + reason + ". Showing the last loaded list."
                    : "Couldn't load models: " + reason + ", using defaults.";
                if (SelectedHarness() == agent && session == _id) UpdateModelCatalogNotice();
                if (openPicker) SetComposerStatus("");
                return;
            }
            _modelLoadErrors.Remove(agent);
            var catalog = ModelPickerCatalog.Parse(result);
            var unchanged = _modelCatalogs.TryGetValue(agent, out var previous) && previous.SameAs(catalog);
            _modelCatalogs[agent] = catalog;
            _modelCatalogLoadedAt[agent] = DateTime.UtcNow;
            if (SelectedHarness() != agent || session != _id) return;
            if (unchanged && !openPicker) { UpdateModelCatalogNotice(); return; }
            var selection = SelectedModel();
            _modelByHarness[agent] = selection;
            var wasOpen = _model.IsDropDownOpen;
            ConfigureModel();
            if ((openPicker || wasOpen) && !_sendInFlight) _model.IsDropDownOpen = true;
            if (openPicker) SetComposerStatus("Model catalog loaded. Your model selection has not been changed.");
        }
        catch (Exception ex)
        {
            _modelLoadErrors[agent] = (hadCatalog ? "Couldn't refresh models: " : "Couldn't load models: ") + ex.Message.TrimEnd('.') + (hadCatalog ? ". Showing the last loaded list." : ", using defaults.");
            if (SelectedHarness() == agent && session == _id) UpdateModelCatalogNotice();
        }
        finally
        {
            _loadingModelAgents.Remove(agent);
            if (SelectedHarness() == agent) { _refreshModels.IsEnabled = !_sendInFlight; _model.PlaceholderText = "Default model"; }
        }
    }

    // Keep the user's last pick per harness (persisted) as soon as it changes,
    // not only when a message is sent.
    private void RememberModelChoice()
    {
        if (_configuringModel || _sendInFlight) return;
        var agent = SelectedHarness();
        if (agent.Length == 0) return;
        var model = SelectedModel();
        if (_modelByHarness.TryGetValue(agent, out var current) && current == model) return;
        _modelByHarness[agent] = model;
        AppPrefs.Data.ChatModels ??= new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        AppPrefs.Data.ChatModels[agent] = model;
        AppPrefs.Save();
    }

    private void UpdateModelCatalogNotice()
    {
        var agent = SelectedHarness();
        var error = _modelLoadErrors.GetValueOrDefault(agent, "");
        if (!_modelCatalogs.TryGetValue(agent, out var catalog))
        {
            _modelCatalogNotice.Text = error;
            _modelCatalogNotice.Visibility = error.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
            return;
        }
        var selected = SelectedModel();
        var missing = selected.Length > 0 && !catalog.Choices.Any(choice => choice.ID == selected);
        var lines = new List<string>();
        if (error.Length > 0) lines.Add(error);
        if (catalog.Warning.Length > 0) lines.Add(catalog.Warning);
        if (missing) lines.Add("Your saved/typed model is not in this catalog. It has been preserved; choose another model if it is no longer available.");
        _modelCatalogNotice.Text = string.Join("\n", lines);
        _modelCatalogNotice.Visibility = _modelCatalogNotice.Text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        SetModelTip(catalog.Source + ". Catalog entries do not guarantee account access or remaining quota. Type a custom model ID if needed.");
    }

    // The picker's tooltip is withheld while its list is open so it never covers the list.
    private void SetModelTip(string text)
    {
        _modelTip = text;
        if (!_model.IsDropDownOpen) ToolTipService.SetToolTip(_model, text);
    }
}
