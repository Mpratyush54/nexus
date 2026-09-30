namespace Nexus.Pages;

public static class AgentCatalog
{
    public static readonly Harness[] All =
    [
        new("Claude Code", "Native resume", false),
        new("Codex CLI", "Native resume", false),
        new("Cursor CLI", "Native resume", false),
        new("Cursor IDE", "Doesn't support resume; restores into chat history", true),
        new("Gemini CLI", "Native resume", false),
        new("Antigravity", "Native resume on the CLI. The IDE doesn't support resume; restores into chat history", true),
        new("GitHub Copilot CLI", "Native resume", false),
        new("OpenCode", "Native resume", false),
        new("Windsurf", "Doesn't support resume; restores into chat history", true),
        new("Kimi Code", "Native resume", false),
        new("Hermes Agent", "Native resume", false),
        new("Grok CLI", "Seeded", false),
        new("Codeium", "View-only", false),
        new("Command Code", "Seeded", false),
        new("cagent", "Seeded", false),
        new("Z Code", "Seeded", false),
        new("DeepSeek CLI", "Seeded", false)
    ];

    public static IEnumerable<string> Names => All.Select(item => item.Name);

    public readonly record struct Harness(string Name, string Resume, bool IdeHistory);
}
