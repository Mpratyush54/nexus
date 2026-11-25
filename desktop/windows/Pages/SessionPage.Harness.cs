using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using System.Diagnostics;
using System.Text.Json;

namespace Nexus.Pages;

public sealed partial class SessionPage
{
    private readonly Button _refreshModels = Named(new Button { Content = new FontIcon { Glyph = "\uE72C", FontSize = 13 }, MinWidth = 30, MinHeight = 30, Padding = new Thickness(4), Visibility = Visibility.Collapsed }, "Refresh the model list");
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
        if (agent.Length == 0) { SetComposerStatus(AgentAvailability.NoneReady); return null; }
        var name = AgentAvailability.DisplayName(agent);
        var response = await Call(NxMethods.HarnessStatus, new { agent });
        if (!response.Ok || response.Result is not { } status) { SetComposerStatus($"Nexus couldn't check {name} on this computer. Try again."); return null; }
        var state = AgentAvailability.FromStatus(status);
        if (state is null || state.Usable) return status;
        // The pickers should not have offered it; re-check them in the background.
        AgentAvailabilityService.Refresh(force: true);
        var others = AgentAvailabilityService.PickerChoices().Where(c => c.Value != state.Agent).ToList();
        var choice = await AgentProblemDialog.ShowAsync(XamlRoot, state, others.Count > 0);
        if (choice == AgentProblemDialog.Choice.UseAnother && others.Count > 0 && SelectedHarness() == agent)
        {
            var next = _harness.Items.OfType<ComboBoxItem>().FirstOrDefault(i => (i.Tag as string) == others[0].Value);
            if (next is not null) _harness.SelectedItem = next;
            SetComposerStatus($"Switched to {others[0].Name}. Send again when you're ready.");
        }
        else if (choice == AgentProblemDialog.Choice.Fix)
        {
            SetComposerStatus(AgentAvailability.FixSentence(state));
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
        if (openPicker) SetComposerStatus("Loading models from " + AgentAvailability.DisplayName(agent) + "…");
        var session = _id;
        try
        {
            // Listing is read-only and runs off the UI thread (NxClient uses
            // the thread pool). Do not show installation/login dialogs merely
            // because the user opened the editable model picker.
            var response = await Call(NxMethods.HarnessModels, new { agent, refresh });
            if (!response.Ok || response.Result is not { } result)
            {
                var name = AgentAvailability.DisplayName(agent);
                _modelLoadErrors[agent] = hadCatalog
                    ? $"Couldn't refresh {name}'s models, so the last list is shown."
                    : $"Couldn't get {name}'s models, so its default model is used.";
                if (response.RawError is { Length: > 0 } raw) Nexus.Core.Diagnostics.DiagLog.Warn("models", $"{agent}: {raw}");
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
            if (openPicker) SetComposerStatus("Models updated.");
        }
        catch (Exception ex)
        {
            var name = AgentAvailability.DisplayName(agent);
            _modelLoadErrors[agent] = NxErrors.ForException(hadCatalog ? $"Couldn't refresh {name}'s models, so the last list is shown." : $"Couldn't get {name}'s models, so its default model is used.", ex, "models");
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
        var notice = ModelPickerCatalog.Notice(catalog.Warning, AgentAvailability.DisplayName(agent));
        if (notice.Length > 0) lines.Add(notice);
        if (missing) lines.Add("Your model isn't in this list. Nexus still uses it; pick another one if it stops working.");
        _modelCatalogNotice.Text = string.Join("\n", lines);
        _modelCatalogNotice.Visibility = _modelCatalogNotice.Text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        SetModelTip("Pick a model, or type a model name.");
    }

    // The picker's tooltip is withheld while its list is open so it never covers the list.
    private void SetModelTip(string text)
    {
        _modelTip = text;
        if (!_model.IsDropDownOpen) ToolTipService.SetToolTip(_model, text);
    }
}
