using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using VirtualKey = Windows.System.VirtualKey;

namespace Nexus.Pages;

/// <summary>
/// The composer's power keys for one message box: "/" commands, "@"
/// mentions and ↑ recall. Used by the chat composer (SessionPage.Pickers.cs)
/// and the new-chat composer on Home (DashboardPage), each through
/// <see cref="IHost"/>.
///
/// "/" at the start of the message opens the command list (the host's own
/// commands plus the agent's custom commands found on disk). "@" opens files
/// and folders from the project (git's list, .gitignore respected), other
/// Nexus chats and memories; each pick becomes a chip (<see cref="ChipStrip"/>).
/// On send the host passes <see cref="Mentions"/> to MentionText. ↑ in an
/// empty box brings back an earlier prompt; ↑/↓ walk the history, Esc clears
/// it. The list never takes focus and its searches run off the UI thread, so
/// typing is never held up.
/// </summary>
internal sealed class ComposerAssist
{
    /// <summary>What the composer around the box provides.</summary>
    internal interface IHost
    {
        string Agent { get; }
        /// <summary>The project folder ("" when unknown: no file mentions).</summary>
        string Workspace { get; }
        string ProjectId { get; }
        /// <summary>The chat being written in, left out of "@" chats ("" on Home).</summary>
        string SessionId { get; }
        /// <summary>Nexus's own commands here, in menu order.</summary>
        IReadOnlyList<ComposerCommands.Command> Builtins();
        /// <summary>Earlier prompts from the transcript, oldest first.</summary>
        IEnumerable<string> TranscriptPrompts();
        /// <summary>False while attachments or pasted blocks are in the composer (↑/↓ then move the caret).</summary>
        bool CanRecall { get; }
        /// <summary>True when Esc isn't needed to stop a reply.</summary>
        bool IsIdle { get; }
        /// <summary>Runs one of the host's own commands (<see cref="ComposerCommands.Kind.Action"/>).</summary>
        Task RunCommandAsync(string name, string argument);
        /// <summary>Sends what is in the box now (/compact).</summary>
        Task SendAsync();
        /// <summary>The chips changed: save the draft, restyle Send.</summary>
        void MentionsChanged();
    }

    private static readonly Dictionary<string, List<string>> s_recentMentions = new(StringComparer.OrdinalIgnoreCase);

    private readonly TextBox _box;
    private readonly FrameworkElement _anchor;
    private readonly IHost _host;
    private readonly ComposerPopup _picker;
    private readonly List<ComposerMention> _mentions = new();
    private readonly StackPanel _chips = new() { Orientation = Orientation.Horizontal, Spacing = 6 };
    private readonly PromptRecall _recall = new();
    private readonly List<string> _sentHere = new();
    private readonly List<ComposerPopup.Row> _remoteRows = new();
    private ComposerCommands.Trigger? _trigger;
    private int _version;
    private bool _setting;
    private bool _composing;
    // Text put in the box by code (a pick, a recall, a restored draft):
    // TextChanged arrives later, and it must not open the list by itself.
    private string? _programmatic;
    private IReadOnlyList<ComposerCommands.Command> _custom = Array.Empty<ComposerCommands.Command>();
    private string _customKey = "";
    private string _remoteQuery = "\0";
    private bool _remoteLoading;
    private DispatcherQueueTimer? _remoteTimer;
    private DispatcherQueueTimer? _closeTimer;
    private IReadOnlyList<ProjectFileIndex.Hit> _shownFiles = Array.Empty<ProjectFileIndex.Hit>();
    private string _shownQuery = "";
    private bool _shownLoading;
    private string _shownEmpty = "";

    public ComposerAssist(TextBox box, FrameworkElement anchor, IHost host)
    {
        _box = box;
        _anchor = anchor;
        _host = host;
        _picker = new ComposerPopup(anchor);
        _picker.Picked += row => { _ = PickAsync(row); };
        ChipStrip = new ScrollViewer
        {
            Content = _chips, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
            HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled, Visibility = Visibility.Collapsed
        };
        box.TextCompositionStarted += (_, _) => _composing = true;
        box.TextCompositionEnded += (_, _) => _composing = false;
        box.TextChanged += (_, _) => { if (!_setting) ForgetRecallIfEdited(); Update(); };
        box.SelectionChanged += (_, _) => { if (_picker.IsOpen || box.Text.Contains('@') || box.Text.StartsWith('/')) Update(); };
        box.LostFocus += (_, _) => QueueClose();
        box.GotFocus += (_, _) => _closeTimer?.Stop();
        box.Unloaded += (_, _) => _picker.Close();
    }

