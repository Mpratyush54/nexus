using System.Diagnostics;
using System.Text;
using System.Text.Json;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Controls.Primitives;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;

namespace Nexus.Pages;

// Read-only preview of a file mentioned in a chat: rendered markdown (with
// a Source toggle) or numbered text scrolled to the mentioned line. It never
// writes to the file.
internal sealed class FilePreviewPanel : UserControl
{
    private const int MaxBytes = 1024 * 1024;
    private const double CodeLineHeight = 18;
    private static readonly HashSet<string> MarkdownExtensions = new(StringComparer.OrdinalIgnoreCase) { ".md", ".mdx", ".markdown" };
    // Opening these "in the default app" runs them; Reveal is offered instead.
    private static readonly HashSet<string> RunnableExtensions = new(StringComparer.OrdinalIgnoreCase)
    {
        ".exe", ".com", ".bat", ".cmd", ".ps1", ".psm1", ".vbs", ".vbe", ".js", ".jse", ".wsf", ".wsh", ".msi", ".msp",
        ".lnk", ".scr", ".hta", ".cpl", ".reg", ".jar", ".appref-ms", ".url", ".pif", ".application", ".gadget", ".msc",
    };

    private readonly TextBlock _title = new() { FontSize = 14, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, TextTrimming = TextTrimming.CharacterEllipsis, VerticalAlignment = VerticalAlignment.Center };
    private readonly TextBlock _path = new() { FontSize = 11.5, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true };
    private readonly ComboBox _picker = new() { HorizontalAlignment = HorizontalAlignment.Stretch, Visibility = Visibility.Collapsed, FontSize = 12 };
    private readonly ToggleButton _source = new() { Content = "Source", Padding = new Thickness(8, 3, 8, 3), FontSize = 12, Visibility = Visibility.Collapsed };
    private readonly Button _open = ActionButton("Open", "Open in the default app");
    private readonly Button _reveal = ActionButton("Reveal", "Show in File Explorer");
    private readonly Button _copy = ActionButton("Copy path", "Copy the full path");
    private readonly TextBlock _status = new() { FontSize = 12.5, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
    private readonly ScrollViewer _scroll = new() { HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
    private string _file = "";
    private string _workspace = "";
    private string _text = "";
    private FileRef? _reference;
    private int _generation;

    public event Action? CloseRequested;

    public FilePreviewPanel(bool framed = true)
    {
        _path.Foreground = NexusPalette.Muted;
        _path.FontFamily = new FontFamily("Cascadia Mono, Consolas");
        var close = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 11 }, Padding = new Thickness(6), MinWidth = 28, MinHeight = 28,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), BorderThickness = new Thickness(0)
        };
        ToolTipService.SetToolTip(close, "Close preview (Esc)");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(close, "Close preview");
        close.Click += (_, _) => CloseRequested?.Invoke();
        var head = new Grid { ColumnSpacing = 8 };
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.Children.Add(_title);
        Grid.SetColumn(close, 1);
        head.Children.Add(close);

        var actions = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        actions.Children.Add(_source);
        actions.Children.Add(_open);
        actions.Children.Add(_reveal);
        actions.Children.Add(_copy);
        _source.Click += (_, _) => Render();
        _open.Click += (_, _) => OpenInDefaultApp(_file);
        _reveal.Click += (_, _) => Reveal(_file);
        _copy.Click += (_, _) => { var data = new DataPackage(); data.SetText(_file); Clipboard.SetContent(data); };
        _picker.SelectionChanged += async (_, _) =>
        {
            if (_picker.SelectedItem is ComboBoxItem { Tag: string path } && !string.Equals(path, _file, StringComparison.OrdinalIgnoreCase))
                await LoadAsync(path, ++_generation);
        };

        var grid = new Grid { RowSpacing = 8 };
        for (var i = 0; i < 5; i++) grid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        grid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        UIElement[] rows = { head, _path, _picker, actions, _status };
        for (var i = 0; i < rows.Length; i++) { Grid.SetRow((FrameworkElement)rows[i], i); grid.Children.Add(rows[i]); }
        Grid.SetRow(_scroll, 5);
        grid.Children.Add(_scroll);

