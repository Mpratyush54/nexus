#nullable enable
using System.Diagnostics;
using System.Text;
using System.Text.RegularExpressions;

namespace Nexus.Core;

/// <summary>
/// Something picked with "@" in the composer: a file or folder in the
/// project, another Nexus chat, or a memory. Shown as a chip above the
/// message and kept in the chat's draft.
/// </summary>
internal sealed record ComposerMention
{
    public const string File = "file";
    public const string Folder = "folder";
    public const string Chat = "chat";
    public const string Memory = "memory";

    public string Kind { get; init; } = File;
    /// <summary>A relative path (folders end with "/"), or the chat or memory id.</summary>
    public string Value { get; init; } = "";
    /// <summary>What the chip says.</summary>
    public string Label { get; init; } = "";
    /// <summary>For a chat or memory: the text sent along as context.</summary>
    public string Context { get; init; } = "";

    public bool IsPath => Kind is File or Folder;
}

/// <summary>How picked mentions travel to the agent.</summary>
internal static class MentionText
{
    /// <summary>Context taken from a chat or memory is cut to this many characters.</summary>
    public const int MaxContextChars = 6000;

    /// <summary>
    /// A path reference in the agent's own format. Claude Code, Codex,
    /// Cursor, Gemini, OpenCode and Kimi all read "@path" (relative to the
    /// project); a path with spaces is quoted.
    /// </summary>
    public static string PathRef(string agent, string path)
    {
        path = (path ?? "").Replace('\\', '/');
        return path.Contains(' ') ? "@\"" + path + "\"" : "@" + path;
    }

    /// <summary>
    /// The prompt with the mentions: path references on one line after the
    /// text, then each chat or memory as a labelled context block.
    /// </summary>
    public static string Apply(string prompt, IReadOnlyList<ComposerMention> mentions, string agent)
    {
        if (mentions.Count == 0) return prompt;
        var b = new StringBuilder((prompt ?? "").TrimEnd());
        var paths = mentions.Where(m => m.IsPath && m.Value.Length > 0).Select(m => PathRef(agent, m.Value)).Distinct().ToList();
        // A reference typed into the text already is not repeated.
        paths.RemoveAll(p => (" " + b + " ").Contains(" " + p + " ", StringComparison.Ordinal) || b.ToString().Contains(p + "\n", StringComparison.Ordinal));
        if (paths.Count > 0)
        {
            if (b.Length > 0) b.Append("\n\n");
            b.Append(string.Join(" ", paths));
        }
        foreach (var m in mentions.Where(m => !m.IsPath))
        {
            var context = Clip(m.Context);
            if (context.Length == 0) continue;
            var what = m.Kind == ComposerMention.Chat ? ChatSource : MemorySource;
            if (b.Length > 0) b.Append("\n\n");
            b.Append("<context source=\"").Append(what).Append(": ").Append(Attr(m.Label)).Append('"');
            // The id lets the chip under the sent message open its source.
            if (m.Value.Length > 0) b.Append(" ref=\"").Append(m.Kind).Append(':').Append(Attr(m.Value)).Append('"');
            b.Append(">\n").Append(context).Append("\n</context>");
        }
        return b.ToString();
    }

    private const string ChatSource = "Earlier Nexus chat";
    private const string MemorySource = "Nexus memory";
    private static readonly Regex ContextBlock = new(
        @"\s*<context source=""(?<kind>" + ChatSource + "|" + MemorySource + @"): (?<label>[^""]*)""(?:\s+ref=""(?<ref>[^""]*)"")?\s*>\n?(?<body>.*?)\n?</context>",
        RegexOptions.Singleline | RegexOptions.Compiled);
    // A preview cut inside a block leaves only its start.
    private static readonly Regex OpenBlock = new(
        @"\s*<context source=""(?:" + ChatSource + "|" + MemorySource + @"):.*$",
        RegexOptions.Singleline | RegexOptions.Compiled);

