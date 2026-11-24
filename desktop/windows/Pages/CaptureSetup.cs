using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;
using Windows.Storage.Pickers;
using WinRT.Interop;

namespace Nexus.Pages;

internal static class CaptureSetup
{
    // path -> "42 sessions · Claude Code, Codex", from capture status
    // "discovered_workspaces" (folders where agents have actually run).
    private static readonly Dictionary<string, string> Discovered = new(StringComparer.OrdinalIgnoreCase);

    internal static void ReadDiscovered(System.Text.Json.JsonElement capture)
    {
        if (capture.ValueKind != System.Text.Json.JsonValueKind.Object ||
            !capture.TryGetProperty("discovered_workspaces", out var rows) || rows.ValueKind != System.Text.Json.JsonValueKind.Array) return;
        Discovered.Clear();
        foreach (var row in rows.EnumerateArray())
        {
            var path = NxJson.Text(row, "path");
            if (path.Length == 0) continue;
            var agents = new List<string>();
            if (row.TryGetProperty("agents", out var map) && map.ValueKind == System.Text.Json.JsonValueKind.Object)
                foreach (var agent in map.EnumerateObject().OrderByDescending(a => a.Value.TryGetInt32(out var n) ? n : 0))
                    agents.Add(ChatTitles.AgentName(agent.Name));
            var sessions = NxJson.Text(row, "sessions");
            Discovered[path] = (sessions.Length > 0 ? sessions + (sessions == "1" ? " session" : " sessions") : "") +
                (agents.Count > 0 ? " · " + string.Join(", ", agents) : "");
        }
    }

    /// <summary>
    /// The folder list with "select all" and "choose another folder": used
    /// by the capture dialog below and inline by the setup wizard.
    /// </summary>
    internal sealed class Chooser
    {
        private readonly List<CheckBox> _checks = new();
        private readonly StackPanel _choices = new() { Spacing = 8 };
        private readonly Button _selectAll = new() { Content = "Select all found projects" };

        public StackPanel View { get; } = new() { Spacing = 10 };
        public event Action? Changed;
        public int Count => _checks.Count;
        public string[] Selected => _checks.Where(c => c.IsChecked == true).Select(c => c.Tag as string ?? "").Where(p => p.Length > 0).ToArray();

        public Chooser(IReadOnlyList<string> approved, IReadOnlyList<string>? suggested)
        {
            foreach (var root in approved) Add(root, true);
            foreach (var root in suggested ?? Array.Empty<string>()) if (Directory.Exists(root)) Add(root, false);
            AutomationProperties.SetName(_choices, "Workspace folders");
            _selectAll.Click += (_, _) => { foreach (var check in _checks) check.IsChecked = true; };
            var add = new Button { Content = "Choose another folder…" };
            add.Click += async (_, _) =>
            {
                if (MainWindow.Current is not { } window) return;
                var picker = new FolderPicker { SuggestedStartLocation = PickerLocationId.DocumentsLibrary };
                picker.FileTypeFilter.Add("*");
                InitializeWithWindow.Initialize(picker, WindowNative.GetWindowHandle(window));
                if (await picker.PickSingleFolderAsync() is { } folder) Add(folder.Path, true);
            };
            if (_checks.Count == 0)
                View.Children.Add(new TextBlock { Text = "No projects with agent sessions found yet. You can choose a folder yourself.", TextWrapping = TextWrapping.Wrap, Opacity = 0.65, FontSize = 12 });
            View.Children.Add(_choices);
            var pickRow = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
            pickRow.Children.Add(_selectAll);
            pickRow.Children.Add(add);
            View.Children.Add(pickRow);
            _selectAll.Visibility = _checks.Count > 1 ? Visibility.Visible : Visibility.Collapsed;
        }

