namespace Nexus.Core;

/// <summary>
/// Every keyboard shortcut Nexus has, in one place. The shortcut sheet
/// (Ctrl/Cmd+/) and the command palette's key hints are generated from this
/// list, and the main window binds its accelerators from it, so a shortcut
/// added here shows up everywhere. Pure data: no WinUI types, testable.
/// </summary>
internal static class ShortcutRegistry
{
    /// <summary>
    /// One shortcut. <paramref name="Key"/> is the key as printed ("K", "/",
    /// "Tab", "F2", "Esc", "1–9"). <paramref name="Primary"/> means Ctrl on
    /// Windows and Command on macOS. <paramref name="Global"/> shortcuts work
    /// while Nexus is in the background (the quick-ask hotkey).
    /// <paramref name="Control"/> is the Control key on every platform (Ctrl+Tab
    /// stays Control+Tab on a Mac, where Command+Tab switches apps).
    /// </summary>
    internal sealed record Shortcut(
        string Id, string Title, string Group, string Key,
        bool Primary = false, bool Shift = false, bool Alt = false,
        bool Global = false, string Note = "", bool Control = false);

    public const string GroupGeneral = "General";
    public const string GroupChats = "Chats";
    public const string GroupChat = "In a chat";
    public const string GroupView = "View";

    // Ids the main window binds (MainWindow.Shell.cs).
    public const string Palette = "palette";
    public const string NewChat = "new-chat";
    public const string SwitchProject = "switch-project";
    public const string ShortcutSheet = "shortcuts";
    public const string ToggleSidebar = "toggle-sidebar";
    public const string ToggleSidebarAlt = "toggle-sidebar-alt";
    public const string NextChat = "next-chat";
    public const string PreviousChat = "previous-chat";
    public const string NextChatBracket = "next-chat-bracket";
    public const string PreviousChatBracket = "previous-chat-bracket";
    public const string PinnedChat = "pinned-chat";
    public const string RenameChat = "rename-chat";
    public const string PinChat = "pin-chat";
    public const string ArchiveChat = "archive-chat";
    public const string Undo = "undo";
    public const string ZoomIn = "zoom-in";
    public const string ZoomOut = "zoom-out";
    public const string ZoomReset = "zoom-reset";
    public const string QuickAsk = "quick-ask";
    public const string Settings = "settings";

    /// <summary>The registry, in the order the shortcut sheet lists it.</summary>
    public static IReadOnlyList<Shortcut> All(string quickAskKeys = "Alt+Space", bool mac = false) => new[]
    {
        new Shortcut(Palette, "Search and run a command", GroupGeneral, "K", Primary: true),
        new Shortcut(NewChat, "New chat", GroupGeneral, "N", Primary: true),
        new Shortcut(SwitchProject, "Switch project", GroupGeneral, "P", Primary: true),
        new Shortcut(Settings, "Settings", GroupGeneral, ",", Primary: true),
        new Shortcut(ShortcutSheet, "Keyboard shortcuts", GroupGeneral, "/", Primary: true),
        QuickAskShortcut(quickAskKeys, mac),
        new Shortcut(Undo, "Undo the last archive or delete", GroupGeneral, "Z", Primary: true, Note: "While its notice is showing"),

        new Shortcut(NextChat, "Next chat", GroupChats, "Tab", Control: true),
        new Shortcut(PreviousChat, "Previous chat", GroupChats, "Tab", Shift: true, Control: true),
        new Shortcut(NextChatBracket, "Next chat", GroupChats, "]", Primary: true, Shift: true),
        new Shortcut(PreviousChatBracket, "Previous chat", GroupChats, "[", Primary: true, Shift: true),
        new Shortcut(PinnedChat, "Open pinned chat 1 to 9", GroupChats, "1–9", Primary: true),
        new Shortcut(RenameChat, "Rename the selected chat", GroupChats, "F2"),
        new Shortcut(PinChat, "Pin or unpin this chat", GroupChats, "P", Primary: true, Alt: true),
        new Shortcut(ArchiveChat, "Archive this chat", GroupChats, "A", Primary: true, Shift: true),

        new Shortcut("send", "Send", GroupChat, "Enter", Note: "Shift+Enter for a new line"),
        new Shortcut("find", "Find in this chat", GroupChat, "F", Primary: true),
        new Shortcut("stop", "Stop the reply", GroupChat, "Esc"),

        new Shortcut(ToggleSidebar, "Show or hide the sidebar", GroupView, "B", Primary: true),
        new Shortcut(ToggleSidebarAlt, "Show or hide the sidebar", GroupView, "S", Primary: true, Shift: true),
        new Shortcut(ZoomIn, "Bigger text", GroupView, "=", Primary: true),
        new Shortcut(ZoomOut, "Smaller text", GroupView, "-", Primary: true),
        new Shortcut(ZoomReset, "Default text size", GroupView, "0", Primary: true),
    };

    private static Shortcut QuickAskShortcut(string keys, bool mac)
    {
        var parsed = HotkeyText.Parse(keys) ?? HotkeyText.Parse(HotkeyText.DefaultFor(mac))!;
        return new Shortcut(QuickAsk, "Quick ask from anywhere", GroupGeneral, parsed.Key,
            Primary: parsed.Primary, Shift: parsed.Shift, Alt: parsed.Alt, Global: true,
            Note: "Works while Nexus is in the background; change it in Settings");
    }

    public static Shortcut? Find(string id, IReadOnlyList<Shortcut>? all = null) =>
        (all ?? All()).FirstOrDefault(s => s.Id == id);

