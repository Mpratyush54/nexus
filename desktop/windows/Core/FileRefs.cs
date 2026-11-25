#nullable enable
using System.Collections.Concurrent;
using System.Text.RegularExpressions;

namespace Nexus.Core;

// A file mentioned in a chat message: "NOTIFICATIONS.md", "src/app.ts:42:7",
// "C:\repo\file.cs". Line and column are 1-based when present.
internal sealed record FileRef(string Path, int? Line = null, int? Column = null);

internal readonly record struct FileRefSpan(int Index, int Length, FileRef Ref);

internal enum FileRefStatus { Found, NotFound, OutsideRoots, NoWorkspace }

internal sealed record FileRefResolution(FileRefStatus Status, IReadOnlyList<string> Matches, string Candidate = "");

// Finds file-like references in prose and resolves them against the chat's
// own workspace folder. Pure (no UI) so it is unit-tested.
internal static class FileRefs
{
    // Extensions that make a bare name ("README.md") a file. With a slash
    // ("docs/x.foo") any short extension is accepted.
    private static readonly HashSet<string> KnownExtensions = new(StringComparer.OrdinalIgnoreCase)
    {
        "md", "mdx", "markdown", "txt", "rst", "adoc",
        "cs", "csproj", "sln", "props", "targets", "xaml", "resx", "fs", "vb",
        "go", "mod", "sum", "rs", "toml", "py", "pyi", "ipynb",
        "js", "mjs", "cjs", "jsx", "ts", "tsx", "mts", "cts", "vue", "svelte", "astro",
        "json", "jsonc", "json5", "yaml", "yml", "xml", "html", "htm", "css", "scss", "sass", "less",
        "sh", "bash", "zsh", "fish", "ps1", "psm1", "psd1", "bat", "cmd",
        "c", "h", "cc", "cpp", "cxx", "hpp", "hh", "swift", "kt", "kts", "java", "gradle",
        "rb", "php", "lua", "sql", "graphql", "gql", "proto", "ini", "cfg", "conf", "env",
        "csv", "tsv", "dart", "ex", "exs", "erl", "hs", "scala", "clj", "tf", "hcl",
        "dockerfile", "gitignore", "gitattributes", "editorconfig", "lock",
        "svg", "png", "jpg", "jpeg", "gif", "webp", "ico", "pdf",
    };

    private static readonly HashSet<string> ExtensionlessNames = new(StringComparer.OrdinalIgnoreCase)
    {
        "Makefile", "Dockerfile", "Containerfile", "Procfile", "Gemfile", "Rakefile", "Jenkinsfile", "Vagrantfile",
        "LICENSE", "CODEOWNERS", "Justfile", "Taskfile",
    };

    // Library names written like files ("Node.js") are prose, not paths.
    private static readonly HashSet<string> ProseNames = new(StringComparer.OrdinalIgnoreCase)
    {
        "node.js", "next.js", "nuxt.js", "vue.js", "react.js", "angular.js", "express.js", "three.js", "d3.js",
        "chart.js", "ember.js", "backbone.js", "alpine.js", "solid.js", "p5.js", "socket.io", "deno.js",
        "preact.js", "meteor.js", "gatsby.js", "nest.js", "mithril.js", "riot.js", "inferno.js", "polymer.js",
        "handlebars.js", "mustache.js", "require.js", "underscore.js", "moment.js", "day.js", "anime.js",
        "highlight.js", "pdf.js", "video.js", "paper.js", "pixi.js", "babylon.js", "knockout.js", "zod.js",
    };

    private static readonly string[] DomainSuffixes = { ".com", ".org", ".net", ".io", ".dev", ".ai", ".co", ".app", ".gov", ".edu" };

