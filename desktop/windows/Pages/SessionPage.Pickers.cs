using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;
using VirtualKey = Windows.System.VirtualKey;

namespace Nexus.Pages;

// The composer's power keys: "/" commands, "@" mentions and ↑ recall.
//
// "/" at the start of the message opens the command list (Nexus's own
// /model, /mode, /new, /clear, /compact, /help, plus the agent's custom
// commands found on disk). "@" opens files and folders from the project
// (git's list, .gitignore respected), other Nexus chats and memories; each
// pick becomes a chip above the message. On send, paths go as "@path" and
// chats and memories as context (MentionText). ↑ in an empty composer brings
// back the last prompt sent here; more ↑/↓ walks the history, Esc clears it.
// The list never takes focus and its searches run off the UI thread, so
// typing is never held up.
public sealed partial class SessionPage
{
    private ComposerPopup _picker = null!;
    private ComposerCommands.Trigger? _trigger;
    private int _pickerVersion;
    private bool _settingComposerText;
    // Text this page put in the box (a pick, a recall, a restored draft):
    // TextChanged arrives later, and it must not open the list by itself.
    private string? _programmaticText;
    private readonly List<ComposerMention> _mentions = new();
    private readonly StackPanel _mentionChips = new() { Orientation = Orientation.Horizontal, Spacing = 6 };
    private readonly ScrollViewer _mentionStrip = new()
    {
        HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
        HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled, Visibility = Visibility.Collapsed
    };
    private readonly PromptRecall _recall = new();
    private readonly List<string> _sentHere = new();
    private IReadOnlyList<ComposerCommands.Command> _customCommands = Array.Empty<ComposerCommands.Command>();
    private string _customCommandsKey = "";
    private readonly List<ComposerPopup.Row> _remoteRows = new();
    private string _remoteQuery = "\0";
    private bool _remoteLoading;
    private DispatcherQueueTimer? _remoteTimer;
    private DispatcherQueueTimer? _pickerCloseTimer;
    private static readonly Dictionary<string, List<string>> s_recentMentions = new(StringComparer.OrdinalIgnoreCase);

    // The checkpoint view fills these in (feat/turn-checkpoints); without it /rewind stays hidden.
    partial void ComposerCanRewind(ref bool available);
    partial void ComposerRewind(ref bool handled);

    private bool _openedByCommand;

    private void OpenPickerFromCommand(ComboBox picker)
    {
        _openedByCommand = true;
        picker.Focus(FocusState.Keyboard);
        picker.IsDropDownOpen = true;
    }

    private void ReturnFromCommandPicker()
    {
        if (!_openedByCommand) return;
        _openedByCommand = false;
        FocusComposerSoon(force: true);
    }

    private bool CanRewind()
    {
        var available = false;
        ComposerCanRewind(ref available);
        return available;
    }

    /// <summary>InitComposer hook: the list above the composer, the mention chips and the listeners.</summary>
    private void InitPickers()
    {
        // Above the whole composer card, so the list never covers the chips over the message box.
        _picker = new ComposerPopup(_composer);
        _picker.Picked += row => { _ = PickAsync(row); };
        _mentionStrip.Content = _mentionChips;
        var inputAt = _composer.Children.IndexOf(_inputFrame);
        _composer.Children.Insert(Math.Max(0, inputAt), _mentionStrip);
        _prompt.TextChanged += (_, _) => { if (!_settingComposerText) ForgetRecallIfEdited(); UpdatePicker(); };
        _prompt.SelectionChanged += (_, _) => { if (_picker.IsOpen || _prompt.Text.Contains('@') || _prompt.Text.StartsWith('/')) UpdatePicker(); };
        _prompt.LostFocus += (_, _) => QueuePickerClose();
        _prompt.GotFocus += (_, _) => _pickerCloseTimer?.Stop();
        Unloaded += (_, _) =>
        {
            _picker.Close();
            if (string.Equals(ComposerDraftStore.Default.ActiveSession, _id, StringComparison.OrdinalIgnoreCase)) ComposerDraftStore.Default.ActiveSession = "";
        };
        Loaded += (_, _) => { if (_id.Length > 0) ComposerDraftStore.Default.ActiveSession = _id; };
        // /model and /mode open their picker; choosing (or Esc) comes back to the message box.
        _model.DropDownClosed += (_, _) => ReturnFromCommandPicker();
        _access.DropDownClosed += (_, _) => ReturnFromCommandPicker();
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetHelpText(_prompt,
            "Enter sends, Shift+Enter adds a line. Type / for commands, @ to mention a file, chat or memory, ↑ to bring back your last message.");
    }

