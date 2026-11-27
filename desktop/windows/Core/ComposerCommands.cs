#nullable enable
namespace Nexus.Core;

/// <summary>
/// The composer's slash commands: what "/" and "@" typed in the message box
/// mean, the built-in commands, and the agent's own custom commands found on
/// disk (names only). Pure apart from the directory listing, so the rules are
/// tested without a window.
/// </summary>
internal static class ComposerCommands
{
    /// <summary>What a command does when picked.</summary>
    public enum Kind
    {
        /// <summary>A Nexus action (/model, /new…), run in the app; nothing is sent.</summary>
        Action,
        /// <summary>Sent to the agent as typed (/compact, or one of its custom commands).</summary>
        Agent
    }

    internal sealed record Command(string Name, string Description, Kind Kind, string Glyph, string Source = "Nexus", bool TakesArgument = false);

    /// <summary>A "/" or "@" being typed: where it starts and what follows it up to the caret.</summary>
    internal readonly record struct Trigger(char Symbol, int Start, string Query)
    {
        /// <summary>Characters from the symbol up to the caret.</summary>
        public int Length => Query.Length + 1;
    }

    public const string Model = "model";
    public const string Mode = "mode";
    public const string New = "new";
    public const string Clear = "clear";
    public const string Compact = "compact";
    public const string Rewind = "rewind";
    public const string Help = "help";

    /// <summary>
    /// The trigger the caret is in, or null. "/" counts only at the very
    /// start of the message (a path like a/b never opens the menu); "@" counts
    /// at the start or after a space or new line, so mail@example.com doesn't.
    /// The query runs from the symbol to the caret and has no spaces.
    /// </summary>
    public static Trigger? Find(string text, int caret)
    {
        text ??= "";
        if (caret < 1 || caret > text.Length) return null;
        // Walk back from the caret to the start of the word.
        var i = caret - 1;
        while (i >= 0 && !char.IsWhiteSpace(text[i]) && text[i] != '@' && !(text[i] == '/' && i == 0)) i--;
        if (i < 0) return null;
        var symbol = text[i];
        if (symbol == '/' && i == 0)
        {
            var query = text[1..caret];
            return query.Contains('/') ? null : new Trigger('/', 0, query);
        }
        if (symbol == '@' && (i == 0 || char.IsWhiteSpace(text[i - 1])))
            return new Trigger('@', i, text[(i + 1)..caret]);
        return null;
    }

    /// <summary>"/model sonnet" → ("model", "sonnet"). Null unless the whole message is one command.</summary>
    public static (string Name, string Argument)? Parse(string text)
    {
        text = (text ?? "").Trim();
        if (text.Length < 2 || text[0] != '/') return null;
        var space = text.IndexOfAny(new[] { ' ', '\t', '\n', '\r' });
        var name = space < 0 ? text[1..] : text[1..space];
        if (name.Length == 0 || !name.All(IsNameChar)) return null;
        var argument = space < 0 ? "" : text[(space + 1)..].Trim();
        return (name.ToLowerInvariant(), argument);
    }

    private static bool IsNameChar(char c) => char.IsLetterOrDigit(c) || c is '-' or '_' or ':' or '.';

    /// <summary>Agents whose chats can be compacted by sending /compact.</summary>
    public static bool SupportsCompact(string agent) => agent is "claude";

    /// <summary>The built-in commands for this chat, in menu order.</summary>
    public static IReadOnlyList<Command> Builtins(string agent, bool live, bool canRewind)
    {
        var list = new List<Command>
        {
            new(Model, "Choose the model for this chat", Kind.Action, "\uE945", TakesArgument: true),
        };
        if (live) list.Add(new(Mode, "Ask every time, auto-approve edits or full access", Kind.Action, "\uE72E", TakesArgument: true));
        list.Add(new(New, "Start a new chat", Kind.Action, "\uE710"));
        list.Add(new(Clear, "Start fresh in the same folder and agent", Kind.Action, "\uE894"));
        if (SupportsCompact(agent)) list.Add(new(Compact, "Summarize the chat so far to free up context", Kind.Agent, "\uE73F"));
        if (canRewind) list.Add(new(Rewind, "Go back to an earlier checkpoint", Kind.Action, "\uE7A7"));
        list.Add(new(Help, "What you can type here", Kind.Action, "\uE897"));
        return list;
    }

