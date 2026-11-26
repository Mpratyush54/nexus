using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using Nexus.Core;

namespace Nexus.Platform;

/// <summary>
/// Dictation on macOS: SFSpeechRecognizer + AVAudioEngine in libnexusmac
/// (desktop/uno/macos/native/NexusDictation.m), on this Mac when the
/// recognizer supports on-device recognition. macOS asks for Speech
/// Recognition and Microphone permission the first time; a denial comes back
/// as SpeechDenied / MicrophoneDenied and the chat box offers "Open System
/// Settings". Partial results are the whole session so far, so they arrive
/// as one growing Hypothesis and a final Phrase.
/// </summary>
internal sealed partial class MacDictation : IDictationEngine
{
    private const string Lib = "nexusmac";

    // One recognizer at a time (the native side keeps one); events go to the active instance.
    private static MacDictation? s_active;
    private static bool s_missing;

    private TaskCompletionSource<DictationProblem>? _starting;

    public event Action<string>? Hypothesis;
    public event Action<string>? Phrase;
    public event Action<double>? Level;
    public event Action<DictationProblem>? Ended;

    public static bool IsSupported => OperatingSystem.IsMacOS() && !s_missing && MacNative.Available;

    public unsafe Task<DictationProblem> StartAsync(string language)
    {
        var starting = new TaskCompletionSource<DictationProblem>(TaskCreationOptions.RunContinuationsAsynchronously);
        _starting = starting;
        s_active = this;
        try { nx_mac_dictation_start(language ?? "", &OnEvent); }
        catch (Exception ex) when (ex is DllNotFoundException or EntryPointNotFoundException)
        {
            s_missing = true;
            Nexus.Core.Diagnostics.DiagLog.Warn("voice", "libnexusmac has no dictation", ex);
            return Task.FromResult(DictationProblem.Failed);
        }
        // The permission prompts wait for the person; the answer arrives as Listening or Ended.
        return starting.Task;
    }

    public Task StopAsync()
    {
        if (ReferenceEquals(s_active, this))
        {
            try { nx_mac_dictation_stop(); } catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Warn("voice", "dictation stop failed", ex); }
        }
        return Task.CompletedTask;
    }

    public void Dispose()
    {
        if (!ReferenceEquals(s_active, this)) return;
        try { nx_mac_dictation_cancel(); } catch { }
        s_active = null;
    }

    public static unsafe IReadOnlyList<(string Tag, string Name)> Languages()
    {
        if (!IsSupported) return Array.Empty<(string, string)>();
        var buffer = new byte[16384];
        fixed (byte* p = buffer) nx_mac_dictation_locales(p, buffer.Length);
        var end = Array.IndexOf(buffer, (byte)0);
        var joined = System.Text.Encoding.UTF8.GetString(buffer, 0, end < 0 ? buffer.Length : end);
        return joined.Split(',', StringSplitOptions.RemoveEmptyEntries)
            .Select(tag => (tag, DictationEngines.DisplayName(tag)))
            .OrderBy(t => t.Item2, StringComparer.CurrentCultureIgnoreCase)
            .ToList();
    }

    private void Raise(int kind, string text, float value)
    {
        switch (kind)
        {
            case 1: Hypothesis?.Invoke(text); break;
            case 2: Phrase?.Invoke(text); break;
            case 3: Level?.Invoke(value); break;
            case 5: _starting?.TrySetResult(DictationProblem.None); break;
            case 4:
                var problem = (DictationProblem)(int)value;
                // Ended before listening: that is the start's answer.
                if (_starting is { Task.IsCompleted: false } starting) starting.TrySetResult(problem);
                else Ended?.Invoke(problem);
                if (ReferenceEquals(s_active, this)) s_active = null;
                break;
        }
    }

    [UnmanagedCallersOnly(CallConvs = new[] { typeof(CallConvCdecl) })]
    private static void OnEvent(int kind, IntPtr utf8, float value)
    {
        try { s_active?.Raise(kind, Marshal.PtrToStringUTF8(utf8) ?? "", value); }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Error("voice", "dictation event failed", ex); }
    }

    [LibraryImport(Lib, StringMarshalling = StringMarshalling.Utf8)]
    private static unsafe partial int nx_mac_dictation_start(string locale, delegate* unmanaged[Cdecl]<int, IntPtr, float, void> callback);

    [LibraryImport(Lib)]
    private static partial void nx_mac_dictation_stop();

    [LibraryImport(Lib)]
    private static partial void nx_mac_dictation_cancel();

    [LibraryImport(Lib)]
    private static unsafe partial int nx_mac_dictation_locales(byte* output, int length);
}
