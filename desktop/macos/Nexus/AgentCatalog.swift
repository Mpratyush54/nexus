import Foundation

struct Harness: Identifiable {
    var id: String { name }
    var name: String
    var resume: String
    var ideHistory: Bool
}

enum AgentCatalog {
    static let all: [Harness] = [
        Harness(name: "Claude Code", resume: "Native resume", ideHistory: false),
        Harness(name: "Codex CLI", resume: "Native resume", ideHistory: false),
        Harness(name: "Cursor CLI", resume: "Native resume", ideHistory: false),
        Harness(name: "Cursor IDE", resume: "Doesn't support resume; restores into chat history", ideHistory: true),
        Harness(name: "Gemini CLI", resume: "Native resume", ideHistory: false),
        Harness(name: "Antigravity", resume: "Native resume on the CLI. The IDE doesn't support resume; restores into chat history", ideHistory: true),
        Harness(name: "GitHub Copilot CLI", resume: "Native resume", ideHistory: false),
        Harness(name: "OpenCode", resume: "Native resume", ideHistory: false),
        Harness(name: "Windsurf", resume: "Doesn't support resume; restores into chat history", ideHistory: true),
        Harness(name: "Kimi Code", resume: "Native resume", ideHistory: false),
        Harness(name: "Hermes Agent", resume: "Native resume", ideHistory: false),
        Harness(name: "Grok CLI", resume: "Seeded", ideHistory: false),
        Harness(name: "Codeium", resume: "View-only", ideHistory: false),
        Harness(name: "Command Code", resume: "Seeded", ideHistory: false),
        Harness(name: "cagent", resume: "Seeded", ideHistory: false),
        Harness(name: "Z Code", resume: "Seeded", ideHistory: false),
        Harness(name: "DeepSeek CLI", resume: "Seeded", ideHistory: false)
    ]
}