    /// <summary>The permission mode a /mode argument names, or "" ("ask", "edits", "full" and friendly words).</summary>
    public static string ModeFrom(string argument) => (argument ?? "").Trim().ToLowerInvariant() switch
    {
        "ask" or "ask every time" or "default" or "safe" => "ask",
        "edits" or "edit" or "auto" or "auto-approve edits" or "acceptedits" => "edits",
        "full" or "full access" or "yolo" or "bypass" => "full",
        _ => ""
    };

    /// <summary>
    /// The agent's own commands, names only: Claude Code's
    /// .claude/commands/**/*.md in the project and the user's folder
    /// (sub-folders become "folder:name"), and Codex's ~/.codex/prompts/*.md
    /// ("prompts:name"). Capped and best-effort: an unreadable folder is skipped.
    /// </summary>
    public static IReadOnlyList<Command> DiscoverCustom(string agent, string projectDir, string homeDir, int max = 200)
    {
        var found = new Dictionary<string, Command>(StringComparer.OrdinalIgnoreCase);
        void Scan(string root, string source, string prefix, bool nested)
        {
            if (root.Length == 0 || !Directory.Exists(root)) return;
            try
            {
                var options = new EnumerationOptions { RecurseSubdirectories = nested, MaxRecursionDepth = 3, IgnoreInaccessible = true };
                foreach (var file in Directory.EnumerateFiles(root, "*.md", options))
                {
                    if (found.Count >= max) return;
                    var relative = Path.GetRelativePath(root, file);
                    var parts = relative[..^3].Split(new[] { Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar }, StringSplitOptions.RemoveEmptyEntries);
                    if (parts.Length == 0) continue;
                    var name = prefix + string.Join(":", parts);
                    if (!name.All(IsNameChar)) continue;
                    // The project's command wins over the user's one with the same name.
                    found.TryAdd(name, new Command(name, source, Kind.Agent, "\uE756", source));
                }
            }
            catch (Exception ex) when (ex is IOException or UnauthorizedAccessException or System.Security.SecurityException) { }
        }
        if (agent == "claude")
        {
            if (projectDir.Length > 0) Scan(Path.Combine(projectDir, ".claude", "commands"), "Project command", "", nested: true);
            if (homeDir.Length > 0) Scan(Path.Combine(homeDir, ".claude", "commands"), "Your command", "", nested: true);
        }
        else if (agent == "codex" && homeDir.Length > 0)
        {
            Scan(Path.Combine(homeDir, ".codex", "prompts"), "Your prompt", "prompts:", nested: false);
        }
        // Built-in names stay Nexus's.
        foreach (var reserved in new[] { Model, Mode, New, Clear, Compact, Rewind, Help }) found.Remove(reserved);
        return found.Values.OrderBy(c => c.Name, StringComparer.OrdinalIgnoreCase).ToList();
    }

    internal sealed record Ranked(Command Command, int[] Positions);

    /// <summary>The commands matching the typed name, best first; all of them (in order) when nothing is typed.</summary>
    public static IReadOnlyList<Ranked> Rank(IEnumerable<Command> commands, string query)
    {
        query = (query ?? "").Trim();
        var list = new List<(Ranked R, int Score, int Order)>();
        var order = 0;
        foreach (var command in commands)
        {
            order++;
            if (FuzzyMatch.Match(query, command.Name) is not { } m) continue;
            // A built-in beats a custom command that matches as well.
            var score = m.Score + (command.Source == "Nexus" ? 5 : 0);
            list.Add((new Ranked(command, m.Positions), score, order));
        }
        return list.OrderByDescending(x => query.Length == 0 ? 0 : x.Score).ThenBy(x => x.Order).Select(x => x.R).ToList();
    }
}
