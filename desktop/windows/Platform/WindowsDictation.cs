using Microsoft.Win32;
using Nexus.Core;
using Windows.Media.SpeechRecognition;

namespace Nexus.Platform;

/// <summary>
/// Dictation on Windows, with the speech recognizers built into Windows.
/// Nexus never sees the audio: Windows captures and recognizes it.
///
///  1. Windows.Media.SpeechRecognition (the recognizer behind Windows
///     dictation): a ContinuousRecognitionSession with the dictation topic,
///     interim hypotheses and punctuation. It works in an unpackaged WinUI
///     app. Its dictation grammar needs Settings > Privacy > Speech > "Online
///     speech recognition" (error 0x80045509 when that is off).
///  2. When it is off, the on-device SAPI desktop recognizer (System.Speech,
///     "Microsoft Speech Recognizer 8.0 for Windows") if one is installed for
///     the language: no network at all, lower accuracy, no automatic
///     punctuation. Without one the person gets a button to the setting.
///
/// The microphone switch (Settings > Privacy > Microphone, and "Let desktop
/// apps access your microphone") is read from the registry before starting,
/// so a denied microphone shows the settings button instead of silence.
/// NEXUS_DICTATION_ENGINE=sapi forces the on-device recognizer;
/// =simulate-denied shows the "microphone is off" path, and =wav:&lt;file&gt;
/// dictates a WAV file instead of the microphone (support and QA).
/// </summary>
internal sealed class WindowsDictation : IDictationEngine
{
    private const int PrivacyPolicyNotAccepted = unchecked((int)0x80045509);
    private const int AccessDenied = unchecked((int)0x80070005);

    private IDictationEngine? _inner;

    public event Action<string>? Hypothesis;
    public event Action<string>? Phrase;
    public event Action<double>? Level;
    public event Action<DictationProblem>? Ended;

    /// <summary>Which recognizer the last start used: "windows" or "on-device".</summary>
    public string EngineName { get; private set; } = "";

    public async Task<DictationProblem> StartAsync(string language)
    {
        await StopAsync();
        var choice = Environment.GetEnvironmentVariable("NEXUS_DICTATION_ENGINE") ?? "";
        // Support/QA: show the "microphone is off" path without changing Windows settings.
        if (MicrophoneDeniedInSettings() || choice.Equals("simulate-denied", StringComparison.OrdinalIgnoreCase)) return DictationProblem.MicrophoneDenied;
        if (choice.StartsWith("wav:", StringComparison.OrdinalIgnoreCase))
        {
            // QA: the on-device recognizer reads a WAV file instead of the microphone.
            var file = new SapiDictation(choice[4..]);
            var fileProblem = await file.StartAsync(language);
            if (fileProblem == DictationProblem.None) Use(file, "on-device, wav file");
            else file.Dispose();
            return fileProblem;
        }
        var forceSapi = choice.Equals("sapi", StringComparison.OrdinalIgnoreCase);
        var onlineOff = forceSapi;
        if (!forceSapi)
        {
            var winrt = new WinRtDictation();
            var problem = await winrt.StartAsync(language);
            if (problem == DictationProblem.None)
            {
                Use(winrt, "windows");
                return problem;
            }
            winrt.Dispose();
            if (problem != DictationProblem.OnlineSpeechOff && problem != DictationProblem.NoRecognizer) return problem;
            onlineOff = problem == DictationProblem.OnlineSpeechOff;
        }
        if (SapiDictation.HasRecognizer(language))
        {
            var sapi = new SapiDictation();
            var problem = await sapi.StartAsync(language);
            if (problem == DictationProblem.None)
            {
                Use(sapi, "on-device");
                return problem;
            }
            sapi.Dispose();
            return problem;
        }
        return onlineOff ? DictationProblem.OnlineSpeechOff : DictationProblem.NoRecognizer;
    }