    // Typing over a recalled prompt makes it the person's text.
    private void ForgetRecallIfEdited()
    {
        if (_recall.Index >= 0 && _prompt.Text != _recall.Shown) _recall.Reset();
    }

    private void QueuePickerClose()
    {
        if (DispatcherQueue is null) return;
        _pickerCloseTimer ??= DispatcherQueue.CreateTimer();
        _pickerCloseTimer.Interval = TimeSpan.FromMilliseconds(180);
        _pickerCloseTimer.IsRepeating = false;
        _pickerCloseTimer.Tick -= OnPickerCloseTick;
        _pickerCloseTimer.Tick += OnPickerCloseTick;
        _pickerCloseTimer.Start();
    }

    private void OnPickerCloseTick(DispatcherQueueTimer sender, object args)
    {
        if (_prompt.FocusState == FocusState.Unfocused) ClosePicker();
    }

    private void ClosePicker()
    {
        _trigger = null;
        _pickerVersion++;
        _picker.Close();
    }

    private double PickerWidth(double wanted) => Math.Max(240, Math.Min(wanted, _composer.ActualWidth > 0 ? _composer.ActualWidth : wanted));

    // ── Keys (called first from OnComposerKeyDown) ──────────────────────

    /// <summary>↑/↓, Enter/Tab and Esc while the list is open; ↑/↓ recall and Esc when it isn't. True when handled.</summary>
    private bool HandlePickerKey(KeyRoutedEventArgs e)
    {
        if (_imeComposing) return false;
        var shift = IsDown(VirtualKey.Shift);
        var primary = Shortcuts.IsPrimaryDown();
        if (_picker.IsOpen)
        {
            switch (e.Key)
            {
                case VirtualKey.Escape:
                    ClosePicker();
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
            }
            return false;
        }
        if (e.Key is VirtualKey.Up or VirtualKey.Down && !shift && !primary && !IsDown(VirtualKey.Menu))
        {
            if (_pastes.Count > 0 || _images.Count > 0 || _files.Count > 0 || _mentions.Count > 0) return false;
            var next = _recall.Step(e.Key == VirtualKey.Up, _prompt.Text, _prompt.SelectionStart, RecallHistory());
            if (next is null) return false;
            SetComposerText(next, e.Key == VirtualKey.Up ? 0 : next.Length);
            return true;
        }
        if (e.Key == VirtualKey.Escape && _recall.Index >= 0 && CurrentRun() == ComposerKeys.Run.Idle)
        {
            _recall.Reset();
            SetComposerText("", 0);
            return true;
        }
        return false;
    }

    private IReadOnlyList<string> RecallHistory() =>
        PromptRecall.Build(_sentHere, _conversation.Prompts.Select(p => p.Text));

    private void SetComposerText(string text, int caret)
    {
        _settingComposerText = true;
        try
        {
            _programmaticText = text;
            _prompt.Text = text;
            _prompt.SelectionStart = Math.Clamp(caret, 0, text.Length);
        }
        finally { _settingComposerText = false; }
    }

    /// <summary>Hook after a message leaves the composer: it is first in the ↑ history.</summary>
    private void RememberSent(string typed)
    {
        _recall.Reset();
        if (typed.Trim().Length == 0) return;
        _sentHere.Remove(typed);
        _sentHere.Add(typed);
        if (_sentHere.Count > 100) _sentHere.RemoveAt(0);
    }

    /// <summary>OnEnter hook: a new chat has its own history and no list open.</summary>
    private void ResetPickers()
    {
        ClosePicker();
        _recall.Reset();
        _sentHere.Clear();
        ClearMentions();
        _remoteRows.Clear();
        _remoteQuery = "\0";
        ComposerDraftStore.Default.ActiveSession = _id;
    }