    private const string Seg = @"[\w@+\-.]";
    private static readonly Regex Candidate = new(
        @"(?<![\w@$%&*=~\\/.:\-])" +
        @"(?<path>(?:[A-Za-z]:[\\/]|\.{1,2}[\\/]|~[\\/]|/)?(?:" + Seg + @"+[\\/])*" + Seg + @"*[\w@+\-])" +
        @"(?:(?::(?<line>\d+)(?::(?<col>\d+))?)|#L(?<line>\d+)(?:C(?<col>\d+))?)?" +
        @"(?![\w\\/@+\-]|\.\w)",
        RegexOptions.Compiled | RegexOptions.CultureInvariant);

    private static readonly Regex Url = new(@"\b(?:[a-z][a-z0-9+.\-]*://|mailto:|www\.)\S+", RegexOptions.Compiled | RegexOptions.IgnoreCase);

    // Every file-like reference in a run of prose (or an inline-code span).
    public static IReadOnlyList<FileRefSpan> Find(string text)
    {
        var found = new List<FileRefSpan>();
        if (string.IsNullOrEmpty(text) || text.IndexOfAny(new[] { '.', '/', '\\' }) < 0 && !ContainsExtensionlessName(text)) return found;
        var urls = Url.Matches(text).Select(m => (m.Index, End: m.Index + m.Length)).ToList();
        foreach (Match match in Candidate.Matches(text))
        {
            if (urls.Any(u => match.Index < u.End && match.Index + match.Length > u.Index)) continue;
            var end = match.Index + match.Length;
            // "res.json()" is a method call, not a file.
            if (end < text.Length && text[end] == '(') continue;
            var path = match.Groups["path"].Value;
            if (!IsFileLike(path)) continue;
            found.Add(new FileRefSpan(match.Index, match.Length, new FileRef(path, Number(match.Groups["line"]), Number(match.Groups["col"]))));
        }
        return found;
    }

    // A whole string that is exactly one reference (an inline-code span).
    public static FileRef? Parse(string text)
    {
        var trimmed = text.Trim();
        var spans = Find(trimmed);
        return spans.Count == 1 && spans[0].Index == 0 && spans[0].Length == trimmed.Length ? spans[0].Ref : null;
    }

    // A markdown link target: lenient (any local path), but never a URL.
    public static FileRef? ParseLinkTarget(string target)
    {
        var value = target.Trim().Trim('<', '>').Trim();
        var space = value.IndexOf(" \"", StringComparison.Ordinal);
        if (space > 0) value = value[..space];
        if (value.StartsWith("file:///", StringComparison.OrdinalIgnoreCase)) value = value[8..];
        else if (value.StartsWith("file://", StringComparison.OrdinalIgnoreCase)) value = value[7..];
        else if (value.Length == 0 || value.StartsWith('#') ||
                 !IsAbsolute(value) && Regex.IsMatch(value, @"^[a-z][a-z0-9+.\-]*:", RegexOptions.IgnoreCase)) return null;
        try { value = Uri.UnescapeDataString(value); } catch (UriFormatException) { }
        int? line = null, col = null;
        var anchor = Regex.Match(value, @"#L(\d+)(?:C(\d+))?$");
        if (anchor.Success) { line = Number(anchor.Groups[1]); col = Number(anchor.Groups[2]); value = value[..anchor.Index]; }
        else
        {
            var suffix = Regex.Match(value, @"(?<!^[A-Za-z]):(\d+)(?::(\d+))?$");
            if (suffix.Success) { line = Number(suffix.Groups[1]); col = Number(suffix.Groups[2]); value = value[..suffix.Index]; }
        }
        var hash = value.IndexOf('#');
        if (hash >= 0) value = value[..hash];
        if (value.Length == 0 || value.EndsWith('/') || value.EndsWith('\\')) return null;
        return new FileRef(value, line, col);
    }