    private void Use(IDictationEngine engine, string name)
    {
        _inner = engine;
        EngineName = name;
        engine.Hypothesis += text => Hypothesis?.Invoke(text);
        engine.Phrase += text => Phrase?.Invoke(text);
        engine.Level += level => Level?.Invoke(level);
        engine.Ended += problem =>
        {
            if (ReferenceEquals(_inner, engine)) _inner = null;
            Ended?.Invoke(problem);
            // Not from inside the engine's own callback.
            _ = Task.Run(engine.Dispose);
        };
        Nexus.Core.Diagnostics.DiagLog.Info("voice", $"dictation started ({name})");
    }

    public Task StopAsync() => _inner?.StopAsync() ?? Task.CompletedTask;

    public void Dispose()
    {
        _inner?.Dispose();
        _inner = null;
    }

    /// <summary>Languages Windows dictation supports, plus on-device ones.</summary>
    public static IReadOnlyList<(string Tag, string Name)> Languages()
    {
        var tags = new SortedDictionary<string, string>(StringComparer.OrdinalIgnoreCase);
        try
        {
            foreach (var language in SpeechRecognizer.SupportedTopicLanguages)
                tags[language.LanguageTag] = language.DisplayName;
        }
        catch (Exception ex) { Nexus.Core.Diagnostics.DiagLog.Warn("voice", "SupportedTopicLanguages failed", ex); }
        foreach (var tag in SapiDictation.InstalledLanguages())
            if (!tags.ContainsKey(tag)) tags[tag] = DictationEngines.DisplayName(tag);
        return tags.Select(t => (t.Key, t.Value)).OrderBy(t => t.Value, StringComparer.CurrentCultureIgnoreCase).ToList();
    }

    /// <summary>
    /// Windows privacy settings: the microphone is off for everyone, or for
    /// desktop (unpackaged) apps, which is what Nexus is.
    /// </summary>
    public static bool MicrophoneDeniedInSettings()
    {
        const string store = @"Software\Microsoft\Windows\CurrentVersion\CapabilityAccessManager\ConsentStore\microphone";
        static bool Denied(RegistryKey root, string path)
        {
            try
            {
                using var key = root.OpenSubKey(path);
                return string.Equals(key?.GetValue("Value") as string, "Deny", StringComparison.OrdinalIgnoreCase);
            }
            catch { return false; }
        }
        return Denied(Registry.LocalMachine, store) || Denied(Registry.CurrentUser, store) || Denied(Registry.CurrentUser, store + @"\NonPackaged");
    }

    internal static DictationProblem ProblemFor(Exception ex) => ex.HResult switch
    {
        PrivacyPolicyNotAccepted => DictationProblem.OnlineSpeechOff,
        AccessDenied => DictationProblem.MicrophoneDenied,
        _ when ex is UnauthorizedAccessException => DictationProblem.MicrophoneDenied,
        _ => DictationProblem.Failed,
    };

    /// <summary>Windows.Media.SpeechRecognition, continuous dictation.</summary>
    private sealed class WinRtDictation : IDictationEngine
    {
        private SpeechRecognizer? _recognizer;
        private int _ended;

        public event Action<string>? Hypothesis;
        public event Action<string>? Phrase;
        public event Action<double>? Level;
        public event Action<DictationProblem>? Ended;