    /// <summary>The mention chips; the host places it above the message box.</summary>
    public ScrollViewer ChipStrip { get; }

    public IReadOnlyList<ComposerMention> Mentions => _mentions;

    /// <summary>The agent's own commands found for the current agent and folder.</summary>
    public IReadOnlyList<ComposerCommands.Command> CustomCommands => _custom;

    public bool IsOpen => _picker.IsOpen;

    public const string HelpText = "Type / for commands, @ to mention a file, chat or memory, ↑ to bring back your last message.";

    public IReadOnlyList<ComposerCommands.Command> AllCommands() => _host.Builtins().Concat(_custom).ToList();

    private DispatcherQueue? Queue => _box.DispatcherQueue;

    private void ForgetRecallIfEdited()
    {
        if (_recall.Index >= 0 && _box.Text != _recall.Shown) _recall.Reset();
    }

    private void QueueClose()
    {
        if (Queue is null) return;
        _closeTimer ??= Queue.CreateTimer();
        _closeTimer.Interval = TimeSpan.FromMilliseconds(180);
        _closeTimer.IsRepeating = false;
        _closeTimer.Tick -= OnCloseTick;
        _closeTimer.Tick += OnCloseTick;
        _closeTimer.Start();
    }

    private void OnCloseTick(DispatcherQueueTimer sender, object args)
    {
        if (_box.FocusState == FocusState.Unfocused) Close();
    }

    public void Close()
    {
        _trigger = null;
        _version++;
        _picker.Close();
    }

    private double Width(double wanted) => Math.Max(240, Math.Min(wanted, _anchor.ActualWidth > 0 ? _anchor.ActualWidth : wanted));

    private static bool IsDown(VirtualKey key) =>
        Microsoft.UI.Input.InputKeyboardSource.GetKeyStateForCurrentThread(key).HasFlag(Windows.UI.Core.CoreVirtualKeyStates.Down);

    // ── Keys ─────────────────────────────────────────────────────────────

    /// <summary>
    /// Call first from the box's (Preview)KeyDown. ↑/↓, Enter/Tab and Esc
    /// while the list is open; ↑/↓ recall and Esc over a recalled prompt when
    /// it isn't. True when the key was used here.
    /// </summary>
    public bool HandleKey(KeyRoutedEventArgs e)
    {
        if (_composing) return false;
        var shift = IsDown(VirtualKey.Shift);
        var primary = Shortcuts.IsPrimaryDown();
        if (_picker.IsOpen)
        {
            switch (e.Key)
            {
                case VirtualKey.Escape:
                    Close();
                    return true;
                case VirtualKey.Up when _picker.HasRows && !shift && !primary:
                    _picker.Move(-1);
                    return true;
                case VirtualKey.Down when _picker.HasRows && !shift && !primary:
                    _picker.Move(1);
                    return true;
                case VirtualKey.Enter or VirtualKey.Tab when _picker.HasRows && !shift && !primary:
                    if (_picker.Pick() is { } row) _ = PickAsync(row);
                    return true;
                case VirtualKey.Tab:
                    // Nothing to pick: Tab still mustn't jump out of the message box.
                    return true;
                case VirtualKey.Enter when _picker.IsLoading && !shift:
                    // Still looking: Enter must not send a half-typed mention.
                    return true;
            }
            return false;
        }
        if (e.Key is VirtualKey.Up or VirtualKey.Down && !shift && !primary && !IsDown(VirtualKey.Menu))
        {
            if (!_host.CanRecall || _mentions.Count > 0) return false;
            var history = PromptRecall.Build(_sentHere, _host.TranscriptPrompts());
            var next = _recall.Step(e.Key == VirtualKey.Up, _box.Text, _box.SelectionStart, history);
            if (next is null) return false;
            SetText(next, e.Key == VirtualKey.Up ? 0 : next.Length);
            return true;
        }
        if (e.Key == VirtualKey.Escape && _recall.Index >= 0 && _host.IsIdle)
        {
            _recall.Reset();
            SetText("", 0);
            return true;
        }
        return false;
    }

