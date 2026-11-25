using System.Text.Json;
using Nexus.Core;
using Xunit;

public class AgentAvailabilityTests
{
    private static JsonElement Json(string text) => JsonDocument.Parse(text).RootElement.Clone();

    private const string Answer = """
        {"agents":[
          {"agent":"claude","installed":true,"usable":true,"state":"ready","signed_in":"yes"},
          {"agent":"codex","installed":true,"usable":true,"state":"ready","signed_in":"yes"},
          {"agent":"cursor","installed":false,"usable":false,"state":"not_installed","problem":"Cursor isn't installed on this computer.","detail":"No \"cursor-agent\" command was found on PATH."},
          {"agent":"opencode","installed":true,"usable":true,"state":"ready"},
          {"agent":"gemini","installed":true,"usable":false,"state":"signed_out","problem":"Gemini isn't signed in on this computer.","install_url":"https://geminicli.com/docs/get-started/installation/"},
          {"agent":"kimi","installed":true,"usable":false,"state":"cannot_start","problem":"Nexus couldn't start Kimi Code on this computer."}
        ]}
        """;

    [Fact]
    public void PickerListsOnlyUsableAgents()
    {
        var states = AgentAvailability.Parse(Json(Answer));
        var choices = AgentAvailability.PickerChoices(states).Select(c => c.Value).ToList();
        Assert.Equal(new[] { "claude", "codex", "opencode" }, choices);
    }

    [Fact]
    public void BeforeTheFirstCheckEveryAgentIsOfferedSoNothingBlocks()
    {
        Assert.Equal(AgentAvailability.ChatAgents.Length, AgentAvailability.PickerChoices(Array.Empty<AgentState>()).Count);
    }

    [Fact]
    public void ChooseKeepsTheWantedAgentOrFallsBackToTheFirstUsable()
    {
        var choices = AgentAvailability.PickerChoices(AgentAvailability.Parse(Json(Answer)));
        Assert.Equal("opencode", AgentAvailability.Choose(choices, "opencode"));
        Assert.Equal("claude", AgentAvailability.Choose(choices, "gemini"));
        Assert.Equal("claude", AgentAvailability.Choose(choices, "claude-code"));
        Assert.Equal("", AgentAvailability.Choose(Array.Empty<(string, string)>(), "codex"));
    }

    [Fact]
    public void OlderEngineProblemTextIsReworded()
    {
        // beta.17 sent only installed + a technical problem.
        var state = AgentAvailability.FromStatus(Json("""
            {"agent":"opencode","name":"OpenCode","installed":false,
             "problem":"opencode is installed only as a shell shim; its native executable was not found"}
            """))!;
        Assert.False(state.Usable);
        Assert.Equal("OpenCode isn't installed on this computer.", state.Problem);
        Assert.Contains("shell shim", state.Detail);
        foreach (var jargon in new[] { "shim", "PATH", "CLI", "executable" })
            Assert.DoesNotContain(jargon, state.Problem);
    }

    [Fact]
    public void FixActionsAreOneClearStep()
    {
        var states = AgentAvailability.Parse(Json(Answer));
        Assert.Equal("Sign in", AgentAvailability.FixLabel(states.Single(s => s.Agent == "gemini")));
        Assert.Equal("Install", AgentAvailability.FixLabel(states.Single(s => s.Agent == "cursor")));
        Assert.Equal("Fix it", AgentAvailability.FixLabel(states.Single(s => s.Agent == "kimi")));
        foreach (var s in states.Where(s => !s.Usable))
        {
            Assert.False(NxErrors.UsesJargon(s.Problem), s.Problem);
            Assert.False(NxErrors.UsesJargon(AgentAvailability.FixSentence(s)), AgentAvailability.FixSentence(s));
        }
    }
}

public class ChatListRetryTests
{
    [Fact]
    public void AFailedProjectLoadRetriesAndSaysTheListIsIncomplete()
    {
        Assert.Equal(TimeSpan.FromSeconds(2), ChatListRetry.NextDelay(0));
        Assert.NotNull(ChatListRetry.NextDelay(2));
        Assert.Null(ChatListRetry.NextDelay(3));
        // While retrying, a partial preview is labelled as such.
        Assert.Equal("Loading this project's earlier chats…", ChatListRetry.Status(project: true, shown: 2, unreachable: true, willRetry: true, error: null));
        // Retries used up: the person is told, never shown a silent partial list.
        Assert.StartsWith("Some of this project's chats couldn't load.", ChatListRetry.Status(true, 2, true, false, null));
        Assert.Equal("Your chats appear here when Nexus reconnects.", ChatListRetry.Status(true, 0, true, false, null));
        // All projects keep the old behaviour: the banner speaks for itself.
        Assert.Equal("", ChatListRetry.Status(false, 30, true, false, null));
    }
}

public class NxErrorsPlainLanguageTests
{
    [Fact]
    public void DetailAfterTheSentenceIsHiddenBehindARef()
    {
        var raw = "Nexus couldn't start OpenCode on this computer. [detail: launcher C:\\npm\\opencode.cmd has no runnable program behind it]";
        var (message, reference) = NxErrors.Friendly(raw);
        Assert.Equal("Nexus couldn't start OpenCode on this computer. (ref " + reference + ")", message);
        Assert.DoesNotContain("opencode.cmd", message);
        var (sentence, detail) = NxErrors.SplitDetail(raw);
        Assert.Equal("Nexus couldn't start OpenCode on this computer.", sentence);
        Assert.Contains("opencode.cmd", detail);
    }

    [Theory]
    [InlineData("opencode is installed only as a shell shim; its native executable was not found")]
    [InlineData("OpenCode is not installed or is not available to Nexus. Install its CLI, sign in, then restart Nexus to refresh PATH.")]
    [InlineData("continue: look path \"codex\": exec: \"codex\": executable file not found in %PATH%")]
    public void JargonIsNeverShown(string raw)
    {
        var (message, reference) = NxErrors.Friendly(raw);
        Assert.NotEmpty(reference);
        foreach (var jargon in new[] { "shim", "PATH", "CLI", "executable", "exec:" })
            Assert.DoesNotContain(jargon, message);
    }

    [Fact]
    public void AgentFailuresNameTheAgent()
    {
        var (generic, reference) = NxErrors.Friendly("exit status 1: panic in agent");
        Assert.Equal("Codex stopped before it finished replying. Try sending again. (ref " + reference + ")", NxErrors.ForAgent(generic, "Codex"));
        Assert.Equal("Choose a project folder on this computer before sending", NxErrors.ForAgent("Choose a project folder on this computer before sending", "Codex"));
    }
}
