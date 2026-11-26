using System.Text.Json;
using Nexus.Core;

namespace Nexus;

/// <summary>
/// Preferences for the shell features (command palette, quick ask, text
/// size, sidebar density), in %LOCALAPPDATA%\Nexus\shell-prefs.json, next to
/// ui-prefs.json. A separate file keeps these independent of AppPrefs.
/// </summary>
internal static class ShellPrefs
{
    private static readonly JsonSerializerOptions JsonOptions = new() { WriteIndented = true };
    private static Data? s_data;

    public static event Action? Changed;

    public sealed class Data
    {
        /// <summary>The global quick-ask hotkey is registered.</summary>
        public bool QuickAskEnabled { get; set; } = true;
        /// <summary>"Alt+Space" (Windows) or "Option+Space" (macOS) by default; empty = the default.</summary>
        public string QuickAskHotkey { get; set; } = "";
        /// <summary>After sending: open the chat (false) or keep it in the background and notify when done (true).</summary>
        public bool QuickAskBackground { get; set; }
        public string QuickAskAgent { get; set; } = "";
        public string QuickAskProject { get; set; } = "";
        /// <summary>Transcript text size, percent (80 to 150).</summary>
        public int TextScale { get; set; } = 100;
        /// <summary>"comfortable" (three-line chat rows) or "compact" (two lines).</summary>
        public string Density { get; set; } = "comfortable";
        /// <summary>Command palette picks, most recent first.</summary>
        public List<string> PaletteRecent { get; set; } = new();
    }

    public static Data Current => s_data ??= Load();

    public const int MinScale = 80, MaxScale = 150, ScaleStep = 10;

    public static string QuickAskKeys => Current.QuickAskHotkey is { Length: > 0 } keys && HotkeyText.Parse(keys) is not null
        ? keys : HotkeyText.DefaultFor(OperatingSystem.IsMacOS());

    public static bool Compact => string.Equals(Current.Density, "compact", StringComparison.OrdinalIgnoreCase);

    public static int ClampScale(int percent) => Math.Clamp(percent, MinScale, MaxScale);

    public static void Update(Action<Data> change)
    {
        change(Current);
        Current.TextScale = ClampScale(Current.TextScale);
        Save();
        Changed?.Invoke();
    }

    private static string FilePath => Path.Combine(ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "shell-prefs.json");

    private static Data Load()
    {
        try
        {
            if (File.Exists(FilePath) && JsonSerializer.Deserialize<Data>(File.ReadAllText(FilePath)) is { } data)
            {
                data.TextScale = ClampScale(data.TextScale == 0 ? 100 : data.TextScale);
                data.PaletteRecent ??= new();
                return data;
            }
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("prefs", "could not read shell-prefs.json; using defaults", ex);
        }
        return new Data();
    }

    private static void Save()
    {
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(FilePath)!);
            File.WriteAllText(FilePath, JsonSerializer.Serialize(Current, JsonOptions));
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("prefs", "could not save shell-prefs.json", ex);
        }
    }
}