    /// <summary>
    /// Takes the chats and memories sent with a message back out of it: the
    /// text without the context blocks, and each block as a mention (for the
    /// chips under the sent bubble). Text without blocks comes back as it was.
    /// </summary>
    public static (string Text, IReadOnlyList<ComposerMention> Mentions) Extract(string? text)
    {
        text ??= "";
        if (!text.Contains("<context source=\"", StringComparison.Ordinal)) return (text, Array.Empty<ComposerMention>());
        var found = new List<ComposerMention>();
        var clean = ContextBlock.Replace(text, m =>
        {
            var kind = m.Groups["kind"].Value == ChatSource ? ComposerMention.Chat : ComposerMention.Memory;
            var reference = m.Groups["ref"].Value;
            var id = reference.StartsWith(kind + ":", StringComparison.Ordinal) ? reference[(kind.Length + 1)..] : "";
            found.Add(new ComposerMention { Kind = kind, Value = id, Label = m.Groups["label"].Value.Trim(), Context = m.Groups["body"].Value.Trim() });
            return "";
        });
        clean = OpenBlock.Replace(clean, "");
        return (clean.Trim(), found);
    }

    /// <summary>The text without chat and memory context blocks (titles, previews, recall).</summary>
    public static string Strip(string? text) => Extract(text).Text;

    private static string Clip(string text)
    {
        text = (text ?? "").Trim();
        return text.Length <= MaxContextChars ? text : text[..MaxContextChars].TrimEnd() + "\n…";
    }

    private static string Attr(string text) => (text ?? "").Replace("\"", "'").Replace("\n", " ").Trim();

    /// <summary>A chip's short label: the file or folder name, or a trimmed title.</summary>
    public static string ChipLabel(ComposerMention m)
    {
        if (m.IsPath)
        {
            var trimmed = m.Value.TrimEnd('/');
            var name = trimmed[(trimmed.LastIndexOf('/') + 1)..];
            return m.Kind == ComposerMention.Folder ? name + "/" : name;
        }
        var label = (m.Label ?? "").Trim();
        return label.Length > 40 ? label[..39] + "…" : label;
    }
}

/// <summary>
/// The files and folders in a project folder for the "@" picker: git's own
/// list (tracked plus untracked, .gitignore respected) when the folder is a
/// repository, otherwise a walk that skips the usual build and dependency
/// folders and the simple patterns of a top-level .gitignore. Capped at
/// <see cref="MaxEntries"/>, cached per folder, refreshed in the background.
/// </summary>
internal sealed class ProjectFileIndex
{
    public const int MaxEntries = 20000;
    private static readonly TimeSpan Fresh = TimeSpan.FromSeconds(30);
    private static readonly Dictionary<string, ProjectFileIndex> s_byFolder = new(StringComparer.OrdinalIgnoreCase);
    private static readonly object s_gate = new();

    private static readonly HashSet<string> SkippedFolders = new(StringComparer.OrdinalIgnoreCase)
    {
        ".git", ".hg", ".svn", "node_modules", "bin", "obj", "dist", "build", "out", "target", ".venv", "venv",
        "__pycache__", ".next", ".nuxt", ".cache", ".idea", ".vs", ".gradle", "Pods", "DerivedData", "vendor", "coverage"
    };

    private readonly string _root;
    private IReadOnlyList<string> _entries = Array.Empty<string>();
    private DateTime _loadedAt = DateTime.MinValue;
    private Task<IReadOnlyList<string>>? _loading;

    private ProjectFileIndex(string root) => _root = root;

    public static ProjectFileIndex For(string root)
    {
        lock (s_gate)
        {
            if (!s_byFolder.TryGetValue(root, out var index)) s_byFolder[root] = index = new ProjectFileIndex(root);
            return index;
        }
    }

    /// <summary>What is known now (possibly empty while the first load runs).</summary>
    public IReadOnlyList<string> Current => _entries;

    /// <summary>The list, loading it (off the UI thread) when it is missing or stale.</summary>
    public Task<IReadOnlyList<string>> GetAsync()
    {
        lock (s_gate)
        {
            if (_loading is { IsCompleted: false } running) return _entries.Count > 0 ? Task.FromResult(_entries) : running;
            if (_entries.Count > 0 && DateTime.UtcNow - _loadedAt < Fresh) return Task.FromResult(_entries);
            var load = Task.Run(() =>
            {
                var list = WithFolders(ListFiles(_root));
                lock (s_gate) { _entries = list; _loadedAt = DateTime.UtcNow; }
                return list;
            });
            _loading = load;
            // A stale list answers at once; the fresh one is there next time.
            return _entries.Count > 0 ? Task.FromResult(_entries) : load;
        }
    }