        Content = framed
            ? new Border
            {
                Child = grid,
                Padding = new Thickness(14, 10, 10, 12),
                CornerRadius = new CornerRadius(12),
                BorderThickness = new Thickness(1),
                BorderBrush = NexusPalette.Hairline,
                Background = NexusPalette.Surface,
                Shadow = new ThemeShadow(),
                Translation = new System.Numerics.Vector3(0, 0, 24),
            }
            : grid;
        Transitions = new TransitionCollection { new EntranceThemeTransition { FromHorizontalOffset = 48, FromVerticalOffset = 0 } };
        var escape = new Microsoft.UI.Xaml.Input.KeyboardAccelerator { Key = Windows.System.VirtualKey.Escape };
        escape.Invoked += (_, e) => { e.Handled = true; CloseRequested?.Invoke(); };
        KeyboardAccelerators.Add(escape);
    }

    public async Task ShowAsync(FileRef reference, string workspace)
    {
        var generation = ++_generation;
        _reference = reference;
        _workspace = workspace;
        _file = "";
        _text = "";
        _title.Text = Path.GetFileName(reference.Path.TrimEnd('/', '\\')) is { Length: > 0 } name ? name : reference.Path;
        _path.Text = reference.Path + LineSuffix(reference);
        _picker.Visibility = Visibility.Collapsed;
        _source.Visibility = Visibility.Collapsed;
        SetActions(open: false, reveal: false, copy: false);
        _scroll.Content = null;
        Status("Finding file…");
        var roots = await KnownFolders.GetAsync();
        FileRefResolution result;
        try { result = await Task.Run(() => Resolve(reference, workspace, roots)); }
        catch (Exception ex) { if (generation == _generation) Status("Could not look up this file: " + ex.Message); return; }
        if (generation != _generation) return;
        switch (result.Status)
        {
            case FileRefStatus.Found:
                FillPicker(result.Matches);
                await LoadAsync(result.Matches[0], generation);
                break;
            case FileRefStatus.OutsideRoots:
                _file = result.Candidate;
                _path.Text = result.Candidate + LineSuffix(reference);
                var exists = await Task.Run(() => File.Exists(result.Candidate) || Directory.Exists(Path.GetDirectoryName(result.Candidate) ?? ""));
                if (generation != _generation) return;
                SetActions(open: false, reveal: exists, copy: true);
                Status("File is outside this chat's folder, so it isn't previewed here." + (exists ? " Use Reveal to see it in File Explorer." : ""));
                break;
            case FileRefStatus.NoWorkspace:
                SetActions(open: false, reveal: false, copy: false);
                Status("This chat has no project folder, so this relative path can't be found. Choose the chat's folder first.");
                break;
            default:
                _file = result.Candidate;
                SetActions(open: false, reveal: false, copy: result.Candidate.Length > 0);
                Status(workspace.Length > 0
                    ? "Couldn't find " + _title.Text + " in this chat's folder (" + workspace + ")."
                    : "Couldn't find " + _title.Text + ".");
                break;
        }
    }

    // A dialog for messages shown outside a chat page.
    public static async Task ShowDialogAsync(XamlRoot root, FileRef reference, string workspace)
    {
        var panel = new FilePreviewPanel(framed: false) { Width = 620, Height = 520 };
        var dialog = new ContentDialog { XamlRoot = root, Content = panel, CloseButtonText = "Close" };
        panel.CloseRequested += () => dialog.Hide();
        _ = panel.ShowAsync(reference, workspace);
        try { await dialog.ShowThemedAsync(); } catch (Exception) { /* another dialog is open */ }
    }

    // The workspace first; with no workspace, each known project folder in
    // turn (only the first that has the file wins).
    private static FileRefResolution Resolve(FileRef reference, string workspace, IReadOnlyList<string> roots)
    {
        if (workspace.Length > 0 && Directory.Exists(workspace)) return FileRefs.Resolve(reference, workspace, roots);
        if (FileRefs.IsAbsolute(reference.Path)) return FileRefs.Resolve(reference, "", roots);
        foreach (var root in roots)
        {
            var attempt = FileRefs.Resolve(reference, root, roots);
            if (attempt.Status == FileRefStatus.Found) return attempt;
        }
        return new FileRefResolution(FileRefStatus.NoWorkspace, Array.Empty<string>(), reference.Path);
    }

    private void FillPicker(IReadOnlyList<string> matches)
    {
        _picker.Items.Clear();
        if (matches.Count < 2) { _picker.Visibility = Visibility.Collapsed; return; }
        foreach (var match in matches)
        {
            var label = _workspace.Length > 0 && FileRefs.IsInside(match, _workspace) ? Path.GetRelativePath(_workspace, match) : match;
            _picker.Items.Add(new ComboBoxItem { Content = label, Tag = match });
        }
        _picker.SelectedIndex = 0;
        _picker.Visibility = Visibility.Visible;
        ToolTipService.SetToolTip(_picker, matches.Count + " files share this name");
    }

    private async Task LoadAsync(string file, int generation)
    {
        _file = file;
        _path.Text = file + (_reference is { } r ? LineSuffix(r) : "");
        SetActions(open: !RunnableExtensions.Contains(Path.GetExtension(file)), reveal: true, copy: true);
        ToolTipService.SetToolTip(_open, _open.IsEnabled ? "Open in the default app" : "Files that run as programs aren't opened from here; use Reveal");
        Status("Loading…");
        _scroll.Content = null;
        (string Text, bool Binary, bool Truncated, long Size) read;
        try { read = await Task.Run(() => Read(file)); }
        catch (Exception ex) when (ex is IOException or UnauthorizedAccessException)
        {
            if (generation == _generation) Status("Could not read this file: " + ex.Message);
            return;
        }
        if (generation != _generation) return;
        if (read.Binary)
        {
            _text = "";
            _source.Visibility = Visibility.Collapsed;
            Status("Binary file · " + Size(read.Size));
            return;
        }
        _text = read.Text;
        var markdown = MarkdownExtensions.Contains(Path.GetExtension(file));
        _source.Visibility = markdown ? Visibility.Visible : Visibility.Collapsed;
        // A line reference into markdown means the source line.
        _source.IsChecked = markdown && _reference?.Line is not null;
        Status(read.Truncated ? "Large file: showing the first 1 MB of " + Size(read.Size) + "." : "");
        Render();
    }

    private static (string Text, bool Binary, bool Truncated, long Size) Read(string file)
    {
        using var stream = new FileStream(file, FileMode.Open, FileAccess.Read, FileShare.ReadWrite | FileShare.Delete);
        var size = stream.Length;
        var buffer = new byte[(int)Math.Min(size, MaxBytes)];
        var read = 0;
        while (read < buffer.Length)
        {
            var n = stream.Read(buffer, read, buffer.Length - read);
            if (n == 0) break;
            read += n;
        }
        var probe = Math.Min(read, 8000);
        var utf16 = read >= 2 && (buffer[0] == 0xFF && buffer[1] == 0xFE || buffer[0] == 0xFE && buffer[1] == 0xFF);
        if (!utf16 && Array.IndexOf(buffer, (byte)0, 0, probe) >= 0) return ("", true, false, size);
        using var reader = new StreamReader(new MemoryStream(buffer, 0, read), Encoding.UTF8, detectEncodingFromByteOrderMarks: true);
        return (reader.ReadToEnd(), false, size > read, size);
    }

    private void Render()
    {
        if (_text.Length == 0) { _scroll.Content = Note("Empty file."); return; }
        var markdown = _source.Visibility == Visibility.Visible && _source.IsChecked != true;
        if (markdown)
        {
            var document = SessionPage.FormattedMessage(_text);
            document.Margin = new Thickness(0, 0, 10, 0);
            _scroll.HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled;
            _scroll.Content = document;
            return;
        }
        _scroll.HorizontalScrollBarVisibility = ScrollBarVisibility.Auto;
        _scroll.Content = CodeView(_text, _reference?.Line);
    }

    private FrameworkElement CodeView(string text, int? line)
    {
        var lines = text.Replace("\r\n", "\n").Split('\n');
        if (lines.Length > 1 && lines[^1].Length == 0) lines = lines[..^1];
        var body = string.Join("\n", lines);
        var mono = new FontFamily("Cascadia Mono, Consolas");
        TextBlock Block(string value) => new()
        {
            Text = value, FontFamily = mono, FontSize = 12, LineHeight = CodeLineHeight,
            LineStackingStrategy = LineStackingStrategy.BlockLineHeight, TextWrapping = TextWrapping.NoWrap,
        };
        var numbers = Block(string.Join("\n", Enumerable.Range(1, lines.Length)));
        numbers.Foreground = NexusPalette.Muted;
        numbers.TextAlignment = TextAlignment.Right;
        numbers.Opacity = 0.7;
        var code = Block(body);
        code.IsTextSelectionEnabled = true;
        var grid = new Grid { ColumnSpacing = 14, Padding = new Thickness(0, 2, 12, 8) };
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        grid.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        grid.Children.Add(numbers);
        Grid.SetColumn(code, 1);
        grid.Children.Add(code);
        if (line is int target && target >= 1 && target <= lines.Length)
        {
            var start = 0;
            for (var i = 0; i < target - 1; i++) start += lines[i].Length + 1;
            code.TextHighlighters.Add(Highlight(start, Math.Max(1, lines[target - 1].Length)));
            var numberStart = 0;
            for (var i = 1; i < target; i++) numberStart += i.ToString().Length + 1;
            numbers.TextHighlighters.Add(Highlight(numberStart, target.ToString().Length));
            grid.Loaded += (_, _) => DispatcherQueue.TryEnqueue(Microsoft.UI.Dispatching.DispatcherQueuePriority.Low, () =>
                _scroll.ChangeView(null, Math.Max(0, (target - 1) * CodeLineHeight - _scroll.ViewportHeight / 3), null, true));
        }
        return grid;
    }

    private static TextHighlighter Highlight(int start, int length)
    {
        var highlighter = new TextHighlighter { Background = NexusPalette.AccentFill };
        highlighter.Ranges.Add(new TextRange { StartIndex = start, Length = length });
        return highlighter;
    }

    private void Status(string text)
    {
        _status.Text = text;
        _status.Foreground = NexusPalette.Muted;
        _status.Visibility = text.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private void SetActions(bool open, bool reveal, bool copy)
    {
        _open.IsEnabled = open;
        _reveal.IsEnabled = reveal;
        _copy.IsEnabled = copy;
    }

    private static TextBlock Note(string text) => new() { Text = text, FontSize = 12.5, Foreground = NexusPalette.Muted };

    private static Button ActionButton(string text, string tip)
    {
        var button = new Button { Content = text, Padding = new Thickness(8, 3, 8, 3), FontSize = 12 };
        ToolTipService.SetToolTip(button, tip);
        return button;
    }

    private static string LineSuffix(FileRef reference) =>
        reference.Line is int line ? ":" + line + (reference.Column is int column ? ":" + column : "") : "";

    private static string Size(long bytes) =>
        bytes >= 1024 * 1024 ? (bytes / 1024d / 1024d).ToString("0.#") + " MB" : bytes >= 1024 ? (bytes / 1024d).ToString("0.#") + " KB" : bytes + " bytes";

    private static void OpenInDefaultApp(string file)
    {
        if (file.Length == 0 || RunnableExtensions.Contains(Path.GetExtension(file)) || !File.Exists(file)) return;
        try { Process.Start(new ProcessStartInfo(file) { UseShellExecute = true }); }
        catch (Exception ex) when (ex is System.ComponentModel.Win32Exception or InvalidOperationException) { }
    }

    private static void Reveal(string file)
    {
        if (file.Length == 0) return;
        try
        {
            if (File.Exists(file)) Process.Start("explorer.exe", "/select,\"" + file + "\"");
            else if (Path.GetDirectoryName(file) is { Length: > 0 } folder && Directory.Exists(folder)) Process.Start("explorer.exe", "\"" + folder + "\"");
        }
        catch (Exception ex) when (ex is System.ComponentModel.Win32Exception or InvalidOperationException) { }
    }
}

