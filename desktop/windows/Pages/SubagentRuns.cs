using System.Text.Json;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Sub-agent runs: chats an agent started from another chat (Claude Code
/// Task/Agent runs, Cursor "(subagent)" chats). They are hidden from the chat
/// list, so this finds them and their parent chat.
///
/// Claude Code saves a run as
/// ~/.claude/projects/&lt;folder&gt;/&lt;parent session uuid&gt;/subagents/agent-&lt;hex&gt;.jsonl
/// next to agent-&lt;hex&gt;.meta.json (agentType, description). The capture
/// mirror does not send that parent link to the server (the session upsert
/// takes no parent/lineage fields), so it is derived here from the local
/// files: run native id → parent native id → the parent's Nexus chat.
/// </summary>
internal static class SubagentRuns
{
    internal sealed record LocalRun(string NativeId, string ParentNativeId, string Description, string AgentType, DateTime Modified);

    internal sealed record Run(string Id, string Title, string Agent, string AgentType, string When, string ParentNativeId)
    {
        public string ParentId { get; set; } = "";
        public string ParentTitle { get; set; } = "";
    }

    private static readonly object Gate = new();
    private static Task<Dictionary<string, LocalRun>>? s_index;
    private static DateTime s_indexAt;
    private static readonly Dictionary<string, (string Id, string Title)> s_byNative = new(StringComparer.OrdinalIgnoreCase);

    /// <summary>Claude Code sub-agent transcripts on this computer, by native id (agent-&lt;hex&gt;). Cached for a minute.</summary>
    public static Task<Dictionary<string, LocalRun>> LocalIndexAsync(bool refresh = false)
    {
        lock (Gate)
        {
            if (s_index is null || refresh || (s_index.IsCompleted && DateTime.UtcNow - s_indexAt > TimeSpan.FromMinutes(1)))
            {
                s_indexAt = DateTime.UtcNow;
                s_index = Task.Run(ScanLocal);
            }
            return s_index;
        }
    }

    private static Dictionary<string, LocalRun> ScanLocal()
    {
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var runs = new Dictionary<string, LocalRun>(StringComparer.OrdinalIgnoreCase);
        var root = Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.UserProfile), ".claude", "projects");
        try
        {
            if (!Directory.Exists(root)) return runs;
            foreach (var folder in Directory.EnumerateDirectories(root))
            foreach (var parent in Directory.EnumerateDirectories(folder))
            {
                var dir = Path.Combine(parent, "subagents");
                if (!Directory.Exists(dir)) continue;
                foreach (var file in Directory.EnumerateFiles(dir, "agent-*.jsonl"))
                {
                    var native = Path.GetFileNameWithoutExtension(file);
                    runs[native] = ReadRun(parent, native);
                }
            }
        }
        catch { /* unreadable folders: show what was found */ }
        System.Diagnostics.Trace.WriteLine($"[perf] sub-agent runs: local index {runs.Count} runs {watch.ElapsedMilliseconds} ms");
        return runs;
    }

    private static LocalRun ReadRun(string parent, string native)
    {
        var dir = Path.Combine(parent, "subagents");
        string description = "", type = "";
        var meta = Path.Combine(dir, native + ".meta.json");
        try
        {
            if (File.Exists(meta) && new FileInfo(meta).Length < 64 * 1024)
            {
                using var doc = JsonDocument.Parse(File.ReadAllText(meta));
                description = NxJson.Text(doc.RootElement, "description");
                type = NxJson.Text(doc.RootElement, "agentType");
            }
        }
        catch { /* a bad meta file only loses the label */ }
        var transcript = Path.Combine(dir, native + ".jsonl");
        var modified = File.Exists(transcript) ? File.GetLastWriteTimeUtc(transcript) : File.Exists(meta) ? File.GetLastWriteTimeUtc(meta) : DateTime.MinValue;
        return new LocalRun(native, Path.GetFileName(parent), description, type, modified);
    }

    /// <summary>
    /// The local run for a sub-agent chat's native id (Claude Code:
    /// "agent-&lt;hex&gt;", the file name of its transcript). Looked up in the
    /// index first; a run newer than the cached index is read from its
    /// ~/.claude/projects/*/&lt;parent&gt;/subagents/agent-&lt;hex&gt;.meta.json directly.
    /// </summary>
    public static async Task<LocalRun?> LocalRunAsync(string nativeId)
    {
        var native = Path.GetFileNameWithoutExtension(nativeId.Trim().Replace('\\', '/').Split('/')[^1]);
        if (!native.StartsWith("agent-", StringComparison.OrdinalIgnoreCase) || native.Length < 10) return null;
        try
        {
            if ((await LocalIndexAsync()).TryGetValue(native, out var indexed)) return indexed;
            return await Task.Run(() => FindRun(native));
        }
        catch { return null; }
    }

    private static LocalRun? FindRun(string native)
    {
        var root = Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.UserProfile), ".claude", "projects");
        if (!Directory.Exists(root)) return null;
        foreach (var folder in Directory.EnumerateDirectories(root))
        foreach (var parent in Directory.EnumerateDirectories(folder))
        {
            var dir = Path.Combine(parent, "subagents");
            if (File.Exists(Path.Combine(dir, native + ".meta.json")) || File.Exists(Path.Combine(dir, native + ".jsonl")))
                return ReadRun(parent, native);
        }
        return null;
    }

    /// <summary>
    /// Agent sessions Nexus itself drove: a chat sent from Nexus is saved under
    /// its Nexus session (native id "nexus-…"), and each turn records the
    /// agent's own session id in the local continuation store
    /// (%AppData%\Nexus\continuations\&lt;hash&gt;\*.json, written by nexuscore).
    /// timeline.list hides the captured copy of such an agent session, so a
    /// sub-agent's parent is found here instead. Cached for a minute.
    /// </summary>
    internal sealed record DrivenIndex(Dictionary<string, string> SessionByNative, Dictionary<string, HashSet<string>> NativesBySession);

    private static Task<DrivenIndex>? s_driven;
    private static DateTime s_drivenAt;

    public static Task<DrivenIndex> DrivenIndexAsync(bool refresh = false)
    {
        lock (Gate)
        {
            if (s_driven is null || refresh || (s_driven.IsCompleted && DateTime.UtcNow - s_drivenAt > TimeSpan.FromMinutes(1)))
            {
                s_drivenAt = DateTime.UtcNow;
                s_driven = Task.Run(() => ScanDriven(Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.ApplicationData), "Nexus", "continuations")));
            }
            return s_driven;
        }
    }

    internal static DrivenIndex ScanDriven(string root)
    {
        var byNative = new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        var bySession = new Dictionary<string, HashSet<string>>(StringComparer.OrdinalIgnoreCase);
        try
        {
            if (!Directory.Exists(root)) return new DrivenIndex(byNative, bySession);
            // File names start with the exchange time: in name order the
            // newest exchange for a native id wins.
            foreach (var file in Directory.EnumerateDirectories(root).SelectMany(d => Directory.EnumerateFiles(d, "*.json")).OrderBy(f => Path.GetFileName(f), StringComparer.Ordinal))
            {
                try
                {
                    using var stream = File.OpenRead(file);
                    using var doc = JsonDocument.Parse(stream);
                    var session = NxJson.Text(doc.RootElement, "session_id").Trim();
                    var native = NxJson.Text(doc.RootElement, "native_id").Trim();
                    if (session.Length == 0 || native.Length == 0) continue;
                    byNative[native] = session;
                    if (!bySession.TryGetValue(session, out var natives)) bySession[session] = natives = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
                    natives.Add(native);
                }
                catch { /* a half-written or foreign file only loses its link */ }
            }
        }
        catch { /* unreadable folder: no links */ }
        return new DrivenIndex(byNative, bySession);
    }

    /// <summary>The Nexus chat id (and title) for a native session id, or empty.</summary>
    public static async Task<(string Id, string Title)> ChatForNativeAsync(NxClient core, string projectId, string nativeId)
    {
        if (nativeId.Length == 0) return ("", "");
        lock (Gate)
            if (s_byNative.TryGetValue(nativeId, out var known)) return known;
        // A session Nexus drove is listed under its Nexus chat, not as a capture.
        var driven = await DrivenIndexAsync();
        if (driven.SessionByNative.TryGetValue(nativeId, out var sessionId))
        {
            var session = await core.CallAsync(NxMethods.SessionsGet, new { session_id = sessionId });
            if (session.Ok && session.Result is { } body)
            {
                var chat = (sessionId, ChatTitles.Title(NxJson.Text(body, "title"), NxJson.Text(body, "summary", "preview"), NxJson.Text(body, "agent", "harness")));
                lock (Gate) s_byNative[nativeId] = chat;
                return chat;
            }
        }
        var response = await core.CallAsync(NxMethods.TimelineList, Args(projectId, nativeId, 10));
        if (!response.Ok) return ("", "");
        foreach (var item in NxJson.Items(response.Result))
        {
            if (!NxJson.Text(item, "native_id", "nativeId").Equals(nativeId, StringComparison.OrdinalIgnoreCase)) continue;
            // Never an empty title: the runs page reads "" as "not captured".
            var found = (NxJson.Text(item, "session_id", "id", "sessionId"),
                ChatTitles.Title(NxJson.Text(item, "title"), NxJson.Text(item, "summary", "preview"), NxJson.Text(item, "agent", "harness")));
            lock (Gate) s_byNative[nativeId] = found;
            return found;
        }
        return ("", "");
    }

    /// <summary>
    /// The captured chat of the sub-agent a parent chat started with this
    /// description (Claude Code: matched through the local meta files). A chat
    /// held in Nexus has its own native id ("nexus-…"); its runs hang off the
    /// agent sessions it drove.
    /// </summary>
    public static async Task<string> FindChildChatAsync(NxClient core, string projectId, string parentSessionId, string parentNativeId, string description)
    {
        var parents = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        if (parentNativeId.Length > 0) parents.Add(parentNativeId);
        if (parentSessionId.Length > 0 && (await DrivenIndexAsync()).NativesBySession.TryGetValue(parentSessionId, out var driven))
            parents.UnionWith(driven);
        if (parents.Count == 0) return "";
        var index = await LocalIndexAsync();
        var children = index.Values.Where(r => parents.Contains(r.ParentNativeId)).OrderByDescending(r => r.Modified).ToList();
        if (children.Count == 0) return "";
        var wanted = description.Trim();
        var match = children.FirstOrDefault(r => wanted.Length > 0 && r.Description.Equals(wanted, StringComparison.OrdinalIgnoreCase))
            ?? (children.Count == 1 ? children[0] : null);
        if (match is null) return "";
        return (await ChatForNativeAsync(core, projectId, match.NativeId)).Id;
    }

    /// <summary>Recent sub-agent chats for a project (newest first), with their parent chat when it can be found.</summary>
    public static async Task<List<Run>> RecentAsync(NxClient core, string projectId)
    {
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var indexTask = LocalIndexAsync();
        // timeline.list matches q against titles and native ids: "agent-"
        // finds Claude Code runs, "subagent" Cursor-style titles.
        var calls = new[] { "agent-", "subagent" }.Select(q => core.CallAsync(NxMethods.TimelineList, Args(projectId, q, 100))).ToList();
        await Task.WhenAll(calls);
        var index = await indexTask;
        var seen = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        var runs = new List<(Run Run, DateTimeOffset At)>();
        foreach (var call in calls)
        {
            var response = await call;
            if (!response.Ok) continue;
            foreach (var item in NxJson.Items(response.Result))
            {
                if (!SessionLineage.IsSubagentSession(item)) continue;
                if (projectId.Length > 0 && !ProjectScope.TimelineItemMatches(item, projectId)) continue;
                var id = NxJson.Text(item, "session_id", "id", "sessionId");
                if (id.Length == 0 || !seen.Add(id)) continue;
                var native = NxJson.Text(item, "native_id", "nativeId");
                index.TryGetValue(native, out var local);
                var title = local?.Description is { Length: > 0 } d ? d : ChatTitles.Title(NxJson.Text(item, "title"), NxJson.Text(item, "summary", "preview"), NxJson.Text(item, "agent", "harness"));
                var whenRaw = NxJson.Text(item, "last_active_at", "updated_at", "started_at");
                DateTimeOffset.TryParse(whenRaw, out var at);
                runs.Add((new Run(id, title, NxJson.Text(item, "agent", "harness"), local?.AgentType ?? "", at == default ? "" : at.ToLocalTime().ToString("MMM d, h:mm tt"), local?.ParentNativeId ?? ""), at));
                lock (Gate) s_byNative[native] = (id, title);
            }
        }
        var list = runs.OrderByDescending(r => r.At).Select(r => r.Run).Take(150).ToList();
        // Parent chats: a few distinct ones, looked up in parallel.
        var parents = list.Select(r => r.ParentNativeId).Where(p => p.Length > 0).Distinct(StringComparer.OrdinalIgnoreCase).Take(20).ToList();
        var lookups = parents.ToDictionary(p => p, p => ChatForNativeAsync(core, projectId, p), StringComparer.OrdinalIgnoreCase);
        await Task.WhenAll(lookups.Values);
        foreach (var run in list)
            if (run.ParentNativeId.Length > 0 && lookups.TryGetValue(run.ParentNativeId, out var parent))
                (run.ParentId, run.ParentTitle) = await parent;
        System.Diagnostics.Trace.WriteLine($"[perf] sub-agent runs: {list.Count} runs, {parents.Count} parents {watch.ElapsedMilliseconds} ms");
        return list;
    }

    private static object Args(string projectId, string q, int limit) =>
        projectId.Length > 0 ? new { project_id = projectId, q, limit } : (object)new { q, limit };
}
