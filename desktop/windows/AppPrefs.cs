using System.Text.Json;

namespace Nexus;

/// <summary>Persisted WinUI shell preferences under %LOCALAPPDATA%\Nexus\ui-prefs.json.</summary>
public static class AppPrefs
{
    private static readonly JsonSerializerOptions JsonOptions = new() { WriteIndented = true };

    public static PrefsData Data { get; private set; } = new();

    public static bool IsFirstRun { get; private set; }

    public static void Load()
    {
        try
        {
            var path = PrefsPath();
            if (!File.Exists(path))
            {
                IsFirstRun = true;
                Data = new PrefsData { SetupCompleted = false };
                return;
            }
            IsFirstRun = false;
            var json = File.ReadAllText(path);
            Data = JsonSerializer.Deserialize<PrefsData>(json) ?? new PrefsData();
            // Preferences from before the setup wizard existed belong to
            // someone already using Nexus: don't greet them with setup.
            Data.SetupCompleted ??= true;
        }
        catch
        {
            IsFirstRun = false;
            Data = new PrefsData();
        }
    }

    public static void Save()
    {
        try
        {
            var path = PrefsPath();
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            File.WriteAllText(path, JsonSerializer.Serialize(Data, JsonOptions));
        }
        catch
        {
            // Best-effort; shell still works for this session.
        }
    }

    public static void ApplyToShell(ShellState state)
    {
        if (Data.ProjectId.Length > 0)
        {
            state.SetProject(Data.ProjectId, Data.ProjectName, persist: false);
        }
        if (Data.MemoryScope.Length > 0)
        {
            state.MemoryScope = Data.MemoryScope;
        }
    }

    public static void CaptureFromShell(ShellState state)
    {
        Data.ProjectId = state.ProjectId;
        Data.ProjectName = state.ProjectName;
        Data.MemoryScope = state.MemoryScope;
        Save();
    }

    public static string LastNavTag
    {
        get => Data.LastNav;
        set
        {
            if (string.Equals(Data.LastNav, value, StringComparison.OrdinalIgnoreCase))
            {
                return;
            }
            Data.LastNav = value;
            Save();
        }
    }

    private static string PrefsPath()
    {
        var root = Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData);
        return Path.Combine(root, "Nexus", "ui-prefs.json");
    }

    /// <summary>True once the setup wizard was finished or skipped.</summary>
    public static bool SetupCompleted
    {
        get => Data.SetupCompleted == true;
        set
        {
            Data.SetupCompleted = value;
            Save();
        }
    }

    public sealed class PrefsData
    {
        // null = written before the wizard existed (see Load).
        public bool? SetupCompleted { get; set; }
        // Installed version seen at the last start ("Updated to vX" toast).
        public string LastVersion { get; set; } = "";
        public string ProjectId { get; set; } = "";
        public string ProjectName { get; set; } = "";
        public string LastNav { get; set; } = "timeline";
        public string MemoryScope { get; set; } = "all";
        // Agent last chosen for a new chat (Home's start composer).
        public string LastStartAgent { get; set; } = "claude";
        // Appearance: "system" (follow Windows), "light" or "dark".
        public string Theme { get; set; } = "system";
        public Dictionary<string, string> ChatModels { get; set; } = new(StringComparer.OrdinalIgnoreCase);
        // project id -> local folder last used for chats in that project
        public Dictionary<string, string> ProjectFolders { get; set; } = new(StringComparer.OrdinalIgnoreCase);
    }
}
