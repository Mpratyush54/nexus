using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

public sealed partial class SettingsPage
{
    // ---- General: voice input (dictation in chat boxes; Pages/VoiceInput.cs)

    private UIElement BuildVoiceInputGroup()
    {
        var enabled = new ToggleSwitch { OnContent = "", OffContent = "", MinWidth = 0, IsOn = AppPrefs.Data.VoiceInput };
        AutomationProperties.SetName(enabled, "Voice input");
        var language = new ComboBox { MinWidth = 200, MaxWidth = 280 };
        AutomationProperties.SetName(language, "Voice input language");
        var system = new ComboBoxItem { Content = PlatformText.IsMac ? "Same as macOS" : "Same as Windows", Tag = "" };
        language.Items.Add(system);
        language.SelectedItem = system;
        var current = AppPrefs.Data.VoiceLanguage ?? "";
        var filling = true;
        void Fill(IReadOnlyList<(string Tag, string Name)> languages)
        {
            filling = true;
            foreach (var (tag, name) in languages)
            {
                var item = new ComboBoxItem { Content = name, Tag = tag };
                language.Items.Add(item);
                if (string.Equals(tag, current, StringComparison.OrdinalIgnoreCase)) language.SelectedItem = item;
            }
            if (current.Length > 0 && !languages.Any(l => string.Equals(l.Tag, current, StringComparison.OrdinalIgnoreCase)))
            {
                var kept = new ComboBoxItem { Content = DictationEngines.DisplayName(current), Tag = current };
                language.Items.Add(kept);
                language.SelectedItem = kept;
            }
            filling = false;
        }
        // Listing recognizers can take a moment on first use; off the UI thread.
        _ = Task.Run(DictationEngines.Languages).ContinueWith(t =>
            DispatcherQueue.TryEnqueue(() => Fill(t.IsCompletedSuccessfully ? t.Result : Array.Empty<(string, string)>())),
            TaskScheduler.Default);
        language.SelectionChanged += (_, _) =>
        {
            if (filling || (language.SelectedItem as ComboBoxItem)?.Tag is not string tag) return;
            AppPrefs.Data.VoiceLanguage = tag;
            AppPrefs.Save();
            VoiceInput.NotifySettingsChanged();
        };
        language.IsEnabled = enabled.IsOn;
        enabled.Toggled += (_, _) =>
        {
            AppPrefs.Data.VoiceInput = enabled.IsOn;
            AppPrefs.Save();
            language.IsEnabled = enabled.IsOn;
            VoiceInput.NotifySettingsChanged();
        };
        var how = $"Click the microphone in the chat box or press {VoiceInput.ShortcutLabel} to speak instead of typing. Your words appear in the box to edit; nothing is sent until you press Send.";
        var privacy = PlatformText.IsMac
            ? "macOS turns speech into text, on this Mac when it can. Nexus never records, keeps or uploads your audio."
            : "Windows turns speech into text with its own speech recognition. Nexus never records, keeps or uploads your audio.";
        return SettingsGroup("Voice input",
            SettingRow(RowText("Voice input", how), enabled),
            SettingRow(RowText("Language", privacy), language));
    }
}