    public static bool IsFileLike(string path)
    {
        if (path.Length < 2 || path.Length > 400) return false;
        var hasSlash = path.IndexOfAny(new[] { '/', '\\' }) >= 0;
        var name = LastSegment(path);
        if (name.Length == 0) return false;
        if (ExtensionlessNames.Contains(name)) return true;
        var dot = name.LastIndexOf('.');
        if (dot < 0 || dot == name.Length - 1) return false;
        var extension = name[(dot + 1)..];
        if (!hasSlash)
        {
            if (!KnownExtensions.Contains(extension)) return false;
            if (ProseNames.Contains(name)) return false;
            // "a.c" / "e.h" are abbreviations far more often than files.
            if (dot == 1 && extension.Length == 1) return false;
            return true;
        }
        if (IsAbsolute(path)) return Regex.IsMatch(extension, @"^[A-Za-z0-9_\-]{1,12}$");
        // "example.com/docs/a.md" is a URL without its scheme.
        var first = path.Split('/', '\\')[0];
        if (DomainSuffixes.Any(suffix => first.EndsWith(suffix, StringComparison.OrdinalIgnoreCase))) return false;
        return KnownExtensions.Contains(extension) || Regex.IsMatch(extension, @"^(?=.*[A-Za-z])[A-Za-z0-9_\-]{1,10}$");
    }

    public static bool IsAbsolute(string path) =>
        Regex.IsMatch(path, @"^[A-Za-z]:[\\/]") || path.StartsWith(@"\\", StringComparison.Ordinal);

    // Resolution, in order: an absolute path inside an allowed folder; the
    // exact path relative to the workspace; then a bounded search of the
    // workspace for the file name (deepest-matching suffix first).
    public static FileRefResolution Resolve(FileRef reference, string workspace, IEnumerable<string> otherRoots)
    {
        var path = reference.Path.Trim();
        var hasWorkspace = workspace.Length > 0 && Directory.Exists(workspace);
        var roots = new List<string>();
        if (hasWorkspace) roots.Add(workspace);
        roots.AddRange(otherRoots.Where(root => root.Length > 0 && Directory.Exists(root)));
        if (path.StartsWith("~/", StringComparison.Ordinal) || path.StartsWith(@"~\", StringComparison.Ordinal))
            path = System.IO.Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.UserProfile), path[2..]);
        string full;
        if (IsAbsolute(path) || path.StartsWith('/') && !hasWorkspace)
        {
            full = Normalize(path);
        }
        else
        {
            if (!hasWorkspace) return new FileRefResolution(FileRefStatus.NoWorkspace, Array.Empty<string>(), path);
            full = Normalize(System.IO.Path.Combine(workspace, path.TrimStart('/', '\\')));
        }
        if (full.Length == 0) return new FileRefResolution(FileRefStatus.NotFound, Array.Empty<string>(), path);
        var inside = roots.Any(root => IsInside(full, root));
        if (!inside) return new FileRefResolution(FileRefStatus.OutsideRoots, Array.Empty<string>(), full);
        if (File.Exists(full)) return new FileRefResolution(FileRefStatus.Found, new[] { full }, full);
        if (!hasWorkspace || IsAbsolute(path)) return new FileRefResolution(FileRefStatus.NotFound, Array.Empty<string>(), full);
        var matches = Search(workspace, path);
        return matches.Count > 0
            ? new FileRefResolution(FileRefStatus.Found, matches, full)
            : new FileRefResolution(FileRefStatus.NotFound, Array.Empty<string>(), full);
    }

    public static bool IsInside(string path, string root)
    {
        var normalRoot = Normalize(root);
        if (normalRoot.Length == 0) return false;
        var normalPath = Normalize(path);
        normalRoot = normalRoot.TrimEnd('\\', '/');
        return normalPath.Equals(normalRoot, StringComparison.OrdinalIgnoreCase) ||
               normalPath.StartsWith(normalRoot + System.IO.Path.DirectorySeparatorChar, StringComparison.OrdinalIgnoreCase);
    }

    private static readonly HashSet<string> SkippedFolders = new(StringComparer.OrdinalIgnoreCase)
    {
        "node_modules", ".git", "bin", "obj", "dist", "build", ".venv", "venv", "__pycache__", ".next", "target", ".vs", ".idea",
    };