    // ── What the caret is in ─────────────────────────────────────────────

    private void UpdatePicker()
    {
        if (_settingComposerText || _imeComposing) return;
        if (_programmaticText is not null)
        {
            if (_prompt.Text == _programmaticText) { if (_picker.IsOpen) ClosePicker(); return; }
            _programmaticText = null;
        }
        var trigger = _prompt.SelectionLength == 0 ? ComposerCommands.Find(_prompt.Text, _prompt.SelectionStart) : null;
        if (trigger is null || !_prompt.IsEnabled)
        {
            if (_trigger is not null || _picker.IsOpen) ClosePicker();
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

    private IReadOnlyList<ComposerCommands.Command> AllCommands()
    {
        var agent = SelectedHarness();
        var builtins = ComposerCommands.Builtins(agent, SupportsLive(agent), CanRewind());
        return builtins.Concat(_customCommands).ToList();
    }

    private void ShowCommands(string query)
    {
        var version = ++_pickerVersion;
        var agent = SelectedHarness();
        var workspace = _workspace.Text.Trim();
        var key = agent + "|" + workspace;
        if (key != _customCommandsKey)
        {
            // The agent's own commands: a quick directory listing, off the UI thread.
            _customCommandsKey = key;
            _customCommands = Array.Empty<ComposerCommands.Command>();
            var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            _ = Task.Run(() => ComposerCommands.DiscoverCustom(agent, workspace, home)).ContinueWith(task =>
            {
                if (task.IsFaulted) return;
                DispatcherQueue?.TryEnqueue(() =>
                {
                    if (_customCommandsKey != key) return;
                    _customCommands = task.Result;
                    if (_trigger is { Symbol: '/' } t && _picker.IsOpen) ShowCommands(t.Query);
                });
            }, TaskScheduler.Default);
        }
        var rows = ComposerCommands.Rank(AllCommands(), query).Select(r => new ComposerPopup.Row(
            "cmd:" + r.Command.Name, r.Command.Glyph, "/" + r.Command.Name, r.Positions.Select(p => p + 1).ToArray(),
            r.Command.Description == r.Command.Source ? "" : r.Command.Description, Array.Empty<int>(),
            r.Command.Source == "Nexus" ? "Commands" : AgentAvailability.DisplayName(agent) + " commands", r.Command,
            r.Command.Source == "Nexus" ? "" : r.Command.Source)).ToList();
        if (version != _pickerVersion) return;
        _picker.Show("Commands", rows, "No commands match. Enter sends it to the agent as you typed it.", loading: false, PickerWidth(400));
    }

    /// <summary>Runs a picked command: Nexus's own run here; the agent's go into the message.</summary>
    private async Task PickCommandAsync(ComposerCommands.Command command)
    {
        ClosePicker();
        if (command.Kind == ComposerCommands.Kind.Agent)
        {
            if (command.Name == ComposerCommands.Compact)
            {
                SetComposerText("/compact", 8);
                await SendHereAsync();
                return;
            }
            SetComposerText("/" + command.Name + " ", command.Name.Length + 2);
            _prompt.Focus(FocusState.Programmatic);
            return;
        }
        SetComposerText("", 0);
        await RunCommandAsync(command.Name, "");
    }

    /// <summary>
    /// A whole message that is one of Nexus's own commands ("/model opus",
    /// "/mode full") runs instead of being sent. True when it ran.
    /// </summary>
    private async Task<bool> TryRunTypedCommandAsync()
    {
        if (_pastes.Count > 0 || _images.Count > 0 || _files.Count > 0 || _mentions.Count > 0) return false;
        if (ComposerCommands.Parse(_prompt.Text) is not { } parsed) return false;
        var builtin = AllCommands().FirstOrDefault(c => c.Kind == ComposerCommands.Kind.Action && c.Name == parsed.Name);
        if (builtin is null) return false;
        ClosePicker();
        RememberSent(_prompt.Text.Trim());
        SetComposerText("", 0);
        QueueDraftSave();
        await RunCommandAsync(parsed.Name, parsed.Argument);
        return true;
    }

    private async Task RunCommandAsync(string name, string argument)
    {
        switch (name)
        {
            case ComposerCommands.Model:
                if (argument.Length > 0)
                {
                    var match = _model.Items.OfType<ComboBoxItem>().FirstOrDefault(i => i.IsEnabled && i.Tag is string id && id.Length > 0 &&
                        (string.Equals(id, argument, StringComparison.OrdinalIgnoreCase) || string.Equals(i.Content?.ToString(), argument, StringComparison.OrdinalIgnoreCase)));
                    if (match is not null) _model.SelectedItem = match;
                    else { _model.SelectedIndex = -1; _model.Text = argument; }
                    RememberModelChoice();
                    QueueComposerFooterLayout();
                    SetComposerStatus("Model set to " + (match?.Content?.ToString() ?? argument) + " for this chat.");
                    FocusComposerSoon(force: true);
                }
                else
                {
                    OpenPickerFromCommand(_model);
                }
                break;
            case ComposerCommands.Mode:
                if (!SupportsLive(SelectedHarness())) { SetComposerStatus("Permission modes apply to agents that chat live in Nexus."); break; }
                var mode = ComposerCommands.ModeFrom(argument);
                if (mode.Length > 0)
                {
                    var item = _access.Items.OfType<ComboBoxItem>().FirstOrDefault(i => i.Tag as string == mode);
                    if (item is not null) _access.SelectedItem = item;
                    SetComposerStatus("Permission: " + (item?.Content?.ToString() ?? mode) + ".");
                    FocusComposerSoon(force: true);
                }
                else
                {
                    if (argument.Length > 0) SetComposerStatus("Try /mode ask, /mode edits or /mode full.");
                    OpenPickerFromCommand(_access);
                }
                break;
            case ComposerCommands.New:
                MainWindow.Current?.StartNewChat();
                break;
            case ComposerCommands.Clear:
                StartFreshChat();
                break;
            case ComposerCommands.Rewind:
                var handled = false;
                ComposerRewind(ref handled);
                if (!handled) SetComposerStatus("Checkpoints aren't available in this chat yet.");
                break;
            case ComposerCommands.Help:
                ShowCommandHelp();
                break;
        }
        await Task.CompletedTask;
    }

    /// <summary>/clear: a new chat with this chat's agent and folder (the composer on Home, ready to type).</summary>
    private void StartFreshChat()
    {
        var workspace = _workspace.Text.Trim();
        if (_projectID.Length > 0 && Directory.Exists(workspace)) ProjectFolders.Remember(_projectID, workspace);
        var agent = SelectedHarness();
        if (agent.Length > 0) PageCache.Dashboard.PrepareNewChat(agent);
        MainWindow.Current?.StartNewChat();
    }

    private Border? _helpCard;

    /// <summary>/help: a short card in the chat (not sent) with what the composer understands.</summary>
    private void ShowCommandHelp()
    {
        if (_helpCard is not null) _chat.Children.Remove(_helpCard);
        var lines = new StackPanel { Spacing = 6 };
        var head = new Grid();
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        head.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        head.Children.Add(new TextBlock { Text = "What you can type", FontSize = 13, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, VerticalAlignment = VerticalAlignment.Center });
        var close = GhostIconButton(new FontIcon { Glyph = "\uE711", FontSize = 10 }, "Close help");
        Grid.SetColumn(close, 1);
        head.Children.Add(close);
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
        foreach (var command in AllCommands().Where(c => c.Source == "Nexus"))
            Line("/" + command.Name + (command.TakesArgument ? " …" : ""), command.Description);
        var custom = _customCommands.Count;
        if (custom > 0) Line("/…", custom == 1 ? "1 command of the agent's own, listed as you type /" : custom + " commands of the agent's own, listed as you type /");
        Line("@", "Mention a file or folder in this project, another chat, or a memory");
        Line("↑ / ↓", "Bring back a message you sent earlier");
        Line("Esc", "Stop the agent while it works");
        var card = new Border
        {
            Child = lines, Padding = new Thickness(14, 10, 10, 12), CornerRadius = new CornerRadius(10), Margin = new Thickness(0, 4, 0, 8),
            Background = SessionChatRender.SubtleFill, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(card, "Composer help");
        close.Click += (_, _) => { _chat.Children.Remove(card); _helpCard = null; FocusComposerSoon(force: true); };
        _helpCard = card;
        _chat.Children.Add(card);
        Motion.FadeSlideIn(card, dy: 6, durationMs: 160);
        KeepLatestInView();
        FocusComposerSoon(force: true);
    }

    // ── "@" mentions ─────────────────────────────────────────────────────

    private void ShowMentions(string query)
    {
        var version = ++_pickerVersion;
        var workspace = _workspace.Text.Trim();
        var hasFolder = workspace.Length > 0 && Directory.Exists(workspace);
        QueueRemoteMentions(query);
        if (!hasFolder)
        {
            RenderMentions(version, query, Array.Empty<ProjectFileIndex.Hit>(), filesLoading: false,
                "No folder for this chat yet, so there are no files to mention. Chats and memories still work.");
            return;
        }
        var index = ProjectFileIndex.For(workspace);
        var entries = index.Current;
        var recent = s_recentMentions.TryGetValue(workspace, out var r) ? r.ToList() : new List<string>();
        if (entries.Count == 0)
        {
            // First "@" in this folder: show chats and memories (and "Looking…") while the list loads.
            RenderMentions(version, query, Array.Empty<ProjectFileIndex.Hit>(), filesLoading: true, "");
            _ = index.GetAsync().ContinueWith(_ => DispatcherQueue?.TryEnqueue(() =>
            {
                if (version == _pickerVersion && _trigger is { Symbol: '@' } t) ShowMentions(t.Query);
            }), TaskScheduler.Default);
            return;
        }
        _ = index.GetAsync(); // refreshes in the background when stale
        _ = Task.Run(() => ProjectFileIndex.Search(entries, query, recent, 30)).ContinueWith(task =>
        {
            if (task.IsFaulted) return;
            DispatcherQueue?.TryEnqueue(() =>
            {
                if (version != _pickerVersion) return;
                RenderMentions(version, query, task.Result, filesLoading: false, query.Length > 0 ? "No files match “" + query + "”." : "This folder has no files to mention.");
            });
        }, TaskScheduler.Default);
    }

    private IReadOnlyList<ProjectFileIndex.Hit> _shownFileHits = Array.Empty<ProjectFileIndex.Hit>();
    private string _shownMentionQuery = "";
    private bool _shownFilesLoading;
    private string _shownMentionEmpty = "";

    private void RenderMentions(int version, string query, IReadOnlyList<ProjectFileIndex.Hit> files, bool filesLoading, string emptyText)
    {
        if (version != _pickerVersion || _trigger is not { Symbol: '@' }) return;
        _shownFileHits = files;
        _shownMentionQuery = query;
        _shownFilesLoading = filesLoading;
        _shownMentionEmpty = emptyText;
        var rows = new List<ComposerPopup.Row>();
        foreach (var hit in files)
        {
            var trimmed = hit.Path.TrimEnd('/');
            var nameStart = trimmed.LastIndexOf('/') + 1;
            var name = hit.Path[nameStart..];
            var dir = nameStart > 0 ? hit.Path[..(nameStart - 1)] : "";
            var nameHits = hit.Positions.Where(p => p >= nameStart).Select(p => p - nameStart).ToArray();
            var dirHits = hit.Positions.Where(p => p < nameStart - 1).ToArray();
            rows.Add(new ComposerPopup.Row("path:" + hit.Path, hit.IsFolder ? "\uE8B7" : "\uE8A5", name, nameHits, dir, dirHits, "Files",
                new ComposerMention { Kind = hit.IsFolder ? ComposerMention.Folder : ComposerMention.File, Value = hit.Path, Label = name }));
        }
        if (_remoteQuery == query) rows.AddRange(_remoteRows.Where(row => !_mentions.Any(m => "m:" + m.Kind + ":" + m.Value == row.Id)));
        var loading = filesLoading || (_remoteLoading && rows.Count == 0);
        var empty = emptyText.Length > 0 ? emptyText : "No files, chats or memories match.";
        if (files.Count == 0 && !filesLoading && query.Length > 0 && emptyText.StartsWith("No files", StringComparison.Ordinal) && !_remoteLoading)
            empty = "No files, chats or memories match “" + query + "”.";
        // Chats or memories match but no file does: say so above them.
        var note = files.Count == 0 && !filesLoading && rows.Count > 0 && emptyText.Length > 0 ? emptyText : "";
        _picker.Show("Mention", rows, empty, loading, PickerWidth(520), note);
    }

    // Other chats and memories come from the core, a moment after typing stops.
    private void QueueRemoteMentions(string query)
    {
        if (query == _remoteQuery) return;
        _remoteLoading = true;
        if (DispatcherQueue is null) return;
        _remoteTimer ??= DispatcherQueue.CreateTimer();
        _remoteTimer.Stop();
        _remoteTimer.Interval = TimeSpan.FromMilliseconds(query.Length == 0 ? 0 : 160);
        _remoteTimer.IsRepeating = false;
        _remoteTimer.Tick -= OnRemoteMentionTick;
        _remoteTimer.Tick += OnRemoteMentionTick;
        _remoteTimer.Start();
    }

    private void OnRemoteMentionTick(DispatcherQueueTimer sender, object args) => _ = LoadRemoteMentionsAsync();

    private async Task LoadRemoteMentionsAsync()
    {
        if (_trigger is not { Symbol: '@' } trigger) { _remoteLoading = false; return; }
        var query = trigger.Query;
        var project = _projectID.Length > 0 ? _projectID : ShellState.Current.ProjectId;
        var chats = Core.CallAsync(NxMethods.TimelineList, query.Length > 0 ? new { q = query, limit = 6, project_id = project } : new { q = "", limit = 6, project_id = project });
        var memories = ProjectScope.LooksLikeUuid(project)
            ? Core.CallAsync(NxMethods.MemorySearch, new { browse = true, q = query, project_id = project, limit = 5, offset = 0 })
            : Task.FromResult(new NxResponse(false, null, null));
        try { await Task.WhenAll(chats, memories); } catch (Exception) { }
        await UiThread.Resume(DispatcherQueue);
        if (_trigger is not { Symbol: '@' } now || now.Query != query) return;
        _remoteRows.Clear();
        if (chats.IsCompletedSuccessfully && chats.Result is { Ok: true } c)
        {
            foreach (var item in NxJson.Items(c.Result))
            {
                var id = NxJson.Text(item, "id", "session_id");
                if (id.Length == 0 || id == _id || SessionLineage.IsSubagentSession(item)) continue;
                var agent = NxJson.Text(item, "agent", "harness");
                var title = ChatTitles.Title(NxJson.Text(item, "title"), NxJson.Text(item, "summary"), agent);
                var hits = FuzzyMatch.Highlights(query, title);
                var summary = ChatTitles.Clean(NxJson.Text(item, "summary"));
                _remoteRows.Add(new ComposerPopup.Row("m:" + ComposerMention.Chat + ":" + id, "\uE8BD", title, hits, ChatTitles.AgentName(agent), Array.Empty<int>(), "Chats",
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
                var hits = FuzzyMatch.Highlights(query, shown);
                _remoteRows.Add(new ComposerPopup.Row("m:" + ComposerMention.Memory + ":" + id, "\uE8F1", shown, hits, "", Array.Empty<int>(), "Memories",
                    new ComposerMention { Kind = ComposerMention.Memory, Value = id, Label = shown, Context = text }));
            }
        }
        _remoteQuery = query;
        _remoteLoading = false;
        if (_picker.IsOpen && _shownMentionQuery == query) RenderMentions(_pickerVersion, query, _shownFileHits, _shownFilesLoading, _shownMentionEmpty);
    }

    private async Task PickAsync(ComposerPopup.Row row)
    {
        if (row.Payload is ComposerCommands.Command command) { await PickCommandAsync(command); return; }
        if (row.Payload is not ComposerMention mention || _trigger is not { Symbol: '@' } trigger) return;
        // The typed "@query" makes way for the chip.
        var text = _prompt.Text;
        var end = Math.Min(text.Length, trigger.Start + trigger.Length);
        var before = text[..trigger.Start];
        var after = text[end..];
        if (before.EndsWith(' ') && after.StartsWith(' ')) after = after[1..];
        ClosePicker();
        SetComposerText(before + after, before.Length);
        AddMention(mention, animate: true);
        if (mention.IsPath)
        {
            var workspace = _workspace.Text.Trim();
            if (!s_recentMentions.TryGetValue(workspace, out var recent)) s_recentMentions[workspace] = recent = new List<string>();
            recent.Remove(mention.Value);
            recent.Insert(0, mention.Value);
            if (recent.Count > 20) recent.RemoveAt(recent.Count - 1);
        }
        else if (mention.Kind == ComposerMention.Chat)
        {
            _ = FillChatContextAsync(mention);
        }
        _prompt.Focus(FocusState.Programmatic);
        QueueDraftSave();
        UpdateComposerButton();
    }

    // A chat's summary from the core is better context than the list's one-liner.
    private async Task FillChatContextAsync(ComposerMention mention)
    {
        var response = await Call(NxMethods.SessionsSummary, new { session_id = mention.Value });
        if (!response.Ok || response.Result is not { } result) return;
        var summary = ChatTitles.Clean(NxJson.Text(result, "summary", "text", "content"));
        if (summary.Length <= mention.Context.Length) return;
        var at = _mentions.FindIndex(m => m.Kind == ComposerMention.Chat && m.Value == mention.Value);
        if (at < 0) return;
        _mentions[at] = mention with { Context = summary };
        QueueDraftSave();
    }

    // ── Mention chips ────────────────────────────────────────────────────

    private void AddMention(ComposerMention mention, bool animate)
    {
        if (_mentions.Any(m => m.Kind == mention.Kind && m.Value == mention.Value)) return;
        _mentions.Add(mention);
        var glyph = mention.Kind switch
        {
            ComposerMention.Folder => "\uE8B7",
            ComposerMention.Chat => "\uE8BD",
            ComposerMention.Memory => "\uE8F1",
            _ => "\uE8A5"
        };
        var kind = mention.Kind switch
        {
            ComposerMention.Folder => "Folder",
            ComposerMention.Chat => "Chat",
            ComposerMention.Memory => "Memory",
            _ => "File"
        };
        var label = MentionText.ChipLabel(mention);
        var remove = GhostIconButton(new FontIcon { Glyph = "\uE711", FontSize = 8 }, "Remove " + label);
        remove.Width = remove.Height = remove.MinWidth = remove.MinHeight = 20;
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        row.Children.Add(new FontIcon { Glyph = glyph, FontSize = 12, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(new TextBlock { Text = label, FontSize = 12.5, VerticalAlignment = VerticalAlignment.Center, MaxWidth = 260, TextTrimming = TextTrimming.CharacterEllipsis });
        row.Children.Add(remove);
        var chip = new Border
        {
            Child = row, Padding = new Thickness(8, 2, 2, 2), CornerRadius = new CornerRadius(12), MinHeight = 26,
            Background = NexusPalette.AccentFill, BorderBrush = NexusPalette.AccentBorder, BorderThickness = new Thickness(1),
            Tag = mention
        };
        var tip = mention.IsPath ? mention.Value + "\nSent to the agent as " + MentionText.PathRef(SelectedHarness(), mention.Value)
            : kind + " · sent along as context";
        ToolTipService.SetToolTip(chip, tip);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chip, kind + ": " + (mention.IsPath ? mention.Value : mention.Label));
        remove.Click += (_, _) =>
        {
            _mentions.RemoveAll(m => m.Kind == mention.Kind && m.Value == mention.Value);
            _mentionChips.Children.Remove(chip);
            _mentionStrip.Visibility = _mentions.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
            QueueDraftSave();
            UpdateComposerButton();
            _prompt.Focus(FocusState.Programmatic);
        };
        _mentionChips.Children.Add(chip);
        _mentionStrip.Visibility = Visibility.Visible;
        if (animate) Motion.FadeSlideIn(chip, dy: 4, durationMs: 150);
    }

    private void ClearMentions()
    {
        _mentions.Clear();
        _mentionChips.Children.Clear();
        _mentionStrip.Visibility = Visibility.Collapsed;
    }
}
