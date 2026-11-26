using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

// Settings › General: text size, chat list density, quick ask and the
// keyboard shortcut sheet (ShellPrefs.cs, QuickAsk.cs).
public sealed partial class SettingsPage
{
    private UIElement[] BuildShellSettingsGroups()
    {
        // ---- Text & layout
        var size = new ComboBox { MinWidth = 160 };
        foreach (var (label, value) in new[] { ("Small (90%)", 90), ("Default (100%)", 100), ("Large (115%)", 115), ("Larger (130%)", 130), ("Largest (150%)", 150) })
            size.Items.Add(new ComboBoxItem { Content = label, Tag = value });
        var current = ShellPrefs.Current.TextScale;
        if (size.Items.OfType<ComboBoxItem>().All(i => (int)i.Tag != current))
            size.Items.Add(new ComboBoxItem { Content = $"Custom ({current}%)", Tag = current });
        size.SelectedItem = size.Items.OfType<ComboBoxItem>().First(i => (int)i.Tag == current);
        AutomationProperties.SetName(size, "Chat text size");
        size.SelectionChanged += (_, _) =>
        {
            if ((size.SelectedItem as ComboBoxItem)?.Tag is int chosen && chosen != ShellPrefs.Current.TextScale)
                ShellPrefs.Update(p => p.TextScale = chosen);
        };
        var mac = PlatformText.IsMac;
        string Keys(string id) => ShortcutRegistry.Label(ShortcutRegistry.Find(id)!, mac);

        var density = new ComboBox { MinWidth = 160 };
        density.Items.Add(new ComboBoxItem { Content = "Comfortable", Tag = "comfortable" });
        density.Items.Add(new ComboBoxItem { Content = "Compact", Tag = "compact" });
        density.SelectedIndex = ShellPrefs.Compact ? 1 : 0;
        AutomationProperties.SetName(density, "Chat list density");
        density.SelectionChanged += (_, _) =>
        {
            if ((density.SelectedItem as ComboBoxItem)?.Tag is string chosen && chosen != ShellPrefs.Current.Density)
                ShellPrefs.Update(p => p.Density = chosen);
        };
        var text = SettingsGroup("Text & layout",
            SettingRow(RowText("Chat text size", $"How big messages read. {Keys(ShortcutRegistry.ZoomIn)} and {Keys(ShortcutRegistry.ZoomOut)} change it from anywhere; {Keys(ShortcutRegistry.ZoomReset)} goes back to 100%."), size),
            SettingRow(RowText("Chat list", "Compact shows each chat on two lines, so more fit in the sidebar."), density));

        // ---- Quick ask
        var enabled = new ToggleSwitch { OnContent = "", OffContent = "", MinWidth = 0, IsOn = ShellPrefs.Current.QuickAskEnabled };
        AutomationProperties.SetName(enabled, "Quick ask shortcut");
        var hotkey = new ComboBox { MinWidth = 180 };
        foreach (var choice in HotkeyText.Choices(mac)) hotkey.Items.Add(new ComboBoxItem { Content = PrettyKeys(choice, mac), Tag = choice });
        var keys = ShellPrefs.QuickAskKeys;
        if (hotkey.Items.OfType<ComboBoxItem>().All(i => (string)i.Tag != keys)) hotkey.Items.Add(new ComboBoxItem { Content = PrettyKeys(keys, mac), Tag = keys });
        hotkey.SelectedItem = hotkey.Items.OfType<ComboBoxItem>().First(i => (string)i.Tag == keys);
        hotkey.IsEnabled = enabled.IsOn;
        AutomationProperties.SetName(hotkey, "Quick ask keys");
        var status = new TextBlock { FontSize = NexusTokens.FontCaption, TextWrapping = TextWrapping.Wrap, Foreground = NexusPalette.Amber };
        void ShowStatus()
        {
            var problem = QuickAsk.Problem;
            status.Text = problem.Length > 0 ? problem + " Pick another shortcut." : "";
            status.Visibility = problem.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
        }
        enabled.Toggled += (_, _) =>
        {
            hotkey.IsEnabled = enabled.IsOn;
            ShellPrefs.Update(p => p.QuickAskEnabled = enabled.IsOn);
            QuickAsk.Apply();
            ShowStatus();
        };
        hotkey.SelectionChanged += (_, _) =>
        {
            if ((hotkey.SelectedItem as ComboBoxItem)?.Tag is string chosen && chosen != ShellPrefs.QuickAskKeys)
            {
                ShellPrefs.Update(p => p.QuickAskHotkey = chosen);
                QuickAsk.Apply();
                ShowStatus();
            }
        };
        ShowStatus();
        var background = new ToggleSwitch { OnContent = "", OffContent = "", MinWidth = 0, IsOn = ShellPrefs.Current.QuickAskBackground };
        AutomationProperties.SetName(background, "Keep quick ask chats in the background");
        background.Toggled += (_, _) => ShellPrefs.Update(p => p.QuickAskBackground = background.IsOn);
        var tryIt = new Button { Content = "Try it" };
        tryIt.Click += (_, _) => MainWindow.Current?.ShowQuickAsk();
        var sheet = new Button { Content = "Keyboard shortcuts" };
        AutomationProperties.SetName(sheet, "Show all keyboard shortcuts");
        sheet.Click += (_, _) => MainWindow.Current?.ShowShortcutSheet();
        var quick = SettingsGroup("Quick ask & shortcuts",
            SettingRow(RowText("Quick ask from anywhere", "A small prompt over any app. It starts a chat with the agent and project you pick."), enabled),
            SettingRow(RowText("Shortcut", status), hotkey),
            SettingRow(RowText("Keep quick ask chats in the background", "On: the agent works without opening Nexus and a notification says when it's done. Off: the chat opens."), background),
            SettingRow(RowText("Keyboard shortcuts", $"Every shortcut in one place ({Keys(ShortcutRegistry.ShortcutSheet)}). {Keys(ShortcutRegistry.Palette)} searches chats and runs commands."), Row2(tryIt, sheet)));
        return new UIElement[] { text, quick };
    }

    private static StackPanel Row2(FrameworkElement a, FrameworkElement b) => new() { Orientation = Orientation.Horizontal, Spacing = 8, Children = { a, b } };

    private static string PrettyKeys(string keys, bool mac) =>
        HotkeyText.Parse(keys) is { } parsed
            ? ShortcutRegistry.Label(new ShortcutRegistry.Shortcut("", "", "", parsed.Key, parsed.Primary, parsed.Shift, parsed.Alt), mac)
            : keys;
}