    /// <summary>"Ctrl+Shift+K" on Windows, "⇧⌘K" on macOS (Apple's modifier order).</summary>
    public static string Label(Shortcut s, bool mac)
    {
        if (mac)
        {
            var m = (s.Control ? "⌃" : "") + (s.Alt ? "⌥" : "") + (s.Shift ? "⇧" : "") + (s.Primary ? "⌘" : "");
            return m + MacKey(s.Key);
        }
        var parts = new List<string>();
        if (s.Primary || s.Control) parts.Add("Ctrl");
        if (s.Shift) parts.Add("Shift");
        if (s.Alt) parts.Add("Alt");
        parts.Add(s.Key);
        return string.Join("+", parts);
    }

    /// <summary>The label split into keycaps for the sheet: ["Ctrl", "Shift", "K"].</summary>
    public static IReadOnlyList<string> Keycaps(Shortcut s, bool mac)
    {
        var caps = new List<string>();
        if (mac)
        {
            if (s.Control) caps.Add("⌃");
            if (s.Alt) caps.Add("⌥");
            if (s.Shift) caps.Add("⇧");
            if (s.Primary) caps.Add("⌘");
            caps.Add(MacKey(s.Key));
            return caps;
        }
        if (s.Primary || s.Control) caps.Add("Ctrl");
        if (s.Shift) caps.Add("Shift");
        if (s.Alt) caps.Add("Alt");
        caps.Add(s.Key);
        return caps;
    }

    private static string MacKey(string key) => key switch
    {
        "Enter" => "↩",
        "Tab" => "⇥",
        "Esc" => "⎋",
        "Space" => "Space",
        _ => key
    };

    /// <summary>
    /// Shortcuts that collide (same keys, same scope). The registry must have
    /// none; a test keeps it that way when a shortcut is added.
    /// </summary>
    public static IReadOnlyList<(Shortcut A, Shortcut B)> Conflicts(IReadOnlyList<Shortcut> all)
    {
        var found = new List<(Shortcut, Shortcut)>();
        for (var i = 0; i < all.Count; i++)
            for (var j = i + 1; j < all.Count; j++)
            {
                var a = all[i];
                var b = all[j];
                if (a.Key != b.Key || a.Shift != b.Shift || a.Alt != b.Alt || a.Global != b.Global) continue;
                // Windows: Command and Control are both Ctrl. macOS: distinct keys.
                var windows = (a.Primary || a.Control) == (b.Primary || b.Control);
                var mac = a.Primary == b.Primary && a.Control == b.Control;
                if (windows || mac) found.Add((a, b));
            }
        return found;
    }

    /// <summary>The registry grouped for the sheet, in registry order; duplicate titles (alternate keys) share a row.</summary>
    public static IReadOnlyList<(string Group, IReadOnlyList<(string Title, IReadOnlyList<Shortcut> Keys, string Note)> Rows)> Sheet(IReadOnlyList<Shortcut> all)
    {
        var groups = new List<(string, IReadOnlyList<(string, IReadOnlyList<Shortcut>, string)>)>();
        foreach (var group in all.Select(s => s.Group).Distinct())
        {
            var rows = new List<(string, IReadOnlyList<Shortcut>, string)>();
            foreach (var title in all.Where(s => s.Group == group).Select(s => s.Title).Distinct())
            {
                var keys = all.Where(s => s.Group == group && s.Title == title).ToList();
                rows.Add((title, keys, keys.Select(k => k.Note).FirstOrDefault(n => n.Length > 0) ?? ""));
            }
            groups.Add((group, rows));
        }
        return groups;
    }
}

/// <summary>A global hotkey as text ("Alt+Space", "Ctrl+Shift+Space", "Option+Space").</summary>
internal sealed record HotkeyText(string Key, bool Primary, bool Shift, bool Alt)
{
    public static string DefaultFor(bool mac) => mac ? "Option+Space" : "Alt+Space";

    /// <summary>Choices offered in Settings (the first is the default).</summary>
    public static IReadOnlyList<string> Choices(bool mac) => mac
        ? new[] { "Option+Space", "Option+Shift+Space", "Cmd+Shift+Space", "Cmd+Option+Space" }
        : new[] { "Alt+Space", "Alt+Shift+Space", "Ctrl+Shift+Space", "Ctrl+Alt+Space", "Ctrl+Alt+N" };

    /// <summary>Parses "Ctrl+Alt+Space"; null when it has no key or no modifier.</summary>
    public static HotkeyText? Parse(string? text)
    {
        if (string.IsNullOrWhiteSpace(text)) return null;
        bool primary = false, shift = false, alt = false;
        var key = "";
        foreach (var raw in text.Split('+', StringSplitOptions.RemoveEmptyEntries | StringSplitOptions.TrimEntries))
        {
            switch (raw.ToLowerInvariant())
            {
                case "ctrl": case "control": case "cmd": case "command": case "⌘": primary = true; break;
                case "shift": case "⇧": shift = true; break;
                case "alt": case "option": case "opt": case "⌥": alt = true; break;
                default:
                    if (key.Length > 0) return null;
                    key = raw.Length == 1 ? raw.ToUpperInvariant() : char.ToUpperInvariant(raw[0]) + raw[1..].ToLowerInvariant();
                    break;
            }
        }
        if (key.Length == 0 || !(primary || alt)) return null;
        return new HotkeyText(key, primary, shift, alt);
    }

    /// <summary>Back to text, macOS names on a Mac ("Option+Space").</summary>
    public string Format(bool mac)
    {
        var parts = new List<string>();
        if (Primary) parts.Add(mac ? "Cmd" : "Ctrl");
        if (Alt) parts.Add(mac ? "Option" : "Alt");
        if (Shift) parts.Add("Shift");
        parts.Add(Key);
        return string.Join("+", parts);
    }
}
