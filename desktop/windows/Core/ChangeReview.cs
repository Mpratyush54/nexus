#nullable enable
using System.Text.Json;

namespace Nexus.Core;

// View models for checkpoints (internal/checkpoint in the core): the "Review
// changes" card and diff under a turn, the "Restore to before this message"
// sheet, and which messages a restore hides. No UI types, so they are tested
// in ProjectScope.Tests.

internal sealed record ReviewLine(char Kind, string Text, int Old, int New)
{
    public bool Added => Kind == '+';
    public bool Removed => Kind == '-';
}

internal sealed record ReviewHunk(int Index, string Header, int Added, int Removed, IReadOnlyList<ReviewLine> Lines);

internal sealed record ReviewFile(
    string Path, string Status, bool Binary, bool Sensitive, bool TooLarge, string Notice,
    int Added, int Removed, string Version, bool CanUndo, IReadOnlyList<ReviewHunk> Hunks)
{
    public string FileName => Path.Contains('/') ? Path[(Path.LastIndexOf('/') + 1)..] : Path;
    public string Folder => Path.Contains('/') ? Path[..Path.LastIndexOf('/')] : "";
    /// "A", "D" or "M", as in Git.
    public string Letter => Status switch { "added" => "A", "deleted" => "D", _ => "M" };
    public string StatusLabel => Status switch { "added" => "New file", "deleted" => "Deleted", _ => "Edited" };
    /// Whole-file only: no hunks to keep or undo one by one.
    public bool WholeFileOnly => Hunks.Count == 0;
    /// The highlighter's language for this file's extension.
    public string Language => ChangeReview.LanguageFor(Path);
}

internal sealed class ChangeReview
{
    public string CheckpointId { get; init; } = "";
    public int Total { get; init; }
    public int Pending { get; init; }
    public int Added { get; init; }
    public int Removed { get; init; }
    public IReadOnlyList<ReviewFile> Files { get; init; } = Array.Empty<ReviewFile>();

    /// Every change was kept or undone.
    public bool Settled => Pending == 0;

    /// "Review changes (3 files, +24 −7)".
    public string Title => Settled ? "Changes reviewed" : "Review changes (" + Summary + ")";

    /// "3 files, +24 −7" (pending files), or "3 files" once settled.
    public string Summary => Settled ? FileCount(Total) : FileCount(Pending) + ", +" + Added + " −" + Removed;

    private static string FileCount(int n) => n == 1 ? "1 file" : n + " files";

    /// The card's title before the full review loaded (only the count is known).
    public static string Placeholder(int files) => "Review changes (" + FileCount(files) + ")";

    public static ChangeReview Parse(JsonElement root)
    {
        if (root.ValueKind != JsonValueKind.Object) return new ChangeReview();
        var files = new List<ReviewFile>();
        if (root.TryGetProperty("files", out var list) && list.ValueKind == JsonValueKind.Array)
        {
            foreach (var f in list.EnumerateArray())
            {
                var hunks = new List<ReviewHunk>();
                if (f.TryGetProperty("hunks", out var hs) && hs.ValueKind == JsonValueKind.Array)
                {
                    foreach (var h in hs.EnumerateArray())
                    {
                        var lines = new List<ReviewLine>();
                        if (h.TryGetProperty("lines", out var ls) && ls.ValueKind == JsonValueKind.Array)
                            foreach (var l in ls.EnumerateArray())
                            {
                                var kind = NxJson.Text(l, "kind");
                                lines.Add(new ReviewLine(kind.Length > 0 ? kind[0] : ' ', NxJson.Text(l, "text"), Int(l, "old"), Int(l, "new")));
                            }
                        hunks.Add(new ReviewHunk(Int(h, "index"), NxJson.Text(h, "header"), Int(h, "added"), Int(h, "removed"), lines));
                    }
                }
                files.Add(new ReviewFile(
                    NxJson.Text(f, "path"), NxJson.Text(f, "status"), Bool(f, "binary"), Bool(f, "sensitive"), Bool(f, "too_large"),
                    NxJson.Text(f, "notice"), Int(f, "added"), Int(f, "removed"), NxJson.Text(f, "version"), Bool(f, "can_undo"), hunks));
            }
        }
        return new ChangeReview
        {
            CheckpointId = NxJson.Text(root, "checkpoint_id"),
            Total = Int(root, "total"), Pending = Int(root, "pending"),
            Added = Int(root, "added"), Removed = Int(root, "removed"), Files = files,
        };
    }

