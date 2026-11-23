using System.Text.Json;
using System.Text.RegularExpressions;

namespace Nexus.Core;

/// <summary>Client-side project scoping for timeline/session lists.</summary>
public static class ProjectScope
{
    private static readonly Regex UuidPattern = new(
        "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$",
        RegexOptions.IgnoreCase | RegexOptions.CultureInvariant);

    public static bool LooksLikeUuid(string value) =>
        value.Length == 36 && UuidPattern.IsMatch(value);

    /// <summary>Canonical project id from session payload only — never the shell selection.</summary>
    public static string SessionProjectId(string fromSession, string workspaceRoot, JsonElement? projects = null)
    {
        if (LooksLikeUuid(fromSession))
        {
            return fromSession;
        }
        var inferred = InferProjectIdFromWorkspace(workspaceRoot, projects);
        if (LooksLikeUuid(inferred))
        {
            return inferred;
        }
        return fromSession.Trim();
    }

    public static bool TimelineItemMatches(JsonElement item, string selectedProjectId, JsonElement? projects = null)
    {
        if (selectedProjectId.Length == 0 || !LooksLikeUuid(selectedProjectId))
        {
            return false;
        }

        var itemProject = NxJson.Text(item, "project_id", "projectId");
        if (!LooksLikeUuid(itemProject) ||
            !string.Equals(itemProject, selectedProjectId, StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }

        var workspace = SessionWorkspacePath(item);
        if (workspace.Length == 0 || projects is null)
        {
            return true;
        }

        var inferred = InferProjectIdFromWorkspace(workspace, projects);
        if (!LooksLikeUuid(inferred))
        {
            return true;
        }

        return string.Equals(inferred, selectedProjectId, StringComparison.OrdinalIgnoreCase);
    }

    public static string SessionWorkspacePath(JsonElement item) =>
        NxJson.Text(item, "workspace_root_hint", "workspace_root", "workspace", "cwd", "repo");

    public static string InferProjectIdFromWorkspace(string workspaceRoot, JsonElement? projects)
    {
        var root = NormalizePath(workspaceRoot);
        if (root.Length == 0)
        {
            return "";
        }

        var folder = PathBasename(root);
        foreach (var project in NxJson.Items(projects))
        {
            var id = NxJson.Text(project, "id", "project_id");
            if (!LooksLikeUuid(id))
            {
                continue;
            }
            var slug = NormalizeSlug(NxJson.Text(project, "folder_name", "slug", "name", "display_name"));
            if (slug.Length > 0 && string.Equals(slug, folder, StringComparison.OrdinalIgnoreCase))
            {
                return id;
            }
            var pathHint = NormalizePath(NxJson.Text(project, "path", "workspace_path", "root_path"));
            if (pathHint.Length > 0 && PathsEqual(root, pathHint))
            {
                return id;
            }
        }
        return "";
    }

    private static string PathBasename(string normalizedPath)
    {
        var last = normalizedPath.LastIndexOf('/');
        return last >= 0 ? normalizedPath[(last + 1)..] : normalizedPath;
    }

    private static string NormalizePath(string value)
    {
        if (value.Length == 0)
        {
            return "";
        }
        return value.Trim().Replace('\\', '/').TrimEnd('/');
    }

    private static bool PathsEqual(string a, string b) =>
        string.Equals(NormalizePath(a), NormalizePath(b), StringComparison.OrdinalIgnoreCase);

    private static string NormalizeSlug(string value) =>
        value.Trim().Trim('\\', '/').ToLowerInvariant();
}