    /// <summary>Relative file paths with "/" separators.</summary>
    public static IReadOnlyList<string> ListFiles(string root)
    {
        if (string.IsNullOrWhiteSpace(root) || !Directory.Exists(root)) return Array.Empty<string>();
        if (Directory.Exists(Path.Combine(root, ".git")) || System.IO.File.Exists(Path.Combine(root, ".git")))
        {
            var git = GitFiles(root);
            if (git is not null) return git;
        }
        return Walk(root, IgnoreRules.Load(root));
    }

    private static IReadOnlyList<string>? GitFiles(string root)
    {
        try
        {
            var info = new ProcessStartInfo("git")
            {
                RedirectStandardOutput = true, RedirectStandardError = true, UseShellExecute = false, CreateNoWindow = true,
                StandardOutputEncoding = Encoding.UTF8
            };
            foreach (var arg in new[] { "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z" }) info.ArgumentList.Add(arg);
            using var process = Process.Start(info);
            if (process is null) return null;
            process.ErrorDataReceived += (_, _) => { };
            process.BeginErrorReadLine();
            var files = new List<string>();
            var seen = new HashSet<string>(StringComparer.Ordinal);
            var buffer = new StringBuilder();
            var reader = process.StandardOutput;
            int ch;
            while ((ch = reader.Read()) >= 0)
            {
                if (ch != 0) { buffer.Append((char)ch); continue; }
                var path = buffer.ToString();
                buffer.Clear();
                if (path.Length > 0 && seen.Add(path)) files.Add(path);
                if (files.Count >= MaxEntries) { try { process.Kill(); } catch { } break; }
            }
            if (!process.WaitForExit(10000)) { try { process.Kill(); } catch { } }
            return process.HasExited && process.ExitCode != 0 && files.Count == 0 ? null : files;
        }
        catch (Exception ex) when (ex is System.ComponentModel.Win32Exception or InvalidOperationException or IOException)
        {
            return null; // no git on PATH: walk instead
        }
    }

    internal static IReadOnlyList<string> Walk(string root, IgnoreRules rules)
    {
        var files = new List<string>();
        var pending = new Queue<string>();
        pending.Enqueue("");
        while (pending.Count > 0 && files.Count < MaxEntries)
        {
            var relative = pending.Dequeue();
            var full = relative.Length == 0 ? root : Path.Combine(root, relative);
            IEnumerable<string> dirs, here;
            try
            {
                dirs = Directory.EnumerateDirectories(full).ToList();
                here = Directory.EnumerateFiles(full).ToList();
            }
            catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { continue; }
            foreach (var file in here.OrderBy(f => f, StringComparer.OrdinalIgnoreCase))
            {
                var rel = Join(relative, Path.GetFileName(file));
                if (!rules.Ignored(rel, false)) files.Add(rel);
                if (files.Count >= MaxEntries) break;
            }
            foreach (var dir in dirs.OrderBy(d => d, StringComparer.OrdinalIgnoreCase))
            {
                var name = Path.GetFileName(dir);
                var rel = Join(relative, name);
                if (SkippedFolders.Contains(name) || rules.Ignored(rel, true)) continue;
                try { if (new DirectoryInfo(dir).Attributes.HasFlag(System.IO.FileAttributes.ReparsePoint)) continue; } catch { continue; }
                pending.Enqueue(rel);
            }
        }
        return files;
    }

    private static string Join(string a, string b) => a.Length == 0 ? b : a + "/" + b;

    /// <summary>The files plus every folder that holds one ("src/", "src/app/"), folders first.</summary>
    public static IReadOnlyList<string> WithFolders(IReadOnlyList<string> files)
    {
        var folders = new SortedSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (var file in files)
        {
            var at = file.IndexOf('/');
            while (at > 0)
            {
                folders.Add(file[..(at + 1)]);
                at = file.IndexOf('/', at + 1);
            }
        }
        var all = new List<string>(folders.Count + files.Count);
        all.AddRange(folders);
        all.AddRange(files);
        return all;
    }

    internal sealed record Hit(string Path, int[] Positions)
    {
        public bool IsFolder => Path.EndsWith('/');
    }