    internal static int Int(JsonElement e, string name) =>
        e.ValueKind == JsonValueKind.Object && e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetInt32(out var n) ? n : 0;

    internal static long Long(JsonElement e, string name) =>
        e.ValueKind == JsonValueKind.Object && e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetInt64(out var n) ? n : 0;

    internal static bool Bool(JsonElement e, string name) =>
        e.ValueKind == JsonValueKind.Object && e.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.True;

    public static string LanguageFor(string path)
    {
        var dot = path.LastIndexOf('.');
        var slash = path.LastIndexOf('/');
        var name = slash >= 0 ? path[(slash + 1)..] : path;
        if (name.Equals("Dockerfile", StringComparison.OrdinalIgnoreCase)) return "dockerfile";
        if (name.Equals("Makefile", StringComparison.OrdinalIgnoreCase)) return "makefile";
        if (dot < 0 || dot < slash) return "";
        return path[(dot + 1)..].ToLowerInvariant() switch
        {
            "md" or "markdown" or "txt" => "",
            "yml" => "yaml",
            "htm" => "html",
            var ext => ext,
        };
    }
}

/// One file a restore changes.
internal sealed record RewindRow(string Path, string Action, string Problem)
{
    public bool CanRestore => Problem.Length == 0;
    public string Label => Problem.Length > 0 ? "Can't restore · " + Problem : Action switch
    {
        "delete" => "Removed · the agent created it",
        "recreate" => "Brought back · the agent deleted it",
        _ => "Put back as it was",
    };
}

/// What "Restore to before this message" will do, for the confirm sheet.
internal sealed class RewindPlanView
{
    public string CheckpointId { get; init; } = "";
    public long FromIdx { get; init; }
    public int Later { get; init; }
    public IReadOnlyList<RewindRow> Rows { get; init; } = Array.Empty<RewindRow>();

    public int Restorable => Rows.Count(r => r.CanRestore);

    public string Title => "Restore to before this message?";

    /// One plain sentence over the file list.
    public string Lead
    {
        get
        {
            if (Rows.Count == 0)
                return "No files need to change: the project folder already matches this point. The chat still goes back to before this message.";
            var files = Restorable == 1 ? "1 file goes" : Restorable + " files go";
            var turns = Later switch { <= 0 => "this message", 1 => "this message and the one after it", _ => "this message and the " + Later + " after it" };
            return Restorable == 0
                ? "Nexus has no copy of these files from before this message, so they stay as they are. The chat still goes back."
                : files + " back to how " + (Restorable == 1 ? "it was" : "they were") + " before " + turns + ". The chat goes back too; the agent still remembers the conversation.";
        }
    }

    public string ConfirmLabel => Restorable == 0 ? "Go back in chat" : Restorable == 1 ? "Restore 1 file" : "Restore " + Restorable + " files";

    public static RewindPlanView Parse(JsonElement root)
    {
        var rows = new List<RewindRow>();
        if (root.ValueKind == JsonValueKind.Object && root.TryGetProperty("files", out var files) && files.ValueKind == JsonValueKind.Array)
            foreach (var f in files.EnumerateArray())
                rows.Add(new RewindRow(NxJson.Text(f, "path"), NxJson.Text(f, "action"), NxJson.Text(f, "problem")));
        return new RewindPlanView
        {
            CheckpointId = NxJson.Text(root, "checkpoint_id"),
            FromIdx = ChangeReview.Long(root, "from_idx"),
            Later = ChangeReview.Int(root, "later"),
            Rows = rows,
        };
    }
}

/// A restore the chat remembers: messages from FromIdx up to CutoffIdx are hidden.
internal sealed record ChatRewind(long FromIdx, long CutoffIdx, string UndoId, int Files, DateTimeOffset At);

/// A chat's checkpoints (by saved user message index) and its restores.
internal sealed class CheckpointIndex
{
    public sealed record Item(string Id, long UserIdx, int Changed, bool Done, bool Rewound);

    private readonly Dictionary<long, Item> _byIdx = new();
    public IReadOnlyList<Item> Items { get; private set; } = Array.Empty<Item>();
    public IReadOnlyList<ChatRewind> Rewinds { get; private set; } = Array.Empty<ChatRewind>();

