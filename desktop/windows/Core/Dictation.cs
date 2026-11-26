namespace Nexus.Core;

/// <summary>Why dictation could not start, or why it ended. Keep the numbers in sync with NexusMac.m.</summary>
public enum DictationProblem
{
    None = 0,
    /// <summary>The OS denied the microphone (Windows privacy settings, macOS TCC).</summary>
    MicrophoneDenied = 1,
    /// <summary>macOS: Speech Recognition permission denied.</summary>
    SpeechDenied = 2,
    /// <summary>Windows: the dictation grammar needs "Online speech recognition" and no on-device recognizer covers the language.</summary>
    OnlineSpeechOff = 3,
    /// <summary>No recognizer for the chosen language on this computer.</summary>
    NoRecognizer = 4,
    /// <summary>No input device.</summary>
    NoMicrophone = 5,
    /// <summary>Anything else; details are in the log.</summary>
    Failed = 6,
}

/// <summary>
/// A speech recognizer the chat box can dictate with: Windows speech
/// (Platform/WindowsDictation.cs) or macOS Speech + AVAudioEngine
/// (desktop/uno/Platform/MacDictation.cs). Events can arrive on any thread.
/// Audio never leaves the OS recognizer: Nexus does not record, store or
/// upload it.
/// </summary>
public interface IDictationEngine : IDisposable
{
    /// <summary>The words heard so far in the current phrase (replaced by the next Hypothesis or Phrase).</summary>
    event Action<string>? Hypothesis;

    /// <summary>A finished phrase, with the recognizer's punctuation.</summary>
    event Action<string>? Phrase;

    /// <summary>Input level, 0 to 1, for the level bars.</summary>
    event Action<double>? Level;

    /// <summary>The engine stopped by itself or after StopAsync; None is a normal stop.</summary>
    event Action<DictationProblem>? Ended;

    /// <summary>Starts listening; language is a BCP-47 tag or "" for the system language.</summary>
    Task<DictationProblem> StartAsync(string language);

    /// <summary>Stops listening; the last phrase still arrives, then Ended.</summary>
    Task StopAsync();
}

/// <summary>The platform's dictation engine and its languages.</summary>
public static partial class DictationEngines
{
    /// <summary>A new engine, or null when this platform has none (the mic button is then hidden).</summary>
    public static IDictationEngine? Create()
    {
#if HAS_UNO
        return Nexus.Platform.MacDictation.IsSupported ? new Nexus.Platform.MacDictation() : null;
#else
        return new Nexus.Platform.WindowsDictation();
#endif
    }

    /// <summary>Languages dictation can use here: (BCP-47 tag, display name).</summary>
    public static IReadOnlyList<(string Tag, string Name)> Languages()
    {
        try
        {
#if HAS_UNO
            return Nexus.Platform.MacDictation.Languages();
#else
            return Nexus.Platform.WindowsDictation.Languages();
#endif
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("voice", "could not list dictation languages", ex);
            return Array.Empty<(string, string)>();
        }
    }

    /// <summary>"English (United States)" for "en-US"; the tag itself when unknown.</summary>
    public static string DisplayName(string tag)
    {
        try { return new System.Globalization.CultureInfo(tag).DisplayName; }
        catch { return tag; }
    }
}