    /// <summary>Puts text in the box without opening the list.</summary>
    public void SetText(string text, int caret)
    {
        _setting = true;
        try
        {
            _programmatic = text;
            _box.Text = text;
            _box.SelectionStart = Math.Clamp(caret, 0, text.Length);
        }
        finally { _setting = false; }
    }

    /// <summary>After a message leaves the box: it is first in the ↑ history.</summary>
    public void RememberSent(string typed)
    {
        _recall.Reset();
        typed = typed.Trim();
        if (typed.Length == 0) return;
        _sentHere.Remove(typed);
        _sentHere.Add(typed);
        if (_sentHere.Count > 100) _sentHere.RemoveAt(0);
    }

    /// <summary>Another chat: no list open, no chips, and (optionally) a fresh history.</summary>
    public void Reset(bool clearHistory)
    {
        Close();
        _recall.Reset();
        if (clearHistory) _sentHere.Clear();
        ClearMentions();
        _remoteRows.Clear();
        _remoteQuery = "\0";
    }

    // ── What the caret is in ─────────────────────────────────────────────

    private void Update()
    {
        if (_setting || _composing) return;
        if (_programmatic is not null)
        {
            if (_box.Text == _programmatic) { if (_picker.IsOpen) Close(); return; }
            _programmatic = null;
        }
        var trigger = _box.SelectionLength == 0 ? ComposerCommands.Find(_box.Text, _box.SelectionStart) : null;
        if (trigger is null || !_box.IsEnabled)
        {
            if (_trigger is not null || _picker.IsOpen) Close();
            return;
        }
        var same = _trigger is { } t && t.Symbol == trigger.Value.Symbol && t.Start == trigger.Value.Start && t.Query == trigger.Value.Query && _picker.IsOpen;
        _trigger = trigger;
        if (same) return;
        _picker.NewQuery();
        if (trigger.Value.Symbol == '/') ShowCommands(trigger.Value.Query);
        else ShowMentions(trigger.Value.Query);
    }

    // ── "/" commands ─────────────────────────────────────────────────────

    private void ShowCommands(string query)
    {
        var version = ++_version;
        var agent = _host.Agent;
        var workspace = _host.Workspace;
        var key = agent + "|" + workspace;
        if (key != _customKey)
        {
            // The agent's own commands: a quick directory listing, off the UI thread.
            _customKey = key;
            _custom = Array.Empty<ComposerCommands.Command>();
            var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            _ = Task.Run(() => ComposerCommands.DiscoverCustom(agent, workspace, home)).ContinueWith(task =>
            {
                if (task.IsFaulted) return;
                Queue?.TryEnqueue(() =>
                {
                    if (_customKey != key) return;
                    _custom = task.Result;
                    if (_trigger is { Symbol: '/' } t && _picker.IsOpen) ShowCommands(t.Query);
                });
            }, TaskScheduler.Default);
        }
        var rows = ComposerCommands.Rank(AllCommands(), query).Select(r => new ComposerPopup.Row(
            "cmd:" + r.Command.Name, r.Command.Glyph, "/" + r.Command.Name, r.Positions.Select(p => p + 1).ToArray(),
            r.Command.Description == r.Command.Source ? "" : r.Command.Description, Array.Empty<int>(),
            r.Command.Source == "Nexus" ? "Commands" : AgentAvailability.DisplayName(agent) + " commands", r.Command,
            r.Command.Source == "Nexus" ? "" : r.Command.Source)).ToList();
        if (version != _version) return;
        _picker.Show("Commands", rows, "No commands match. Enter sends it to the agent as you typed it.", loading: false, Width(400));
    }

    private async Task PickCommandAsync(ComposerCommands.Command command)
    {
        Close();
        if (command.Kind == ComposerCommands.Kind.Agent)
        {
            if (command.Name == ComposerCommands.Compact)
            {
                SetText("/compact", 8);
                await _host.SendAsync();
                return;
            }
            SetText("/" + command.Name + " ", command.Name.Length + 2);
            _box.Focus(FocusState.Programmatic);
            return;
        }
        SetText("", 0);
        await _host.RunCommandAsync(command.Name, "");
    }