    public const int MaxSearchDepth = 8;
    public const int MaxSearchMatches = 5;
    private const int MaxFoldersVisited = 20000;
    private static readonly ConcurrentDictionary<string, (DateTime At, IReadOnlyList<string> Matches)> SearchCache = new(StringComparer.OrdinalIgnoreCase);
    private static readonly TimeSpan CacheLifetime = TimeSpan.FromMinutes(2);

    // Breadth-first, so shallow files come first; bounded by depth, folder
    // count and match count. Cached per workspace + name for a short while.
    public static IReadOnlyList<string> Search(string workspace, string relativePath)
    {
        var name = LastSegment(relativePath);
        if (name.Length == 0 || name.IndexOfAny(new[] { '*', '?' }) >= 0 || !Directory.Exists(workspace)) return Array.Empty<string>();
        var key = Normalize(workspace) + "|" + name;
        if (SearchCache.TryGetValue(key, out var cached) && DateTime.UtcNow - cached.At < CacheLifetime)
            return Rank(cached.Matches, relativePath);
        var matches = new List<string>();
        var queue = new Queue<(string Dir, int Depth)>();
        queue.Enqueue((workspace, 0));
        var visited = 0;
        var options = new EnumerationOptions { IgnoreInaccessible = true, RecurseSubdirectories = false, AttributesToSkip = System.IO.FileAttributes.ReparsePoint | System.IO.FileAttributes.System };
        while (queue.Count > 0 && matches.Count < MaxSearchMatches && visited++ < MaxFoldersVisited)
        {
            var (dir, depth) = queue.Dequeue();
            try
            {
                foreach (var file in Directory.EnumerateFiles(dir, name, options))
                {
                    if (!string.Equals(System.IO.Path.GetFileName(file), name, StringComparison.OrdinalIgnoreCase)) continue;
                    matches.Add(file);
                    if (matches.Count >= MaxSearchMatches) break;
                }
                if (depth >= MaxSearchDepth) continue;
                foreach (var child in Directory.EnumerateDirectories(dir, "*", options))
                    if (!SkippedFolders.Contains(System.IO.Path.GetFileName(child))) queue.Enqueue((child, depth + 1));
            }
            catch (IOException) { }
            catch (UnauthorizedAccessException) { }
        }
        if (SearchCache.Count >= 256) SearchCache.Clear();
        SearchCache[key] = (DateTime.UtcNow, matches);
        return Rank(matches, relativePath);
    }

    public static void ClearSearchCache() => SearchCache.Clear();

    // Matches that end with the mentioned folders ("docs/x.md") first.
    private static IReadOnlyList<string> Rank(IReadOnlyList<string> matches, string relativePath)
    {
        var suffix = relativePath.Replace('/', '\\').TrimStart('.', '\\');
        return matches
            .OrderByDescending(m => m.Replace('/', '\\').EndsWith("\\" + suffix, StringComparison.OrdinalIgnoreCase))
            .ThenBy(m => m.Count(c => c is '\\' or '/'))
            .ToList();
    }

    private static string LastSegment(string path)
    {
        var trimmed = path.TrimEnd('/', '\\');
        var slash = trimmed.LastIndexOfAny(new[] { '/', '\\' });
        return slash >= 0 ? trimmed[(slash + 1)..] : trimmed;
    }

    private static string Normalize(string path)
    {
        try { return System.IO.Path.GetFullPath(path); }
        catch (Exception ex) when (ex is ArgumentException or NotSupportedException or PathTooLongException) { return ""; }
    }

    private static bool ContainsExtensionlessName(string text) =>
        ExtensionlessNames.Any(name => text.Contains(name, StringComparison.OrdinalIgnoreCase));

    private static int? Number(Group group) =>
        group.Success && int.TryParse(group.Value, out var value) && value > 0 ? value : null;
}
