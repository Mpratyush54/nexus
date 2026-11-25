using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using Nexus.Pages;

namespace Nexus;

public static class ShellProjects
{
    public static async Task<bool> RefreshSelectorAsync(
        ComboBox selector,
        NxClient core,
        Microsoft.UI.Dispatching.DispatcherQueue? queue,
        bool preferCaptureProject = true)
    {
        var state = ShellState.Current;
        var previousId = state.ProjectId;
        var status = await core.CallAsync(NxMethods.StatusGet);
        await UiThread.Resume(queue);
        var captureId = status.Result is { } st && st.TryGetProperty("capture", out var capture)
            ? NxJson.Text(capture, "project_id") : "";

        var projects = await core.CallAsync(NxMethods.ProjectsList);
        await UiThread.Resume(queue);
        if (!projects.Ok)
        {
            return false;
        }

        selector.Items.Clear();
        var list = NxJson.Items(projects.Result).ToList();
        foreach (var project in list)
        {
            var id = NxJson.Text(project, "id", "project_id");
            if (id.Length == 0)
            {
                continue;
            }
            selector.Items.Add(new ComboBoxItem
            {
                Content = ProjectLabels.Name(project),
                Tag = id
            });
        }

        if (selector.Items.Count == 0)
        {
            state.ClearProject(persist: false);
            return true;
        }

        var target = previousId.Length > 0 ? previousId :
            preferCaptureProject && captureId.Length > 0 ? captureId : "";
        var chosen = -1;
        for (var i = 0; i < selector.Items.Count; i++)
        {
            if ((selector.Items[i] as ComboBoxItem)?.Tag as string == target)
            {
                chosen = i;
                break;
            }
        }
        if (chosen < 0 && selector.Items.Count == 1)
        {
            chosen = 0;
        }

        ApplySelection(selector, chosen, suppressEvents: true);
        if (chosen >= 0)
        {
            SyncStateFromSelector(selector);
        }
        else if (previousId.Length > 0)
        {
            // Saved project no longer accessible — keep id in prefs but clear active scope.
            state.ClearProject(persist: false);
        }

        return true;
    }

    public static void SyncStateFromSelector(ComboBox selector)
    {
        if (selector.SelectedItem is ComboBoxItem item)
        {
            var id = item.Tag as string ?? "";
            var name = item.Content?.ToString() ?? id;
            ShellState.Current.SetProject(id, name);
        }
    }

    public static void SelectProjectId(ComboBox selector, string projectId, bool suppressEvents = true)
    {
        if (projectId.Length == 0)
        {
            return;
        }
        for (var i = 0; i < selector.Items.Count; i++)
        {
            if ((selector.Items[i] as ComboBoxItem)?.Tag as string == projectId)
            {
                ApplySelection(selector, i, suppressEvents);
                SyncStateFromSelector(selector);
                return;
            }
        }
        ShellState.Current.SetProject(projectId, projectId.Length > 0 ? projectId : "Project");
    }

    private static void ApplySelection(ComboBox selector, int index, bool suppressEvents)
    {
        if (index < 0 || index >= selector.Items.Count)
        {
            selector.SelectedIndex = -1;
            return;
        }
        selector.SelectedIndex = index;
    }
}
