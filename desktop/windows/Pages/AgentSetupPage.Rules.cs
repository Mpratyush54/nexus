using System.Text.Json;
using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Rules editor: CLAUDE.md, AGENTS.md, GEMINI.md, .cursorrules and each
/// agent's user-level file, edited in the detail pane with a Markdown
/// preview. Saving always shows the diff first; the core keeps a backup,
/// writes atomically, refuses a file that changed on disk meanwhile and
/// protects the sections Nexus keeps up to date (agents.config.rules_*).
/// </summary>
public sealed partial class AgentSetupPage
{
    // Nexus's own folder for backups and servers that are turned off.
    private static readonly string StateDir = System.IO.Path.Combine(
        ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "agent-config");

    private List<JsonElement> _rulesFiles = new();
    private RulesEditor? _editor;

    private sealed class RulesEditor
    {
        public string Path = "", Label = "", ETag = "", Saved = "";
        public bool Exists;
        public TextBox Box = null!;
        public List<(int Start, int End, string Label)> Managed = new();
        public bool Dirty => AgentConfigText.Differs(Saved, Box.Text);
    }

    private async Task LoadRulesFilesAsync()
    {
        var response = await Core.CallAsync("agents.config.rules_files", new { workspace = _workspace });
        await UiThread.Resume(DispatcherQueue);
        _rulesFiles = response.Ok && response.Result is { ValueKind: JsonValueKind.Array } list
            ? list.EnumerateArray().Select(e => e.Clone()).ToList()
            : new List<JsonElement>();
    }

    /// <summary>Opens on the Rules section (Settings → "Edit these files").</summary>
    internal void ShowRules()
    {
        if (_editor is { Dirty: true }) return; // never drop unsaved edits
        _editor = null;
        _selectedSection = "rules";
        _selectedItem = "";
        if (_search.Text.Length > 0) _search.Text = "";
        if (_selectedAgent == RunsKey || _selectedAgent.Length == 0) _selectedAgent = "claude";
        if (_agents.Count > 0) { RenderAgents(); RenderMiddle(); RenderDetail(); }
    }

    private bool CanEditRules(string path) =>
        _rulesFiles.Any(f => string.Equals(NxJson.Text(f, "path"), path, StringComparison.OrdinalIgnoreCase));

    // Main instruction files this agent reads that don't exist yet.
    private IEnumerable<JsonElement> CreatableRules(string agent) =>
        _rulesFiles.Where(f => !Flag(f, "exists") && Arr(f, "agents").Any(a => a.GetString() == agent))
                   .OrderBy(f => NxJson.Text(f, "scope") == "project" ? 0 : 1);

    private static string CreateTitle(JsonElement file)
    {
        var name = System.IO.Path.GetFileName(NxJson.Text(file, "path"));
        return NxJson.Text(file, "scope") == "project" ? "Create " + name + " for this project" : "Create your " + name + " for every project";
    }

    // Leaving an editor with unsaved text asks first. True: go ahead.
    private async Task<bool> ConfirmLeaveEditorAsync()
    {
        if (_editor is not { Dirty: true } editor) { _editor = null; return true; }
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Discard your changes?",
            Content = new TextBlock { Text = "Your edits to " + System.IO.Path.GetFileName(editor.Path) + " haven't been saved.", TextWrapping = TextWrapping.Wrap, FontSize = 12.5 },
            PrimaryButtonText = "Discard", CloseButtonText = "Keep editing", DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return false;
        _editor = null;
        return true;
    }

    private async Task OpenRulesEditorAsync(string path)
    {
        if (_editor is { } open && !string.Equals(open.Path, path, StringComparison.OrdinalIgnoreCase) && !await ConfirmLeaveEditorAsync()) return;
        var version = ++_detailVersion;
        _detail.Children.Clear();
        _detail.Children.Add(Tiny("Opening…", new Thickness(0, 6, 0, 0)));
        var read = await Core.CallAsync("agents.config.rules_read", new { path, workspace = _workspace });
        await UiThread.Resume(DispatcherQueue);
        if (version != _detailVersion) return;
        if (!read.Ok || read.Result is not { } doc)
        {
            _detail.Children.Clear();
            _detail.Children.Add(Tiny(read.Error ?? "Nexus couldn't open this file.", new Thickness(0, 6, 0, 0)));
            return;
        }
        doc = doc.Clone();
        var draft = NxJson.Text(doc, "content");
        if (!Flag(doc, "exists"))
        {
            var template = await Core.CallAsync("agents.config.rules_template", new { path, workspace = _workspace });
            await UiThread.Resume(DispatcherQueue);
            if (version != _detailVersion) return;
            draft = template.Ok && template.Result is { } t ? NxJson.Text(t, "content") : "";
        }
        RenderRulesEditor(doc, draft);
    }