        public async Task<DictationProblem> StartAsync(string language)
        {
            try
            {
                var lang = SpeechRecognizer.SystemSpeechLanguage;
                if (language.Length > 0)
                {
                    var supported = SpeechRecognizer.SupportedTopicLanguages.FirstOrDefault(l => string.Equals(l.LanguageTag, language, StringComparison.OrdinalIgnoreCase));
                    if (supported is null) return DictationProblem.NoRecognizer;
                    lang = supported;
                }
                if (lang is null) return DictationProblem.NoRecognizer;
                var recognizer = new SpeechRecognizer(lang);
                _recognizer = recognizer;
                recognizer.Constraints.Add(new SpeechRecognitionTopicConstraint(SpeechRecognitionScenario.Dictation, "dictation"));
                var compiled = await recognizer.CompileConstraintsAsync();
                if (compiled.Status != SpeechRecognitionResultStatus.Success)
                {
                    Nexus.Core.Diagnostics.DiagLog.Info("voice", "dictation grammar not available: " + compiled.Status);
                    return compiled.Status switch
                    {
                        SpeechRecognitionResultStatus.TopicLanguageNotSupported or SpeechRecognitionResultStatus.GrammarLanguageMismatch => DictationProblem.NoRecognizer,
                        SpeechRecognitionResultStatus.MicrophoneUnavailable => DictationProblem.NoMicrophone,
                        _ => DictationProblem.OnlineSpeechOff,
                    };
                }
                // Our own silence timer stops at 20 s too; this is the backstop.
                recognizer.ContinuousRecognitionSession.AutoStopSilenceTimeout = TimeSpan.FromSeconds(25);
                recognizer.Timeouts.InitialSilenceTimeout = TimeSpan.FromSeconds(25);
                recognizer.Timeouts.EndSilenceTimeout = TimeSpan.FromSeconds(1.2);
                recognizer.HypothesisGenerated += (_, e) =>
                {
                    Level?.Invoke(0.9);
                    Hypothesis?.Invoke(e.Hypothesis.Text);
                };
                recognizer.StateChanged += (_, e) =>
                {
                    // Windows reports no audio level; the speech states drive the bars.
                    var level = e.State switch
                    {
                        SpeechRecognizerState.SoundStarted => 0.35,
                        SpeechRecognizerState.SpeechDetected => 0.75,
                        SpeechRecognizerState.SoundEnded => 0.05,
                        _ => -1,
                    };
                    if (level >= 0) Level?.Invoke(level);
                };
                recognizer.ContinuousRecognitionSession.ResultGenerated += (_, e) =>
                {
                    if (e.Result.Status == SpeechRecognitionResultStatus.Success && e.Result.Confidence != SpeechRecognitionConfidence.Rejected && e.Result.Text.Length > 0)
                        Phrase?.Invoke(e.Result.Text);
                };
                recognizer.ContinuousRecognitionSession.Completed += (_, e) => End(e.Status switch
                {
                    SpeechRecognitionResultStatus.Success or SpeechRecognitionResultStatus.TimeoutExceeded or SpeechRecognitionResultStatus.UserCanceled => DictationProblem.None,
                    SpeechRecognitionResultStatus.MicrophoneUnavailable => DictationProblem.NoMicrophone,
                    SpeechRecognitionResultStatus.TopicLanguageNotSupported => DictationProblem.NoRecognizer,
                    _ => DictationProblem.Failed,
                });
                await recognizer.ContinuousRecognitionSession.StartAsync(SpeechContinuousRecognitionMode.Default);
                return DictationProblem.None;
            }
            catch (Exception ex)
            {
                var problem = ProblemFor(ex);
                Nexus.Core.Diagnostics.DiagLog.Info("voice", $"Windows speech recognizer could not start: 0x{ex.HResult:X8} -> {problem}");
                return problem;
            }
        }

        public async Task StopAsync()
        {
            var recognizer = _recognizer;
            if (recognizer is null) return;
            try
            {
                if (recognizer.State != SpeechRecognizerState.Idle) await recognizer.ContinuousRecognitionSession.StopAsync();
                else End(DictationProblem.None);
            }
            catch (Exception ex)
            {
                Nexus.Core.Diagnostics.DiagLog.Info("voice", $"stop: 0x{ex.HResult:X8}");
                End(DictationProblem.None);
            }
        }

        private void End(DictationProblem problem)
        {
            if (Interlocked.Exchange(ref _ended, 1) == 1) return;
            Ended?.Invoke(problem);
        }

        public void Dispose()
        {
            var recognizer = Interlocked.Exchange(ref _recognizer, null);
            if (recognizer is null) return;
            try { recognizer.Dispose(); } catch { }
        }
    }
}

/// <summary>
/// The on-device SAPI desktop recognizer (System.Speech). Also reads a WAV
/// file instead of the microphone, which is how its pipeline is tested
/// without anyone speaking.
/// </summary>
internal sealed class SapiDictation : IDictationEngine
{
    private readonly string? _waveFile;
    private System.Speech.Recognition.SpeechRecognitionEngine? _engine;
    private int _ended;

