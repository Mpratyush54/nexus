using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Input;
using Nexus.Core;

namespace Nexus.Pages;

// The chat composer's "/" commands, "@" mentions and ↑ recall: the shared
// ComposerAssist on this page's message box, plus the commands only a chat
// has (/model, /mode, /new, /clear, /compact, /rewind, /help).
public sealed partial class SessionPage
{
    private ComposerAssist _assist = null!;
    private Border? _helpCard;
    private bool _openedByCommand;

    // SessionPage.Review fills these in from the chat's checkpoints; with none, /rewind stays hidden.
    partial void ComposerCanRewind(ref bool available);
    partial void ComposerRewind(ref bool handled);

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
        _assist = new ComposerAssist(_prompt, _composer, new ChatHost(this));
        var inputAt = _composer.Children.IndexOf(_inputFrame);
        _composer.Children.Insert(Math.Max(0, inputAt), _assist.ChipStrip);
        Unloaded += (_, _) =>
        {
            _assist.Close();
            if (string.Equals(ComposerDraftStore.Default.ActiveSession, _id, StringComparison.OrdinalIgnoreCase)) ComposerDraftStore.Default.ActiveSession = "";
        };
        Loaded += (_, _) => { if (_id.Length > 0) ComposerDraftStore.Default.ActiveSession = _id; };
        // /model and /mode open their picker; choosing (or Esc) comes back to the message box.
        _model.DropDownClosed += (_, _) => ReturnFromCommandPicker();
        _access.DropDownClosed += (_, _) => ReturnFromCommandPicker();
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetHelpText(_prompt, "Enter sends, Shift+Enter adds a line. " + ComposerAssist.HelpText);
    }

    private sealed class ChatHost : ComposerAssist.IHost
    {
        private readonly SessionPage _page;
        public ChatHost(SessionPage page) => _page = page;
        public string Agent => _page.SelectedHarness();
        public string Workspace => _page._workspace.Text.Trim();
        public string ProjectId => _page._projectID;
        public string SessionId => _page._id;
        public IReadOnlyList<ComposerCommands.Command> Builtins() => ComposerCommands.Builtins(Agent, SupportsLive(Agent), _page.CanRewind());
        public IEnumerable<string> TranscriptPrompts() => _page._conversation.Prompts.Select(p => MentionText.Strip(p.Text));
        public bool CanRecall => _page._pastes.Count == 0 && _page._images.Count == 0 && _page._files.Count == 0;
        public bool IsIdle => _page.CurrentRun() == ComposerKeys.Run.Idle;
        public Task RunCommandAsync(string name, string argument) => _page.RunCommandAsync(name, argument);
        public Task SendAsync() => _page.SendHereAsync();
        public void MentionsChanged() { _page.QueueDraftSave(); _page.UpdateComposerButton(); }
    }

    private bool HandlePickerKey(KeyRoutedEventArgs e) => !_imeComposing && _assist.HandleKey(e);

    private void SetComposerText(string text, int caret) => _assist.SetText(text, caret);

    private void RememberSent(string typed) => _assist.RememberSent(typed);

    private void AddMention(ComposerMention mention, bool animate) => _assist.AddMention(mention, animate);

    private void ClearMentions() => _assist.ClearMentions();

    /// <summary>OnEnter hook: a new chat has its own history and no list open.</summary>
    private void ResetPickers()
    {
        _assist.Reset(clearHistory: true);
        ComposerDraftStore.Default.ActiveSession = _id;
    }

    /// <summary>"/model opus", "/mode full"… typed in full run instead of being sent. True when one ran.</summary>
    private async Task<bool> TryRunTypedCommandAsync()
    {
        if (_pastes.Count > 0 || _images.Count > 0 || _files.Count > 0) return false;
        if (!await _assist.TryRunTypedAsync()) return false;
        QueueDraftSave();
        return true;
    }

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
                else OpenPickerFromCommand(_model);
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

    /// <summary>/help: a short card in the chat (not sent) with what the composer understands.</summary>
    private void ShowCommandHelp()
    {
        if (_helpCard is not null) _chat.Children.Remove(_helpCard);
        Border? card = null;
        card = (Border)_assist.HelpCard(() =>
        {
            if (card is not null) _chat.Children.Remove(card);
            _helpCard = null;
            FocusComposerSoon(force: true);
        }, stopLine: true);
        _helpCard = card;
        _chat.Children.Add(card);
        Motion.FadeSlideIn(card, dy: 6, durationMs: 160);
        KeepLatestInView();
        FocusComposerSoon(force: true);
    }
}
