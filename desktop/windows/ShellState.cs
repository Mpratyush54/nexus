using CommunityToolkit.Mvvm.ComponentModel;

namespace Nexus;

public partial class ShellState : ObservableObject
{
    public static ShellState Current { get; } = new();

    public static event EventHandler? Changed;

    [ObservableProperty] private string banner = "";
    [ObservableProperty] private bool offline;
    [ObservableProperty] private int pendingUploads;
    [ObservableProperty] private bool capturePaused;

    [ObservableProperty] private string projectId = "";
    [ObservableProperty] private string projectName = "";
    [ObservableProperty] private string memoryScope = "all";

    partial void OnOfflineChanged(bool value) => Changed?.Invoke(this, EventArgs.Empty);

    partial void OnMemoryScopeChanged(string value)
    {
        AppPrefs.Data.MemoryScope = value;
        AppPrefs.Save();
    }

    /// <summary>Display label for the shell project selector (never empty when unset).</summary>
    public string ProjectDisplay =>
        ProjectName.Length > 0 ? ProjectName :
        ProjectId.Length > 0 ? ProjectId : "Choose a project";

    public bool HasProject => ProjectId.Length > 0;

    /// <summary>Raised when the chat list's project filter changes.</summary>
    public static event EventHandler? ChatFilterChanged;

    /// <summary>The chat list's project filter: a project id, or "" for all projects.
    /// The shell project (where new chats start) is kept when this is "".</summary>
    public string ChatFilter { get; private set; } = "";

    public void SetChatFilter(string projectId)
    {
        if (string.Equals(ChatFilter, projectId, StringComparison.Ordinal)) return;
        ChatFilter = projectId;
        ChatFilterChanged?.Invoke(this, EventArgs.Empty);
    }

    public void SetProject(string id, string name, bool persist = true)
    {
        var normalizedName = name.Length > 0 ? name : id;
        if (string.Equals(ProjectId, id, StringComparison.Ordinal) &&
            string.Equals(ProjectName, normalizedName, StringComparison.Ordinal))
        {
            return;
        }
        ProjectId = id;
        ProjectName = normalizedName;
        if (persist)
        {
            AppPrefs.CaptureFromShell(this);
        }
        Changed?.Invoke(this, EventArgs.Empty);
    }

    public void ClearProject(bool persist = true)
    {
        SetProject("", "", persist);
    }
}