    /// <summary>
    /// The best <paramref name="limit"/> matches. With nothing typed: the
    /// recent picks, then the top-level folders and files.
    /// </summary>
    public static IReadOnlyList<Hit> Search(IReadOnlyList<string> entries, string query, IReadOnlyList<string> recent, int limit = 50)
    {
        query = (query ?? "").Trim().Replace('\\', '/');
        if (query.Length == 0)
        {
            var known = new HashSet<string>(entries, StringComparer.Ordinal);
            var list = recent.Where(known.Contains).Select(p => new Hit(p, Array.Empty<int>())).ToList();
            foreach (var entry in entries)
            {
                if (list.Count >= limit) break;
                var slash = entry.IndexOf('/');
                var topLevel = slash < 0 || slash == entry.Length - 1;
                if (topLevel && !list.Any(h => h.Path == entry)) list.Add(new Hit(entry, Array.Empty<int>()));
            }
            return list;
        }
        var recentRank = recent.Select((p, i) => (p, i)).ToDictionary(x => x.p, x => x.i, StringComparer.Ordinal);
        var top = new List<(Hit Hit, int Score)>();
        var floor = int.MinValue;
        foreach (var entry in entries)
        {
            if (FuzzyMatch.MatchPath(query, entry) is not { } m) continue;
            var score = m.Score + (recentRank.TryGetValue(entry, out var r) ? 400 - r * 10 : 0);
            if (top.Count >= limit && score <= floor) continue;
            top.Add((new Hit(entry, m.Positions), score));
            if (top.Count > limit * 2)
            {
                top.Sort((a, b) => b.Score.CompareTo(a.Score));
                top.RemoveRange(limit, top.Count - limit);
                floor = top[^1].Score;
            }
        }
        return top.OrderByDescending(t => t.Score).ThenBy(t => t.Hit.Path.Length).Take(limit).Select(t => t.Hit).ToList();
    }
}

/// <summary>
/// The simple part of a .gitignore, for folders that aren't repositories:
/// names ("secrets.txt", "logs/"), "*.ext" and "/rooted" paths. Negations
/// and character classes are ignored (git's own list covers repositories).
/// </summary>
internal sealed class IgnoreRules
{
    private readonly List<(string Pattern, bool DirOnly, bool Rooted)> _rules = new();

    public static IgnoreRules Load(string root)
    {
        try
        {
            var path = Path.Combine(root, ".gitignore");
            return Parse(System.IO.File.Exists(path) ? System.IO.File.ReadAllLines(path) : Array.Empty<string>());
        }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException) { return new IgnoreRules(); }
    }

    public static IgnoreRules Parse(IEnumerable<string> lines)
    {
        var rules = new IgnoreRules();
        foreach (var raw in lines)
        {
            var line = raw.Trim();
            if (line.Length == 0 || line[0] is '#' or '!' || line.Contains('[')) continue;
            var dirOnly = line.EndsWith('/');
            line = line.TrimEnd('/');
            var rooted = line.StartsWith('/');
            line = line.TrimStart('/');
            if (line.Length == 0) continue;
            rules._rules.Add((line, dirOnly, rooted || line.Contains('/')));
        }
        return rules;
    }

    public bool Ignored(string relativePath, bool isDir)
    {
        var name = relativePath[(relativePath.LastIndexOf('/') + 1)..];
        foreach (var (pattern, dirOnly, rooted) in _rules)
        {
            if (dirOnly && !isDir) continue;
            var subject = rooted ? relativePath : name;
            if (Glob(pattern, subject)) return true;
        }
        return false;
    }

    // "*" (any run without "/"), "**" (anything) and "?" only; short patterns, so plain recursion.
    internal static bool Glob(string pattern, string text) => Glob(pattern, 0, text, 0);

    private static bool Glob(string p, int pi, string t, int ti)
    {
        if (pi == p.Length) return ti == t.Length;
        if (p[pi] == '*')
        {
            if (pi + 1 < p.Length && p[pi + 1] == '*')
            {
                for (var i = ti; i <= t.Length; i++)
                    if (Glob(p, pi + 2, t, i)) return true;
                return false;
            }
            for (var i = ti; i <= t.Length; i++)
            {
                if (Glob(p, pi + 1, t, i)) return true;
                if (i < t.Length && t[i] == '/') break;
            }
            return false;
        }
        if (ti == t.Length) return false;
        if (p[pi] == '?') return t[ti] != '/' && Glob(p, pi + 1, t, ti + 1);
        return char.ToLowerInvariant(p[pi]) == char.ToLowerInvariant(t[ti]) && Glob(p, pi + 1, t, ti + 1);
    }
}
