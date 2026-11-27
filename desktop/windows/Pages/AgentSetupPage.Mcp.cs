using System.Text.Json;
using Microsoft.UI.Text;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Nexus.Core;

namespace Nexus.Pages;

/// <summary>
/// Tool servers (MCP): add from a small gallery or pasted JSON, edit, turn
/// on/off and test. The command is always shown before anything is saved.
/// Each agent's config is written in its own format by the core
/// (agents.config.mcp_*), keeping everything else in the file.
/// </summary>
public sealed partial class AgentSetupPage
{
    // Agents whose MCP config Nexus can write (agentsetup.EditableAgents).
    private static readonly string[] McpAgents = { "claude", "codex", "cursor", "gemini", "opencode" };

    private List<JsonElement>? _templates;
    // Last test result per server, so it survives re-rendering the detail.
    private readonly Dictionary<string, JsonElement> _tests = new();

    private sealed class McpSpec
    {
        public string Name = "", Transport = "stdio", Command = "", Url = "";
        public List<string> Args = new();
        public Dictionary<string, string> Env = new(), Headers = new();
        public bool Enabled = true;

        public object ToArgs() => new { name = Name, transport = Transport, command = Command, args = Args, env = Env, url = Url, headers = Headers, enabled = Enabled };

        public static McpSpec From(JsonElement e)
        {
            var s = new McpSpec
            {
                Name = NxJson.Text(e, "name"), Transport = NxJson.Text(e, "transport"), Command = NxJson.Text(e, "command"), Url = NxJson.Text(e, "url"),
                Enabled = !e.TryGetProperty("enabled", out var en) || en.ValueKind != JsonValueKind.False
            };
            if (s.Transport.Length == 0) s.Transport = s.Url.Length > 0 ? "http" : "stdio";
            s.Args = Arr(e, "args").Select(a => a.GetString() ?? "").ToList();
            s.Env = Map(e, "env");
            s.Headers = Map(e, "headers");
            return s;
        }

        private static Dictionary<string, string> Map(JsonElement e, string key)
        {
            var map = new Dictionary<string, string>();
            if (e.TryGetProperty(key, out var m) && m.ValueKind == JsonValueKind.Object)
                foreach (var p in m.EnumerateObject()) map[p.Name] = p.Value.ValueKind == JsonValueKind.String ? p.Value.GetString() ?? "" : p.Value.GetRawText();
            return map;
        }
    }

    private bool CanEditMcp(string agent) => McpAgents.Contains(agent);

    private List<string> McpTargets(string current)
    {
        var list = McpAgents.Where(a => _installed.Contains(a)).ToList();
        if (CanEditMcp(current) && !list.Contains(current)) list.Insert(0, current);
        return list;
    }

    private static string TestKey(string agent, JsonElement item) => agent + "|" + NxJson.Text(item, "scope") + "|" + NxJson.Text(item, "path") + "|" + NxJson.Text(item, "name");

    // ── Detail pane controls ───────────────────────────────────────────

    /// <summary>On/off switch, Test and Edit for one server in the detail pane.</summary>
    private UIElement McpControls(string agent, JsonElement item)
    {
        var name = NxJson.Text(item, "name");
        var parked = Flag(item, "parked");
        var disabled = Flag(item, "disabled");
        var panel = new StackPanel { Spacing = 10 };
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };

        var toggle = new ToggleSwitch { IsOn = !disabled, OnContent = "On", OffContent = "Off", MinWidth = 0, VerticalAlignment = VerticalAlignment.Center };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(toggle, "Use " + name);
        ToolTipService.SetToolTip(toggle, "Turn this server off without deleting it. The agent picks up the change the next time it starts.");
        var test = FlatButton("Test");
        ToolTipService.SetToolTip(test, "Start the server, list its tools, then stop it");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(test, "Test " + name);
        var edit = FlatButton("Edit…");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(edit, "Edit " + name);
        if (parked)
        {
            edit.IsEnabled = false;
            ToolTipService.SetToolTip(edit, "Turn it on to edit it");
        }
        var result = new StackPanel { Spacing = 6 };
        var key = TestKey(agent, item);
        if (_tests.TryGetValue(key, out var last)) RenderTestResult(result, last);

        var busy = false;
        toggle.Toggled += async (_, _) =>
        {
            if (busy || toggle.IsOn == !disabled) return;
            busy = true;
            toggle.IsEnabled = false;
            var on = toggle.IsOn;
            var done = await Core.CallAsync("agents.config.mcp_set_enabled", new
            {
                agent, scope = NxJson.Text(item, "scope"), path = NxJson.Text(item, "path"), name, enabled = on, workspace = _workspace, state_dir = StateDir
            });
            await UiThread.Resume(DispatcherQueue);
            busy = false;
            toggle.IsEnabled = true;
            if (!done.Ok)
            {
                toggle.IsOn = !on;
                _status.Text = done.Error ?? "Nexus couldn't change that.";
                return;
            }
            _status.Text = (on ? "Turned on " : "Turned off ") + name + " for " + _agentNames.GetValueOrDefault(agent, agent) + ". Restart the agent to apply.";
            _contents.Clear();
            await LoadAsync();
        };
        test.Click += async (_, _) =>
        {
            test.IsEnabled = false;
            await RunTestAsync(result, new { agent, scope = NxJson.Text(item, "scope"), path = NxJson.Text(item, "path"), name, workspace = _workspace, state_dir = StateDir, timeout_ms = 30000 });
            if (result.Tag is JsonElement r) _tests[key] = r;
            test.IsEnabled = true;
        };
        edit.Click += async (_, _) => await EditServerAsync(agent, item);