    public static CheckpointIndex Parse(JsonElement root)
    {
        var index = new CheckpointIndex();
        var items = new List<Item>();
        if (root.ValueKind == JsonValueKind.Object && root.TryGetProperty("checkpoints", out var list) && list.ValueKind == JsonValueKind.Array)
            foreach (var c in list.EnumerateArray())
                items.Add(new Item(NxJson.Text(c, "id"), ChangeReview.Long(c, "user_idx"), ChangeReview.Int(c, "changed"), ChangeReview.Bool(c, "done"), ChangeReview.Bool(c, "rewound")));
        var rewinds = new List<ChatRewind>();
        if (root.ValueKind == JsonValueKind.Object && root.TryGetProperty("rewinds", out var rw) && rw.ValueKind == JsonValueKind.Array)
            foreach (var r in rw.EnumerateArray())
                rewinds.Add(new ChatRewind(ChangeReview.Long(r, "from_idx"), ChangeReview.Long(r, "cutoff_idx"), NxJson.Text(r, "undo_id"),
                    ChangeReview.Int(r, "files"), DateTimeOffset.TryParse(NxJson.Text(r, "at"), out var at) ? at : DateTimeOffset.MinValue));
        index.Items = items;
        index.Rewinds = rewinds;
        foreach (var item in items)
            if (item.UserIdx > 0) index._byIdx[item.UserIdx] = item;
        return index;
    }

    /// A message can be restored to when its turn has a checkpoint that a
    /// restore hasn't already hidden.
    public bool CanRestore(long userIdx) => _byIdx.TryGetValue(userIdx, out var item) && !item.Rewound;

    public Item? For(long userIdx) => _byIdx.GetValueOrDefault(userIdx);

    /// A turn just finished in this page: its message is restorable at once.
    public void Note(string id, long userIdx, int changed)
    {
        if (id.Length == 0 || userIdx <= 0) return;
        var item = new Item(id, userIdx, changed, true, false);
        _byIdx[userIdx] = item;
        Items = Items.Where(i => i.Id != id).Append(item).ToArray();
    }

    /// Turns with changes to review, newest last.
    public IEnumerable<Item> Reviewable => Items.Where(i => i.Done && !i.Rewound && i.Changed > 0 && i.UserIdx > 0);
}

internal static class ChatRewinds
{
    /// The key of a saved user message ("turn:&lt;idx&gt;:user").
    public static string UserKey(long idx) => "turn:" + idx + ":user";

    /// Which chat elements a restore hides: from the restored message up
    /// to (not including) the first message saved after the restore.
    /// `elements` are the chat's elements in order: a message's key and
    /// order, or (null, null) for anything else (cards, notes). Returns
    /// (start, end) with end exclusive (where the divider goes), or null
    /// when the message isn't on screen.
    public static (int Start, int End)? Range(IReadOnlyList<(string? Key, long? Order)> elements, ChatRewind rewind)
    {
        var key = UserKey(rewind.FromIdx);
        var start = -1;
        for (var i = 0; i < elements.Count; i++)
            if (elements[i].Key == key) { start = i; break; }
        if (start < 0) return null;
        var end = elements.Count;
        for (var i = start + 1; i < elements.Count; i++)
            if (elements[i].Order is { } order && order >= rewind.CutoffIdx) { end = i; break; }
        return (start, end);
    }

    /// Back-to-back restores leave dividers with nothing visible between
    /// them; only the last of each run stays. `elements` are the chat's
    /// elements in order (is it a divider, is it visible). Returns the
    /// indices of the dividers to hide.
    public static IReadOnlyList<int> StackedDividers(IReadOnlyList<(bool IsDivider, bool Visible)> elements)
    {
        var hide = new List<int>();
        var previous = -1; // the last visible divider with nothing visible after it yet
        for (var i = 0; i < elements.Count; i++)
        {
            var (isDivider, visible) = elements[i];
            if (!visible) continue;
            if (!isDivider) { previous = -1; continue; }
            if (previous >= 0) hide.Add(previous);
            previous = i;
        }
        return hide;
    }

    /// The divider's words: "Restored checkpoint · 3 files".
    public static string Divider(ChatRewind rewind) =>
        "Restored checkpoint" + (rewind.Files switch { 0 => "", 1 => " · 1 file", var n => " · " + n + " files" });
}
