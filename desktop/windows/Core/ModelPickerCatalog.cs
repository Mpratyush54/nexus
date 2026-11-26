#nullable enable
using System.Text.Json;

namespace Nexus.Core;

internal sealed record ModelPickerChoice(string ID, string Name, string Description, string Provider = "")
{
    // Friendly name only; the exact ID is in the item's tooltip.
    public string Label => Name.Length == 0 ? ID : Name;
}

internal sealed class ModelPickerCatalog
{
    public IReadOnlyList<ModelPickerChoice> Choices { get; }
    public string Source { get; }
    public string Warning { get; }
    /// The agent's own default model (from its config), or "".
    public string DefaultModel { get; }
    public string DefaultName { get; }
    /// The last refresh failed and this is the previous list: the picker
    /// shows its refresh button, never a sentence.
    public bool RefreshFailed { get; }

    public ModelPickerCatalog(IReadOnlyList<ModelPickerChoice> choices, string source, string warning, string defaultModel = "", string defaultName = "", bool refreshFailed = false)
        => (Choices, Source, Warning, DefaultModel, DefaultName, RefreshFailed) = (choices, source, warning, defaultModel, defaultName, refreshFailed);

    public static ModelPickerCatalog Parse(JsonElement root)
    {
        if (root.ValueKind != JsonValueKind.Object) return new(Array.Empty<ModelPickerChoice>(), "", "", refreshFailed: true);
        var choices = new List<ModelPickerChoice>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        if (root.TryGetProperty("items", out var items) && items.ValueKind == JsonValueKind.Array)
            foreach (var item in items.EnumerateArray())
            {
                var id = NxJson.Text(item, "id");
                if (id.Length is > 0 and <= 160 && seen.Add(id)) choices.Add(new(id, NxJson.Text(item, "name"), NxJson.Text(item, "description"), NxJson.Text(item, "provider")));
            }
        if (root.TryGetProperty("models", out var models) && models.ValueKind == JsonValueKind.Array)
            foreach (var model in models.EnumerateArray())
                if (model.ValueKind == JsonValueKind.String && model.GetString() is { Length: > 0 and <= 160 } id && seen.Add(id)) choices.Add(new(id, id, ""));
        var failed = root.TryGetProperty("refresh_failed", out var f) && f.ValueKind == JsonValueKind.True;
        return new(choices, NxJson.Text(root, "source"), NxJson.Text(root, "warning"), NxJson.Text(root, "default_model"), NxJson.Text(root, "default_name"), failed);
    }

    /// The first entry of the picker: "Default (Claude Fable 5.2)" when the
    /// agent's configured default is known, else "Default model".
    public string DefaultLabel
    {
        get
        {
            var name = DefaultName.Length > 0 ? DefaultName : DefaultModel;
            return name.Length > 0 ? $"Default ({name})" : "Default model";
        }
    }

    /// The picker's rows in order: a provider name (header, Choice null)
    /// before each provider's models when more than one provider is listed.
    public IEnumerable<(string Header, ModelPickerChoice? Choice)> Rows()
    {
        var groups = Choices.GroupBy(c => c.Provider.Length > 0 ? c.Provider : "Other").ToList();
        if (groups.Count <= 1)
        {
            foreach (var choice in Choices) yield return ("", choice);
            yield break;
        }
        foreach (var group in groups)
        {
            yield return (group.Key, null);
            foreach (var choice in group) yield return ("", choice);
        }
    }

    // A background refresh that returns the same list must not rebuild the
    // picker under the user (it would reset typed text and close the popup).
    public bool SameAs(ModelPickerCatalog other)
        => Source == other.Source && DefaultModel == other.DefaultModel && DefaultName == other.DefaultName && Choices.SequenceEqual(other.Choices);

    // An editable ComboBox can keep its old SelectedItem while the user types.
    // Only resolve a display label when the entered value still matches it.
    public static string ResolveValue(string text, string? selectedID, string? selectedLabel)
    {
        text = text.Trim();
        if (text.Equals("Default model", StringComparison.OrdinalIgnoreCase)) return "";
        if (text.StartsWith("Default (", StringComparison.OrdinalIgnoreCase) && text.EndsWith(')')) return "";
        if (selectedID is not null && (text == selectedLabel || text == selectedID)) return selectedID;
        return text;
    }
}