    /// <summary>
    /// A whole message that is one of the host's own commands ("/model opus",
    /// "/mode full") runs instead of being sent. True when it ran.
    /// </summary>
    public async Task<bool> TryRunTypedAsync()
    {
        if (_mentions.Count > 0) return false;
        if (ComposerCommands.Parse(_box.Text) is not { } parsed) return false;
        if (!AllCommands().Any(c => c.Kind == ComposerCommands.Kind.Action && c.Name == parsed.Name)) return false;
        Close();
        RememberSent(_box.Text);
        SetText("", 0);
        await _host.RunCommandAsync(parsed.Name, parsed.Argument);
        return true;
    }

    // ── "@" mentions ─────────────────────────────────────────────────────

    private void ShowMentions(string query)
    {
        var version = ++_version;
        var workspace = _host.Workspace;
        var hasFolder = workspace.Length > 0 && Directory.Exists(workspace);
        QueueRemote(query);
        if (!hasFolder)
        {
            Render(version, query, Array.Empty<ProjectFileIndex.Hit>(), filesLoading: false,
                "No project folder yet, so there are no files to mention. Chats and memories still work.");
            return;
        }
        var index = ProjectFileIndex.For(workspace);
        var entries = index.Current;
        var recent = s_recentMentions.TryGetValue(workspace, out var r) ? r.ToList() : new List<string>();
        if (entries.Count == 0)
        {
            // First "@" in this folder: chats and memories (and "Looking…") while the list loads.
            Render(version, query, Array.Empty<ProjectFileIndex.Hit>(), filesLoading: true, "");
            _ = index.GetAsync().ContinueWith(_ => Queue?.TryEnqueue(() =>
            {
                if (version == _version && _trigger is { Symbol: '@' } t) ShowMentions(t.Query);
            }), TaskScheduler.Default);
            return;
        }
        _ = index.GetAsync(); // refreshes in the background when stale
        _ = Task.Run(() => ProjectFileIndex.Search(entries, query, recent, 30)).ContinueWith(task =>
        {
            if (task.IsFaulted) return;
            Queue?.TryEnqueue(() =>
            {
                if (version != _version) return;
                Render(version, query, task.Result, filesLoading: false, query.Length > 0 ? "No files match “" + query + "”." : "This folder has no files to mention.");
            });
        }, TaskScheduler.Default);
    }

    private void Render(int version, string query, IReadOnlyList<ProjectFileIndex.Hit> files, bool filesLoading, string emptyText)
    {
        if (version != _version || _trigger is not { Symbol: '@' }) return;
        _shownFiles = files;
        _shownQuery = query;
        _shownLoading = filesLoading;
        _shownEmpty = emptyText;
        var rows = new List<ComposerPopup.Row>();
        foreach (var hit in files)
        {
            var trimmed = hit.Path.TrimEnd('/');
            var nameStart = trimmed.LastIndexOf('/') + 1;
            var name = hit.Path[nameStart..];
            var dir = nameStart > 0 ? hit.Path[..(nameStart - 1)] : "";
            var nameHits = hit.Positions.Where(p => p >= nameStart).Select(p => p - nameStart).ToArray();
            var dirHits = hit.Positions.Where(p => p < nameStart - 1).ToArray();
            rows.Add(new ComposerPopup.Row("path:" + hit.Path, hit.IsFolder ? "" : "", name, nameHits, dir, dirHits, "Files",
                new ComposerMention { Kind = hit.IsFolder ? ComposerMention.Folder : ComposerMention.File, Value = hit.Path, Label = name }));
        }
        if (_remoteQuery == query) rows.AddRange(_remoteRows.Where(row => !_mentions.Any(m => "m:" + m.Kind + ":" + m.Value == row.Id)));
        var loading = filesLoading || (_remoteLoading && rows.Count == 0);
        var empty = emptyText.Length > 0 ? emptyText : "No files, chats or memories match.";
        if (files.Count == 0 && !filesLoading && query.Length > 0 && emptyText.StartsWith("No files", StringComparison.Ordinal) && !_remoteLoading)
            empty = "No files, chats or memories match “" + query + "”.";
        // Chats or memories match but no file does: say so above them.
        var note = files.Count == 0 && !filesLoading && rows.Count > 0 && emptyText.Length > 0 ? emptyText : "";
        _picker.Show("Mention", rows, empty, loading, Width(520), note);
    }

