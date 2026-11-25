#nullable enable
using System.Text.Json;

namespace Nexus.Core;

internal sealed record ModelPickerChoice(string ID, string Name, string Description)
{
    public string Label => Name.Length == 0 ? ID : Name.Equals(ID, StringComparison.OrdinalIgnoreCase) ? Name : Name + " · " + ID;
}

internal sealed class ModelPickerCatalog
{
    public IReadOnlyList<ModelPickerChoice> Choices { get; }
    public string Source { get; }
    public string Warning { get; }
    public ModelPickerCatalog(IReadOnlyList<ModelPickerChoice> choices, string source, string warning)
        => (Choices, Source, Warning) = (choices, source, warning);

    public static ModelPickerCatalog Parse(JsonElement root)
    {
		if (root.ValueKind != JsonValueKind.Object) return new(Array.Empty<ModelPickerChoice>(), "", "Invalid model catalog; use the default or enter a model ID.");
        var choices = new List<ModelPickerChoice>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        if (root.TryGetProperty("items", out var items) && items.ValueKind == JsonValueKind.Array)
            foreach (var item in items.EnumerateArray())
            {
                var id = NxJson.Text(item, "id");
                if (id.Length is > 0 and <= 160 && seen.Add(id)) choices.Add(new(id, NxJson.Text(item, "name"), NxJson.Text(item, "description")));
            }
        if (root.TryGetProperty("models", out var models) && models.ValueKind == JsonValueKind.Array)
            foreach (var model in models.EnumerateArray())
                if (model.ValueKind == JsonValueKind.String && model.GetString() is { Length: > 0 and <= 160 } id && seen.Add(id)) choices.Add(new(id, id, ""));
        return new(choices, NxJson.Text(root, "source"), NxJson.Text(root, "warning"));
    }

    /// The engine's catalog warning as one line people can act on, or ""
    /// when it describes a normal state (a cached list, an agent that only
    /// lists the models set up in it). The raw text talks about adapters,
    /// catalogs and quota; it never reaches the composer.
    public static string Notice(string? warning, string agentName)
    {
        var w = (warning ?? "").Trim();
        if (w.Length == 0) return "";
        if (w.StartsWith("Cached model catalog", StringComparison.Ordinal) ||
            w.StartsWith("This harness has no supported", StringComparison.Ordinal)) return "";
        if (w.StartsWith("Couldn't refresh models", StringComparison.Ordinal) ||
            w.StartsWith("Live Codex discovery failed", StringComparison.Ordinal))
            return $"Couldn't refresh {agentName}'s models, so the last list is shown.";
        if (w.StartsWith("Couldn't load models", StringComparison.Ordinal))
            return $"Couldn't get {agentName}'s models, so its usual ones are shown.";
        return NxErrors.Friendly(w).Ref.Length > 0 ? $"{agentName}'s model list may be incomplete. You can type a model name." : w;
    }

    // A background refresh that returns the same list must not rebuild the
    // picker under the user (it would reset typed text and close the popup).
    public bool SameAs(ModelPickerCatalog other)
        => Source == other.Source && Warning == other.Warning && Choices.SequenceEqual(other.Choices);

    // An editable ComboBox can keep its old SelectedItem while the user types.
    // Only resolve a display label when the entered value still matches it.
    public static string ResolveValue(string text, string? selectedID, string? selectedLabel)
    {
        text = text.Trim();
        if (text.Equals("Default model", StringComparison.OrdinalIgnoreCase)) return "";
        if (selectedID is not null && (text == selectedLabel || text == selectedID)) return selectedID;
        return text;
    }
}