    private void RenderRulesEditor(JsonElement doc, string draft)
    {
        _detail.Children.Clear();
        var exists = Flag(doc, "exists");
        var editor = _editor = new RulesEditor
        {
            Path = NxJson.Text(doc, "path"), Label = NxJson.Text(doc, "label"), ETag = NxJson.Text(doc, "etag"),
            Saved = exists ? NxJson.Text(doc, "content") : "", Exists = exists,
            Managed = Arr(doc, "managed").Select(m => (Int(m, "start"), Int(m, "end"), NxJson.Text(m, "label"))).ToList()
        };
        var name = System.IO.Path.GetFileName(editor.Path);

        var head = new StackPanel { Spacing = 3 };
        head.Children.Add(new TextBlock { Text = exists ? name : "New " + name, FontSize = 16, FontWeight = FontWeights.SemiBold });
        var readers = Arr(doc, "agents").Select(a => _agentNames.GetValueOrDefault(a.GetString() ?? "", a.GetString() ?? "")).Where(n => n.Length > 0).ToList();
        head.Children.Add(Tiny(editor.Label + (readers.Count > 0 ? " · read by " + AgentConfigText.AgentList(readers) : "")));
        head.Children.Add(new TextBlock { Text = editor.Path, FontFamily = MonoFont, FontSize = 11, Opacity = 0.6, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        _detail.Children.Add(head);

        if (!exists)
            _detail.Children.Add(Banner("A starting point is filled in below. Change anything you like; nothing is written until you save.", NexusPalette.AccentBorder));
        if (editor.Managed.Count > 0)
            _detail.Children.Add(Banner(AgentConfigText.ManagedNote(editor.Managed), NexusPalette.Amber));

        // Write / Preview, one at a time.
        var box = editor.Box = new TextBox
        {
            AcceptsReturn = true, Text = draft, TextWrapping = TextWrapping.Wrap, FontFamily = MonoFont, FontSize = 12.5,
            MinHeight = 320, IsSpellCheckEnabled = false, Padding = new Thickness(12, 10, 12, 10)
        };
        ScrollViewer.SetVerticalScrollBarVisibility(box, ScrollBarVisibility.Auto);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(box, "Edit " + name);
        var preview = new Border
        {
            Padding = new Thickness(14, 10, 14, 12), CornerRadius = new CornerRadius(6), MinHeight = 320,
            Background = NexusPalette.Surface, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1), Visibility = Visibility.Collapsed
        };
        var writeTab = Segment("Write", true);
        var previewTab = Segment("Preview", false);
        void Show(bool previewing)
        {
            box.Visibility = previewing ? Visibility.Collapsed : Visibility.Visible;
            preview.Visibility = previewing ? Visibility.Visible : Visibility.Collapsed;
            SetSegment(writeTab, !previewing);
            SetSegment(previewTab, previewing);
            if (previewing)
            {
                var text = AgentConfigText.Newlines(box.Text);
                preview.Child = text.Trim().Length == 0 ? Tiny("Nothing to preview yet.") : Markdown(text);
                Motion.FadeSlideIn(preview, dy: 6, durationMs: 220);
            }
            else box.Focus(FocusState.Programmatic);
        }
        writeTab.Click += (_, _) => Show(false);
        previewTab.Click += (_, _) => Show(true);
        var tabs = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 2, VerticalAlignment = VerticalAlignment.Center };
        tabs.Children.Add(writeTab);
        tabs.Children.Add(previewTab);

        // Toolbar above the text, so the one primary action is always in view.
        var status = new TextBlock { FontSize = 11.5, Foreground = NexusPalette.Muted, VerticalAlignment = VerticalAlignment.Center, TextWrapping = TextWrapping.NoWrap, TextTrimming = TextTrimming.CharacterEllipsis };
        var undo = new HyperlinkButton { Content = "Undo", Padding = new Thickness(4, 0, 4, 0), FontSize = 11.5, MinHeight = 0, Visibility = Visibility.Collapsed, VerticalAlignment = VerticalAlignment.Center };
        var discard = FlatButton("Discard changes");
        var save = new Button { Content = exists ? "Review and save" : "Review and create", Padding = new Thickness(14, 6, 14, 7) };
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accent) && accent is Style style) save.Style = style;
        ToolTipService.SetToolTip(save, "See exactly what changes before anything is written (" + Shortcuts.Label("S") + ")");
        string? undoText = null;
        void Refresh()
        {
            if (_editor != editor) return;
            var dirty = editor.Dirty || !editor.Exists;
            save.IsEnabled = dirty;
            discard.Visibility = editor.Dirty && editor.Exists ? Visibility.Visible : Visibility.Collapsed;
            if (editor.Dirty) { status.Text = editor.Exists ? "Unsaved changes" : "Not created yet"; undo.Visibility = Visibility.Collapsed; }
        }
        box.TextChanged += (_, _) => Refresh();
        discard.Click += (_, _) => { box.Text = editor.Saved; status.Text = "Changes discarded."; Refresh(); };
        async Task SaveFlow(string? content = null, bool isUndo = false)
        {
            var text = content ?? AgentConfigText.Newlines(box.Text);
            var before = editor.Saved;
            var existedBefore = editor.Exists;
            if (!await ReviewAndSaveRulesAsync(editor, text, isUndo, status)) return;
            if (_editor != editor) return;
            if (!existedBefore)
            {
                // A new file: reopen it as an ordinary saved file. The editor
                // stays set meanwhile, so a background refresh leaves it alone.
                await OpenRulesEditorAsync(editor.Path);
                return;
            }
            if (isUndo) box.Text = editor.Saved;
            // Undo puts back the version before this save (a new file has none).
            undoText = !isUndo && existedBefore ? before : null;
            undo.Visibility = undoText is null ? Visibility.Collapsed : Visibility.Visible;
            save.Content = "Review and save";
            Refresh();
        }
        save.Click += async (_, _) => await SaveFlow();
        undo.Click += async (_, _) => { if (undoText is { } t) await SaveFlow(t, isUndo: true); };
        var saveKey = new Microsoft.UI.Xaml.Input.KeyboardAccelerator { Key = Windows.System.VirtualKey.S, Modifiers = Shortcuts.Primary };
        saveKey.Invoked += async (_, e) => { e.Handled = true; if (save.IsEnabled) await SaveFlow(); };
        box.KeyboardAccelerators.Add(saveKey);

        var statusRow = new Grid { ColumnSpacing = 2, VerticalAlignment = VerticalAlignment.Center };
        statusRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        statusRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        statusRow.Children.Add(status);
        Grid.SetColumn(undo, 1);
        statusRow.Children.Add(undo);
        statusRow.Margin = new Thickness(8, 0, 0, 0);
        var toolbar = new Grid { ColumnSpacing = 8 };
        toolbar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        toolbar.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        toolbar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        toolbar.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        toolbar.Children.Add(tabs);
        Grid.SetColumn(statusRow, 1); toolbar.Children.Add(statusRow);
        Grid.SetColumn(discard, 2); toolbar.Children.Add(discard);
        Grid.SetColumn(save, 3); toolbar.Children.Add(save);
        _detail.Children.Add(toolbar);
        _detail.Children.Add(box);
        _detail.Children.Add(preview);
        status.Text = exists ? "Backed up before each save." : "";
        Refresh();
        Motion.FadeSlideIn(box, dy: 8, durationMs: 260);
    }

    // Shows the diff, then saves. True when the file was written.
    private async Task<bool> ReviewAndSaveRulesAsync(RulesEditor editor, string text, bool isUndo, TextBlock status)
    {
        var name = System.IO.Path.GetFileName(editor.Path);
        var check = await Core.CallAsync("agents.config.rules_preview", new { path = editor.Path, workspace = _workspace, content = text, etag = editor.ETag });
        await UiThread.Resume(DispatcherQueue);
        if (!check.Ok || check.Result is not { } preview) { status.Text = check.Error ?? "Nexus couldn't compare the file."; return false; }
        if (Flag(preview, "unchanged")) { status.Text = "No changes to save."; return false; }

        var body = new StackPanel { Spacing = 10, MinWidth = 420, MaxWidth = 640 };
        var conflict = Flag(preview, "conflict");
        var create = Flag(preview, "create");
        if (conflict)
            body.Children.Add(Banner(name + " changed on disk since you opened it (another app or agent edited it). Reload to get the new version; your edits here are kept until you do.", NexusPalette.Amber));
        CheckBox? allowManaged = null;
        if (Flag(preview, "managed_changed") && !conflict)
        {
            body.Children.Add(Banner("This edit changes the section Nexus keeps up to date. Nexus will replace it on its next update, so your edit there won't last.", NexusPalette.Amber));
            allowManaged = new CheckBox { Content = "Save it anyway", FontSize = 12.5 };
            body.Children.Add(allowManaged);
        }
        body.Children.Add(Tiny(create ? "Nexus will create this file:" : isUndo ? "Nexus will put back the previous version:" : "Nexus will make these changes:"));
        body.Children.Add(FileDiffView.Create(new FileDiffPreview(editor.Path, NxJson.Text(preview, "diff"))));
        if (!create) body.Children.Add(Tiny("The current file is backed up first, so you can undo."));

        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = (create ? "Create " : "Save ") + name + "?",
            Content = new ScrollViewer { Content = body, MaxHeight = 560, VerticalScrollBarVisibility = ScrollBarVisibility.Auto },
            PrimaryButtonText = conflict ? "Reload file" : create ? "Create" : "Save", CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Primary
        };
        if (allowManaged is not null)
        {
            dialog.IsPrimaryButtonEnabled = false;
            allowManaged.Checked += (_, _) => dialog.IsPrimaryButtonEnabled = true;
            allowManaged.Unchecked += (_, _) => dialog.IsPrimaryButtonEnabled = false;
        }
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return false;
        if (conflict)
        {
            await OpenRulesEditorAsync(editor.Path);
            return false;
        }

        var done = await Core.CallAsync("agents.config.rules_save", new
        {
            path = editor.Path, workspace = _workspace, content = text, etag = editor.ETag,
            allow_managed = allowManaged?.IsChecked == true, state_dir = StateDir
        });
        await UiThread.Resume(DispatcherQueue);
        if (!done.Ok || done.Result is not { } saved) { status.Text = done.Error ?? "Nexus couldn't save the file."; return false; }
        var created = !editor.Exists;
        editor.ETag = NxJson.Text(saved, "etag");
        editor.Saved = text;
        editor.Exists = true;
        status.Text = isUndo ? "Previous version restored." : created ? "Created. Agents use it from their next chat." : "Saved. Agents use it from their next chat.";
        _status.Text = (created ? "Created " : "Saved ") + editor.Path;
        if (created) _ = LoadAsync();
        return true;
    }

    // ── Small pieces ───────────────────────────────────────────────────

    private static int Int(JsonElement obj, string key) =>
        obj.ValueKind == JsonValueKind.Object && obj.TryGetProperty(key, out var v) && v.TryGetInt32(out var n) ? n : 0;

    private static Border Banner(string text, Brush accent) => new()
    {
        Child = new TextBlock { Text = text, FontSize = 12, TextWrapping = TextWrapping.Wrap },
        Padding = new Thickness(12, 8, 12, 9), CornerRadius = new CornerRadius(6),
        Background = NexusPalette.Surface, BorderBrush = accent, BorderThickness = new Thickness(3, 1, 1, 1)
    };

    private static Button Segment(string text, bool selected)
    {
        var b = new Button
        {
            Content = new TextBlock { Text = text, FontSize = 12 }, Padding = new Thickness(12, 4, 12, 5), MinHeight = 26,
            CornerRadius = new CornerRadius(5), BorderThickness = new Thickness(0)
        };
        SetSegment(b, selected);
        return b;
    }

    private static void SetSegment(Button b, bool selected)
    {
        b.Background = selected ? SessionChatRender.SubtleFill : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        if (b.Content is TextBlock t) { t.Opacity = selected ? 1 : 0.7; t.FontWeight = selected ? FontWeights.SemiBold : FontWeights.Normal; }
    }
}
