using System.Text.Json;

namespace Nexus.Core;

public static class ProjectLabels
{
    public static string Name(JsonElement project)
    {
        var name = NxJson.Text(project, "display_name", "folder_name", "name", "slug", "title").Trim();
        return name.Length > 0 ? name : Fallback(NxJson.Text(project, "id", "project_id"));
    }

    public static string ForSession(string projectId, string workspaceRoot, JsonElement? projects)
    {
        foreach (var project in NxJson.Items(projects))
        {
            if (NxJson.Text(project, "id", "project_id") == projectId) return Name(project);
        }
        var root = workspaceRoot.TrimEnd('\\', '/');
        var separator = Math.Max(root.LastIndexOf('\\'), root.LastIndexOf('/'));
        if (root.Length > 0)
        {
            var folder = root[(separator + 1)..];
            if (folder.Length > 0) return folder;
        }
        return Fallback(projectId);
    }

    private static string Fallback(string id) => id.Length > 0 ? "Unnamed project" : "Project";
}
