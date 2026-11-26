using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

// Home's new-chat composer gets the same "/", "@" and ↑ as a chat
// (ComposerAssist): the agent's own commands and /help, files from the
// project's folder, other chats and memories, and the prompts that started
// chats here. The mentions go to the new chat with its first message.
public sealed partial class DashboardPage
{
    private ComposerAssist _assist = null!;
    private Panel? _assistHost;
    private FrameworkElement? _startHelp;

    /// <summary>BuildStartComposer hook: chips above the message box, help below the bar.</summary>
    private void InitStartAssist(StackPanel stack)
    {
        _assistHost = stack;
        _assist = new ComposerAssist(_startPrompt, stack, new HomeHost(this));
        stack.Children.Insert(Math.Max(0, stack.Children.IndexOf(_startPrompt)), _assist.ChipStrip);
        AutomationHelp();
    }

    private void AutomationHelp() =>
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetHelpText(_startPrompt, "Enter starts the chat, Shift+Enter adds a line. " + ComposerAssist.HelpText);

    private string StartAgent() => (_startAgent.SelectedItem as ComboBoxItem)?.Tag as string ?? _lastStartAgent;

    // The project's folder when Nexus already knows it (no lookup while typing).
    private string StartFolder()
    {
        var project = ShellProjectId();
        return project.Length > 0 && AppPrefs.Data.ProjectFolders?.TryGetValue(project, out var folder) == true && Directory.Exists(folder) ? folder : "";
    }

    private void ShowStartHelp()
    {
        if (_assistHost is null) return;
        if (_startHelp is not null) _assistHost.Children.Remove(_startHelp);
        FrameworkElement? card = null;
        card = _assist.HelpCard(() =>
        {
            if (card is not null) _assistHost.Children.Remove(card);
            _startHelp = null;
            _startPrompt.Focus(FocusState.Programmatic);
        }, stopLine: false);
        _startHelp = card;
        _assistHost.Children.Add(card);
        Motion.FadeSlideIn(card, dy: 6, durationMs: 160);
        _startPrompt.Focus(FocusState.Programmatic);
    }

    private sealed class HomeHost : ComposerAssist.IHost
    {
        private static readonly IReadOnlyList<ComposerCommands.Command> HomeCommands = ComposerCommands.Builtins("", live: false, canRewind: false)
            .Where(c => c.Name == ComposerCommands.Help).ToList();
        private readonly DashboardPage _page;
        public HomeHost(DashboardPage page) => _page = page;
        public string Agent => _page.StartAgent();
        public string Workspace => _page.StartFolder();
        public string ProjectId => _page.ShellProjectId();
        public string SessionId => "";
        public IReadOnlyList<ComposerCommands.Command> Builtins() => HomeCommands;
        public IEnumerable<string> TranscriptPrompts() => Array.Empty<string>();
        public bool CanRecall => true;
        public bool IsIdle => true;
        public Task RunCommandAsync(string name, string argument)
        {
            if (name == ComposerCommands.Help) _page.ShowStartHelp();
            return Task.CompletedTask;
        }
        public Task SendAsync() => _page.StartChatAsync();
        public void MentionsChanged() => _page.StyleSend();
    }
}
