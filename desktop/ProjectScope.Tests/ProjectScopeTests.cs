using System.Text.Json;
using Nexus.Core;
using Xunit;

namespace Nexus.Core.Tests;

public sealed class ProjectScopeTests
{
    [Fact]
    public void TimelineItemMatches_requires_uuid_equality()
    {
        var projectA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
        var projectB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb";
        var item = JsonDocument.Parse("""
            {"session_id":"s1","project_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","harness":"opencode"}
            """).RootElement;

        Assert.False(ProjectScope.TimelineItemMatches(item, projectA));
        Assert.True(ProjectScope.TimelineItemMatches(item, projectB));
    }

    [Fact]
    public void TimelineItemMatches_rejects_empty_shell_project()
    {
        var item = JsonDocument.Parse("""
            {"session_id":"s1","project_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
            """).RootElement;
        Assert.False(ProjectScope.TimelineItemMatches(item, ""));
    }

    [Fact]
    public void TimelineItemMatches_excludes_workspace_folder_mismatch()
    {
        var central = "cccccccc-cccc-cccc-cccc-cccccccccccc";
        var oddo = "dddddddd-dddd-dddd-dddd-dddddddddddd";
        var projects = JsonDocument.Parse("""
            {"items":[
              {"id":"cccccccc-cccc-cccc-cccc-cccccccccccc","folder_name":"central-memory"},
              {"id":"dddddddd-dddd-dddd-dddd-dddddddddddd","folder_name":"oddo-hackathon"}
            ]}
            """).RootElement;
        var item = JsonDocument.Parse("""
            {
              "session_id":"s1",
              "project_id":"cccccccc-cccc-cccc-cccc-cccccccccccc",
              "workspace_root_hint":"D:/repos/oddo-hackathon"
            }
            """).RootElement;

        Assert.False(ProjectScope.TimelineItemMatches(item, central, projects));
        Assert.False(ProjectScope.TimelineItemMatches(item, oddo, projects));
    }

    [Fact]
    public void SessionProjectId_never_uses_shell_fallback()
    {
        var id = ProjectScope.SessionProjectId("", "D:/central-memory", null);
        Assert.Equal("", id);
    }
}