        private void Add(string path, bool selected)
        {
            if (path.Length == 0 || _checks.Any(c => string.Equals(c.Tag as string, path, StringComparison.OrdinalIgnoreCase))) return;
            var label = new StackPanel { Spacing = 1 };
            label.Children.Add(new TextBlock { Text = path, TextWrapping = TextWrapping.Wrap });
            var found = Discovered.TryGetValue(path, out var text) ? text : "";
            if (found.Length > 0)
                label.Children.Add(new TextBlock { Text = found, FontSize = 12, Opacity = 0.65 });
            var check = new CheckBox
            {
                Content = label,
                Tag = path, IsChecked = selected, HorizontalAlignment = HorizontalAlignment.Stretch
            };
            AutomationProperties.SetName(check, found.Length > 0 ? path + ", " + found : path);
            check.Checked += (_, _) => Changed?.Invoke();
            check.Unchecked += (_, _) => Changed?.Invoke();
            _checks.Add(check);
            _choices.Children.Add(check);
            _selectAll.Visibility = _checks.Count > 1 ? Visibility.Visible : Visibility.Collapsed;
            Changed?.Invoke();
        }
    }

    internal static async Task<NxResponse?> ShowAsync(Page owner, IReadOnlyList<string> approved, IReadOnlyList<string>? suggested = null)
    {
        var chooser = new Chooser(approved, suggested);
        var allow = ConsentBox();
        var body = new StackPanel { Spacing = 14, MaxWidth = 500 };
        body.Children.Add(new TextBlock { Text = "1. Choose your workspaces", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        body.Children.Add(new TextBlock { Text = FoldersExplained, TextWrapping = TextWrapping.Wrap, Opacity = 0.75 });
        body.Children.Add(chooser.View);
        body.Children.Add(new TextBlock { Text = "2. Review what is captured", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        body.Children.Add(new TextBlock { Text = WhatIsCaptured, TextWrapping = TextWrapping.Wrap, Opacity = 0.75 });
        body.Children.Add(new TextBlock { Text = "3. Allow monitoring", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold });
        body.Children.Add(allow);
        body.Children.Add(new TextBlock { Text = "You can pause capture or remove a folder in Settings at any time. Removing it stops future monitoring and keeps your existing history.", TextWrapping = TextWrapping.Wrap, Opacity = 0.7, FontSize = 12 });
        var dialog = new ContentDialog
        {
            Title = approved.Count == 0 ? "Set up workspace capture" : "Manage monitored workspaces",
            Content = new ScrollViewer { Content = body, MaxHeight = 520, HorizontalScrollBarVisibility = ScrollBarVisibility.Disabled },
            PrimaryButtonText = "Save monitoring choices", CloseButtonText = "Cancel",
            IsPrimaryButtonEnabled = false, XamlRoot = owner.XamlRoot,
            DefaultButton = ContentDialogButton.Close
        };
        allow.Checked += (_, _) => dialog.IsPrimaryButtonEnabled = true;
        allow.Unchecked += (_, _) => dialog.IsPrimaryButtonEnabled = false;
        if (await dialog.ShowAsync() != ContentDialogResult.Primary) return null;
        return await SaveAsync(owner, chooser.Selected);
    }

    internal const string FoldersExplained = "These are the folders where your agents have run on this computer. Only checked folders are monitored; Nexus will not add other projects automatically.";

    internal const string WhatIsCaptured = "Chats, tool activity, and available workspace context from supported local agents such as Codex, Cursor, OpenCode, and Claude Code. Captured sessions and extracted memories sync to your signed-in account. Sharing with other people is a separate action.";

    internal static CheckBox ConsentBox()
    {
        var allow = new CheckBox
        {
            Content = new TextBlock { Text = "I allow Nexus to monitor the selected folders and sync their captured sessions and memories to my account.", TextWrapping = TextWrapping.Wrap },
            IsChecked = false
        };
        AutomationProperties.SetName(allow, "Allow monitoring of the selected folders");
        return allow;
    }

    internal static async Task<NxResponse> SaveAsync(FrameworkElement owner, string[] roots)
    {
        var result = await App.Core.CallAsync(NxMethods.CaptureConfigure, new { workspaces = roots, confirm = true });
        await UiThread.Resume(owner.DispatcherQueue);
        return result;
    }
}
