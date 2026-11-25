#nullable enable
using System.Text.Json;

namespace Nexus.Core;

/// <summary>One agent's state on this computer, from harness.available.</summary>
/// <param name="State">ready, not_installed, cannot_start or signed_out.</param>
/// <param name="Problem">A plain sentence for people ("" when ready).</param>
/// <param name="Detail">Technical detail, only for an "Advanced details" expander.</param>
internal sealed record AgentState(string Agent, string Name, bool Usable, string State, string Problem, string Detail, string InstallUrl);

/// <summary>
/// Which coding agents can run chats here. Pickers list only usable agents;
/// Settings › Agents lists the rest with a "Set up" action. Pure: the
/// service that fills it lives in AgentAvailabilityService.
/// </summary>
internal static class AgentAvailability
{
    /// Every agent Nexus can chat with, in picker order (value, name).
    internal static readonly (string Value, string Name)[] ChatAgents =
    [
        ("claude", "Claude Code"), ("codex", "Codex"), ("cursor", "Cursor"), ("opencode", "OpenCode"), ("gemini", "Gemini"), ("kimi", "Kimi")
    ];

    internal static IReadOnlyList<AgentState> Parse(JsonElement? result)
    {
        var list = new List<AgentState>();
        if (result is not { ValueKind: JsonValueKind.Object } body || !body.TryGetProperty("agents", out var agents) || agents.ValueKind != JsonValueKind.Array)
            return list;
        foreach (var a in agents.EnumerateArray())
            if (FromStatus(a) is { } state) list.Add(state);
        return list;
    }

    /// One harness.status answer. Older engines sent only "installed" and a
    /// "problem"; usable then means installed.
    internal static AgentState? FromStatus(JsonElement a)
    {
        if (a.ValueKind != JsonValueKind.Object) return null;
        var agent = Normalize(NxJson.Text(a, "agent"));
        if (agent.Length == 0) return null;
        var installed = NxJson.Text(a, "installed") == "true";
        var usable = a.TryGetProperty("usable", out _) ? NxJson.Text(a, "usable") == "true" : installed;
        var state = NxJson.Text(a, "state");
        if (state.Length == 0) state = usable ? "ready" : installed ? "cannot_start" : "not_installed";
        var problem = NxJson.Text(a, "problem");
        var detail = NxJson.Text(a, "detail");
        // An older engine's problem text may be technical: keep it for the
        // details expander and say it plainly.
        if (!usable && (problem.Length == 0 || NxErrors.UsesJargon(problem)))
        {
            if (problem.Length > 0 && detail.Length == 0) detail = problem;
            problem = state == "not_installed" ? $"{DisplayName(agent)} isn't installed on this computer."
                : state == "signed_out" ? $"{DisplayName(agent)} isn't signed in on this computer."
                : $"Nexus couldn't start {DisplayName(agent)} on this computer.";
        }
        return new AgentState(agent, DisplayName(agent), usable, state, problem, detail, NxJson.Text(a, "install_url"));
    }

    /// The picker's choices: usable agents only. Before the first check has
    /// finished (states empty), every agent is offered so nothing blocks.
    internal static IReadOnlyList<(string Value, string Name)> PickerChoices(IReadOnlyList<AgentState> states, string? keep = null)
    {
        if (states.Count == 0) return ChatAgents;
        var usable = states.Where(s => s.Usable).Select(s => s.Agent).ToHashSet(StringComparer.OrdinalIgnoreCase);
        return ChatAgents.Where(c => usable.Contains(c.Value)).ToList();
    }

    /// The agent to select: the wanted one when usable, else the first usable
    /// one ("" when none is usable).
    internal static string Choose(IReadOnlyList<(string Value, string Name)> choices, string wanted)
    {
        var want = Normalize(wanted);
        if (choices.Any(c => c.Value == want)) return want;
        return choices.Count > 0 ? choices[0].Value : "";
    }

    internal static string Normalize(string agent)
    {
        var a = (agent ?? "").Trim().ToLowerInvariant();
        return a switch
        {
            "agent" or "cursor-cli" or "cursor-agent" or "cursor ide" => "cursor",
            "claude-code" or "claude_code" => "claude",
            _ => a
        };
    }

    internal static string DisplayName(string agent) =>
        ChatAgents.FirstOrDefault(c => c.Value == Normalize(agent)).Name ?? agent;

    /// The sentence a picker shows when no agent can run here.
    internal const string NoneReady = "No coding agent is ready on this computer yet. Set one up in Settings › Agents.";

    /// The one action that fixes a state, for buttons.
    internal static string FixLabel(AgentState s) => s.State switch
    {
        "signed_out" => "Sign in",
        "not_installed" => "Install",
        _ => "Fix it"
    };

    /// What to do, in a sentence, for an agent that isn't ready.
    internal static string FixSentence(AgentState s) => s.State switch
    {
        "signed_out" => $"Open {s.Name} once on its own and sign in. Nexus picks it up when you come back.",
        "not_installed" => $"Install {s.Name}, then come back. Nexus notices it within a few seconds.",
        _ => $"Reinstalling {s.Name} usually fixes this. Nexus checks again when you come back."
    };
}