    // Other chats and memories come from the core, a moment after typing stops.
    private void QueueRemote(string query)
    {
        if (query == _remoteQuery) return;
        _remoteLoading = true;
        if (Queue is null) return;
        _remoteTimer ??= Queue.CreateTimer();
        _remoteTimer.Stop();
        _remoteTimer.Interval = TimeSpan.FromMilliseconds(query.Length == 0 ? 0 : 160);
        _remoteTimer.IsRepeating = false;
        _remoteTimer.Tick -= OnRemoteTick;
        _remoteTimer.Tick += OnRemoteTick;
        _remoteTimer.Start();
    }

    private void OnRemoteTick(DispatcherQueueTimer sender, object args) => _ = LoadRemoteAsync();

    private async Task LoadRemoteAsync()
    {
        if (_trigger is not { Symbol: '@' } trigger) { _remoteLoading = false; return; }
        var query = trigger.Query;
        var project = _host.ProjectId.Length > 0 ? _host.ProjectId : ShellState.Current.ProjectId;
        var chats = App.Core.CallAsync(NxMethods.TimelineList, new { q = query, limit = 6, project_id = project });
        var memories = ProjectScope.LooksLikeUuid(project)
            ? App.Core.CallAsync(NxMethods.MemorySearch, new { browse = true, q = query, project_id = project, limit = 5, offset = 0 })
            : Task.FromResult(new NxResponse(false, null, null));
        try { await Task.WhenAll(chats, memories); } catch (Exception) { }
        await UiThread.Resume(Queue);
        if (_trigger is not { Symbol: '@' } now || now.Query != query) return;
        _remoteRows.Clear();
        var self = _host.SessionId;
        if (chats.IsCompletedSuccessfully && chats.Result is { Ok: true } c)
        {
            foreach (var item in NxJson.Items(c.Result))
            {
                var id = NxJson.Text(item, "id", "session_id");
                if (id.Length == 0 || id == self || SessionLineage.IsSubagentSession(item)) continue;
                var agent = NxJson.Text(item, "agent", "harness");
                var title = ChatTitles.Title(NxJson.Text(item, "title"), NxJson.Text(item, "summary"), agent);
                var summary = ChatTitles.Clean(NxJson.Text(item, "summary"));
                _remoteRows.Add(new ComposerPopup.Row("m:" + ComposerMention.Chat + ":" + id, "", title, FuzzyMatch.Highlights(query, title), ChatTitles.AgentName(agent), Array.Empty<int>(), "Chats",
                    new ComposerMention { Kind = ComposerMention.Chat, Value = id, Label = title, Context = summary.Length > 0 ? summary : title }));
            }
        }
        if (memories.IsCompletedSuccessfully && memories.Result is { Ok: true } m)
        {
            foreach (var item in NxJson.Items(m.Result))
            {
                var id = NxJson.Text(item, "id", "memory_id");
                var text = ChatTitles.Clean(NxJson.Text(item, "content", "text", "title", "key"));
                if (id.Length == 0 || text.Length == 0) continue;
                var shown = text.Length > 80 ? text[..80] + "…" : text;
                _remoteRows.Add(new ComposerPopup.Row("m:" + ComposerMention.Memory + ":" + id, "", shown, FuzzyMatch.Highlights(query, shown), "", Array.Empty<int>(), "Memories",
                    new ComposerMention { Kind = ComposerMention.Memory, Value = id, Label = shown, Context = text }));
            }
        }
        _remoteQuery = query;
        _remoteLoading = false;
        if (_picker.IsOpen && _shownQuery == query) Render(_version, query, _shownFiles, _shownLoading, _shownEmpty);
    }

    private async Task PickAsync(ComposerPopup.Row row)
    {
        if (row.Payload is ComposerCommands.Command command) { await PickCommandAsync(command); return; }
        if (row.Payload is not ComposerMention mention || _trigger is not { Symbol: '@' } trigger) return;
        // The typed "@query" makes way for the chip.
        var text = _box.Text;
        var end = Math.Min(text.Length, trigger.Start + trigger.Length);
        var before = text[..trigger.Start];
        var after = text[end..];
        if (before.EndsWith(' ') && after.StartsWith(' ')) after = after[1..];
        Close();
        SetText(before + after, before.Length);
        AddMention(mention, animate: true);
        if (mention.IsPath)
        {
            var workspace = _host.Workspace;
            if (!s_recentMentions.TryGetValue(workspace, out var recent)) s_recentMentions[workspace] = recent = new List<string>();
            recent.Remove(mention.Value);
            recent.Insert(0, mention.Value);
            if (recent.Count > 20) recent.RemoveAt(recent.Count - 1);
        }
        else if (mention.Kind == ComposerMention.Chat)
        {
            _ = FillChatContextAsync(mention);
        }
        _box.Focus(FocusState.Programmatic);
        _host.MentionsChanged();
    }

