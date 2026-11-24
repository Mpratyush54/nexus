#nullable enable
namespace Nexus.Core;

/// <summary>
/// Where a new chat starts when the sidebar shows "All projects".
///
/// A project restored from last session's preferences can be months stale
/// (an old project the person no longer works in), so the default is
/// recomputed from what is happening now:
///   1. the project whose local folder is the workspace Nexus captures from;
///   2. otherwise the project of the most recently active chat.
/// An empty result keeps the current choice.
/// </summary>
public static class DefaultProject
{
    public static string Pick(
        IEnumerable<string> chatProjectsNewestFirst,
        IReadOnlyDictionary<string, string> projectFolders,
        string workspaceRoot,
        ICollection<string> knownProjects)
    {
        var workspace = NormalizePath(workspaceRoot);
        if (workspace.Length > 0)
        {
            foreach (var (project, folder) in projectFolders)
            {
                if (knownProjects.Contains(project) && NormalizePath(folder) == workspace) return project;
            }
        }
        foreach (var project in chatProjectsNewestFirst)
        {
            if (project.Length > 0 && knownProjects.Contains(project)) return project;
        }
        return "";
    }

    internal static string NormalizePath(string? path)
    {
        var text = (path ?? "").Trim().Replace('/', '\\').TrimEnd('\\');
        return text.ToLowerInvariant();
    }
}
