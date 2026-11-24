using System.Text.Json;

namespace Nexus.Core;

// Finds the local folder for a cloud project so a new chat can run without
// asking "choose the project folder" every time. Sources, in order:
// the folder last used for this project, the project's recorded path,
// approved capture folders resolved to this project, then folders where
// agents ran whose name matches the project.
internal static class ProjectFolders
{
    public static void Remember(string projectId, string folder)
    {
        if (projectId.Length == 0 || folder.Length == 0 || !Directory.Exists(folder)) return;
        AppPrefs.Data.ProjectFolders ??= new Dictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        if (AppPrefs.Data.ProjectFolders.TryGetValue(projectId, out var existing) &&
            string.Equals(existing, folder, StringComparison.OrdinalIgnoreCase)) return;
        AppPrefs.Data.ProjectFolders[projectId] = folder;
        AppPrefs.Save();
    }

    public static async Task<string> ResolveAsync(NxClient core, string projectId, string projectName)
    {
        if (projectId.Length == 0) return "";
        if (AppPrefs.Data.ProjectFolders?.TryGetValue(projectId, out var saved) == true && Directory.Exists(saved)) return saved;

        var projects = await core.CallAsync(NxMethods.ProjectsList);
        if (projects.Ok)
        {
            var recorded = NxJson.Items(projects.Result)
                .Where(item => NxJson.Text(item, "id", "project_id") == projectId)
                .Select(item => NxJson.Text(item, "path", "workspace_path", "root_path"))
                .FirstOrDefault(Directory.Exists);
            if (!string.IsNullOrEmpty(recorded)) return recorded;
        }

        var status = await core.CallAsync(NxMethods.StatusGet);
        if (status.Result is not { } body || !body.TryGetProperty("capture", out var capture) || capture.ValueKind != JsonValueKind.Object) return "";
        foreach (var row in Rows(capture, "workspace_details"))
            if (NxJson.Text(row, "project_id") == projectId && Directory.Exists(NxJson.Text(row, "path")))
                return NxJson.Text(row, "path");
        if (projectName.Length == 0) return "";
        bool NameMatches(string path) => string.Equals(Path.GetFileName(path.TrimEnd('\\', '/')), projectName, StringComparison.OrdinalIgnoreCase);
        foreach (var row in Rows(capture, "discovered_workspaces"))
        {
            var path = NxJson.Text(row, "path");
            if (NameMatches(path) && Directory.Exists(path)) return path;
        }
        if (capture.TryGetProperty("approved_workspaces", out var approved) && approved.ValueKind == JsonValueKind.Array)
            foreach (var item in approved.EnumerateArray())
                if (item.ValueKind == JsonValueKind.String && item.GetString() is { } path && NameMatches(path) && Directory.Exists(path))
                    return path;
        return "";
    }

    private static IEnumerable<JsonElement> Rows(JsonElement capture, string key) =>
        capture.TryGetProperty(key, out var rows) && rows.ValueKind == JsonValueKind.Array ? rows.EnumerateArray() : Enumerable.Empty<JsonElement>();
}
