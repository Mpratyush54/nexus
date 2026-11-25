#nullable enable
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Nexus.Core;

/// <summary>
/// What someone was writing in a chat's composer: the text and caret, the
/// attached images and files, and large pastes kept as chips. Saved per chat
/// so switching chats or restarting Nexus never loses a draft.
/// </summary>
internal sealed record ComposerDraft
{
    public string Text { get; init; } = "";
    public int Caret { get; init; }
    public List<string> Images { get; init; } = new();
    public List<string> Files { get; init; } = new();
    public List<string> Pastes { get; init; } = new();
    public DateTimeOffset SavedAt { get; init; }

    [JsonIgnore]
    public bool IsEmpty => Text.Trim().Length == 0 && Images.Count == 0 && Files.Count == 0 && Pastes.Count == 0;
}

/// <summary>
/// Drafts by chat id, persisted as one JSON file under the profile
/// (%LOCALAPPDATA%\Nexus\drafts.json). The page saves on a 300 ms debounce
/// and when it leaves a chat; a successful send removes the entry.
/// </summary>
internal sealed class ComposerDraftStore
{
    /// <summary>Oldest drafts beyond this are dropped when saving.</summary>
    public const int MaxDrafts = 200;
    /// <summary>A pasted block bigger than this is not kept in the draft file.</summary>
    public const int MaxPasteChars = 200_000;

    private static readonly JsonSerializerOptions s_json = new() { WriteIndented = false };
    private readonly string _path;
    private readonly Func<DateTimeOffset> _now;
    private readonly object _gate = new();
    private Dictionary<string, ComposerDraft>? _drafts;

    public ComposerDraftStore(string path, Func<DateTimeOffset>? now = null)
    {
        _path = path;
        _now = now ?? (() => DateTimeOffset.UtcNow);
    }

    private static ComposerDraftStore? s_default;

    /// <summary>The store for this Nexus profile.</summary>
    public static ComposerDraftStore Default => s_default ??= new ComposerDraftStore(
        Path.Combine(ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "drafts.json"));

    private Dictionary<string, ComposerDraft> Drafts()
    {
        if (_drafts is not null) return _drafts;
        _drafts = new Dictionary<string, ComposerDraft>(StringComparer.OrdinalIgnoreCase);
        try
        {
            if (File.Exists(_path) && JsonSerializer.Deserialize<Dictionary<string, ComposerDraft>>(File.ReadAllText(_path), s_json) is { } saved)
                foreach (var (id, draft) in saved)
                    if (id.Length > 0 && draft is not null && !draft.IsEmpty) _drafts[id] = Clean(draft);
        }
        catch (Exception ex) when (ex is IOException or JsonException or UnauthorizedAccessException or NotSupportedException)
        {
            // A damaged file loses old drafts, never the app.
        }
        return _drafts;
    }

    private static ComposerDraft Clean(ComposerDraft draft) => draft with
    {
        Text = draft.Text ?? "",
        Caret = Math.Clamp(draft.Caret, 0, (draft.Text ?? "").Length),
        Images = draft.Images ?? new(),
        Files = draft.Files ?? new(),
        Pastes = (draft.Pastes ?? new()).Where(p => !string.IsNullOrEmpty(p)).ToList()
    };

    public ComposerDraft? Get(string sessionId)
    {
        if (string.IsNullOrEmpty(sessionId)) return null;
        lock (_gate) return Drafts().GetValueOrDefault(sessionId);
    }

    public bool Has(string sessionId) => Get(sessionId) is not null;

    /// <summary>Keeps (or, when empty, forgets) a chat's draft in memory. Returns true when it changed.</summary>
    public bool Set(string sessionId, ComposerDraft draft)
    {
        if (string.IsNullOrEmpty(sessionId)) return false;
        lock (_gate)
        {
            var drafts = Drafts();
            if (draft.IsEmpty) return drafts.Remove(sessionId);
            var clean = Clean(draft) with
            {
                SavedAt = _now(),
                Pastes = draft.Pastes.Where(p => p.Length <= MaxPasteChars).ToList()
            };
            if (drafts.TryGetValue(sessionId, out var old) && Same(old, clean)) return false;
            drafts[sessionId] = clean;
            return true;
        }
    }

    public bool Remove(string sessionId)
    {
        if (string.IsNullOrEmpty(sessionId)) return false;
        lock (_gate) return Drafts().Remove(sessionId);
    }

    private static bool Same(ComposerDraft a, ComposerDraft b) =>
        a.Text == b.Text && a.Caret == b.Caret && a.Images.SequenceEqual(b.Images) && a.Files.SequenceEqual(b.Files) && a.Pastes.SequenceEqual(b.Pastes);

    /// <summary>Writes every draft to disk (atomically: temp file, then move).</summary>
    public void Save()
    {
        string json;
        lock (_gate)
        {
            var drafts = Drafts();
            foreach (var stale in drafts.OrderByDescending(d => d.Value.SavedAt).Skip(MaxDrafts).Select(d => d.Key).ToList())
                drafts.Remove(stale);
            json = JsonSerializer.Serialize(drafts, s_json);
        }
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(_path)!);
            var temp = _path + ".tmp";
            File.WriteAllText(temp, json);
            File.Move(temp, _path, overwrite: true);
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            // Best effort; the in-memory draft still survives chat switches.
        }
    }
}