        row.Children.Add(toggle);
        row.Children.Add(test);
        row.Children.Add(edit);
        panel.Children.Add(row);
        panel.Children.Add(result);
        return panel;
    }

    private async Task RunTestAsync(StackPanel host, object args)
    {
        host.Children.Clear();
        var working = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 8 };
        working.Children.Add(new ProgressRing { IsActive = true, Width = 14, Height = 14 });
        working.Children.Add(Tiny("Starting the server and asking for its tools…"));
        host.Children.Add(working);
        var response = await Core.CallAsync("agents.config.mcp_test", args);
        await UiThread.Resume(DispatcherQueue);
        host.Children.Clear();
        if (!response.Ok || response.Result is not { } r)
        {
            host.Children.Add(new TextBlock { Text = response.Error ?? "Nexus couldn't run the test.", FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap });
            return;
        }
        host.Tag = r.Clone();
        RenderTestResult(host, r);
    }

    private static void RenderTestResult(StackPanel host, JsonElement r)
    {
        host.Children.Clear();
        var ok = Flag(r, "ok");
        var line = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        line.Children.Add(new FontIcon { Glyph = ok ? "" : "", FontSize = 13, Foreground = ok ? NexusPalette.Teal : NexusPalette.Danger, VerticalAlignment = VerticalAlignment.Center });
        line.Children.Add(new TextBlock { Text = NxJson.Text(r, "message"), FontSize = 12.5, TextWrapping = TextWrapping.Wrap, VerticalAlignment = VerticalAlignment.Center, MaxWidth = 500 });
        host.Children.Add(line);
        var tools = Arr(r, "tools").Select(t => t.GetString() ?? "").Where(t => t.Length > 0).ToList();
        if (tools.Count > 0)
            host.Children.Add(new TextBlock { Text = string.Join(" · ", tools), FontFamily = MonoFont, FontSize = 11, Opacity = 0.7, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true });
        if (NxJson.Text(r, "detail") is { Length: > 0 } detail)
        {
            var more = new HyperlinkButton { Content = "Show what the server printed", FontSize = 11.5, Padding = new Thickness(0), MinHeight = 0 };
            var text = new TextBlock { Text = detail, FontFamily = MonoFont, FontSize = 11, Opacity = 0.75, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true, Visibility = Visibility.Collapsed };
            more.Click += (_, _) => { text.Visibility = text.Visibility == Visibility.Visible ? Visibility.Collapsed : Visibility.Visible; };
            host.Children.Add(more);
            host.Children.Add(text);
        }
        Motion.FadeSlideIn(host, dy: 6, durationMs: 240);
    }

    // ── Add ────────────────────────────────────────────────────────────

    private async Task<List<JsonElement>> TemplatesAsync()
    {
        if (_templates is { } cached) return cached;
        var response = await Core.CallAsync("agents.config.mcp_templates", new { });
        await UiThread.Resume(DispatcherQueue);
        var list = response.Ok && response.Result is { ValueKind: JsonValueKind.Array } a ? a.EnumerateArray().Select(e => e.Clone()).ToList() : new List<JsonElement>();
        if (list.Count > 0) _templates = list;
        return list;
    }

    /// <summary>
    /// Gallery → details → review (the exact command) → add. One dialog
    /// whose content changes per step; Back returns to the previous step.
    /// </summary>
    private async Task AddServerAsync(string agent)
    {
        var templates = await TemplatesAsync();
        var host = new ContentControl { HorizontalContentAlignment = HorizontalAlignment.Stretch };
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Add a tool server", CloseButtonText = "Cancel",
            Content = new ScrollViewer { Content = host, MaxHeight = 600, VerticalScrollBarVisibility = ScrollBarVisibility.Auto }
        };
        AccentPrimary(dialog);
        Func<Task<bool>>? primary = null;
        Action? back = null;
        var added = new List<string>();

        void Step(string title, UIElement content, string primaryText, Func<Task<bool>>? onPrimary, Action? onBack)
        {
            dialog.Title = title;
            host.Content = content;
            dialog.PrimaryButtonText = primaryText;
            dialog.IsPrimaryButtonEnabled = true;
            dialog.SecondaryButtonText = onBack is null ? "" : "Back";
            dialog.DefaultButton = primaryText.Length > 0 ? ContentDialogButton.Primary : ContentDialogButton.None;
            primary = onPrimary;
            back = onBack;
            Motion.FadeSlideIn(content, dx: 10, dy: 0, durationMs: 220);
        }

        void Gallery()
        {
            var body = new StackPanel { Spacing = 10, Width = 500 };
            body.Children.Add(Tiny("Tool servers (MCP) give an agent new abilities. Pick one to set it up; you'll see exactly what runs before anything is saved."));
            var grid = new VariableSizedWrapGrid { Orientation = Orientation.Horizontal, ItemWidth = 250, ItemHeight = 80 };
            foreach (var t in templates)
            {
                var card = new StackPanel { Spacing = 2 };
                card.Children.Add(new TextBlock { Text = NxJson.Text(t, "name"), FontSize = 13, FontWeight = FontWeights.SemiBold });
                card.Children.Add(new TextBlock { Text = NxJson.Text(t, "description"), FontSize = 11.5, Foreground = NexusPalette.Muted, TextWrapping = TextWrapping.Wrap, MaxLines = 2, TextTrimming = TextTrimming.CharacterEllipsis });
                if (NxJson.Text(t, "needs") is { Length: > 0 } need) card.Children.Add(new TextBlock { Text = "Needs " + need, FontSize = 10.5, Opacity = 0.5 });
                var button = new Button
                {
                    Content = card, HorizontalAlignment = HorizontalAlignment.Stretch, VerticalAlignment = VerticalAlignment.Stretch,
                    HorizontalContentAlignment = HorizontalAlignment.Left, VerticalContentAlignment = VerticalAlignment.Top,
                    Padding = new Thickness(12, 8, 12, 8), Margin = new Thickness(0, 0, 6, 6), CornerRadius = new CornerRadius(8),
                    Background = NexusPalette.Surface, BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
                };
                Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, "Add " + NxJson.Text(t, "name"));
                var template = t;
                button.Click += (_, _) => FromTemplate(template);
                grid.Children.Add(button);
            }
            body.Children.Add(grid);

            // Advanced: paste the JSON a server's README gives.
            var paste = new TextBox
            {
                AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, FontFamily = MonoFont, FontSize = 12, MinHeight = 140,
                PlaceholderText = "{\n  \"mcpServers\": {\n    \"my-server\": { \"command\": \"npx\", \"args\": [\"-y\", \"some-mcp-server\"] }\n  }\n}",
                IsSpellCheckEnabled = false
            };
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(paste, "Server JSON");
            var error = new TextBlock { FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
            var use = FlatButton("Continue with this JSON");
            var advanced = new StackPanel { Spacing = 8, Visibility = Visibility.Collapsed };
            advanced.Children.Add(Tiny("Paste the JSON from the server's instructions. Claude Code, Cursor, VS Code, OpenCode and Codex formats all work."));
            advanced.Children.Add(paste);
            advanced.Children.Add(error);
            advanced.Children.Add(use);
            var toggle = new HyperlinkButton { Content = "Advanced: paste JSON", FontSize = 12, Padding = new Thickness(0) };
            toggle.Click += (_, _) =>
            {
                advanced.Visibility = advanced.Visibility == Visibility.Visible ? Visibility.Collapsed : Visibility.Visible;
                if (advanced.Visibility == Visibility.Visible) { Motion.FadeSlideIn(advanced, dy: 6, durationMs: 200); paste.Focus(FocusState.Programmatic); }
            };
            use.Click += async (_, _) =>
            {
                var parsed = await Core.CallAsync("agents.config.mcp_parse", new { text = AgentConfigText.Newlines(paste.Text) });
                await UiThread.Resume(DispatcherQueue);
                if (!parsed.Ok || parsed.Result is not { } p)
                {
                    error.Text = parsed.Error ?? "Nexus couldn't read that.";
                    error.Visibility = Visibility.Visible;
                    return;
                }
                Details(Arr(p, "servers").Select(McpSpec.From).ToList(), null, null);
            };
            body.Children.Add(toggle);
            body.Children.Add(advanced);
            Step("Add a tool server", body, "", null, null);
        }

        void FromTemplate(JsonElement template)
        {
            var spec = McpSpec.From(template.GetProperty("spec"));
            Details(new List<McpSpec> { spec }, template, null);
        }

        // Names, template values, which agents and where.
        void Details(List<McpSpec> specs, JsonElement? template, Dictionary<string, string>? values)
        {
            var body = new StackPanel { Spacing = 10, Width = 500 };
            if (template is { } t)
            {
                body.Children.Add(new TextBlock { Text = NxJson.Text(t, "description"), FontSize = 12.5, TextWrapping = TextWrapping.Wrap });
                if (NxJson.Text(t, "needs") is { Length: > 0 } need) body.Children.Add(Tiny("This server needs " + need + " installed on this computer."));
            }
            else body.Children.Add(Tiny(specs.Count == 1 ? "Nexus found one server in the JSON." : $"Nexus found {specs.Count} servers in the JSON. All of them will be added."));

            var names = new List<TextBox>();
            foreach (var spec in specs)
            {
                var what = spec.Url.Length > 0 ? spec.Url : string.Join(" ", new[] { spec.Command }.Concat(spec.Args.Where(a => !a.StartsWith('-'))));
                if (what.Length > 60) what = what[..57] + "…";
                var box = new TextBox { Header = specs.Count == 1 ? "Name" : "Name for " + what, Text = spec.Name, FontSize = 12.5 };
                names.Add(box);
                body.Children.Add(box);
            }
            var inputs = new List<(string Key, Func<string> Value)>();
            if (template is { } tp)
            {
                foreach (var input in Arr(tp, "inputs"))
                {
                    var key = NxJson.Text(input, "key");
                    var label = NxJson.Text(input, "label");
                    var prior = values?.GetValueOrDefault(key) ?? "";
                    if (Flag(input, "secret"))
                    {
                        var pw = new PasswordBox { Header = label, PlaceholderText = NxJson.Text(input, "placeholder"), Password = prior };
                        body.Children.Add(pw);
                        inputs.Add((key, () => pw.Password));
                    }
                    else
                    {
                        var box = new TextBox { Header = label, PlaceholderText = NxJson.Text(input, "placeholder"), Text = prior.Length > 0 ? prior : Flag(input, "folder") ? _workspace : "", FontSize = 12.5 };
                        if (Flag(input, "folder"))
                        {
                            var line = new Grid { ColumnSpacing = 6 };
                            line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
                            line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
                            line.Children.Add(box);
                            var browse = FlatButton("Browse…");
                            browse.VerticalAlignment = VerticalAlignment.Bottom;
                            browse.Click += async (_, _) => { if (await CaptureSetup.PickFolderAsync() is { Length: > 0 } f) box.Text = f; };
                            Grid.SetColumn(browse, 1);
                            line.Children.Add(browse);
                            body.Children.Add(line);
                        }
                        else body.Children.Add(box);
                        inputs.Add((key, () => box.Text));
                    }
                    if (NxJson.Text(input, "help") is { Length: > 0 } help) body.Children.Add(Tiny(help));
                }
            }

            body.Children.Add(new TextBlock { Text = "Add it to", FontSize = 12.5, FontWeight = FontWeights.SemiBold, Margin = new Thickness(0, 6, 0, 0) });
            var targets = McpTargets(agent).Select(id => (id, Box: new CheckBox { Content = _agentNames.GetValueOrDefault(id, id), IsChecked = id == agent, FontSize = 12.5, MinWidth = 0 })).ToList();
            var agentsRow = new VariableSizedWrapGrid { Orientation = Orientation.Horizontal, ItemWidth = 165 };
            foreach (var (_, box) in targets) agentsRow.Children.Add(box);
            body.Children.Add(agentsRow);
            var hasFolder = _workspace.Length > 0;
            var everywhere = new RadioButton { Content = "All my projects", GroupName = "mcp-add-scope", IsChecked = true, FontSize = 12.5 };
            var projectOnly = new RadioButton { Content = hasFolder ? "Only this project (" + System.IO.Path.GetFileName(_workspace.TrimEnd('\\', '/')) + ")" : "Only this project (pick a project first)", GroupName = "mcp-add-scope", IsEnabled = hasFolder, FontSize = 12.5 };
            body.Children.Add(everywhere);
            body.Children.Add(projectOnly);
            var error = new TextBlock { FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
            body.Children.Add(error);

            Step("Set up " + (template is { } tt ? NxJson.Text(tt, "name") : specs.Count == 1 ? specs[0].Name : "servers"), body, "Continue", async () =>
            {
                error.Visibility = Visibility.Collapsed;
                var chosen = targets.Where(x => x.Box.IsChecked == true).Select(x => x.id).ToList();
                var scope = projectOnly.IsChecked == true ? "project" : "user";
                var filled = inputs.ToDictionary(i => i.Key, i => i.Value());
                List<McpSpec> final;
                if (template is { } tpl)
                {
                    var made = await Core.CallAsync("agents.config.mcp_from_template", new { template = NxJson.Text(tpl, "id"), values = filled });
                    await UiThread.Resume(DispatcherQueue);
                    if (!made.Ok || made.Result is not { } m) { Fail(made.Error ?? "Fill in the fields above."); return false; }
                    final = new List<McpSpec> { McpSpec.From(m) };
                }
                else final = specs;
                for (var i = 0; i < final.Count; i++) final[i].Name = names[i].Text.Trim();
                if (chosen.Count == 0) { Fail("Choose at least one agent."); return false; }

                // Dry runs: catch every problem (bad name, duplicate, no project file) before the review.
                var plans = new List<(string Agent, McpSpec Spec, JsonElement Plan)>();
                foreach (var target in chosen)
                    foreach (var spec in final)
                    {
                        var dry = await Core.CallAsync("agents.config.mcp_save", new { agent = target, scope, spec = spec.ToArgs(), dry_run = true, workspace = _workspace, state_dir = StateDir });
                        await UiThread.Resume(DispatcherQueue);
                        if (!dry.Ok || dry.Result is not { } plan) { Fail(_agentNames.GetValueOrDefault(target, target) + ": " + (dry.Error ?? "can't add this.")); return false; }
                        plans.Add((target, spec, plan.Clone()));
                    }
                Review(plans, scope, () => Details(final, template, filled));
                return false;

                void Fail(string message) { error.Text = message; error.Visibility = Visibility.Visible; }
            }, Gallery);
        }

        // The exact command and the files that change.
        void Review(List<(string Agent, McpSpec Spec, JsonElement Plan)> plans, string scope, Action backTo)
        {
            var body = new StackPanel { Spacing = 10, Width = 500 };
            var agentNames = plans.Select(p => _agentNames.GetValueOrDefault(p.Agent, p.Agent)).Distinct().ToList();
            body.Children.Add(new TextBlock
            {
                Text = "Nexus will add this to " + AgentConfigText.AgentList(agentNames) + (scope == "project" ? ", for this project only." : ", for all your projects."),
                FontSize = 12.5, TextWrapping = TextWrapping.Wrap
            });
            foreach (var group in plans.GroupBy(p => p.Spec.Name))
            {
                var first = group.First();
                var spec = first.Spec;
                body.Children.Add(new TextBlock { Text = spec.Name, FontSize = 13, FontWeight = FontWeights.SemiBold, Margin = new Thickness(0, 4, 0, 0) });
                body.Children.Add(Tiny(spec.Transport == "stdio" ? "Each time the agent starts, it runs this command on your computer:" : "The agent connects to this address:"));
                body.Children.Add(CommandBox(NxJson.Text(first.Plan, "command_line")));
                var secretNames = spec.Env.Keys.Concat(spec.Headers.Keys).ToList();
                if (secretNames.Count > 0) body.Children.Add(Tiny("With " + string.Join(", ", secretNames) + " (values hidden)."));
                var testHost = new StackPanel { Spacing = 6 };
                var test = FlatButton("Test it first");
                test.Click += async (_, _) =>
                {
                    test.IsEnabled = false;
                    await RunTestAsync(testHost, new { spec = spec.ToArgs(), timeout_ms = 30000, workspace = _workspace });
                    test.IsEnabled = true;
                };
                body.Children.Add(test);
                body.Children.Add(testHost);
            }
            body.Children.Add(Tiny("Files Nexus changes (each is backed up first):"));
            foreach (var path in plans.Select(p => NxJson.Text(p.Plan, "path")).Distinct())
                body.Children.Add(new TextBlock { Text = path, FontFamily = MonoFont, FontSize = 11, Opacity = 0.7, TextWrapping = TextWrapping.Wrap });
            var error = new TextBlock { FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
            body.Children.Add(error);
            Step("Check and add", body, "Add", async () =>
            {
                var failures = new List<string>();
                foreach (var (target, spec, _) in plans)
                {
                    var done = await Core.CallAsync("agents.config.mcp_save", new { agent = target, scope, spec = spec.ToArgs(), workspace = _workspace, state_dir = StateDir });
                    await UiThread.Resume(DispatcherQueue);
                    if (done.Ok) added.Add(spec.Name + " → " + _agentNames.GetValueOrDefault(target, target));
                    else failures.Add(_agentNames.GetValueOrDefault(target, target) + ": " + (done.Error ?? "failed"));
                }
                if (failures.Count == 0) return true;
                error.Text = string.Join("\n", failures);
                error.Visibility = Visibility.Visible;
                dialog.PrimaryButtonText = "";
                return false;
            }, backTo);
        }

        dialog.PrimaryButtonClick += async (_, e) =>
        {
            if (primary is null) return;
            var deferral = e.GetDeferral();
            dialog.IsPrimaryButtonEnabled = false;
            try { e.Cancel = !await primary(); }
            finally { dialog.IsPrimaryButtonEnabled = true; deferral.Complete(); }
        };
        dialog.SecondaryButtonClick += (_, e) => { e.Cancel = true; back?.Invoke(); };
        Gallery();
        await dialog.ShowThemedAsync();
        if (added.Count > 0)
        {
            _status.Text = "Added " + string.Join(", ", added) + ". Restart the agent to use it.";
            _contents.Clear();
            await LoadAsync();
        }
    }

    // The primary button keeps the accent through step changes.
    private static void AccentPrimary(ContentDialog dialog)
    {
        if (Application.Current.Resources.TryGetValue("AccentButtonStyle", out var accent) && accent is Style style)
            dialog.PrimaryButtonStyle = style;
    }

    private static Border CommandBox(string text) => new()
    {
        Child = new TextBlock { Text = text, FontFamily = MonoFont, FontSize = 12, TextWrapping = TextWrapping.Wrap, IsTextSelectionEnabled = true },
        Padding = new Thickness(12, 8, 12, 9), CornerRadius = new CornerRadius(6), Background = NexusPalette.Surface,
        BorderBrush = NexusPalette.Hairline, BorderThickness = new Thickness(1)
    };

    // ── Edit ───────────────────────────────────────────────────────────

    private async Task EditServerAsync(string agent, JsonElement item)
    {
        var scope = NxJson.Text(item, "scope");
        var path = NxJson.Text(item, "path");
        var original = NxJson.Text(item, "name");
        var got = await Core.CallAsync("agents.config.mcp_get", new { agent, scope, path, name = original, workspace = _workspace, state_dir = StateDir });
        await UiThread.Resume(DispatcherQueue);
        if (!got.Ok || got.Result is not { } edit) { _status.Text = got.Error ?? "Nexus couldn't open that server."; return; }
        var spec = McpSpec.From(edit.GetProperty("spec"));

        var form = new StackPanel { Spacing = 10, Width = 500 };
        var name = new TextBox { Header = "Name", Text = spec.Name, FontSize = 12.5 };
        var runs = new RadioButton { Content = "Runs a command on this computer", GroupName = "mcp-edit-kind", IsChecked = spec.Transport == "stdio", FontSize = 12.5 };
        var remote = new RadioButton { Content = "Connects to a web address", GroupName = "mcp-edit-kind", IsChecked = spec.Transport != "stdio", FontSize = 12.5 };
        var command = new TextBox { Header = "Command", Text = spec.Command, FontFamily = MonoFont, FontSize = 12.5, PlaceholderText = "npx" };
        var args = new TextBox { Header = "Arguments (one per line)", AcceptsReturn = true, Text = string.Join("\n", spec.Args), FontFamily = MonoFont, FontSize = 12.5, MinHeight = 70, TextWrapping = TextWrapping.Wrap };
        var env = new TextBox { Header = "Environment variables (NAME=value, one per line)", AcceptsReturn = true, Text = AgentConfigText.FormatPairs(spec.Env, '='), FontFamily = MonoFont, FontSize = 12.5, MinHeight = 56, TextWrapping = TextWrapping.Wrap };
        var url = new TextBox { Header = "Address", Text = spec.Url, FontFamily = MonoFont, FontSize = 12.5, PlaceholderText = "https://" };
        var sse = new CheckBox { Content = "Older SSE server", IsChecked = spec.Transport == "sse", FontSize = 12.5 };
        var headers = new TextBox { Header = "Headers (Name: value, one per line)", AcceptsReturn = true, Text = AgentConfigText.FormatPairs(spec.Headers, ':'), FontFamily = MonoFont, FontSize = 12.5, MinHeight = 56, TextWrapping = TextWrapping.Wrap };
        var local = new StackPanel { Spacing = 8 };
        local.Children.Add(command); local.Children.Add(args); local.Children.Add(env);
        var web = new StackPanel { Spacing = 8 };
        web.Children.Add(url); web.Children.Add(headers); web.Children.Add(sse);
        void Kind() { local.Visibility = runs.IsChecked == true ? Visibility.Visible : Visibility.Collapsed; web.Visibility = runs.IsChecked == true ? Visibility.Collapsed : Visibility.Visible; }
        runs.Checked += (_, _) => Kind();
        remote.Checked += (_, _) => Kind();
        Kind();
        form.Children.Add(name);
        form.Children.Add(runs);
        form.Children.Add(remote);
        form.Children.Add(local);
        form.Children.Add(web);
        if (spec.Env.Values.Concat(spec.Headers.Values).Any(v => v.Contains(AgentConfigText.Mask)) || spec.Args.Any(a => a.Contains(AgentConfigText.Mask)))
            form.Children.Add(Tiny(AgentConfigText.Mask + " stands for a saved secret. Leave it as it is to keep the value, or type a new one."));
        var kept = Arr(edit, "kept").Select(k => k.GetString() ?? "").ToList();
        if (kept.Count > 0) form.Children.Add(Tiny("Other settings stay as they are: " + string.Join(", ", kept) + "."));

        // Advanced: the entry as this agent stores it; editing it fills the form.
        var json = new TextBox { AcceptsReturn = true, Text = NxJson.Text(edit, "json"), FontFamily = MonoFont, FontSize = 12, MinHeight = 140, TextWrapping = TextWrapping.Wrap, IsSpellCheckEnabled = false };
        var jsonError = new TextBlock { FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
        var applyJson = FlatButton("Use this JSON");
        var advanced = new StackPanel { Spacing = 8, Visibility = Visibility.Collapsed };
        advanced.Children.Add(Tiny("How " + _agentNames.GetValueOrDefault(agent, agent) + " stores this server. Edit it and choose Use this JSON to fill in the form."));
        advanced.Children.Add(json);
        advanced.Children.Add(jsonError);
        advanced.Children.Add(applyJson);
        var toggleAdvanced = new HyperlinkButton { Content = "Advanced: edit as JSON", FontSize = 12, Padding = new Thickness(0) };
        toggleAdvanced.Click += (_, _) => advanced.Visibility = advanced.Visibility == Visibility.Visible ? Visibility.Collapsed : Visibility.Visible;
        applyJson.Click += async (_, _) =>
        {
            var parsed = await Core.CallAsync("agents.config.mcp_parse", new { text = AgentConfigText.Newlines(json.Text) });
            await UiThread.Resume(DispatcherQueue);
            if (!parsed.Ok || parsed.Result is not { } p || Arr(p, "servers").FirstOrDefault() is not { ValueKind: JsonValueKind.Object } first)
            {
                jsonError.Text = parsed.Error ?? "Nexus couldn't read that.";
                jsonError.Visibility = Visibility.Visible;
                return;
            }
            jsonError.Visibility = Visibility.Collapsed;
            var s = McpSpec.From(first);
            name.Text = s.Name;
            runs.IsChecked = s.Transport == "stdio";
            remote.IsChecked = s.Transport != "stdio";
            command.Text = s.Command;
            args.Text = string.Join("\n", s.Args);
            env.Text = AgentConfigText.FormatPairs(s.Env, '=');
            url.Text = s.Url;
            headers.Text = AgentConfigText.FormatPairs(s.Headers, ':');
            sse.IsChecked = s.Transport == "sse";
            Kind();
        };
        form.Children.Add(toggleAdvanced);
        form.Children.Add(advanced);
        var testHost = new StackPanel { Spacing = 6 };
        var test = FlatButton("Test");
        form.Children.Add(test);
        form.Children.Add(testHost);
        var error = new TextBlock { FontSize = 12, Foreground = NexusPalette.Danger, TextWrapping = TextWrapping.Wrap, Visibility = Visibility.Collapsed };
        form.Children.Add(error);

        McpSpec? Build()
        {
            var s = new McpSpec { Name = name.Text.Trim(), Enabled = spec.Enabled };
            if (runs.IsChecked == true)
            {
                s.Transport = "stdio";
                s.Command = command.Text.Trim();
                s.Args = AgentConfigText.Lines(args.Text);
                s.Env = AgentConfigText.Pairs(env.Text, '=', out var bad);
                if (bad > 0) { Fail($"Environment line {bad} needs a name and a value, like API_KEY=abc."); return null; }
            }
            else
            {
                s.Transport = sse.IsChecked == true ? "sse" : "http";
                s.Url = url.Text.Trim();
                s.Headers = AgentConfigText.Pairs(headers.Text, ':', out var bad);
                if (bad > 0) { Fail($"Header line {bad} needs a name and a value, like Authorization: Bearer abc."); return null; }
            }
            return s;
        }
        void Fail(string message) { error.Text = message; error.Visibility = Visibility.Visible; }
        test.Click += async (_, _) =>
        {
            if (Build() is not { } s) return;
            test.IsEnabled = false;
            await RunTestAsync(testHost, new { agent, scope, path, original_name = original, spec = s.ToArgs(), timeout_ms = 30000, workspace = _workspace, state_dir = StateDir });
            test.IsEnabled = true;
        };

        var host = new ContentControl { Content = form, HorizontalContentAlignment = HorizontalAlignment.Stretch };
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Edit " + original, PrimaryButtonText = "Review", CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Primary,
            Content = new ScrollViewer { Content = host, MaxHeight = 600, VerticalScrollBarVisibility = ScrollBarVisibility.Auto }
        };
        AccentPrimary(dialog);
        McpSpec? reviewed = null;
        var saved = false;
        dialog.PrimaryButtonClick += async (_, e) =>
        {
            var deferral = e.GetDeferral();
            try
            {
                if (reviewed is null)
                {
                    e.Cancel = true;
                    error.Visibility = Visibility.Collapsed;
                    if (Build() is not { } s) return;
                    var dry = await Core.CallAsync("agents.config.mcp_save", new { agent, scope, path, original_name = original, spec = s.ToArgs(), dry_run = true, workspace = _workspace, state_dir = StateDir });
                    await UiThread.Resume(DispatcherQueue);
                    if (!dry.Ok || dry.Result is not { } plan) { Fail(dry.Error ?? "Nexus can't save this."); return; }
                    reviewed = s;
                    var review = new StackPanel { Spacing = 10, Width = 500 };
                    review.Children.Add(Tiny(s.Transport == "stdio" ? "Each time " + _agentNames.GetValueOrDefault(agent, agent) + " starts, it will run:" : _agentNames.GetValueOrDefault(agent, agent) + " will connect to:"));
                    review.Children.Add(CommandBox(NxJson.Text(plan, "command_line")));
                    if (s.Name != original) review.Children.Add(Tiny("Renamed from " + original + " to " + s.Name + "."));
                    review.Children.Add(Tiny("Nexus changes only this server in:"));
                    review.Children.Add(new TextBlock { Text = NxJson.Text(plan, "path"), FontFamily = MonoFont, FontSize = 11, Opacity = 0.7, TextWrapping = TextWrapping.Wrap });
                    review.Children.Add(Tiny("The file is backed up first. Restart the agent to apply."));
                    host.Content = review;
                    Motion.FadeSlideIn(review, dx: 10, dy: 0, durationMs: 220);
                    dialog.Title = "Save " + s.Name + "?";
                    dialog.PrimaryButtonText = "Save";
                    dialog.SecondaryButtonText = "Back";
                    return;
                }
                var done = await Core.CallAsync("agents.config.mcp_save", new { agent, scope, path, original_name = original, spec = reviewed.ToArgs(), workspace = _workspace, state_dir = StateDir });
                await UiThread.Resume(DispatcherQueue);
                if (!done.Ok)
                {
                    e.Cancel = true;
                    reviewed = null;
                    host.Content = form;
                    dialog.PrimaryButtonText = "Review";
                    dialog.SecondaryButtonText = "";
                    Fail(done.Error ?? "Nexus couldn't save this.");
                    return;
                }
                saved = true;
                _status.Text = "Saved " + reviewed.Name + " for " + _agentNames.GetValueOrDefault(agent, agent) + ". Restart the agent to apply.";
            }
            finally { deferral.Complete(); }
        };
        dialog.SecondaryButtonClick += (_, e) =>
        {
            e.Cancel = true;
            reviewed = null;
            host.Content = form;
            dialog.Title = "Edit " + original;
            dialog.PrimaryButtonText = "Review";
            dialog.SecondaryButtonText = "";
        };
        await dialog.ShowThemedAsync();
        if (saved)
        {
            _tests.Clear();
            _contents.Clear();
            _selectedItem = "";
            await LoadAsync();
        }
    }

    // ── Section actions and removing turned-off servers ────────────────

    // One clear action at the top of the Rules and Tools sections.
    private void AddActionRows(string agent, string section)
    {
        if (section == "mcp_servers" && CanEditMcp(agent))
            _rows.Add(new ActionRow("Add a tool server", "Files, GitHub, a browser and more, or paste JSON", "\uE710", () => AddServerAsync(agent)));
        if (section == "rules")
            foreach (var file in CreatableRules(agent))
            {
                var path = NxJson.Text(file, "path");
                var hint = NxJson.Text(file, "scope") == "project" ? "Starts from a template with this project's build and test commands" : "Your own rules, used in every project";
                _rows.Add(new ActionRow(CreateTitle(file), hint, "\uE710", () => OpenRulesEditorAsync(path)));
            }
    }

    private UIElement ActionRowView(ActionRow action)
    {
        var line = new Grid { ColumnSpacing = 10 };
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        line.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        line.Children.Add(new FontIcon { Glyph = action.Glyph, FontSize = 13, Foreground = NexusPalette.Accent, VerticalAlignment = VerticalAlignment.Center });
        var text = new StackPanel { Spacing = 1 };
        text.Children.Add(new TextBlock { Text = action.Title, FontSize = 12.5, FontWeight = FontWeights.SemiBold, Foreground = NexusPalette.Accent, TextTrimming = TextTrimming.CharacterEllipsis });
        text.Children.Add(new TextBlock { Text = action.Hint, FontSize = 11, Foreground = NexusPalette.Muted, TextTrimming = TextTrimming.CharacterEllipsis });
        Grid.SetColumn(text, 1);
        line.Children.Add(text);
        var button = RowButton(line, false);
        button.BorderBrush = NexusPalette.Hairline;
        button.BorderThickness = new Thickness(1);
        button.Margin = new Thickness(0, 2, 0, 6);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, action.Title);
        button.Click += async (_, _) => await action.Run();
        return button;
    }

    private async Task ForgetParkedAsync(string agent, JsonElement item)
    {
        var name = NxJson.Text(item, "name");
        var dialog = new ContentDialog
        {
            XamlRoot = XamlRoot, Title = "Delete " + name + "?",
            Content = new TextBlock { Text = name + " is turned off, so it is no longer in " + _agentNames.GetValueOrDefault(agent, agent) + "'s settings. Deleting it means you can't turn it back on; you'd have to add it again.", TextWrapping = TextWrapping.Wrap, FontSize = 12.5, MaxWidth = 460 },
            PrimaryButtonText = "Delete", CloseButtonText = "Cancel", DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return;
        var done = await Core.CallAsync("agents.config.mcp_forget", new { agent, scope = NxJson.Text(item, "scope"), path = NxJson.Text(item, "path"), name, workspace = _workspace, state_dir = StateDir });
        await UiThread.Resume(DispatcherQueue);
        _status.Text = done.Ok ? "Deleted " + name + "." : done.Error ?? "Nexus couldn't delete it.";
        _selectedItem = "";
        _contents.Clear();
        await LoadAsync();
    }
}