// Folders a file may be previewed from besides the chat's own: the user's
// saved project folders and approved capture folders. Cached briefly.
internal static class KnownFolders
{
    private static IReadOnlyList<string> _cached = Array.Empty<string>();
    private static DateTime _at = DateTime.MinValue;

    public static async Task<IReadOnlyList<string>> GetAsync()
    {
        if (DateTime.UtcNow - _at < TimeSpan.FromMinutes(5)) return _cached;
        var folders = new List<string>();
        if (AppPrefs.Data.ProjectFolders is { } saved) folders.AddRange(saved.Values);
        try
        {
            var status = await App.Core.CallAsync(NxMethods.StatusGet);
            if (status.Ok && status.Result is { } body && body.TryGetProperty("capture", out var capture) && capture.ValueKind == JsonValueKind.Object)
            {
                if (capture.TryGetProperty("approved_workspaces", out var approved) && approved.ValueKind == JsonValueKind.Array)
                    folders.AddRange(approved.EnumerateArray().Where(item => item.ValueKind == JsonValueKind.String).Select(item => item.GetString() ?? ""));
                if (capture.TryGetProperty("workspace_details", out var details) && details.ValueKind == JsonValueKind.Array)
                    folders.AddRange(details.EnumerateArray().Select(item => NxJson.Text(item, "path")));
            }
        }
        catch (Exception) { /* offline core: saved folders still apply */ }
        _cached = folders.Where(folder => folder.Length > 0).Distinct(StringComparer.OrdinalIgnoreCase).ToList();
        _at = DateTime.UtcNow;
        return _cached;
    }
}