    // A chat's summary from the core is better context than the list's one-liner.
    private async Task FillChatContextAsync(ComposerMention mention)
    {
        var response = await App.Core.CallAsync(NxMethods.SessionsSummary, new { session_id = mention.Value });
        await UiThread.Resume(Queue);
        if (!response.Ok || response.Result is not { } result) return;
        var summary = ChatTitles.Clean(NxJson.Text(result, "summary", "text", "content"));
        if (summary.Length <= mention.Context.Length) return;
        var at = _mentions.FindIndex(m => m.Kind == ComposerMention.Chat && m.Value == mention.Value);
        if (at < 0) return;
        _mentions[at] = mention with { Context = summary };
        _host.MentionsChanged();
    }

    // ── Chips ────────────────────────────────────────────────────────────

    public void AddMention(ComposerMention mention, bool animate)
    {
        if (_mentions.Any(m => m.Kind == mention.Kind && m.Value == mention.Value)) return;
        _mentions.Add(mention);
        var chip = MentionChip(mention, _host.Agent, onRemove: null);
        chip.RemoveRequested = () =>
        {
            _mentions.RemoveAll(m => m.Kind == mention.Kind && m.Value == mention.Value);
            _chips.Children.Remove(chip.Element);
            ChipStrip.Visibility = _mentions.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
            _host.MentionsChanged();
            _box.Focus(FocusState.Programmatic);
        };
        _chips.Children.Add(chip.Element);
        ChipStrip.Visibility = Visibility.Visible;
        if (animate) Motion.FadeSlideIn(chip.Element, dy: 4, durationMs: 150);
    }

    public void ClearMentions()
    {
        _mentions.Clear();
        _chips.Children.Clear();
        ChipStrip.Visibility = Visibility.Collapsed;
    }

    internal sealed class Chip
    {
        public Chip(Border element) => Element = element;
        public Border Element { get; }
        public Action? RemoveRequested { get; set; }
    }

    internal static string GlyphOf(string kind) => kind switch
    {
        ComposerMention.Folder => "",
        ComposerMention.Chat => "",
        ComposerMention.Memory => "",
        _ => ""
    };

    internal static string KindName(string kind) => kind switch
    {
        ComposerMention.Folder => "Folder",
        ComposerMention.Chat => "Chat",
        ComposerMention.Memory => "Memory",
        _ => "File"
    };

    /// <summary>
    /// A mention chip: in the composer with a remove ✕ (<paramref name="onRemove"/>
    /// set later through <see cref="Chip.RemoveRequested"/>), or read-only under a sent message.
    /// </summary>
    internal static Chip MentionChip(ComposerMention mention, string agent, Action? onRemove, bool removable = true, string prefix = "")
    {
        var label = MentionText.ChipLabel(mention);
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        row.Children.Add(new FontIcon { Glyph = GlyphOf(mention.Kind), FontSize = 12, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(new TextBlock { Text = prefix + label, FontSize = 12.5, VerticalAlignment = VerticalAlignment.Center, MaxWidth = 280, TextTrimming = TextTrimming.CharacterEllipsis });
        var chip = new Chip(new Border
        {
            Child = row, Padding = new Thickness(8, 2, removable ? 2 : 10, 2), CornerRadius = new CornerRadius(12), MinHeight = 26,
            Background = NexusPalette.AccentFill, BorderBrush = NexusPalette.AccentBorder, BorderThickness = new Thickness(1),
            Tag = mention
        }) { RemoveRequested = onRemove };
        if (removable)
        {
            var remove = new Button
            {
                Content = new FontIcon { Glyph = "", FontSize = 8 }, Width = 20, Height = 20, MinWidth = 20, MinHeight = 20,
                Padding = new Thickness(0), CornerRadius = new CornerRadius(10), BorderThickness = new Thickness(0),
                Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), VerticalAlignment = VerticalAlignment.Center
            };
            ToolTipService.SetToolTip(remove, "Remove " + label);
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(remove, "Remove " + label);
            remove.Click += (_, _) => chip.RemoveRequested?.Invoke();
            row.Children.Add(remove);
            var tip = mention.IsPath ? mention.Value + "\nSent to the agent as " + MentionText.PathRef(agent, mention.Value)
                : KindName(mention.Kind) + " · sent along as context";
            ToolTipService.SetToolTip(chip.Element, tip);
        }
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chip.Element, KindName(mention.Kind) + ": " + (mention.IsPath ? mention.Value : mention.Label));
        return chip;
    }

