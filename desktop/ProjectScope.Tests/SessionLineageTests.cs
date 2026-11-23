using System.Text.Json;
using Nexus.Pages;
using Xunit;

namespace Nexus.Core.Tests;

public sealed class SessionLineageTests
{
    [Fact]
    public void Explicit_fork_is_a_primary_chat_even_with_a_parent()
    {
        var item = JsonDocument.Parse("""{"lineage_kind":"fork","parent_session_id":"parent","title":"Forked chat"}""").RootElement;
        Assert.True(SessionLineage.IsForkSession(item));
        Assert.False(SessionLineage.IsSubagentSession(item));
    }

    [Fact]
    public void Agent_spawn_is_still_hidden_from_primary_chat_list()
    {
        var item = JsonDocument.Parse("""{"lineage_kind":"agent_spawn","parent_session_id":"parent"}""").RootElement;
        Assert.False(SessionLineage.IsForkSession(item));
        Assert.True(SessionLineage.IsSubagentSession(item));
    }

    [Fact]
    public void Subagent_title_prefers_description_then_first_request_then_type()
    {
        Assert.Equal("Map memory storage model", SessionLineage.SubagentTitle("  Map memory storage model ", "List files", "Explore"));
        Assert.Equal("List the top-level files", SessionLineage.SubagentTitle("", "\n## List the top-level files\nNo changes.", "Explore"));
        Assert.Equal("Sub-agent: Explore", SessionLineage.SubagentTitle("", "", "Explore"));
        Assert.Equal("Sub-agent chat", SessionLineage.SubagentTitle("", "", ""));
        Assert.True(SessionLineage.SubagentTitle("", new string('x', 200), "").Length <= 80);
    }
}