    public SapiDictation(string? waveFile = null) => _waveFile = waveFile;

    public event Action<string>? Hypothesis;
    public event Action<string>? Phrase;
    public event Action<double>? Level;
    public event Action<DictationProblem>? Ended;

    public static IEnumerable<string> InstalledLanguages()
    {
        try { return System.Speech.Recognition.SpeechRecognitionEngine.InstalledRecognizers().Select(r => r.Culture.Name).ToList(); }
        catch { return Array.Empty<string>(); }
    }

    public static bool HasRecognizer(string language) => Pick(language) is not null;

    private static System.Speech.Recognition.RecognizerInfo? Pick(string language)
    {
        try
        {
            var all = System.Speech.Recognition.SpeechRecognitionEngine.InstalledRecognizers();
            var want = language.Length > 0 ? language : System.Globalization.CultureInfo.CurrentUICulture.Name;
            return all.FirstOrDefault(r => string.Equals(r.Culture.Name, want, StringComparison.OrdinalIgnoreCase))
                   ?? (language.Length > 0 ? null : all.FirstOrDefault(r => r.Culture.TwoLetterISOLanguageName == System.Globalization.CultureInfo.CurrentUICulture.TwoLetterISOLanguageName))
                   ?? (language.Length > 0 ? null : all.FirstOrDefault());
        }
        catch { return null; }
    }

    public Task<DictationProblem> StartAsync(string language)
    {
        var info = Pick(language);
        if (info is null) return Task.FromResult(DictationProblem.NoRecognizer);
        try
        {
            var engine = new System.Speech.Recognition.SpeechRecognitionEngine(info);
            _engine = engine;
            engine.LoadGrammar(new System.Speech.Recognition.DictationGrammar());
            try
            {
                if (_waveFile is { Length: > 0 }) engine.SetInputToWaveFile(_waveFile);
                else engine.SetInputToDefaultAudioDevice();
            }
            catch (InvalidOperationException) { return Task.FromResult(DictationProblem.NoMicrophone); }
            // SAPI dictation turns room noise into single letters ("A", "F");
            // those, and low-confidence phrases, are not words.
            engine.SpeechHypothesized += (_, e) => { if (IsWords(e.Result.Text)) Hypothesis?.Invoke(e.Result.Text); };
            engine.SpeechRecognized += (_, e) =>
            {
                if (e.Result.Confidence >= 0.3f && IsWords(e.Result.Text)) Phrase?.Invoke(e.Result.Text);
                else Hypothesis?.Invoke("");
            };
            engine.SpeechRecognitionRejected += (_, _) => Hypothesis?.Invoke("");
            engine.AudioLevelUpdated += (_, e) => Level?.Invoke(Math.Clamp(e.AudioLevel / 100.0, 0, 1));
            engine.RecognizeCompleted += (_, e) => End(e.Error is null ? DictationProblem.None : WindowsDictation.ProblemFor(e.Error));
            engine.RecognizeAsync(System.Speech.Recognition.RecognizeMode.Multiple);
            return Task.FromResult(DictationProblem.None);
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Info("voice", $"on-device recognizer could not start: 0x{ex.HResult:X8}");
            return Task.FromResult(WindowsDictation.ProblemFor(ex));
        }
    }

    internal static bool IsWords(string? text)
    {
        var t = (text ?? "").Trim();
        return t.Length > 1 || t == "I";
    }

    public Task StopAsync()
    {
        try { _engine?.RecognizeAsyncStop(); }
        catch { End(DictationProblem.None); }
        return Task.CompletedTask;
    }

    private void End(DictationProblem problem)
    {
        if (Interlocked.Exchange(ref _ended, 1) == 1) return;
        Ended?.Invoke(problem);
    }

    public void Dispose()
    {
        var engine = Interlocked.Exchange(ref _engine, null);
        if (engine is null) return;
        try { engine.RecognizeAsyncCancel(); } catch { }
        try { engine.SetInputToNull(); } catch { }
        try { engine.Dispose(); } catch { }
    }
}