    /// <summary>
    /// The chats and memories sent with a message, as a row of chips under
    /// its bubble (the raw context text is not shown). A chip opens its
    /// source: the chat, or the memory in search. Null when there are none.
    /// </summary>
    internal static FrameworkElement? SentContextRow(IReadOnlyList<ComposerMention> sent)
    {
        if (sent.Count == 0) return null;
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, HorizontalAlignment = HorizontalAlignment.Right };
        foreach (var mention in sent.Take(8))
        {
            var chip = MentionChip(mention, "", null, removable: false, prefix: KindName(mention.Kind) + ": ");
            var preview = mention.Context.Length > 280 ? mention.Context[..280] + "…" : mention.Context;
            var canOpen = mention.Kind == ComposerMention.Chat ? mention.Value.Length > 0 : mention.Label.Length > 0;
            ToolTipService.SetToolTip(chip.Element, (canOpen ? (mention.Kind == ComposerMention.Chat ? "Open this chat" : "Find this memory") + "\n\n" : "") + preview);
            var button = new Button
            {
                Content = chip.Element, Padding = new Thickness(0), BorderThickness = new Thickness(0), MinHeight = 0, MinWidth = 0,
                Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent), IsEnabled = canOpen
            };
            button.Resources["ButtonBackgroundDisabled"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            button.Resources["ButtonBackgroundPointerOver"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            button.Resources["ButtonBackgroundPressed"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, KindName(mention.Kind) + ": " + mention.Label);
            button.Click += (_, _) =>
            {
                if (mention.Kind == ComposerMention.Chat) MainWindow.Current?.OpenSession(mention.Value);
                else MainWindow.Current?.ShowSearch(mention.Label);
            };
            Motion.AddPressFeedback(button);
            row.Children.Add(button);
        }
        return row;
    }

    /// <summary>The /help card: Nexus's commands here, the agent's own, "@" and ↑.</summary>
    internal FrameworkElement HelpCard(Action close, bool stopLine)
    {
        var lines = new StackPanel { Spacing = 6 };
        var head = new Grid();
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.Children.Add(new TextBlock { Text = "What you can type", FontSize = 13, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, VerticalAlignment = VerticalAlignment.Center });
        var closeButton = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 10 }, Width = 26, Height = 26, MinWidth = 26, MinHeight = 26, Padding = new Thickness(0),
            CornerRadius = new CornerRadius(13), BorderThickness = new Thickness(0), Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent)
        };
        ToolTipService.SetToolTip(closeButton, "Close help");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(closeButton, "Close help");
        closeButton.Click += (_, _) => close();
        Grid.SetColumn(closeButton, 1);
        head.Children.Add(closeButton);
        lines.Children.Add(head);
        void Line(string keys, string what)
        {
            var row = new Grid { ColumnSpacing = 12 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(150) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.Children.Add(new TextBlock { Text = keys, FontSize = 12.5, FontFamily = new FontFamily("Cascadia Mono, Consolas, Menlo, monospace"), Foreground = NexusPalette.Accent });
            var text = new TextBlock { Text = what, FontSize = 12.5, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap };
            Grid.SetColumn(text, 1);
            row.Children.Add(text);
            lines.Children.Add(row);
        }
        foreach (var command in _host.Builtins())
            Line("/" + command.Name + (command.TakesArgument ? " …" : ""), command.Description);
        var custom = _custom.Count;
        if (custom > 0) Line("/…", custom == 1 ? "1 command of the agent's own, listed as you type /" : custom + " commands of the agent's own, listed as you type /");
        Line("@", "Mention a file or folder in this project, another chat, or a memory");
        Line("↑ / ↓", "Bring back a message you sent earlier");
        if (stopLine) Line("Esc", "Stop the agent while it works");
        var card = new Border
        {
            Child = lines, Padding = new Thickness(14, 10, 10, 12), CornerRadius = new CornerRadius(10), Margin = new Thickness(0, 4, 0, 8),
            Background = SessionChatRender.SubtleFill, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(card, "Composer help");
        return card;
    }
}
