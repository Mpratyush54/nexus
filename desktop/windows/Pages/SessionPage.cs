using Microsoft.UI.Dispatching;
using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Automation;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Documents;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Animation;
using Microsoft.UI.Xaml.Media.Imaging;
using Nexus.Core;
using System.Text.Json;
using System.Text.RegularExpressions;
using Windows.ApplicationModel.DataTransfer;
using Windows.Storage.Pickers;
using WinRT.Interop;

namespace Nexus.Pages;

public sealed partial class SessionPage : NexusPage
{
    private string _id = "";
    public string SessionId => _id;
    private readonly TextBlock _crumbProject = new();
    private readonly TextBlock _crumbSlash = new();
    private readonly TextBlock _header = new() { TextWrapping = TextWrapping.NoWrap, TextTrimming = TextTrimming.CharacterEllipsis, FontSize = 14, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, VerticalAlignment = VerticalAlignment.Center };
    private readonly StackPanel _chat = new() { Spacing = SessionChatRender.TurnSpacing, HorizontalAlignment = HorizontalAlignment.Stretch, MaxWidth = 736 };
    private readonly ScrollViewer _chatScroll = new() { VerticalScrollBarVisibility = ScrollBarVisibility.Auto };
    private readonly Button _chatEarlier = new() { Content = "Load earlier turns", HorizontalAlignment = HorizontalAlignment.Stretch };
    private const int TurnPageSize = 80;
    private const int MaxTurnWalkPages = 400;
    private readonly List<string> _turnPageStartCursors = new() { "" };
    private int _earlierPageIndex;
    private List<UIElement>? _chatRenderSink;
    // Turns a capture stored twice (TranscriptDedupe); cleared with the chat.
    private readonly TranscriptDedupe _turnDedupe = new();
    private readonly StackPanel _activity = new() { Spacing = 8 };
    private readonly StackPanel _memoryTab = new() { Spacing = 8 };
    private readonly StackPanel _meta = new() { Spacing = 6, Visibility = Visibility.Collapsed };
    private readonly TextBox _railSearch = new() { PlaceholderText = "Search chats" };
    private readonly StackPanel _railList = new() { Spacing = 2 };
    private readonly Button _railMore = new() { Content = "Load more chats", Visibility = Visibility.Collapsed, HorizontalAlignment = HorizontalAlignment.Stretch };
    private readonly List<RailEntry> _railEntries = new();
    private readonly Dictionary<string, string> _localAgentByTurnIndex = new(StringComparer.Ordinal);
    private sealed record RailEntry(string Id, string Title, string Meta);
    private sealed record ChildSessionEntry(string Id, string Title, string Meta);
    private sealed record SharePerson(string Id, string Name, string ProjectRole, string OrganizationRole);
    private sealed record ShareDirectory(string OrganizationId, string OrganizationName, IReadOnlyList<SharePerson> People);
    private sealed class ParsedToolCall(string name, string argsSummary, string result)
    {
        public string Name { get; } = name;
        public string ArgsSummary { get; } = argsSummary;
        public string Result { get; set; } = result;
    }

    private readonly List<ChildSessionEntry> _childSessions = new();
    private readonly Dictionary<string, ChildSessionEntry> _childById = new(StringComparer.OrdinalIgnoreCase);
    private readonly HashSet<string> _inlineSubagentIds = new(StringComparer.OrdinalIgnoreCase);
    private readonly List<JsonElement> _pendingCommands = new();
    private readonly HashSet<string> _shownCommandKeys = new(StringComparer.Ordinal);
    private int _chatCommandsShown;
    private string _railCursor = "";
    private string _projectID = "";
    private JsonElement? _projectsCatalog;
    private bool _railLoading;
    private readonly TextBlock _notice = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.8 };
    private string _agent = "";
    private string _nativeID = "";
    private string _workspaceHint = "";
    private bool _sessionLoaded;
    private bool _legacy;
    private bool _hasTransferableVersion;
    private int _loadGeneration;
    private MenuFlyoutItem _share = null!;
    private MenuFlyoutItem _teleport = null!;
    private MenuFlyoutItem _resume = null!;
    private MenuFlyoutItem _fork = null!;
    private readonly StackPanel _composer = new() { Spacing = 7 };
    private readonly TextBox _prompt = new() { PlaceholderText = "Ask anything…", AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, MinHeight = 68, MaxHeight = 160 };
    private readonly Button _sendHere = new() { Content = "Send ↗" };
    private readonly Button _cancelHere = new() { Content = "Stop", Visibility = Visibility.Collapsed };
    private readonly Button _attachImage = Named(new Button { Padding = new Thickness(8), MinWidth = 36, MinHeight = 36 }, "Attach images or files");

    private static T Named<T>(T element, string name) where T : DependencyObject
    {
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(element, name);
        return element;
    }
    private readonly ComboBox _harness = Named(new ComboBox { MinWidth = 128, MaxWidth = 160, PlaceholderText = "Agent" }, "Agent");
    private readonly ComboBox _model = Named(new ComboBox { MinWidth = 150, MaxWidth = 260, PlaceholderText = "Default model", IsEditable = true }, "Model");
    private string _modelTip = "";
    private readonly Dictionary<string, string> _modelByHarness = new(StringComparer.OrdinalIgnoreCase);
    private string _lastHarness = "";
    private readonly TextBox _workspace = new() { PlaceholderText = "Local project folder", MinWidth = 240 };
    private readonly Button _workspaceMenu = new() { Content = "Workspace…", Padding = new Thickness(8, 4, 8, 4), VerticalAlignment = VerticalAlignment.Center };
    private readonly Flyout _workspaceFlyout = new();
    private readonly TextBlock _attachmentsLabel = new() { TextWrapping = TextWrapping.Wrap, Opacity = 0.7 };
    private readonly StackPanel _imagePreviews = new() { Orientation = Orientation.Horizontal, Spacing = 8 };
    private readonly ScrollViewer _attachmentStrip = new() { HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled, Visibility = Visibility.Collapsed, MaxHeight = 76 };
    private readonly HashSet<string> _trustedCursorFolders = new(StringComparer.OrdinalIgnoreCase);
    private readonly List<string> _images = new();
    private string _continuationContext = "";
    private bool _sendInFlight;
    private string _streamRequestID = "";
    private string _streamAgent = "";
    private TextBlock? _streamText;
    private bool _streamHasText;
    private string _localNativeID = "";
    private string _localNativeAgent = "";
    private readonly TextBlock _composerStatus = new() { TextWrapping = TextWrapping.Wrap, FontSize = 11.5, Opacity = 0.72, Visibility = Visibility.Collapsed };
    private readonly Border _inputFrame = new() { CornerRadius = new CornerRadius(12), BorderThickness = new Thickness(1) };
    private readonly FontIcon _sendGlyph = new() { Glyph = "\uE74A", FontSize = 14 };
    private readonly ProgressRing _sendProgress = new() { Width = 16, Height = 16, IsActive = false, Visibility = Visibility.Collapsed };
    private Button _chatTabBtn = null!;
    private Button _activityTabBtn = null!;
    private Button _memoryTabBtn = null!;
    private Button _forkButton = null!;
    private readonly TextBlock _lineageMarker = new() { Text = "⑂ Forked chat", FontSize = 12, Opacity = 0.75, Visibility = Visibility.Collapsed, VerticalAlignment = VerticalAlignment.Center };

    public SessionPage()
    {
        foreach (var choice in AppPrefs.Data.ChatModels ?? new Dictionary<string, string>()) _modelByHarness[choice.Key] = choice.Value;
        _chatEarlier.Click += async (_, _) => await LoadEarlierTurnsAsync();
        // Reaching the top of the newest turns pulls in the page before them.
        _chatScroll.ViewChanged += (_, e) =>
        {
            if (e.IsIntermediate || !_backwardPaging || _olderCursor.Length == 0 || _olderLoading || _jumpInProgress) return;
            if (_chatScroll.ScrollableHeight <= 0 || _chatScroll.VerticalOffset > 48) return;
            if (Environment.TickCount64 - _paintedAt < 800) return;
            _ = LoadOlderTurnsAsync();
        };
        _railMore.Click += async (_, _) => await LoadMoreRailAsync();
        _railSearch.TextChanged += (_, _) => RenderRail();
        var back = new Button { Content = "← Chats" };
        back.Click += (_, _) => MainWindow.Current?.ShowTimeline();
        _resume = new MenuFlyoutItem { Text = "Copy command to continue in a terminal" };
        _resume.Click += async (_, _) => await CopyResumeAsync();
        _share = new MenuFlyoutItem { Text = "Share access…" };
        _share.Click += async (_, _) => await ShareAsync();
        _teleport = new MenuFlyoutItem { Text = "Share chat + code" };
        _teleport.Click += async (_, _) => await SendTeleportAsync();
        _fork = new MenuFlyoutItem { Text = "Fork into a new chat" };
        _fork.Click += async (_, _) => await ForkChatAsync();
        _sendHere.Click += async (_, _) => await SendHereAsync();
        _cancelHere.Click += async (_, _) => await CancelHereAsync();
        _attachImage.Click += async (_, _) => await AttachImageAsync();
        _prompt.Paste += Prompt_Paste;
        var imagePaste = new Microsoft.UI.Xaml.Input.KeyboardAccelerator { Key = Windows.System.VirtualKey.V, Modifiers = Shortcuts.Primary };
        imagePaste.Invoked += async (_, e) =>
        {
            try
            {
                var content = Clipboard.GetContent();
                if (!ClipboardHasImage(content)) return;
                e.Handled = true;
                await PasteClipboardImageAsync(content);
            }
            catch (Exception ex) { SetComposerStatus(NxErrors.ForException("Nexus couldn't paste that image. Try saving it and attaching the file.", ex, "attach")); }
        };
        _prompt.KeyboardAccelerators.Add(imagePaste);
        WireAttachmentDrop();
        // The macOS Edit menu's Paste (and Cmd+V there) comes through this.
        Shortcuts.SetPasteHandler(_prompt, TryPasteClipboardImageAsync);
        _harness.SelectionChanged += (_, _) => {
            if (_lastHarness.Length > 0) _modelByHarness[_lastHarness] = SelectedModel();
            _lastHarness = SelectedHarness();
            ConfigureModel();
            // Fill the picker in the background (cached lists return at once).
            _ = LoadHarnessModelsAsync(false, false);
            UpdateWorkspaceFlyoutVisibility();
            DropImagesFor(SelectedHarness());
            UpdateActions();
        };
        _harness.Padding = new Thickness(8, 3, 6, 3);
        // Fits the longest agent name ("Claude Code") in full, as the model
        // picker fits "Default model"; the compact footer never squeezes it.
        _harness.MinWidth = HarnessFitWidth();
        _harness.MinHeight = 32;
        _harness.MaxWidth = Math.Max(160, _harness.MinWidth);
        _harness.VerticalAlignment = VerticalAlignment.Center;
        _model.MinHeight = 32;
        // Wide enough for "Default model" and short model names in full.
        _model.MinWidth = 150;
        _model.MaxWidth = 260;
        _model.Padding = new Thickness(8, 3, 6, 3);
        SetModelTip("Choose a model, or type a model ID supported by the selected agent. Leave blank for its default.");
        ToolTipService.SetToolTip(_refreshModels, "Refresh this agent's model list (doesn't start a chat)");
        _refreshModels.Click += async (_, _) => await RefreshHarnessModelsAsync();
        _model.DropDownOpened += async (_, _) =>
        {
            // The tooltip would sit over the open list; drop it while open.
            ToolTipService.SetToolTip(_model, null);
            await LoadHarnessModelsAsync(false, false);
        };
        _model.DropDownClosed += (_, _) => ToolTipService.SetToolTip(_model, _modelTip);
        _model.SelectionChanged += (_, _) =>
        {
            DispatcherQueue.TryEnqueue(UpdateModelCatalogNotice);
            if (!_configuringModel) DispatcherQueue.TryEnqueue(RememberModelChoice);
        };
        _model.TextSubmitted += (_, _) => DispatcherQueue.TryEnqueue(RememberModelChoice);
        _model.LostFocus += (_, _) => RememberModelChoice();
        _attachImage.Content = new FontIcon { Glyph = "\uE723", FontSize = 15, Opacity = 0.72 };
        ToolTipService.SetToolTip(_attachImage, "Attach images or files" + Environment.NewLine + $"You can also paste ({PlatformText.Shortcut("V")}) or drop them here.");
        _attachImage.Padding = new Thickness(6);
        _attachImage.MinWidth = 32;
        _attachImage.MinHeight = 32;
        _attachImage.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _attachImage.BorderThickness = new Thickness(0);
        _attachImage.Resources["ButtonBackgroundDisabled"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _attachImage.Resources["ButtonBorderBrushDisabled"] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _attachImage.VerticalAlignment = VerticalAlignment.Center;
        _prompt.MinHeight = 36;
        _prompt.MaxHeight = 132;
        _prompt.Padding = new Thickness(4, 4, 4, 4);
        _prompt.BorderThickness = new Thickness(0);
        _prompt.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        ChatTitles.Bare(_prompt);
        _prompt.PlaceholderForeground = Surface("TextFillColorSecondaryBrush", Windows.UI.Color.FromArgb(130, 255, 255, 255));
        _inputFrame.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _inputFrame.BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _inputFrame.Padding = new Thickness(8, 4, 8, 4);
        _inputFrame.Child = _prompt;
        _inputFrame.HorizontalAlignment = HorizontalAlignment.Stretch;
        _prompt.GotFocus += (_, _) => AnimateInputFocus(true);
        _prompt.LostFocus += (_, _) => AnimateInputFocus(false);
        var workspacePanel = new StackPanel { Spacing = 8, MinWidth = 320 };
        workspacePanel.Children.Add(_workspace);
        _workspaceFlyout.Content = workspacePanel;
        _workspaceMenu.Flyout = _workspaceFlyout;
        _workspaceMenu.Padding = new Thickness(0);
        _workspaceMenu.MinHeight = 32;
        _workspaceMenu.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _workspaceMenu.BorderThickness = new Thickness(0);
        _workspaceMenu.HorizontalAlignment = HorizontalAlignment.Stretch;
        _workspaceMenu.HorizontalContentAlignment = HorizontalAlignment.Left;
        _workspace.TextChanged += (_, _) => { RefreshWorkspaceButton(); UpdateActions(); QueueComposerFooterLayout(); };
        RefreshWorkspaceButton();
        var sendLabel = new TextBlock { Text = "Send", FontSize = 13, VerticalAlignment = VerticalAlignment.Center };
        var sendRow = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, VerticalAlignment = VerticalAlignment.Center };
        sendRow.Children.Add(_sendGlyph);
        sendRow.Children.Add(sendLabel);
        var sendHost = new Grid { VerticalAlignment = VerticalAlignment.Center };
        sendHost.Children.Add(sendRow);
        _sendProgress.HorizontalAlignment = HorizontalAlignment.Center;
        _sendProgress.VerticalAlignment = VerticalAlignment.Center;
        sendHost.Children.Add(_sendProgress);
        _sendHere.Content = sendHost;
        _sendHere.Padding = new Thickness(7);
        _sendHere.MinWidth = 32;
        _sendHere.MinHeight = 32;
        _sendHere.CornerRadius = new CornerRadius(16);
        _sendHere.Background = new SolidColorBrush(Windows.UI.Color.FromArgb(255, 0x33, 0x33, 0x33)); // T3: round grey send
        _sendHere.BorderBrush = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        _sendHere.Foreground = new SolidColorBrush(Microsoft.UI.Colors.White);
        sendLabel.Visibility = Visibility.Collapsed;
        _sendHere.VerticalAlignment = VerticalAlignment.Center;
        var composerFooter = _composerFooter;
        composerFooter.ColumnSpacing = FooterSpacing;
        // The folder label takes the leftover width (star column): it trims
        // with an ellipsis, then turns into a folder icon, before anything
        // else gives way (LayoutComposerFooter).
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        composerFooter.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        Grid.SetColumn(_harness, 3);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_access, "Permission");
        var modelPicker = _modelPickerRow;
        foreach (var item in new FrameworkElement[] { _model, _refreshModels, _accessWarning, _access }) modelPicker.Children.Add(item);
        _harness.SelectionChanged += (_, _) => _access.Visibility = SupportsLive(SelectedHarness()) ? Visibility.Visible : Visibility.Collapsed;
        Grid.SetColumn(modelPicker, 4);
        Grid.SetColumn(_workspaceMenu, 1);
        Grid.SetColumn(_attachImage, 0);
        Grid.SetColumn(_cancelHere, 5);
        Grid.SetColumn(_sendHere, 6);
        _workspaceMenu.HorizontalAlignment = HorizontalAlignment.Left;
        composerFooter.Children.Add(_harness);
        composerFooter.Children.Add(modelPicker);
        composerFooter.Children.Add(_workspaceMenu);
        composerFooter.Children.Add(_attachImage);
        composerFooter.Children.Add(_cancelHere);
        composerFooter.Children.Add(_sendHere);
        // Stop: round icon like send (reference: T3 Code), not a text button.
        _cancelHere.Content = new FontIcon { Glyph = "\uE71A", FontSize = 12 };
        _cancelHere.CornerRadius = new CornerRadius(16);
        _cancelHere.MinWidth = 32;
        _cancelHere.MinHeight = 32;
        _cancelHere.Padding = new Thickness(8);
        _cancelHere.BorderThickness = new Thickness(0);
        _cancelHere.Background = new SolidColorBrush(Windows.UI.Color.FromArgb(255, 0x33, 0x33, 0x33));
        ToolTipService.SetToolTip(_cancelHere, "Stop");
        // Ghost pickers (reference: T3 Code): text only until hovered.
        foreach (var picker in new Control[] { _harness, _model, _access, _refreshModels, _workspaceMenu })
        {
            picker.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            picker.BorderThickness = new Thickness(0);
            // Stay ghost while a reply runs (disabled state), not grey boxes.
            foreach (var key in new[] { "ComboBoxBackgroundDisabled", "ComboBoxBorderBrushDisabled", "ButtonBackgroundDisabled", "ButtonBorderBrushDisabled" })
                picker.Resources[key] = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            picker.FontSize = 12.5;
            picker.Foreground = Surface("TextFillColorSecondaryBrush", Windows.UI.Color.FromArgb(255, 163, 163, 163));
        }
        // Recomputed on every width change and whenever a footer item changes
        // size (agent, model, the access picker or Stop showing up).
        composerFooter.SizeChanged += (_, e) => { if (Math.Abs(e.NewSize.Width - e.PreviousSize.Width) >= 0.5) LayoutComposerFooter(); };
        _harness.SelectionChanged += (_, _) => QueueComposerFooterLayout();
        _model.SelectionChanged += (_, _) => QueueComposerFooterLayout();
        _model.TextSubmitted += (_, _) => QueueComposerFooterLayout();
        _access.RegisterPropertyChangedCallback(VisibilityProperty, (_, _) => QueueComposerFooterLayout());
        _cancelHere.RegisterPropertyChangedCallback(VisibilityProperty, (_, _) => QueueComposerFooterLayout());
        _attachImage.RegisterPropertyChangedCallback(VisibilityProperty, (_, _) => QueueComposerFooterLayout());
        ToolTipService.SetToolTip(_sendHere, "Send message");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(_sendHere, "Send message");
        _composer.Spacing = 6;
        _attachmentStrip.Content = _imagePreviews;
        _composer.Children.Add(_attachmentStrip);
        _composer.Children.Add(_inputFrame);
        _composer.Children.Add(composerFooter);
        _composer.Children.Add(_modelCatalogNotice);
        _attachmentsLabel.FontSize = 11;
        _attachmentsLabel.Visibility = Visibility.Collapsed;
        _composer.Children.Add(_composerStatus);
        InitLiveComposer();
        VoiceInput.Attach(_prompt, _sendHere);
        InitComposer();
        UpdateWorkspaceFlyoutVisibility();
        _chatTabBtn = SessionTabButton("Chat");
        _activityTabBtn = SessionTabButton("Activity");
        _memoryTabBtn = SessionTabButton("Memory");
        var activityScroll = new ScrollViewer { Content = _activity, Visibility = Visibility.Collapsed };
        var memoryScroll = new ScrollViewer { Content = _memoryTab, Visibility = Visibility.Collapsed };
        _chatScroll.Content = _chat;
        _chatScroll.Padding = new Thickness(0, 0, 0, 4);
        void SelectDetail(int selected)
        {
            activityScroll.Visibility = selected == 0 ? Visibility.Visible : Visibility.Collapsed;
            memoryScroll.Visibility = selected == 1 ? Visibility.Visible : Visibility.Collapsed;
            StyleSessionTab(_activityTabBtn, selected == 0);
            StyleSessionTab(_memoryTabBtn, selected == 1);
        }
        _activityTabBtn.Click += (_, _) => SelectDetail(0);
        _memoryTabBtn.Click += (_, _) => SelectDetail(1);
        SelectDetail(0);
        var tabBar = new Border
        {
            Padding = new Thickness(3),
            CornerRadius = new CornerRadius(10),
            Background = SessionChatRender.SubtleFill,
            BorderBrush = SessionChatRender.SubtleBorder,
            BorderThickness = new Thickness(1),
            HorizontalAlignment = HorizontalAlignment.Left,
            Child = Row(_activityTabBtn, _memoryTabBtn)
        };
        var detailsGrid = new Grid { RowSpacing = 10 };
        detailsGrid.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        detailsGrid.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        detailsGrid.Children.Add(tabBar);
        var detailContent = new Grid();
        detailContent.Children.Add(activityScroll);
        detailContent.Children.Add(memoryScroll);
        Grid.SetRow(detailContent, 1);
        detailsGrid.Children.Add(detailContent);
        var details = new Border
        {
            Padding = new Thickness(14, 12, 14, 12),
            CornerRadius = new CornerRadius(12),
            BorderThickness = new Thickness(1),
            Background = Surface("CardBackgroundFillColorDefaultBrush", Windows.UI.Color.FromArgb(26, 255, 255, 255)),
            BorderBrush = Surface("CardStrokeColorDefaultBrush", Windows.UI.Color.FromArgb(60, 255, 255, 255)),
            Child = detailsGrid,
            Visibility = Visibility.Collapsed
        };

        var conversation = new Grid { RowSpacing = 10, MinHeight = 0 };
        conversation.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        conversation.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        conversation.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        conversation.Children.Add(BuildConversationNavigation());
        var conversationViewport = BuildConversationViewport();
        Grid.SetRow(conversationViewport, 1);
        conversation.Children.Add(conversationViewport);
        var composerFrame = new Border
        {
            Child = _composer,
            Padding = new Thickness(12, 10, 10, 8),
            Margin = new Thickness(0, 2, 0, 0),
            CornerRadius = new CornerRadius(16),
            BorderThickness = new Thickness(1),
            MaxWidth = 760,
            BorderBrush = Surface("NexusHairlineBrush", Windows.UI.Color.FromArgb(20, 255, 255, 255)),
            Background = Surface("NexusSurfaceBrush", Windows.UI.Color.FromArgb(255, 15, 15, 15))
        };
        Grid.SetRow(composerFrame, 2);
        conversation.Children.Add(composerFrame);

        var body = new Grid { ColumnSpacing = 18 };
        body.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        var detailColumn = new ColumnDefinition { Width = new GridLength(0) };
        body.ColumnDefinitions.Add(detailColumn);
        body.Children.Add(conversation);
        Grid.SetColumn(details, 1);
        body.Children.Add(details);

        var shareButton = new Button { Content = new FontIcon { Glyph = "\uE72D", FontSize = 14 } };
        ToolTipService.SetToolTip(shareButton, "Share");
        // Header menus open downward: with the window at the top of the
        // screen an upward menu opened off-screen and the button looked dead.
        var shareMenu = new MenuFlyout { Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedRight };
        shareMenu.Items.Add(_share);
        shareMenu.Items.Add(_teleport);
        shareButton.Flyout = shareMenu;
        ToolTipService.SetToolTip(shareButton, "Share access, or send this chat with its code");
        var more = new Button { Content = new FontIcon { Glyph = "\uE712", FontSize = 14 } };
        ToolTipService.SetToolTip(more, "More");
        var menu = new MenuFlyout { Placement = Microsoft.UI.Xaml.Controls.Primitives.FlyoutPlacementMode.BottomEdgeAlignedRight };
        menu.Items.Add(_fork);
        menu.Items.Add(_resume);
        var copyConversation = new MenuFlyoutItem { Text = "Copy conversation text" };
        copyConversation.Click += (_, _) =>
        {
            var data = new DataPackage();
            data.SetText(string.Join("\n\n", _conversation.Entries.Select(e => (e.Role == "user" ? "You" : "Assistant") + "\n" + e.Text)));
            Clipboard.SetContent(data);
        };
        menu.Items.Add(copyConversation);
        var tagsItem = new MenuFlyoutItem { Text = "Tags…", Icon = new FontIcon { Glyph = "" } };
        tagsItem.Click += async (_, _) => { if (_id.Length > 0) await ChatTags.EditAsync(XamlRoot, _id, _header.Text); };
        menu.Items.Add(tagsItem);
        var selectConversation = new MenuFlyoutItem { Text = "Select conversation text…" };
        selectConversation.Click += async (_, _) =>
        {
            var text = string.Join("\n\n", _conversation.Entries.Select(e => (e.Role == "user" ? "You" : "Assistant") + "\n" + e.Text));
            var selection = new TextBox { Text = text, IsReadOnly = true, AcceptsReturn = true, TextWrapping = TextWrapping.Wrap, MinWidth = 480, MaxHeight = 520, Padding = new Thickness(14) };
            selection.Loaded += (_, _) => { selection.Focus(FocusState.Programmatic); selection.SelectAll(); };
            var dialog = new ContentDialog { XamlRoot = XamlRoot, Title = "Conversation text", Content = selection, PrimaryButtonText = "Copy all", CloseButtonText = "Close" };
            if (await dialog.ShowThemedAsync() == ContentDialogResult.Primary) { var data = new DataPackage(); data.SetText(text); Clipboard.SetContent(data); }
        };
        menu.Items.Add(selectConversation);
        more.Flyout = menu;
        var detailsButton = new Button { Content = new FontIcon { Glyph = "\uE8A0", FontSize = 14 } };
        ToolTipService.SetToolTip(detailsButton, "Details panel");
        detailsButton.Click += (_, _) =>
        {
            var show = details.Visibility != Visibility.Visible;
            details.Visibility = show ? Visibility.Visible : Visibility.Collapsed;
            detailColumn.Width = show ? new GridLength(310) : new GridLength(0);
            detailsButton.Opacity = show ? 1 : 0.8;
        };
        _forkButton = new Button { Content = new FontIcon { Glyph = "\uE8AB", FontSize = 14 } };
        _forkButton.Click += async (_, _) => await ForkChatAsync();
        ToolTipService.SetToolTip(_forkButton, "Start a separate chat from this conversation");
        var find = new Button { Content = new FontIcon { Glyph = "\uE721", FontSize = 15 }, MinWidth = 34 };
        ToolTipService.SetToolTip(find, "Find in this chat · " + Shortcuts.Label("F"));
        find.Click += (_, _) => OpenFind();
        // Token usage sits beside the actions while the title fits; when it
        // would squeeze the title it moves to a small line under it.
        var usage = new ChatUsageBar(() => _id) { Margin = new Thickness(0, 0, 8, 0) };
        var usageInline = new Border { Child = usage, VerticalAlignment = VerticalAlignment.Center };
        var usageBelow = new Border { Margin = new Thickness(0, -2, 0, 0) };
        InitSubagentsButton();
        // Fork stays in the More menu; the header keeps the everyday actions.
        _forkButton.Visibility = Visibility.Collapsed;
        AutomationProperties.SetName(find, "Find in this chat");
        AutomationProperties.SetName(detailsButton, "Details panel");
        AutomationProperties.SetName(shareButton, "Share");
        AutomationProperties.SetName(more, "More actions");
        var headerActions = Row(usageInline, _lineageMarker, _subagentsButton, find, detailsButton, _forkButton, shareButton, more);
        headerActions.Spacing = 2;
        foreach (var action in headerActions.Children.OfType<Button>())
        {
            action.Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent);
            action.BorderThickness = new Thickness(0);
            action.MinHeight = 30;
            action.MinWidth = 30;
            action.Padding = new Thickness(7);
            action.Foreground = Surface("TextFillColorSecondaryBrush", Windows.UI.Color.FromArgb(255, 163, 163, 163));
        }
        var titleRow = new Grid { ColumnSpacing = 16, Margin = new Thickness(0, 0, 132, 0) };
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        titleRow.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        // A grid, not a horizontal StackPanel: the title gets the width that
        // is left and trims with an ellipsis instead of being cut off.
        var crumb = new Grid { ColumnSpacing = 8, VerticalAlignment = VerticalAlignment.Center };
        crumb.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        crumb.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
        crumb.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
        Grid.SetColumn(_crumbSlash, 1);
        Grid.SetColumn(_header, 2);
        _crumbProject.MaxWidth = 220;
        _crumbProject.TextTrimming = TextTrimming.CharacterEllipsis;
        _header.TextTrimming = TextTrimming.CharacterEllipsis;
        _header.TextWrapping = TextWrapping.NoWrap;
        _crumbProject.FontSize = 14;
        _crumbProject.Opacity = 0.5;
        _crumbProject.VerticalAlignment = VerticalAlignment.Center;
        _crumbSlash.Text = "/";
        _crumbSlash.FontSize = 14;
        _crumbSlash.Opacity = 0.3;
        _crumbSlash.VerticalAlignment = VerticalAlignment.Center;
        _header.FontWeight = Microsoft.UI.Text.FontWeights.Medium;
        crumb.Children.Add(_crumbProject);
        crumb.Children.Add(_crumbSlash);
        crumb.Children.Add(_header);
        titleRow.Children.Add(crumb);
        Grid.SetColumn(headerActions, 1);
        titleRow.Children.Add(headerActions);
        // The sidebar lists chats, so no separate "back to chats" button.
        _ = back;
        var head = new StackPanel { Spacing = 4 };
        head.Children.Add(usageBelow);
        void PlaceUsage()
        {
            if (titleRow.ActualWidth <= 0) return;
            var probe = new TextBlock { Text = _header.Text, FontSize = _header.FontSize, FontWeight = _header.FontWeight, FontFamily = _header.FontFamily };
            probe.Measure(new Windows.Foundation.Size(double.PositiveInfinity, double.PositiveInfinity));
            var project = _crumbProject.Visibility == Visibility.Visible ? _crumbProject.ActualWidth + _crumbSlash.ActualWidth + 16 : 0;
            var usageWidth = usage.Visibility == Visibility.Visible ? usage.DesiredSize.Width + usage.Margin.Left + usage.Margin.Right : 0;
            var actions = headerActions.ActualWidth - (usageInline.Child is null ? 0 : usageInline.ActualWidth);
            var room = titleRow.ActualWidth - titleRow.ColumnSpacing - actions - project;
            var inline = probe.DesiredSize.Width + usageWidth <= room;
            if (inline && usageInline.Child is null) { usageBelow.Child = null; usageInline.Child = usage; }
            else if (!inline && usageBelow.Child is null) { usageInline.Child = null; usageBelow.Child = usage; }
        }
        titleRow.SizeChanged += (_, _) => PlaceUsage();
        usage.SizeChanged += (_, _) => DispatcherQueue.TryEnqueue(PlaceUsage);
        _header.RegisterPropertyChangedCallback(TextBlock.TextProperty, (_, _) => DispatcherQueue.TryEnqueue(PlaceUsage));
        // Agent/project/last-activity chips are replaced by the breadcrumb.
        head.Children.Add(_notice);
        _notice.Visibility = Visibility.Collapsed;
        _notice.RegisterPropertyChangedCallback(TextBlock.TextProperty, (_, _) =>
            _notice.Visibility = string.IsNullOrWhiteSpace(_notice.Text) ? Visibility.Collapsed : Visibility.Visible);

        var root = new Grid { Padding = new Thickness(20, 14, 20, 12), RowSpacing = 6 };
        var findShortcut = new Microsoft.UI.Xaml.Input.KeyboardAccelerator { Key = Windows.System.VirtualKey.F, Modifiers = Shortcuts.Primary };
        findShortcut.Invoked += (_, e) => { e.Handled = true; OpenFind(); };
        root.KeyboardAccelerators.Add(findShortcut);
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = GridLength.Auto });
        root.RowDefinitions.Add(new RowDefinition { Height = new GridLength(1, GridUnitType.Star) });
        Grid.SetRow(head, 1);
        Grid.SetRow(body, 2);
        root.Children.Add(titleRow);
        root.Children.Add(head);
        root.Children.Add(body);
        Content = root;
        Loaded += (_, _) => { ShellState.Changed += HandleShellChanged; App.Core.EventReceived += HandleCoreEvent; UpdateActions(); AgentAvailabilityService.Changed += OnAgentsChanged; AgentAvailabilityService.Refresh(); _ = ReattachLiveAsync(); };
        Unloaded += (_, _) => { ShellState.Changed -= HandleShellChanged; App.Core.EventReceived -= HandleCoreEvent; AgentAvailabilityService.Changed -= OnAgentsChanged; DetachLiveView(); };
        UpdateActions();
    }

    protected override void OnEnter(object? parameter)
    {
        var nextId = parameter as string ?? "";
        if (nextId.Length > 0 && nextId == _id && _sessionLoaded)
        {
            // Already on screen: refresh quietly, at most every few seconds.
            if (DateTime.UtcNow - _loadedAt > TimeSpan.FromSeconds(10)) _ = RevalidateAsync();
            return;
        }
        var generation = ++_loadGeneration;
        StashDraft();
        if (nextId != _id)
        {
            // The agent keeps working in the core; this page just stops showing it.
            DetachLiveView();
            RenderFollowups(default);
            ShowResumeBanner(default);
        }
        RememberScrollPosition();
        _id = nextId;
        // A newly opened chat starts pinned to its newest message.
        _stickToBottom = true;
        _lastSeenOffset = 0;
        _header.Text = MainWindow.Current?.ChatTitleFor(nextId) is { Length: > 0 } listedTitle ? listedTitle : "Opening chat…";
        // The title glides in from the chat list row that was clicked.
        if (IsLoaded) DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () => Motion.StartChatTitle(_header));
        else
        {
            void StartOnce(object sender, RoutedEventArgs e) { Loaded -= StartOnce; Motion.StartChatTitle(_header); }
            Loaded += StartOnce;
        }
        _notice.Text = "";
        _sessionLoaded = false;
        _legacy = false;
        _hasTransferableVersion = false;
        _nativeID = "";
        _localNativeID = "";
        _localNativeAgent = "";
        _workspaceHint = "";
        SetComposerStatus("");
        _prompt.Text = "";
        _continuationContext = "";
        ClearImageAttachments();
        RestoreDraft();
        _harness.Items.Clear();
        _workspace.Text = "";
        _agent = "";
        _projectID = "";
        _projectsCatalog = null;
        _railCursor = "";
        _meta.Children.Clear();
        _meta.Visibility = Visibility.Collapsed;
        _lineageMarker.Visibility = Visibility.Collapsed;
        _railEntries.Clear();
        _localAgentByTurnIndex.Clear();
        _childSessions.Clear();
        _childById.Clear();
        _inlineSubagentIds.Clear();
        _pendingCommands.Clear();
        _turnSteps.Clear();
        _shownEnvelopes.Clear();
        _turnDedupe.Clear();
        _shownCommandKeys.Clear();
        _chatCommandsShown = 0;
        _turnPageStartCursors.Clear();
        _turnPageStartCursors.Add("");
        _earlierPageIndex = 0;
        ResetConversationNavigation();
        _backwardPaging = false;
        _olderCursor = "";
        _latestPagesShown = 0;
        _latestSignature = "";
        _lastOps = null;
        _lastForks = null;
        _summaryResult = null;
        _loadedAt = DateTime.MinValue;
        UpdateActions();
        // A chat opened recently paints from memory at once; the network load
        // that follows is quiet and re-renders only if something changed.
        var painted = false;
        if (_id.Length > 0 && s_snapshots.TryGetValue(_id, out var snapshot))
        {
            var watch = System.Diagnostics.Stopwatch.StartNew();
            _lastOps = snapshot.Ops;
            _lastForks = snapshot.Forks;
            PaintSession(_id, snapshot.Session, snapshot.Latest, snapshot.Continuations,
                s_projects is { Ok: true } projects ? projects.Result : null, snapshot.Ops, snapshot.Forks, quiet: false, refreshTranscript: true);
            RestoreRememberedScroll();
            painted = true;
            System.Diagnostics.Trace.WriteLine($"[perf] session {_id}: open-to-first-message {watch.ElapsedMilliseconds} ms (from memory)");
        }
        _ = LoadThenStartAsync(generation, quiet: painted);
        FocusComposerSoon(force: true);
    }

    // Set by Home's "start a chat" composer: once the new chat has loaded,
    // its first message is sent without another click.
    internal static (string SessionId, string Prompt, IReadOnlyList<ComposerMention> Mentions)? PendingStart;

    private async Task LoadThenStartAsync(int generation, bool quiet = false)
    {
        await LoadAsync(generation, quiet);
        if (generation != _loadGeneration) return;
        if (_sessionLoaded && !System.IO.Directory.Exists(_workspace.Text.Trim()))
        {
            // Chats created in Nexus have no folder hint; find the project's folder.
            var folder = await ProjectFolders.ResolveAsync(App.Core, _projectID, ShellState.Current.ProjectName);
            await UiThread.Resume(DispatcherQueue);
            if (generation != _loadGeneration) return;
            if (folder.Length > 0 && _workspace.Text.Trim().Length == 0) { _workspace.Text = folder; UpdateActions(); }
        }
        LoadPermissionMode();
        // A turn still running (or stopped by a restart) shows up again.
        await AttachLiveAsync();
        if (generation != _loadGeneration) return;
        FocusComposerSoon(force: false);
        if (PendingStart is not { } start || start.SessionId != _id) return;
        PendingStart = null;
        if (!_sessionLoaded) return;
        _prompt.Text = start.Prompt;
        foreach (var mention in start.Mentions) AddMention(mention, animate: false);
        if (!System.IO.Directory.Exists(_workspace.Text.Trim()))
        {
            SetComposerStatus("Choose the project folder (folder button), then press Send.");
            return;
        }
        await SendHereAsync();
    }

    private async Task RevalidateAsync()
    {
        if (_sendInFlight || _jumpInProgress) return;
        if (_id.Length == 0) return;
        await LoadAsync(++_loadGeneration, quiet: true);
    }

    private sealed record TurnWalkResult(
        NxResponse Response,
        List<string> Cursors,
        List<(NxResponse Response, int Page)> Pages,
        bool Complete);

    // Opening a chat used to wait for every panel AND a forward walk over
    // every page of the transcript (one sequential request per 80 turns)
    // before showing a single message. Now only the session record and the
    // newest turns (fetched backward) gate the first paint; everything else
    // fills in afterwards without blocking.
    private const int LatestPageSize = 40;
    private string _olderCursor = "";
    private bool _backwardPaging;
    private bool _olderLoading;
    private int _latestPagesShown;
    private string _latestSignature = "";
    private NxResponse? _lastOps;
    private NxResponse? _lastForks;
    private JsonElement? _summaryResult;
    private DateTime _loadedAt = DateTime.MinValue;
    private long _paintedAt;

    // Recently opened chats, kept so a chat whose page left PageCache still
    // paints at once; a quiet refresh follows and re-renders only on change.
    private sealed record SessionSnapshot(NxResponse Session, NxResponse Latest, NxResponse Continuations, NxResponse Ops, NxResponse Forks);
    private const int MaxSnapshots = 8;
    private static readonly Dictionary<string, SessionSnapshot> s_snapshots = new(StringComparer.OrdinalIgnoreCase);
    private static readonly LinkedList<string> s_snapshotOrder = new();
    private static NxResponse? s_projects;

    private static void RememberSnapshot(string sessionId, SessionSnapshot snapshot)
    {
        s_snapshots[sessionId] = snapshot;
        s_snapshotOrder.Remove(sessionId);
        s_snapshotOrder.AddFirst(sessionId);
        while (s_snapshotOrder.Count > MaxSnapshots)
        {
            s_snapshots.Remove(s_snapshotOrder.Last!.Value);
            s_snapshotOrder.RemoveLast();
        }
    }

    private static readonly NxResponse NoResponse = new(false, null, null);

    private static string ResponseSignature(NxResponse? response)
    {
        if (response is null) return "-";
        var raw = response.Result is { } body ? body.GetRawText() : response.Error ?? "";
        return (response.Ok ? "1:" : "0:") + raw.Length + ":" + raw.GetHashCode();
    }

    private static string TranscriptSignature(NxResponse latest, NxResponse continuations, NxResponse? ops, NxResponse? forks) =>
        string.Join("|", ResponseSignature(latest), ResponseSignature(continuations), ResponseSignature(ops), ResponseSignature(forks));

    private static bool HasCommandOps(NxResponse ops) =>
        ops.Ok && NxJson.Items(ops.Result).Any(item => NxJson.Text(item, "command", "command_line", "cmd", "text").Length > 0);

    private static bool HasForks(NxResponse forks) => forks.Ok && NxJson.Items(forks.Result).Any();

    private async Task LoadAsync(int generation, bool quiet = false)
    {
        var sessionId = _id;
        if (sessionId.Length == 0)
        {
            _header.Text = "Session";
            return;
        }
        var watch = System.Diagnostics.Stopwatch.StartNew();
        var refreshTranscript = !quiet || !_viewingHistoricalPage;
        // Critical path: the session record and the newest page of turns.
        var sessionTask = Call(NxMethods.SessionsGet, new { session_id = sessionId });
        var latestTask = refreshTranscript
            ? CallLatestTurnsAsync(sessionId, "", LatestPageSize)
            : Task.FromResult(NoResponse);
        var continuationsTask = Call(NxMethods.ContinueLocalList, new { session_id = sessionId });
        // Command output and sub-agent chips enrich the transcript; they get a
        // short grace period, then the transcript paints without them.
        var opsTask = Call(NxMethods.SessionsOperations, new { session_id = sessionId });
        var forksTask = Call(NxMethods.SessionsForks, new { session_id = sessionId });
        // Secondary panels start now, in parallel, and fill in after first paint.
        var filesTask = Call(NxMethods.SessionsFiles, new { session_id = sessionId });
        var memoriesTask = Call(NxMethods.SessionsMemories, new { session_id = sessionId });
        var summaryTask = Call(NxMethods.SessionsSummary, new { session_id = sessionId });
        var versionsTask = Call(NxMethods.SessionsVersions, new { session_id = sessionId });
        var projectsFetch = Call(NxMethods.ProjectsList);
        var projectsTask = s_projects is { Ok: true } knownProjects ? Task.FromResult(knownProjects) : projectsFetch;
        await Task.WhenAll(sessionTask, latestTask, continuationsTask, projectsTask);
        var enrich = Task.WhenAll(opsTask, forksTask);
        if (!enrich.IsCompleted) await Task.WhenAny(enrich, Task.Delay(250));
        if (generation != _loadGeneration || _id != sessionId || _sendInFlight || _jumpInProgress) return;
        var session = await sessionTask;
        var latest = await latestTask;
        var continuations = await continuationsTask;
        var projects = await projectsTask;
        if (projects.Ok) s_projects = projects;
        var enriched = enrich.IsCompleted;
        NxResponse? ops = enriched ? await opsTask : null;
        NxResponse? forks = enriched ? await forksTask : null;
        PaintSession(sessionId, session, latest, continuations, projects.Ok ? projects.Result : null, ops, forks, quiet, refreshTranscript);
        _loadedAt = DateTime.UtcNow;
        System.Diagnostics.Trace.WriteLine($"[perf] session {sessionId}: open-to-first-message {watch.ElapsedMilliseconds} ms (quiet={quiet}, enriched={enriched})");
        _ = FinishLoadAsync(generation, sessionId, quiet, refreshTranscript, watch, session, latest, continuations, enriched,
            opsTask, forksTask, filesTask, memoriesTask, summaryTask, versionsTask, projectsFetch);
    }

    // Paints the header and the newest turns from responses already in hand
    // (network or snapshot). A quiet repaint of an unchanged transcript is
    // skipped: what is on screen is already right.
    private void PaintSession(string sessionId, NxResponse session, NxResponse latest, NxResponse continuations,
        JsonElement? projects, NxResponse? ops, NxResponse? forks, bool quiet, bool refreshTranscript)
    {
        _savedContinuationResponse = continuations;
        _projectsCatalog = projects;
        ApplySessionHeader(sessionId, session, projects, quiet);
        if (!refreshTranscript) return;
        if (quiet && !latest.Ok)
        {
            _notice.Text = latest.Error ?? "Could not refresh this conversation.";
            return;
        }
        ops ??= _lastOps;
        forks ??= _lastForks;
        var signature = TranscriptSignature(latest, continuations, ops, forks);
        if (quiet && _backwardPaging && signature == _latestSignature) return;
        _latestSignature = signature;
        PaintTranscript(latest, continuations, ops, forks, quiet);
    }

    private void PaintTranscript(NxResponse latest, NxResponse continuations, NxResponse? ops, NxResponse? forks, bool quiet)
    {
        var priorOffset = _chatScroll.VerticalOffset;
        // The stick flag, not the live offset: right after a paint the
        // asynchronous ChangeView has often not landed yet (offset still 0),
        // and a quiet re-render that measured "near bottom" from it restored
        // the chat to the top.
        var stick = _stickToBottom;
        // Replacing the children clamps the offset; that is not the user.
        MarkProgrammaticScroll(500);
        _localAgentByTurnIndex.Clear();
        if (continuations.Ok)
        {
            foreach (var saved in NxJson.Items(continuations.Result))
            {
                var idx = NxJson.Text(saved, "answer_idx");
                var agent = NxJson.Text(saved, "agent");
                if (idx.Length > 0 && agent.Length > 0) _localAgentByTurnIndex[idx] = agent;
            }
        }
        CacheChildSessions(forks ?? NoResponse);
        CacheCommandOps(ops ?? NoResponse);
        _continuationContext = BuildContinuationContext(latest.Result, _summaryResult);
        _backwardPaging = true;
        _latestPagesShown = 1;
        _olderCursor = latest.Ok && latest.Result is { } page ? NxJson.Text(page, "next_cursor") : "";
        _viewingHistoricalPage = false;
        _latest.Visibility = Visibility.Collapsed;
        if (latest.Ok) IndexTurnPage(latest, -1, keepExisting: true);
        try
        {
            FillChat(latest, initial: true);
            RenderSavedContinuations(continuations, latest.Ok);
            UpdateEarlierTurnsButton();
            // A find opened from search jumps to its first match; scrolling to
            // the end afterwards would undo that jump.
            var findJump = _pendingFind.Length > 0;
            RefreshConversationNavigation();
            if (findJump && _findMatches.Count > 0) SetStickToBottom(false, "find jump pending");
            else if (!quiet || stick) ScrollChatToEnd();
            else RestoreScrollOffset(priorOffset);
            System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: paint transcript quiet={quiet} stick={stick} findJump={findJump && _findMatches.Count > 0} priorOffset={priorOffset:0} -> {(findJump && _findMatches.Count > 0 ? "find" : !quiet || stick ? "bottom" : "restore")}");
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Error("page", "chat render failed", ex);
            _chat.Children.Clear();
            _chat.Children.Add(Line(NxErrors.ForException("Nexus couldn't show this conversation. Reopen it to try again.", ex, "chat")));
        }
        _paintedAt = Environment.TickCount64;
        _ = LoadTranscriptImagesAsync();
        _ = RefreshSubagentCountAsync();
        // A sub-agent chat with no meta description is named from its first
        // request, which exists only now.
        if (_subagentTitlePending.Length > 0 && _subagentTitlePending == _id) _ = NameSubagentChatAsync(_id, _nativeID);
    }

    private static bool LooksLocal(string path)
    {
        if (path.Length == 0 || path.StartsWith("//", StringComparison.Ordinal) || path.StartsWith(@"\\", StringComparison.Ordinal)) return false;
        try { return System.IO.Path.IsPathRooted(path) && System.IO.Directory.Exists(path); }
        catch { return false; }
    }

    private void ApplySessionHeader(string sessionId, NxResponse session, JsonElement? projects, bool quiet)
    {
        _meta.Children.Clear();
        _meta.Visibility = Visibility.Collapsed;
        if (session.Ok && session.Result is { } body)
        {
            _agent = NxJson.Text(body, "agent", "harness");
            _nativeID = NxJson.Text(body, "native_id");
            _workspaceHint = NxJson.Text(body, "workspace_root_hint");
            // The chat's folder on the computer it was captured on: shown
            // only when it exists here. Another machine's path means nothing
            // on this one; the project's local folder is found instead
            // (ProjectFolders, after load).
            var localHint = LooksLocal(_workspaceHint) ? _workspaceHint : "";
            if (!quiet || _workspace.Text.Trim().Length == 0) _workspace.Text = localHint;
            var workspace = NxJson.Text(body, "workspace_root_hint", "workspace_root", "workspace", "cwd");
            _projectID = ProjectScope.SessionProjectId(
                NxJson.Text(body, "project_id"),
                workspace,
                projects);
            var friendly = ChatTitles.Title(NxJson.Text(body, "title"), NxJson.Text(body, "summary", "preview"), _agent);
            // The session record may carry only a placeholder; the sidebar
            // already derived a readable title from the chat's preview.
            if (friendly.EndsWith(" chat", StringComparison.Ordinal) && MainWindow.Current?.ChatTitleFor(sessionId) is { Length: > 0 } listed)
                friendly = listed;
            // A sub-agent chat is hidden from the sidebar and captured with no
            // title: name it from its description or first request instead of
            // "Claude Code chat".
            var nameSubagent = false;
            if (friendly.EndsWith(" chat", StringComparison.Ordinal) && SessionLineage.IsSubagentSession(body))
            {
                if (s_subagentTitles.TryGetValue(sessionId, out var named)) friendly = named;
                else
                {
                    friendly = SessionLineage.SubagentTitle("", "", "");
                    nameSubagent = true;
                }
            }
            _header.Text = friendly;
            // Named after the placeholder is on screen (the lookup may finish
            // at once), and again once the transcript has painted.
            _subagentTitlePending = nameSubagent ? sessionId : "";
            if (nameSubagent) _ = NameSubagentChatAsync(sessionId, _nativeID);
            var forked = SessionLineage.IsForkSession(body);
            _lineageMarker.Visibility = forked ? Visibility.Visible : Visibility.Collapsed;
            if (forked) ToolTipService.SetToolTip(_lineageMarker, "Forked from " + NxJson.Text(body, "parent_session_id"));
            var chips = Row();
            if (_agent.Length > 0) chips.Children.Add(Chip(ChatTitles.AgentName(_agent)));
            var projectLabel = ProjectLabels.ForSession(_projectID, workspace, projects);
            _crumbProject.Text = projectLabel;
            _crumbProject.Visibility = _crumbSlash.Visibility = projectLabel.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
            if (projectLabel.Length > 0)
            {
                var projectChip = Chip(projectLabel);
                if (workspace.Length > 0) ToolTipService.SetToolTip(projectChip, workspace);
                chips.Children.Add(projectChip);
            }
            var shellProjectId = ShellProjectId();
            if (shellProjectId.Length > 0 && _projectID.Length > 0 &&
                !string.Equals(_projectID, shellProjectId, StringComparison.OrdinalIgnoreCase))
            {
                var shellLabel = ProjectLabels.ForSession(shellProjectId, "", projects);
                var mismatch = Chip("Shell: " + (shellLabel.Length > 0 ? shellLabel : shellProjectId));
                mismatch.Opacity = 0.75;
                ToolTipService.SetToolTip(mismatch, "This session belongs to a different project than the shell selection.");
                chips.Children.Add(mismatch);
            }
            var last = Humanize(NxJson.Text(body, "last_active_at"));
            if (last.Length > 0) chips.Children.Add(Muted("Last activity " + last));
            if (chips.Children.Count > 0)
            {
                _meta.Children.Add(chips);
                _meta.Visibility = Visibility.Visible;
            }
            ToolTipService.SetToolTip(_header, "Session ID: " + sessionId);
            _legacy = NxJson.Text(body, "lineage_kind") == "legacy_snapshot";
            _sessionLoaded = true;
            if (_harness.Items.Count == 0) ConfigureHarness();
            UpdateActions();
            if (_legacy)
            {
                _notice.Text = "Archived snapshot · read-only";
            }
        }
        else if (!session.Ok)
        {
            _sessionLoaded = false;
            _header.Text = "Session unavailable";
            _notice.Text = session.Error ?? "Could not load this session.";
            UpdateActions();
        }
    }

    // The local meta file (Claude Code: agent-<hex>.meta.json) has the
    // description the parent gave the run; the first request is the fallback,
    // read once the transcript has painted.
    // The chat is looked up by its native id, agent-<hex>, which names both
    // the transcript and the meta file. A title from the description or the
    // first request is final and cached; otherwise the chat stays pending and
    // the next paint (when the first request is on screen) tries again.
    private async Task NameSubagentChatAsync(string sessionId, string nativeId)
    {
        SubagentRuns.LocalRun? local = null;
        try { if (nativeId.Length > 0) local = await SubagentRuns.LocalRunAsync(nativeId); }
        catch { local = null; }
        await UiThread.Resume(DispatcherQueue);
        if (_id != sessionId || _subagentTitlePending != sessionId) return;
        var first = _conversation.Prompts.FirstOrDefault()?.Text ?? "";
        var title = SessionLineage.SubagentTitle(local?.Description ?? "", first, local?.AgentType ?? "");
        if (local?.Description.Length > 0 || first.Length > 0)
        {
            // Kept for quiet refreshes and reopening the same chat (UI thread only).
            s_subagentTitles[sessionId] = title;
            _subagentTitlePending = "";
        }
        _header.Text = title;
    }

    private static readonly Dictionary<string, string> s_subagentTitles = new(StringComparer.OrdinalIgnoreCase);
    private string _subagentTitlePending = "";

    // Everything after first paint: enrichment that missed the grace period,
    // the Activity and Memory panels, version state, and the full-history
    // index used by find and the prompt rail.
    private async Task FinishLoadAsync(int generation, string sessionId, bool quiet, bool refreshTranscript,
        System.Diagnostics.Stopwatch watch, NxResponse session, NxResponse latest, NxResponse continuations, bool enriched,
        Task<NxResponse> opsTask, Task<NxResponse> forksTask, Task<NxResponse> filesTask, Task<NxResponse> memoriesTask,
        Task<NxResponse> summaryTask, Task<NxResponse> versionsTask, Task<NxResponse> projectsFetch)
    {
        bool Stale() => generation != _loadGeneration || _id != sessionId;
        try
        {
            var ops = await opsTask;
            var forks = await forksTask;
            if (Stale()) return;
            _lastOps = ops;
            _lastForks = forks;
            if (!enriched && refreshTranscript && _backwardPaging && _latestPagesShown == 1 && !_viewingHistoricalPage &&
                !_sendInFlight && !_jumpInProgress && latest.Ok && (HasCommandOps(ops) || HasForks(forks)))
            {
                var signature = TranscriptSignature(latest, continuations, ops, forks);
                if (signature != _latestSignature)
                {
                    _latestSignature = signature;
                    PaintTranscript(latest, continuations, ops, forks, quiet: true);
                }
            }
            if (refreshTranscript && session.Ok && latest.Ok)
                RememberSnapshot(sessionId, new SessionSnapshot(session, latest, continuations, ops, forks));

            await Task.WhenAll(filesTask, memoriesTask, summaryTask, versionsTask);
            if (Stale()) return;
            var summary = await summaryTask;
            var versions = await versionsTask;
            FillActivity(await filesTask, ops, continuations);
            FillMemoryTab(await memoriesTask, summary);
            if (session.Ok)
            {
                _hasTransferableVersion = versions.Ok && NxJson.Items(versions.Result).Any(v =>
                {
                    var state = NxJson.Text(v, "state");
                    return state.Equals("complete", StringComparison.OrdinalIgnoreCase) ||
                        state.Equals("transcript_only", StringComparison.OrdinalIgnoreCase);
                });
                UpdateActions();
            }
            _summaryResult = summary.Ok ? summary.Result : null;
            if (refreshTranscript && !_sendInFlight && _summaryResult is { } summaryBody &&
                NxJson.Text(summaryBody, "summary", "text") is { Length: > 0 } overview &&
                !_continuationContext.StartsWith("Summary: ", StringComparison.Ordinal))
            {
                _continuationContext = "Summary: " + overview + "\n\n" + _continuationContext;
                if (_continuationContext.Length > 6000) _continuationContext = _continuationContext[^6000..];
            }
            System.Diagnostics.Trace.WriteLine($"[perf] session {sessionId}: panels ready {watch.ElapsedMilliseconds} ms");

            var projects = await projectsFetch;
            if (projects.Ok) s_projects = projects;
            // The full-history walk feeds find and the prompt rail only; it no
            // longer blocks the transcript, and a quiet refresh skips it once
            // the history is indexed.
            if (Stale() || (quiet && _historyIndexed)) return;
            var walk = await WalkTurnPagesAsync(sessionId, generation);
            if (walk is null || Stale()) return;
            ApplyWalkIndex(walk);
            System.Diagnostics.Trace.WriteLine($"[perf] session {sessionId}: history indexed ({walk.Pages.Count} pages) {watch.ElapsedMilliseconds} ms");
        }
        catch (Exception ex)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("page", "chat history background load failed", ex);
        }
    }

    private async Task<TurnWalkResult?> WalkTurnPagesAsync(string sessionId, int generation)
    {
        var cursors = new List<string> { "" };
        var pages = new List<(NxResponse Response, int Page)>();
        var cursor = "";
        NxResponse? lastOk = null;
        var complete = false;
        for (var page = 0; page < MaxTurnWalkPages; page++)
        {
            var response = await CallTurnPageAsync(sessionId, cursor);
            if (generation != _loadGeneration || _id != sessionId) return null;
            if (!response.Ok)
            {
                return new TurnWalkResult(response, cursors, pages, Complete: false);
            }
            lastOk = response;
            pages.Add((response, page));
            var next = response.Result is { } body ? NxJson.Text(body, "next_cursor") : "";
            if (next.Length == 0)
            {
                complete = true;
                break;
            }
            if (page + 1 == MaxTurnWalkPages) break;
            cursors.Add(next);
            cursor = next;
        }
        return new TurnWalkResult(
            lastOk ?? new NxResponse(false, "Could not load turns", null),
            cursors,
            pages,
            complete);
    }

    private void ApplyWalkIndex(TurnWalkResult walk)
    {
        _turnPageStartCursors.Clear();
        _turnPageStartCursors.AddRange(walk.Cursors);
        if (!_backwardPaging) _earlierPageIndex = walk.Pages.Count == 0 ? 0 : walk.Pages[^1].Page;
        // Upserts: entries already on screen keep their anchors and gain the
        // forward page they live on, so a later jump can reopen it.
        foreach (var (response, page) in walk.Pages) IndexTurnPage(response, page);
        _historyIndexed = walk.Complete;
        _indexingHistory = false;
        RefreshConversationNavigation();
    }

    // Newest turns first: direction=backward returns the last `limit` turns
    // before an exclusive idx cursor ("" = the end of the chat), in idx order.
    // A server without backward paging answers with the chat's FIRST turns;
    // then the pages are cut from a forward walk instead (TurnPaging).
    private Task<NxResponse> CallLatestTurnsAsync(string sessionId, string beforeCursor, int limit)
    {
        // Read on the UI thread, before any await: the sidebar's turn count.
        var count = MainWindow.Current?.ChatTurnCountFor(sessionId) ?? -1;
        return s_backwardIgnored ? LatestTurnsWithoutBackwardAsync(sessionId, beforeCursor, limit, count) : CallBackwardTurnsAsync(sessionId, beforeCursor, limit, count);
    }

    // A server that ignores direction=backward (production today) forced a
    // walk over the whole chat before its newest page could show: 2,284 turns
    // took 12 sequential 200-turn requests (16.4 s measured on a real chat).
    // Turn indexes there are sparse but increasing (2108, 281496, …,
    // 57592794), so the tail is found by probing cursors in parallel rounds
    // (TurnPaging.TailProbes) while the walk runs; the first answer wins and
    // the walk keeps going in the background for older pages.
    private async Task<NxResponse> LatestTurnsWithoutBackwardAsync(string sessionId, string beforeCursor, int limit, long count)
    {
        if (beforeCursor.Length > 0 || HasForwardWalk(sessionId)) return await LatestTurnsFromForwardWalkAsync(sessionId, beforeCursor, limit);
        if (count >= 0 && count <= ForwardWalkPageSize * 2) return await LatestTurnsFromForwardWalkAsync(sessionId, "", limit);
        var walk = LatestTurnsFromForwardWalkAsync(sessionId, "", limit);
        var probe = ProbeTailAsync(sessionId, limit);
        if (await Task.WhenAny((Task)walk, probe) == probe && await probe is { } tail)
        {
            System.Diagnostics.Trace.WriteLine($"[paging] session {sessionId}: newest page by cursor probes");
            return tail;
        }
        if (!walk.IsCompleted && await probe is { } late) return late;
        return await walk;
    }

    private static bool HasForwardWalk(string sessionId)
    {
        lock (s_forwardTurns) return s_forwardTurns.ContainsKey(sessionId);
    }

    private const int TailProbeRounds = 5;
    private const int TailProbesPerRound = 8;

    // Finds a cursor after which fewer than a full page (but at least `limit`
    // turns) remain. Each round probes several cursors at once: before the end
    // is bracketed they grow geometrically, then they split the bracket.
    private async Task<NxResponse?> ProbeTailAsync(string sessionId, int limit)
    {
        var first = await Call(NxMethods.SessionsTurns, new { session_id = sessionId, id = sessionId, limit = ForwardWalkPageSize });
        if (!first.Ok || first.Result is not { } firstBody) return null;
        var firstItems = NxJson.Items(firstBody).ToList();
        if (firstItems.Count == 0 || NxJson.Text(firstBody, "next_cursor").Length == 0) return null; // short chat: the walk is one page
        long lo = TurnIdx(firstItems[^1]);
        long? hi = null;
        for (var round = 0; round < TailProbeRounds; round++)
        {
            var cursors = TurnPaging.TailProbes(lo, hi, TailProbesPerRound);
            if (cursors.Count == 0) return null;
            var pending = cursors.Select(c => (Cursor: c, Task: Call(NxMethods.SessionsTurns, new
            {
                session_id = sessionId, id = sessionId, limit = ForwardWalkPageSize,
                cursor = c.ToString(System.Globalization.CultureInfo.InvariantCulture)
            }))).ToList();
            // Answers are used as they arrive: the tail can be found before a
            // slow probe (an empty page can take seconds) comes back.
            while (pending.Count > 0)
            {
                var done = await Task.WhenAny(pending.Select(p => p.Task));
                var index = pending.FindIndex(p => p.Task == done);
                var cursor = pending[index].Cursor;
                pending.RemoveAt(index);
                var answer = await done;
                var items = answer.Ok && answer.Result is { } body ? NxJson.Items(body).Select(t => t.Clone()).ToList() : new List<JsonElement>();
                var more = answer.Ok && answer.Result is { } page && NxJson.Text(page, "next_cursor").Length > 0;
                if (answer.Ok && !more && items.Count >= limit)
                {
                    // The end of the chat, with at least a page before it.
                    items.Sort((a, b) => TurnIdx(a).CompareTo(TurnIdx(b)));
                    var (slice, _) = TurnPaging.Before(items, TurnIdx, null, limit);
                    return PageResponse(slice, TurnIdx(slice[0]).ToString(System.Globalization.CultureInfo.InvariantCulture));
                }
                if (more) lo = Math.Max(lo, TurnIdx(items[^1]));
                else hi = hi is { } h ? Math.Min(h, cursor) : cursor; // too close to the end, or past it (or rejected)
            }
        }
        return null;
    }

    private static NxResponse PageResponse(List<JsonElement> slice, string nextCursor)
    {
        using var stream = new MemoryStream();
        using (var writer = new Utf8JsonWriter(stream))
        {
            writer.WriteStartObject();
            writer.WriteStartArray("items");
            foreach (var turn in slice) turn.WriteTo(writer);
            writer.WriteEndArray();
            writer.WriteString("next_cursor", nextCursor);
            writer.WriteEndObject();
        }
        using var doc = JsonDocument.Parse(stream.ToArray());
        return new NxResponse(true, null, doc.RootElement.Clone());
    }

    private static bool s_backwardIgnored;

    private async Task<NxResponse> CallBackwardTurnsAsync(string sessionId, string beforeCursor, int limit, long count)
    {
        var response = await Call(NxMethods.SessionsTurns, beforeCursor.Length > 0
            ? new { session_id = sessionId, id = sessionId, limit, cursor = beforeCursor, direction = "backward" }
            : (object)new { session_id = sessionId, id = sessionId, limit, direction = "backward" });
        if (!response.Ok || response.Result is not { } body) return response;
        var idxs = new List<long>();
        foreach (var turn in NxJson.Items(body))
        {
            if (!long.TryParse(NxJson.Text(turn, "idx"), out var idx)) return response;
            idxs.Add(idx);
        }
        long? before = long.TryParse(beforeCursor, out var cursor) ? cursor : null;
        if (!TurnPaging.LooksForward(idxs, NxJson.Text(body, "next_cursor"), before)) return response;
        s_backwardIgnored = true;
        System.Diagnostics.Trace.WriteLine($"[paging] session {sessionId}: server ignored direction=backward; paging from a forward walk");
        return await LatestTurnsWithoutBackwardAsync(sessionId, beforeCursor, limit, count);
    }

    // Forward-walked turns per chat (idx order). Older pages reuse the walk;
    // the newest page re-reads its last `limit` turns and anything after.
    private sealed record ForwardTurns(List<JsonElement> Turns);
    private static readonly Dictionary<string, ForwardTurns> s_forwardTurns = new(StringComparer.OrdinalIgnoreCase);
    private static readonly LinkedList<string> s_forwardOrder = new();
    private static readonly SemaphoreSlim s_forwardGate = new(1, 1);
    private const int ForwardWalkPageSize = 200;
    private const int MaxForwardWalkPages = 250;
    private const int MaxForwardWalks = 2;

    private static long TurnIdx(JsonElement turn) => long.TryParse(NxJson.Text(turn, "idx"), out var idx) ? idx : 0;

    private async Task<NxResponse> LatestTurnsFromForwardWalkAsync(string sessionId, string beforeCursor, int limit)
    {
        await s_forwardGate.WaitAsync();
        try
        {
            ForwardTurns? known;
            lock (s_forwardTurns) s_forwardTurns.TryGetValue(sessionId, out known);
            if (known is null || beforeCursor.Length == 0)
            {
                var turns = known?.Turns ?? new List<JsonElement>();
                // The newest turns may have been rewritten (tool results land
                // later): drop them from the cache and read them again.
                var keep = Math.Max(0, turns.Count - Math.Max(limit, 1));
                turns = turns.Take(keep).ToList();
                var cursor = keep > 0 ? NxJson.Text(turns[^1], "idx") : "";
                for (var page = 0; page < MaxForwardWalkPages; page++)
                {
                    var response = await Call(NxMethods.SessionsTurns, cursor.Length > 0
                        ? new { session_id = sessionId, id = sessionId, limit = ForwardWalkPageSize, cursor }
                        : (object)new { session_id = sessionId, id = sessionId, limit = ForwardWalkPageSize });
                    if (!response.Ok || response.Result is not { } body) return response;
                    var items = NxJson.Items(body).Select(t => t.Clone()).ToList();
                    turns.AddRange(items);
                    var next = NxJson.Text(body, "next_cursor");
                    if (items.Count == 0 || next.Length == 0) break;
                    cursor = next;
                }
                known = new ForwardTurns(turns);
                lock (s_forwardTurns) s_forwardTurns[sessionId] = known;
                s_forwardOrder.Remove(sessionId);
                s_forwardOrder.AddFirst(sessionId);
                while (s_forwardOrder.Count > MaxForwardWalks)
                {
                    lock (s_forwardTurns) s_forwardTurns.Remove(s_forwardOrder.Last!.Value);
                    s_forwardOrder.RemoveLast();
                }
            }
            long? before = long.TryParse(beforeCursor, out var exclusive) ? exclusive : null;
            var (slice, nextCursor) = TurnPaging.Before(known.Turns, TurnIdx, before, limit);
            return PageResponse(slice, nextCursor);
        }
        finally { s_forwardGate.Release(); }
    }

    private bool HasEarlierTurns => _backwardPaging ? _olderCursor.Length > 0 : _earlierPageIndex > 0;

    private async Task LoadOlderTurnsAsync()
    {
        if (_olderLoading || _olderCursor.Length == 0) return;
        _olderLoading = true;
        var generation = _loadGeneration;
        var session = _id;
        _chatEarlier.IsEnabled = false;
        try
        {
            var response = await CallLatestTurnsAsync(session, _olderCursor, TurnPageSize);
            if (generation != _loadGeneration || _id != session || !_backwardPaging) return;
            if (!response.Ok)
            {
                _chat.Children.Insert(ChatContentInsertIndex(), Line(response.Error ?? "Could not load earlier turns."));
                return;
            }
            // Measured after the request, right before the prepend: content
            // that grew or scrolled while it was in flight must not count.
            var priorOffset = _chatScroll.VerticalOffset;
            var priorExtent = _chatScroll.ExtentHeight;
            try
            {
                PrependChat(response);
                IndexTurnPage(response, -1, keepExisting: true);
                _olderCursor = response.Result is { } body ? NxJson.Text(body, "next_cursor") : "";
                _latestPagesShown++;
            }
            catch (Exception ex)
            {
                Nexus.Core.Diagnostics.DiagLog.Error("page", "earlier turns render failed", ex);
                _chat.Children.Insert(ChatContentInsertIndex(), Line(NxErrors.ForException("Nexus couldn't show the earlier messages. Scroll up again to retry.", ex, "chat")));
            }
            if (_olderCursor.Length == 0) DiscardUnshownCommands();
            UpdateEarlierTurnsButton();
            _promptNavigator.SetPrompts(_conversation.Prompts);
            PreserveScrollAfterPrepend(priorOffset, priorExtent);
        }
        finally
        {
            _chatEarlier.IsEnabled = true;
            _olderLoading = false;
        }
    }

    private Task<NxResponse> CallTurnPageAsync(string sessionId, string cursor) =>
        Call(NxMethods.SessionsTurns, cursor.Length > 0
            ? new { session_id = sessionId, id = sessionId, limit = TurnPageSize, cursor }
            : new { session_id = sessionId, id = sessionId, limit = TurnPageSize });

    private async Task LoadEarlierTurnsAsync()
    {
        if (_backwardPaging) { await LoadOlderTurnsAsync(); return; }
        if (_earlierPageIndex <= 0) return;
        var generation = _loadGeneration;
        _chatEarlier.IsEnabled = false;
        _earlierPageIndex--;
        var startCursor = _turnPageStartCursors[_earlierPageIndex];
        var response = await CallTurnPageAsync(_id, startCursor);
        if (generation != _loadGeneration)
        {
            _chatEarlier.IsEnabled = true;
            return;
        }
        var priorOffset = _chatScroll.VerticalOffset;
        var priorExtent = _chatScroll.ExtentHeight;
        if (!response.Ok)
        {
            _earlierPageIndex++;
            _chat.Children.Insert(ChatContentInsertIndex(), Line(response.Error ?? "Could not load earlier turns."));
        }
        else
        {
            try
            {
                PrependChat(response);
            }
            catch (Exception ex)
            {
                Nexus.Core.Diagnostics.DiagLog.Error("page", "earlier turns render failed", ex);
                _earlierPageIndex++;
                _chat.Children.Insert(ChatContentInsertIndex(), Line(NxErrors.ForException("Nexus couldn't show the earlier messages. Scroll up again to retry.", ex, "chat")));
            }
        }
        UpdateEarlierTurnsButton();
        _chatEarlier.IsEnabled = true;
        if (_earlierPageIndex == 0)
        {
            DiscardUnshownCommands();
        }
        PreserveScrollAfterPrepend(priorOffset, priorExtent);
    }

    private void UpdateEarlierTurnsButton()
    {
        if (HasEarlierTurns)
        {
            if (!_chat.Children.Contains(_chatEarlier))
            {
                _chat.Children.Insert(ChildBlockCount(), _chatEarlier);
            }
        }
        else
        {
            _chat.Children.Remove(_chatEarlier);
        }
    }

    private int ChildBlockCount() => 0;

    private int ChatContentInsertIndex() => (_chat.Children.Contains(_chatEarlier) ? 1 : 0);

    private void PreserveScrollAfterPrepend(double priorOffset, double priorExtent)
    {
        // Older turns go above what is on screen: shift the offset by the
        // height they added. Applied at once (no frame showing the jump) and
        // again after layout settles; the target is the same both times
        // unless the prepended content grew in between.
        void Apply(string pass)
        {
            try
            {
                if (_stickToBottom) return;
                _chatScroll.UpdateLayout();
                var delta = _chatScroll.ExtentHeight - priorExtent;
                if (delta > 0)
                {
                    MarkProgrammaticScroll();
                    _chatScroll.ChangeView(null, priorOffset + delta, null, disableAnimation: true);
                }
                System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: keep position after prepend ({pass}) offset {priorOffset:0} + {delta:0}");
            }
            catch
            {
                // ignore layout races
            }
        }
        // Loading older turns means the user is reading above the bottom.
        SetStickToBottom(_chatScroll.ScrollableHeight <= 0, "older turns loaded");
        Apply("now");
        if (DispatcherQueue != null)
        {
            _ = DispatcherQueue.TryEnqueue(() => _ = DispatcherQueue.TryEnqueue(() => Apply("settled")));
        }
    }

    private void CacheChildSessions(NxResponse forks)
    {
        _childSessions.Clear();
        _childById.Clear();
        if (!forks.Ok)
        {
            return;
        }
        foreach (var item in NxJson.Items(forks.Result))
        {
            var id = NxJson.Text(item, "id", "session_id", "sessionId");
            if (id.Length == 0 || id == _id)
            {
                continue;
            }
            var title = NxJson.Text(item, "title", "summary", "native_id");
            var harness = NxJson.Text(item, "harness", "agent");
            var kind = NxJson.Text(item, "lineage_kind", "lineageKind");
            var kindLabel = kind.Length > 0 ? kind : SessionLineage.IsSubagentSession(item) ? "sub-agent" : "fork";
            var meta = string.Join(" · ", new[] { harness, kindLabel, Humanize(NxJson.Text(item, "last_active_at")) }.Where(p => p.Length > 0));
            var entry = new ChildSessionEntry(id, title.Length > 0 ? title : id, meta);
            _childSessions.Add(entry);
            _childById[id] = entry;
        }
    }

    private void CacheCommandOps(NxResponse ops)
    {
        _pendingCommands.Clear();
        _turnSteps.Clear();
        _shownCommandKeys.Clear();
        _chatCommandsShown = 0;
        if (!ops.Ok)
        {
            return;
        }
        foreach (var item in NxJson.Items(ops.Result))
        {
            if (NxJson.Text(item, "command", "command_line", "cmd", "text").Length > 0)
            {
                _pendingCommands.Add(item.Clone());
            }
        }
    }

    private object? RailArgs(string cursor)
    {
        var projectId = ShellProjectId();
        if (projectId.Length == 0 || !ProjectScope.LooksLikeUuid(projectId))
        {
            return null;
        }
        return new { project_id = projectId, cursor, limit = 50 };
    }

    private async Task LoadRailAsync()
    {
        if (_railLoading) return;
        var railArgs = RailArgs("");
        if (railArgs is null)
        {
            _railEntries.Clear();
            _railList.Children.Clear();
            _railList.Children.Add(Muted("Choose a project in the shell to list chats here."));
            _railMore.Visibility = Visibility.Collapsed;
            return;
        }
        _railLoading = true;
        _railEntries.Clear();
        _railCursor = "";
        _railList.Children.Clear();
        _railList.Children.Add(Muted("Loading chats…"));
        var response = await Call(NxMethods.TimelineList, railArgs);
        _railLoading = false;
        _railList.Children.Clear();
        if (!response.Ok)
        {
            _railList.Children.Add(Muted(response.Error ?? "Could not load chats."));
            return;
        }
        _railCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        AppendRail(response);
        RenderRail();
    }

    private async Task LoadMoreRailAsync()
    {
        if (_railCursor.Length == 0 || _railLoading) return;
        var railArgs = RailArgs(_railCursor);
        if (railArgs is null) return;
        _railLoading = true;
        _railMore.IsEnabled = false;
        var response = await Call(NxMethods.TimelineList, railArgs);
        _railLoading = false;
        _railMore.IsEnabled = true;
        if (!response.Ok)
        {
            _notice.Text = response.Error ?? "Could not load more chats.";
            return;
        }
        _railCursor = response.Result is { } result ? NxJson.Text(result, "next_cursor") : "";
        AppendRail(response);
        RenderRail();
    }

    private void AppendRail(NxResponse response)
    {
        var projectId = ShellProjectId();
        if (projectId.Length == 0 || !ProjectScope.LooksLikeUuid(projectId))
        {
            return;
        }
        foreach (var item in NxJson.Items(response.Result))
        {
            if (!ProjectScope.TimelineItemMatches(item, projectId, _projectsCatalog))
            {
                continue;
            }
            if (SessionLineage.IsSubagentSession(item))
            {
                continue;
            }
            var id = NxJson.Text(item, "id", "session_id", "sessionId");
            if (id.Length == 0) continue;
            var title = NxJson.Text(item, "title", "summary", "preview", "kind");
            var agent = NxJson.Text(item, "agent", "harness");
            var meta = string.Join(" · ", new[]
            {
                agent,
                Humanize(NxJson.Text(item, "last_active_at", "updated_at", "captured_at"))
            }.Where(part => part.Length > 0));
            _railEntries.Add(new RailEntry(id, title.Length > 0 ? title : id, meta));
        }
    }

    private void RenderRail()
    {
        var query = _railSearch.Text.Trim();
        _railList.Children.Clear();
        var shown = 0;
        foreach (var entry in _railEntries)
        {
            if (query.Length > 0 &&
                entry.Title.IndexOf(query, StringComparison.OrdinalIgnoreCase) < 0 &&
                entry.Meta.IndexOf(query, StringComparison.OrdinalIgnoreCase) < 0) continue;
            _railList.Children.Add(RailItem(entry));
            shown++;
        }
        if (shown == 0 && _railEntries.Count > 0)
        {
            _railList.Children.Add(Muted("No chats match \u201c" + query + "\u201d"));
        }
        if (shown == 0 && _railEntries.Count == 0)
        {
            _railList.Children.Add(Muted("No captured chats in this project yet."));
        }
        _railMore.Visibility = _railCursor.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private Button RailItem(RailEntry entry)
    {
        var current = entry.Id == _id;
        var stack = new StackPanel { Spacing = 2 };
        stack.Children.Add(new TextBlock
        {
            Text = NxJson.Scrub(entry.Title),
            TextWrapping = TextWrapping.NoWrap,
            TextTrimming = TextTrimming.CharacterEllipsis,
            FontSize = 13.5,
            FontWeight = current ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal
        });
        if (entry.Meta.Length > 0)
        {
            stack.Children.Add(new TextBlock
            {
                Text = NxJson.Scrub(entry.Meta),
                TextWrapping = TextWrapping.NoWrap,
                TextTrimming = TextTrimming.CharacterEllipsis,
                FontSize = 11,
                Opacity = 0.6
            });
        }
        var item = new Button
        {
            Content = stack,
            HorizontalAlignment = HorizontalAlignment.Stretch,
            HorizontalContentAlignment = HorizontalAlignment.Left,
            Padding = new Thickness(10, 8, 10, 8),
            CornerRadius = new CornerRadius(8),
            BorderThickness = new Thickness(1),
            Background = current
                ? new SolidColorBrush(Windows.UI.Color.FromArgb(40, 255, 255, 255))
                : new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            BorderBrush = current
                ? new SolidColorBrush(Windows.UI.Color.FromArgb(70, 255, 255, 255))
                : new SolidColorBrush(Microsoft.UI.Colors.Transparent)
        };
        ToolTipService.SetToolTip(item, entry.Title);
        item.Click += (_, _) => { if (!current) MainWindow.Current?.OpenSession(entry.Id); };
        return item;
    }

    private static Brush Surface(string key, Windows.UI.Color fallback)
    {
        return ThemeService.Brush(key, fallback);
    }

    private static string Humanize(string iso)
    {
        if (!DateTimeOffset.TryParse(iso, out var when)) return "";
        var local = when.ToLocalTime();
        var now = DateTimeOffset.Now;
        var span = now - local;
        if (span.TotalMinutes < 1) return "just now";
        if (span.TotalMinutes < 60) return Nexus.Core.FriendlyTime.Relative(when);
        if (span.TotalHours < 24 && local.Date == now.Date) return local.ToString("HH:mm");
        if (local.Year == now.Year) return local.ToString("MMM d");
        return local.ToString("MMM d, yyyy");
    }

    private void FillChat(NxResponse response, bool initial)
    {
        if (initial)
        {
            ClearNavigationHighlight();
            _messageAnchors.Clear();
            _chat.Children.Clear();
            _turnSteps.Clear();
            _inlineSubagentIds.Clear();
            _subagentCards.Clear();
            _shownEnvelopes.Clear();
            _turnDedupe.Clear();
        }
        if (!response.Ok)
        {
            AddChatElement(Line(response.Error ?? "Could not load this conversation."));
            return;
        }
        var shown = 0;
        try
        {
            foreach (var item in NxJson.Items(response.Result))
            {
                if (RenderTurn(item))
                {
                    shown++;
                }
            }
            FlushTurnActivity();
        }
        finally
        {
            _chatRenderSink = null;
        }
        if (initial)
        {
            AppendOrphanSubagentChips();
        }
        if (shown == 0 && initial && _childSessions.Count == 0 && _chatCommandsShown == 0)
        {
            var empty = Muted("No messages yet. Write below to start.");
            empty.Tag = EmptyChatTag;
            AddChatElement(empty);
        }
        PlaceTranscriptImages();
    }

    private void RenderSavedContinuations(NxResponse response, bool cloudTurnsAvailable, bool appendContext = true)
    {
        // Restore and review cards follow the painted transcript (SessionPage.Review.cs).
        QueueCheckpointRefresh();
        if (!response.Ok)
        {
            SetComposerStatus(response.Error is { Length: > 0 } why && !why.StartsWith(NxErrors.GenericFailure, StringComparison.Ordinal) ? why : "Nexus couldn't load the replies it saved for this chat. Reopen the chat to try again.");
            return;
        }
        var saved = NxJson.Items(response.Result).ToList();
        var latestNative = saved.LastOrDefault(item => NxJson.Text(item, "native_id").Length > 0);
        if (latestNative.ValueKind == JsonValueKind.Object)
        {
            _localNativeID = NxJson.Text(latestNative, "native_id");
            _localNativeAgent = NxJson.Text(latestNative, "agent");
        }
        var items = saved
            .Where(item => !cloudTurnsAvailable || !item.TryGetProperty("cloud_saved", out var synced) || synced.ValueKind != JsonValueKind.True ||
                item.TryGetProperty("activities", out var tools) && tools.ValueKind == JsonValueKind.Array && tools.GetArrayLength() > 0 ||
                item.TryGetProperty("file_diffs", out var diffs) && diffs.ValueKind == JsonValueKind.Array && diffs.GetArrayLength() > 0)
            .ToList();
        if (items.Count == 0) return;
        var headerShown = false;
        void Append(UIElement element)
        {
            if (!headerShown)
            {
                headerShown = true;
                AddChatElement(new TextBlock
                {
                    Text = "CONTINUED IN NEXUS · SAVED ON THIS DEVICE",
                    FontSize = 10.5,
                    FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
                    Opacity = 0.48,
                    Margin = new Thickness(0, 14, 0, 8)
                });
            }
            AddChatElement(element);
        }
        foreach (var item in items)
        {
            var prompt = NxJson.Text(item, "prompt");
            var answer = NxJson.Text(item, "answer");
            var time = NxJson.Text(item, "at");
            var showMessages = !cloudTurnsAvailable || !item.TryGetProperty("cloud_saved", out var synced) || synced.ValueKind != JsonValueKind.True;
            foreach (var (role, text, field) in new[] { ("user", prompt, "user_idx"), ("assistant", answer, "answer_idx") })
            {
                if (!showMessages || text.Length == 0) continue;
                var idx = NxJson.Text(item, field);
                var key = idx.Length > 0 && idx != "0" ? TurnKey(idx, role) : "local:" + NxJson.Text(item, "id") + ":" + role;
                var order = long.TryParse(idx, out var number) && number > 0 ? number : _conversation.NextOrder;
                _conversation.Add(new ConversationEntry(key, role, text, time, -1, order));
                Append(IndexedChatMessage(role, text, key, NxJson.Text(item, "agent")));
            }
            // A reply already shown from the cloud gets its steps and file
            // changes under it, not in a pile at the end of the chat. When that
            // reply is not on screen (an older page), they wait for it.
            var place = showMessages ? Append : PlaceAfterReply(NxJson.Text(item, "answer_idx"));
            var changed = StringArray(item, "file_changes");
            if (item.TryGetProperty("activities", out var savedTools) && savedTools.ValueKind == JsonValueKind.Array)
            {
                var activity = new LiveActivityView();
                foreach (var tool in savedTools.EnumerateArray().Take(100)) activity.Apply(tool.GetRawText());
                if (activity.Count > 0) { activity.View.IsExpanded = false; place?.Invoke(activity.View); }
            }
            // A cloud-saved reply already lists these files: Nexus uploaded them
            // as workspace_change tool calls, rendered as its file edits.
            if (changed.Count > 0 && showMessages && FileChangesExpander(changed, item) is { } filesList) place?.Invoke(filesList);
            if (appendContext && showMessages) _continuationContext += "\n\nuser: " + prompt + "\n\nassistant: " + answer;
        }
        if (_continuationContext.Length > 6000) _continuationContext = _continuationContext[^6000..];
    }

    // Returns an inserter that puts elements right after the rendered reply
    // with this turn index (each after the previous one), or null when that
    // reply is not rendered.
    private Action<UIElement>? PlaceAfterReply(string answerIdx)
    {
        if (TurnIndex.FindReplyKey(_messageAnchors.Keys, answerIdx) is not { } key || !_messageAnchors.TryGetValue(key, out var anchor)) return null;
        UIElement after = anchor;
        return element =>
        {
            if (_chatRenderSink is { } sink && sink.IndexOf(after) is var pending and >= 0)
                sink.Insert(pending + 1, element);
            else if (_chat.Children.IndexOf(after) is var shown and >= 0)
                _chat.Children.Insert(shown + 1, element);
            else
                return;
            after = element;
        };
    }

    private void AppendOrphanSubagentChips()
    {
        var orphans = _childSessions.Where(c => !_inlineSubagentIds.Contains(c.Id)).ToList();
        if (orphans.Count == 0)
        {
            return;
        }
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6, Margin = new Thickness(0, 4, 0, 8) };
        foreach (var child in orphans)
        {
            row.Children.Add(InlineSubagentChip(child));
        }
        AddChatElement(row);
    }

    private void PrependChat(NxResponse response)
    {
        if (!response.Ok)
        {
            _chat.Children.Insert(ChatContentInsertIndex(), Line(response.Error ?? "Could not load earlier turns."));
            return;
        }
        var batch = new List<UIElement>();
        _chatRenderSink = batch;
        try
        {
            foreach (var item in NxJson.Items(response.Result))
            {
                RenderTurn(item);
            }
            FlushTurnActivity();
        }
        finally
        {
            _chatRenderSink = null;
        }
        var insertAt = ChatContentInsertIndex();
        foreach (var element in batch)
        {
            _chat.Children.Insert(insertAt++, element);
        }
        PlaceTranscriptImages();
    }

    private const string EmptyChatTag = "empty-chat";

    private void AddChatElement(UIElement element)
    {
        // The empty-state note goes away as soon as the conversation starts.
        if (element is not TextBlock { Tag: EmptyChatTag })
            foreach (var stale in _chat.Children.OfType<TextBlock>().Where(t => Equals(t.Tag, EmptyChatTag)).ToList())
                _chat.Children.Remove(stale);
        if (_chatRenderSink is { } sink)
        {
            sink.Add(element);
            return;
        }
        _chat.Children.Add(element);
    }

    private bool RenderTurn(JsonElement item)
    {
        var role = NxJson.Text(item, "role");
        if (role.Equals("system", StringComparison.OrdinalIgnoreCase) ||
            role.Equals("developer", StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }
        if (IsThinkingRole(role))
        {
            var thinking = ReadableTurn(NxJson.Text(item, "text_preview", "text", "content", "message"));
            if (thinking.Length > 0)
            {
                FlushTurnActivity();
                AddChatElement(ThinkingBlock(thinking));
                return true;
            }
            return false;
        }
        var toolCalls = ParseToolCalls(item);
        if (role.Equals("tool", StringComparison.OrdinalIgnoreCase) ||
            role.Equals("tool_result", StringComparison.OrdinalIgnoreCase) ||
            role.Equals("function", StringComparison.OrdinalIgnoreCase))
        {
            var result = ReadableTurn(NxJson.Text(item, "text_preview", "text", "content", "message"));
            if (result.Length == 0 && toolCalls.Count > 0)
            {
                result = toolCalls[0].Result;
            }
            if (result.Length == 0)
            {
                return false;
            }
            AttachToolResult(result, toolCalls.Count > 0 ? toolCalls[0].Name : "tool");
            return true;
        }
        // Codex-imported tool blocks and sub-agent / task envelopes (SessionPage.Streaming.cs).
        if (RenderHarnessTurn(item, role, toolCalls) is bool harness) return harness;
        var fromUser = role.Equals("user", StringComparison.OrdinalIgnoreCase);
        var rawText = NxJson.Text(item, "text_preview", "text", "content", "message");
        var parsed = fromUser ? ReadableUserMessage(rawText) : null;
        var text = parsed?.Text ?? ReadableTurn(rawText);
        var extras = parsed is { } p && (p.Attachments.Count > 0 || p.Context.Count > 0) ? p : null;
        var rendered = false;
        if (text.Length > 0 || extras?.Attachments.Count > 0)
        {
            FlushTurnActivity();
            var assistantAgent = role.Equals("assistant", StringComparison.OrdinalIgnoreCase) &&
                _localAgentByTurnIndex.TryGetValue(NxJson.Text(item, "idx"), out var localAgent)
                ? localAgent : null;
            // Harnesses sometimes record one prompt twice (once wrapped in an
            // envelope); once the envelope is stripped they are identical.
            var signature = TranscriptDedupe.Signature(role, text + (extras is null ? "" : "\n" + string.Join("\n", extras.Attachments.Select(a => a.Path))));
            if (AdmitTurn(item, signature, out var superseded))
            {
                var element = IndexedChatMessage(role, text, TurnKey(item), assistantAgent, extras);
                AddChatElement(element);
                ShownTurn(item, signature, element, superseded);
            }
            rendered = true;
        }
        // Every call joins the turn's work log (SessionPage.Streaming.cs); the
        // log is placed when the next prose arrives, so results attach first.
        foreach (var call in toolCalls)
        {
            AddCapturedStep(call);
            rendered = true;
        }
        return rendered;
    }

    private ChildSessionEntry? TryResolveSubagentFromTool(ParsedToolCall call)
    {
        if (!IsSubagentSpawnTool(call.Name) && !call.ArgsSummary.Contains("subagent", StringComparison.OrdinalIgnoreCase))
        {
            return null;
        }
        var sessionId = ExtractSessionIdFromArgs(call.ArgsSummary);
        if (sessionId.Length > 0 && _childById.TryGetValue(sessionId, out var known))
        {
            return known;
        }
        foreach (var child in _childSessions)
        {
            if (sessionId.Length > 0 && string.Equals(child.Id, sessionId, StringComparison.OrdinalIgnoreCase))
            {
                return child;
            }
            if (call.ArgsSummary.Contains(child.Id, StringComparison.OrdinalIgnoreCase))
            {
                return child;
            }
            if (child.Title.Length > 0 && call.ArgsSummary.Contains(child.Title, StringComparison.OrdinalIgnoreCase))
            {
                return child;
            }
        }
        if (IsSubagentSpawnTool(call.Name))
        {
            var label = SubagentLabelFromCall(call);
            return new ChildSessionEntry(sessionId.Length > 0 ? sessionId : "", label, call.Name);
        }
        return null;
    }

    private static bool IsSubagentSpawnTool(string name) =>
        name.Equals("Task", StringComparison.OrdinalIgnoreCase) ||
        name.Contains("subagent", StringComparison.OrdinalIgnoreCase) ||
        name.Contains("sub_agent", StringComparison.OrdinalIgnoreCase);

    private static string ExtractSessionIdFromArgs(string args)
    {
        foreach (var key in new[] { "session_id", "sessionId", "child_session_id", "fork_session_id" })
        {
            var match = Regex.Match(args, $"\"{key}\"\\s*:\\s*\"([^\"]+)\"", RegexOptions.IgnoreCase);
            if (match.Success)
            {
                return match.Groups[1].Value;
            }
        }
        return "";
    }

    private static string SubagentLabelFromCall(ParsedToolCall call)
    {
        foreach (var key in new[] { "description", "subagent_type", "agent", "name" })
        {
            var match = Regex.Match(call.ArgsSummary, $"\"{key}\"\\s*:\\s*\"([^\"]+)\"", RegexOptions.IgnoreCase);
            if (match.Success)
            {
                return match.Groups[1].Value;
            }
        }
        return call.ArgsSummary.Length > 0 ? Truncate(call.ArgsSummary.Replace("\n", " "), 72) : "Sub-agent run";
    }

    private Button InlineSubagentChip(ChildSessionEntry child, ParsedToolCall? call = null)
    {
        var title = NxJson.Scrub(child.Title);
        if (title.Length > 52) title = title[..49] + "…";
        var subtitle = child.Meta.Length > 0
            ? child.Meta
            : (call?.ArgsSummary is { Length: > 0 } args ? Truncate(args.Replace("\n", " "), 88) : "");
        var content = new StackPanel { Spacing = 2 };
        content.Children.Add(new TextBlock
        {
            Text = title,
            FontSize = 12,
            FontWeight = Microsoft.UI.Text.FontWeights.SemiBold,
            TextTrimming = TextTrimming.CharacterEllipsis,
            MaxWidth = 360
        });
        if (subtitle.Length > 0)
        {
            content.Children.Add(new TextBlock
            {
                Text = NxJson.Scrub(subtitle),
                FontSize = 11,
                Opacity = 0.72,
                TextTrimming = TextTrimming.CharacterEllipsis,
                MaxWidth = 360
            });
        }
        var chip = new Button
        {
            Content = content,
            HorizontalAlignment = HorizontalAlignment.Left,
            HorizontalContentAlignment = HorizontalAlignment.Left,
            Padding = new Thickness(12, 8, 14, 8),
            CornerRadius = new CornerRadius(10),
            BorderThickness = new Thickness(1),
            Background = SessionChatRender.SubtleFill,
            BorderBrush = SessionChatRender.SubtleBorder,
            MaxWidth = 400
        };
        ToolTipService.SetToolTip(chip, "View sub-agent details");
        chip.Click += async (_, _) => await ShowSubagentDialogAsync(child);
        return chip;
    }

    private async Task ShowSubagentDialogAsync(ChildSessionEntry child)
    {
        if (XamlRoot is null)
        {
            return;
        }
        var body = new StackPanel { Spacing = 10, MinWidth = 320, MaxWidth = 460 };
        body.Children.Add(Line(child.Title, title: true));
        if (child.Meta.Length > 0)
        {
            body.Children.Add(Muted(child.Meta));
        }
        if (child.Id.Length > 0)
        {
            var preview = await CallLatestTurnsAsync(child.Id, "", 8);
            if (preview.Ok)
            {
                var turns = new StackPanel { Spacing = 6 };
                turns.Children.Add(SectionHeader("Recent turns"));
                var count = 0;
                foreach (var turn in NxJson.Items(preview.Result))
                {
                    var role = NxJson.Text(turn, "role");
                    var text = ReadableTurn(NxJson.Text(turn, "text_preview", "text", "content", "message"), role);
                    if (text.Length == 0)
                    {
                        continue;
                    }
                    turns.Children.Add(Muted(role + ": " + Truncate(text.Replace("\n", " "), 140)));
                    if (++count >= 5)
                    {
                        break;
                    }
                }
                if (count == 0)
                {
                    turns.Children.Add(Muted("No messages yet."));
                }
                body.Children.Add(turns);
            }
        }
        else
        {
            body.Children.Add(Muted("This sub-agent's chat hasn't synced yet. Try again in a moment."));
        }
        var scroll = new ScrollViewer { MaxHeight = 360, Content = body };
        var dialog = new ContentDialog
        {
            Title = "Sub-agent",
            Content = scroll,
            PrimaryButtonText = child.Id.Length > 0 ? "Open session" : "",
            CloseButtonText = "Close",
            DefaultButton = ContentDialogButton.Primary,
            XamlRoot = XamlRoot
        };
        if (child.Id.Length == 0)
        {
            dialog.PrimaryButtonText = "";
        }
        var result = await dialog.ShowThemedAsync();
        if (result == ContentDialogResult.Primary && child.Id.Length > 0)
        {
            MainWindow.Current?.OpenSession(child.Id);
        }
    }

    private static UIElement ThinkingBlock(string content) => new ThinkingView(content).View;

    private static string CommandKey(string command) =>
        Regex.Replace(command.Trim(), "\\s+", " ");

    private static bool IsThinkingRole(string role) =>
        role.Equals("thinking", StringComparison.OrdinalIgnoreCase) ||
        role.Equals("reasoning", StringComparison.OrdinalIgnoreCase) ||
        role.Equals("reasoner", StringComparison.OrdinalIgnoreCase);

    private JsonElement? TakeMatchingCommandOp(string commandText, ParsedToolCall call)
    {
        var needle = commandText.Length > 0 ? commandText : ExtractCommandFromTool(call);
        for (var i = 0; i < _pendingCommands.Count; i++)
        {
            var item = _pendingCommands[i];
            var line = NxJson.Text(item, "command", "command_line", "cmd", "text");
            if (line.Length == 0)
            {
                continue;
            }
            if (needle.Length > 0 && !line.Contains(needle, StringComparison.Ordinal) &&
                !needle.Contains(line, StringComparison.Ordinal))
            {
                continue;
            }
            _pendingCommands.RemoveAt(i);
            return item;
        }
        return null;
    }

    private void DiscardUnshownCommands()
    {
        foreach (var item in _pendingCommands)
        {
            var line = NxJson.Text(item, "command", "command_line", "cmd", "text");
            _shownCommandKeys.Add(CommandKey(line));
        }
        _pendingCommands.Clear();
    }

    private static List<ParsedToolCall> ParseToolCalls(JsonElement item)
    {
        var calls = new List<ParsedToolCall>();
        if (item.ValueKind != JsonValueKind.Object)
        {
            return calls;
        }
        if (!item.TryGetProperty("tool_calls", out var raw) && !item.TryGetProperty("toolCalls", out raw))
        {
            return calls;
        }
        if (raw.ValueKind == JsonValueKind.String)
        {
            var text = raw.GetString() ?? "";
            if (text.StartsWith("[", StringComparison.Ordinal))
            {
                try
                {
                    using var doc = JsonDocument.Parse(text);
                    raw = doc.RootElement.Clone();
                }
                catch (JsonException)
                {
                    return calls;
                }
            }
            else
            {
                return calls;
            }
        }
        if (raw.ValueKind != JsonValueKind.Array)
        {
            return calls;
        }
        foreach (var call in raw.EnumerateArray())
        {
            if (call.ValueKind != JsonValueKind.Object)
            {
                continue;
            }
            var name = NxJson.Text(call, "name", "tool_name");
            if (name.Length == 0 && call.TryGetProperty("function", out var fn) && fn.ValueKind == JsonValueKind.Object)
            {
                name = NxJson.Text(fn, "name");
            }
            if (name.Length == 0)
            {
                name = "tool";
            }
            var args = SummarizeToolArgs(call);
            if (TranscriptText.IsOpaque(args)) continue;
            var result = NxJson.Text(call, "result", "output", "content");
            calls.Add(new ParsedToolCall(name, args, result));
        }
        return calls;
    }

    private static string SummarizeToolArgs(JsonElement call)
    {
        foreach (var key in new[] { "args", "input", "arguments" })
        {
            if (!call.TryGetProperty(key, out var value))
            {
                continue;
            }
            if (value.ValueKind == JsonValueKind.String)
            {
                var text = value.GetString() ?? "";
                if (text.StartsWith("{", StringComparison.Ordinal))
                {
                    try
                    {
                        using var doc = JsonDocument.Parse(text);
                        return doc.RootElement.GetRawText();
                    }
                    catch (JsonException)
                    {
                        return text;
                    }
                }
                return text;
            }
            if (value.ValueKind == JsonValueKind.Object || value.ValueKind == JsonValueKind.Array)
            {
                return value.GetRawText();
            }
        }
        if (call.TryGetProperty("function", out var fn) && fn.TryGetProperty("arguments", out var args))
        {
            if (args.ValueKind == JsonValueKind.String)
            {
                return args.GetString() ?? "";
            }
            return args.GetRawText();
        }
        return "";
    }

    private static string CompactJson(JsonElement value, int max)
    {
        var text = value.ValueKind == JsonValueKind.String ? value.GetString() ?? "" : value.GetRawText();
        text = Regex.Replace(text, "\\s+", " ").Trim();
        return Truncate(text, max);
    }

    private static string ToolCallPath(ParsedToolCall call)
    {
        foreach (var key in new[] { "path", "file_path", "target_file", "relative_workspace_path", "filePath" })
        {
            var match = Regex.Match(call.ArgsSummary, $"\"{key}\"\\s*:\\s*\"([^\"]+)\"", RegexOptions.IgnoreCase);
            if (match.Success)
            {
                return match.Groups[1].Value;
            }
        }
        return "";
    }

    private static string ExtractCommandFromTool(ParsedToolCall call)
    {
        var decoded = TranscriptText.Argument(call.ArgsSummary, "command", "cmd", "command_line");
        if (decoded.Length > 0) return decoded;
        foreach (var key in new[] { "command", "cmd", "command_line" })
        {
            var match = Regex.Match(call.ArgsSummary, $"\"{key}\"\\s*:\\s*\"([^\"]+)\"", RegexOptions.IgnoreCase);
            if (match.Success)
            {
                return match.Groups[1].Value;
            }
        }
        return "";
    }

    private static string ExtractDiffSnippet(string argsSummary)
    {
        foreach (var key in new[] { "new_string", "replace", "patch", "diff", "content", "code_edit" })
        {
            var match = Regex.Match(argsSummary, $"\"{key}\"\\s*:\\s*\"((?:\\\\.|[^\"])*)\"", RegexOptions.IgnoreCase);
            if (match.Success)
            {
                return Truncate(match.Groups[1].Value.Replace("\\n", "\n").Replace("\\t", "\t"), 1200);
            }
        }
        return "";
    }

    private static bool IsFileEditTool(string name)
    {
        if (name.Equals("TodoWrite", StringComparison.OrdinalIgnoreCase) ||
            name.Equals("TodoRead", StringComparison.OrdinalIgnoreCase))
        {
            return false;
        }
        return name.Equals("workspace_change", StringComparison.OrdinalIgnoreCase) ||
               name.Contains("edit", StringComparison.OrdinalIgnoreCase) ||
               name.Contains("write", StringComparison.OrdinalIgnoreCase) ||
               name.Contains("patch", StringComparison.OrdinalIgnoreCase) ||
               name.Contains("file", StringComparison.OrdinalIgnoreCase) ||
               name.Equals("apply_patch", StringComparison.OrdinalIgnoreCase) ||
               name.Equals("search_replace", StringComparison.OrdinalIgnoreCase);
    }

    private static bool IsCommandTool(string name) =>
        name.Equals("exec", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("exec_command", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("bash", StringComparison.OrdinalIgnoreCase) ||
        name.Contains("shell", StringComparison.OrdinalIgnoreCase) ||
        name.Contains("terminal", StringComparison.OrdinalIgnoreCase) ||
        name.Contains("command", StringComparison.OrdinalIgnoreCase) ||
        name.Equals("run", StringComparison.OrdinalIgnoreCase);

    private static bool IsPathBrowseTool(string name, string argsSummary)
    {
        if (IsCommandTool(name) || IsFileEditTool(name))
        {
            return false;
        }
        if (name.Contains("grep", StringComparison.OrdinalIgnoreCase) ||
            name.Contains("glob", StringComparison.OrdinalIgnoreCase) ||
            name.Contains("read", StringComparison.OrdinalIgnoreCase) ||
            name.Contains("search", StringComparison.OrdinalIgnoreCase) ||
            name.Contains("list", StringComparison.OrdinalIgnoreCase) ||
            name.Contains("ripgrep", StringComparison.OrdinalIgnoreCase) ||
            name.Equals("codebase_search", StringComparison.OrdinalIgnoreCase))
        {
            return true;
        }
        return ToolCallPath(new ParsedToolCall(name, argsSummary, "")).Length > 0 &&
               !argsSummary.Contains("command", StringComparison.OrdinalIgnoreCase);
    }

    private void UpdateWorkspaceFlyoutVisibility()
    {
        var harness = SelectedHarness();
        _workspaceMenu.Visibility = harness.Length > 0 ? Visibility.Visible : Visibility.Collapsed;
    }

    private async Task<bool> ConfirmCursorWorkspaceAsync(string workspace)
    {
        var normalized = System.IO.Path.GetFullPath(workspace);
        if (_trustedCursorFolders.Contains(normalized)) return true;
        if (XamlRoot is null) return false;
        var dialog = new ContentDialog
        {
            Title = "Allow Cursor in this folder?",
            Content = new TextBlock
            {
                Text = "Cursor can read files and run commands in:\n" + normalized +
                       "\n\nOnly allow this for a project you trust. Nexus will remember your choice until the app closes.",
                TextWrapping = TextWrapping.Wrap,
                IsTextSelectionEnabled = true
            },
            PrimaryButtonText = "Trust and send",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Close,
            XamlRoot = XamlRoot
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary) return false;
        _trustedCursorFolders.Add(normalized);
        return true;
    }

    private static string Truncate(string value, int max)
    {
        if (value.Length <= max)
        {
            return value;
        }
        return value[..max] + "…";
    }

    // Pins the chat to its newest message. The flag keeps it there: content
    // that grows after this (late layout, images, a page attached to the
    // tree later) re-pins through SizeChanged until the user scrolls up.
    private void ScrollChatToEnd()
    {
        SetStickToBottom(true, "scroll to end");
        MarkProgrammaticScroll();
        void Scroll()
        {
            try
            {
                if (!_stickToBottom) return;
                _chatScroll.UpdateLayout();
                MarkProgrammaticScroll();
                _chatScroll.ChangeView(null, _chatScroll.ScrollableHeight, null, disableAnimation: true);
                System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: scroll to end -> {_chatScroll.ScrollableHeight:0} (attached={_chatScroll.XamlRoot is not null})");
            }
            catch
            {
                // ignore layout races during fast navigation
            }
        }
        if (DispatcherQueue == null)
        {
            Scroll();
            return;
        }
        _ = DispatcherQueue.TryEnqueue(() => _ = DispatcherQueue.TryEnqueue(Scroll));
    }

    private void RestoreScrollOffset(double offset)
    {
        void Scroll()
        {
            try
            {
                if (_stickToBottom) return;
                _chatScroll.UpdateLayout();
                MarkProgrammaticScroll();
                _chatScroll.ChangeView(null, Math.Min(offset, _chatScroll.ScrollableHeight), null, disableAnimation: true);
                System.Diagnostics.Trace.WriteLine($"[scroll] session {_id}: restore offset {offset:0} (scrollable {_chatScroll.ScrollableHeight:0})");
            }
            catch
            {
                // ignore layout races during quiet refresh
            }
        }
        if (DispatcherQueue == null)
        {
            Scroll();
            return;
        }
        _ = DispatcherQueue.TryEnqueue(() => _ = DispatcherQueue.TryEnqueue(Scroll));
    }

    // Thumbnails for images still on disk, a chip for anything else (Codex
    // pastes land in %TEMP% and are often gone), and a collapsed "Context"
    // pill for ambient state the harness sent with the request.
    private static FrameworkElement? MessageExtras(HarnessMessage extras)
    {
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        foreach (var attachment in extras.Attachments.Take(8))
        {
            var path = attachment.Path;
            var exists = path.Length > 0 && SafeFileExists(path);
            if (attachment.IsImage && exists)
            {
                // Click opens it full size.
                row.Children.Add(ChatImages.Thumbnail(path, attachment.Label));
                continue;
            }
            var label = attachment.Label + (path.Length > 0 && !exists ? " (no longer on disk)" : "");
            var chip = new Border
            {
                CornerRadius = new CornerRadius(10), Padding = new Thickness(8, 3, 10, 3),
                BorderThickness = new Thickness(1), BorderBrush = NexusPalette.AccentBorder,
                Opacity = exists ? 0.85 : 0.6,
                Child = new StackPanel
                {
                    Orientation = Orientation.Horizontal, Spacing = 6,
                    Children =
                    {
                        new FontIcon { Glyph = attachment.IsImage ? "" : "", FontSize = 11 },
                        new TextBlock { Text = label, FontSize = 11.5, VerticalAlignment = VerticalAlignment.Center, TextTrimming = TextTrimming.CharacterEllipsis, MaxWidth = 260 }
                    }
                }
            };
            if (path.Length > 0) ToolTipService.SetToolTip(chip, path);
            Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(chip, label);
            row.Children.Add(chip);
        }
        if (extras.Context.Count > 0)
        {
            var body = new TextBlock
            {
                Text = string.Join("\n\n", extras.Context), TextWrapping = TextWrapping.Wrap,
                IsTextSelectionEnabled = true, FontSize = 12, MaxWidth = 440, Opacity = 0.85
            };
            var pill = new Button
            {
                Content = new TextBlock { Text = "Context", FontSize = 11 },
                Padding = new Thickness(8, 2, 8, 2), MinHeight = 0, MinWidth = 0,
                CornerRadius = new CornerRadius(10), Opacity = 0.6,
                Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
                BorderBrush = NexusPalette.AccentBorder, BorderThickness = new Thickness(1),
                VerticalAlignment = VerticalAlignment.Center,
                Flyout = new Flyout { Content = new ScrollViewer { Content = body, MaxHeight = 320 } }
            };
            ToolTipService.SetToolTip(pill, "Context the app sent with this message (not part of the request)");
            row.Children.Add(pill);
        }
        return row.Children.Count > 0 ? row : null;
    }

    private static bool SafeFileExists(string path)
    {
        try { return System.IO.File.Exists(path); }
        catch (Exception) { return false; }
    }

    /// <summary>Elements in the chat column (memory log).</summary>
    internal int ChatElementCount => _chat.Children.Count;

    // Markdown → one selectable document (see MarkdownView.cs).
    internal static RichTextBlock FormattedMessage(string content) => MarkdownRender.Document(content);

    private void FillActivity(NxResponse files, NxResponse ops, NxResponse continuations)
    {
        _activity.Children.Clear();
        FillFiles(files, continuations);
        FillCommands(ops);
    }

    private void FillFiles(NxResponse response, NxResponse continuations)
    {
        var files = NxJson.Items(response.Result).ToList();
        var localPaths = new HashSet<string>(StringComparer.OrdinalIgnoreCase);
        foreach (var exchange in NxJson.Items(continuations.Result))
            foreach (var path in StringArray(exchange, "file_changes")) localPaths.Add(path);
        var cloudPaths = new HashSet<string>(files.Select(item => NxJson.Text(item, "path", "file_path", "name")).Where(path => path.Length > 0), StringComparer.OrdinalIgnoreCase);
        _activity.Children.Add(SectionHeader(response.Ok ? "Files · " + cloudPaths.Union(localPaths, StringComparer.OrdinalIgnoreCase).Count() : "Files"));
        if (!response.Ok)
        {
            _activity.Children.Add(Card(Line(response.Error ?? "Could not load this session's files.")));
        }
        if (files.Count == 0 && localPaths.Count == 0)
        {
            _activity.Children.Add(Card(Line("No files were captured for this session.")));
            return;
        }
        foreach (var item in files)
        {
            var path = NxJson.Text(item, "path", "file_path", "name");
            if (path.Length == 0) continue;
            var row = new StackPanel { Spacing = 6 };
            row.Children.Add(Monospace(path));
            var operations = StringArray(item, "operations");
            if (operations.Count > 0)
            {
                var chips = Row();
                foreach (var operation in operations) chips.Children.Add(Chip(operation));
                row.Children.Add(chips);
            }
            _activity.Children.Add(ListItem(row));
        }
        foreach (var path in localPaths.OrderBy(path => path, StringComparer.OrdinalIgnoreCase))
        {
            if (cloudPaths.Contains(path)) continue;
            var row = new StackPanel { Spacing = 6 };
            row.Children.Add(Monospace(path));
            row.Children.Add(Muted("Changed during an in-app reply on this device"));
            _activity.Children.Add(ListItem(row));
        }
    }

    private void FillCommands(NxResponse response)
    {
        var commands = NxJson.Items(response.Result).ToList();
        _activity.Children.Add(SectionHeader(response.Ok ? "Commands · " + commands.Count : "Commands"));
        if (!response.Ok)
        {
            _activity.Children.Add(Card(Line(response.Error ?? "Could not load this session's commands.")));
            return;
        }
        if (commands.Count == 0)
        {
            _activity.Children.Add(Card(Line("No commands were captured for this session.")));
            return;
        }
        foreach (var item in commands)
        {
            var command = NxJson.Text(item, "command", "command_line", "cmd", "text");
            if (command.Length == 0) continue;
            var card = new StackPanel { Spacing = 6 };
            card.Children.Add(Monospace(command));
            var meta = Row();
            var tool = NxJson.Text(item, "tool", "tool_name");
            if (tool.Length > 0) meta.Children.Add(Chip(tool));
            var directory = NxJson.Text(item, "directory");
            if (directory.Length > 0) meta.Children.Add(Muted(directory));
            if (meta.Children.Count > 0) card.Children.Add(meta);
            _activity.Children.Add(ListItem(card));
        }
    }

    private void FillMemoryTab(NxResponse memories, NxResponse summary)
    {
        _memoryTab.Children.Clear();
        _memoryTab.Children.Add(SectionHeader("Summary"));
        _memoryTab.Children.Add(OverviewCards(summary));
        var items = NxJson.Items(memories.Result).ToList();
        _memoryTab.Children.Add(SectionHeader(memories.Ok ? "Linked memories · " + items.Count : "Linked memories"));
        if (!memories.Ok)
        {
            _memoryTab.Children.Add(Card(Line(memories.Error ?? "Could not load session memory.")));
            return;
        }
        if (items.Count == 0)
        {
            _memoryTab.Children.Add(Card(Line("No memory is linked to this session yet. Project knowledge is on the Memory page.")));
            return;
        }
        foreach (var item in items)
        {
            var text = NxJson.Text(item, "content", "text", "title", "key");
            if (text.Length == 0) continue;
            var meta = Row();
            var scope = NxJson.Text(item, "scope", "level");
            if (scope.Length > 0) meta.Children.Add(Chip(scope));
            var status = NxJson.Text(item, "status");
            if (status.Length > 0) meta.Children.Add(Chip(NxJson.PublicStatus(status)));
            var key = NxJson.Text(item, "key");
            if (key.Length > 0) meta.Children.Add(Muted(key));
            _memoryTab.Children.Add(ListItem(Line(text), meta));
        }
    }

    private static UIElement OverviewCards(NxResponse response)
    {
        if (!response.Ok)
        {
            return Card(Line(response.Error ?? "Summary unavailable."));
        }
        var text = response.Result is { } body ? NxJson.Text(body, "summary", "text").Trim() : "";
        if (text.Length == 0)
        {
            return Card(Line("No summary has been captured for this session yet."));
        }
        var requestIndex = text.IndexOf("Request:", StringComparison.Ordinal);
        var responseIndex = text.IndexOf("Latest agent response:", StringComparison.Ordinal);
        if (requestIndex >= 0 && responseIndex > requestIndex)
        {
            var stack = new StackPanel { Spacing = 8 };
            var caption = text[..requestIndex].Trim();
            if (caption.Length > 0 && caption.StartsWith("Conversation overview", StringComparison.OrdinalIgnoreCase))
            {
                stack.Children.Add(Muted(caption));
            }
            var request = text[(requestIndex + "Request:".Length)..responseIndex].Trim();
            var latest = text[(responseIndex + "Latest agent response:".Length)..].Trim();
            if (request.Length > 0) stack.Children.Add(Card(Muted("Request"), FormattedMessage(request)));
            if (latest.Length > 0) stack.Children.Add(Card(Muted("Latest agent response"), FormattedMessage(latest)));
            return stack;
        }
        return Card(FormattedMessage(text));
    }

    private static List<string> StringArray(JsonElement item, string key)
    {
        var values = new List<string>();
        if (item.ValueKind == JsonValueKind.Object && item.TryGetProperty(key, out var array) && array.ValueKind == JsonValueKind.Array)
        {
            foreach (var element in array.EnumerateArray())
            {
                var text = element.ValueKind == JsonValueKind.String ? element.GetString() ?? "" : element.ToString();
                if (text.Length > 0) values.Add(text);
            }
        }
        return values;
    }

    // A user turn's text without harness wrappers (Codex file mentions,
    // ambient context, "My request:"); other roles are left as recorded.
    private static string ReadableTurn(string raw, string role) =>
        role.Equals("user", StringComparison.OrdinalIgnoreCase) ? ReadableUserMessage(raw).Text : ReadableTurn(raw);

    // A user turn the harness wrote (tool results, "Tool loaded.") has no
    // text to show; its tool calls still join the work log.
    private static HarnessMessage ReadableUserMessage(string raw)
    {
        if (HarnessTurn.IsHarnessOnly(raw)) return HarnessMessage.Parse("");
        var text = ReadableTurn(raw);
        return HarnessMessage.Parse(HarnessTurn.IsHarnessOnly(text) ? "" : text);
    }

    private static string ReadableTurn(string raw)
    {
        raw = raw.Trim();
        if (TranscriptText.IsOpaque(raw)) return "";
        if (raw.StartsWith("{", StringComparison.Ordinal))
        {
            try
            {
                using var doc = JsonDocument.Parse(raw);
                raw = ExtractText(doc.RootElement);
                if (raw.Length == 0) return "";
            }
            catch (JsonException) { }
        }
        var query = Regex.Match(raw, "<user_query>(.*?)</user_query>", RegexOptions.Singleline | RegexOptions.IgnoreCase);
        if (query.Success) raw = query.Groups[1].Value;
        raw = ChatTitles.StripEnvelopes(raw);
        if (raw.StartsWith("# AGENTS.md instructions", StringComparison.OrdinalIgnoreCase) ||
            raw.StartsWith("<environment_context>", StringComparison.OrdinalIgnoreCase) ||
            raw.StartsWith("<external_codex_apps_open_page>", StringComparison.OrdinalIgnoreCase) ||
            raw.StartsWith("<INSTRUCTIONS>", StringComparison.OrdinalIgnoreCase)) return "";
        return raw;
    }

    private void UpdateActions()
    {
        _fork.IsEnabled = _sessionLoaded && !_legacy && !_sendInFlight && !ShellState.Current.Offline;
        if (_forkButton is not null) _forkButton.IsEnabled = _fork.IsEnabled;
        _share.IsEnabled = _sessionLoaded && !_legacy && !ShellState.Current.Offline;
        _teleport.IsEnabled = _sessionLoaded && !_legacy && _hasTransferableVersion && _workspaceHint.Length > 0 && !ShellState.Current.Offline;
        _resume.IsEnabled = _sessionLoaded && !_legacy && _nativeID.Length > 0 && _agent.Length > 0 && _agent.Trim().ToLowerInvariant() is not "cursor";
        var selected = SelectedHarness();
        _sendHere.IsEnabled = _sessionLoaded && !_legacy && SupportsInAppReply(selected) && (!_sendInFlight || LiveTurnActive) && System.IO.Directory.Exists(_workspace.Text.Trim());
        _attachImage.IsEnabled = _sendHere.IsEnabled;
        _prompt.IsEnabled = _sessionLoaded && !_legacy;
        _prompt.IsReadOnly = false;
        // Offline: the reply could not be saved, so the composer waits
        // (the draft stays) and says why in one calm line.
        var offline = ShellState.Current.Offline;
        if (offline)
        {
            _sendHere.IsEnabled = false;
            _attachImage.IsEnabled = false;
            _prompt.IsEnabled = false;
            SetComposerStatus(ShellState.Current.SignedOut ? "Sign in to Nexus to send (Settings > Account). Your draft is kept." : "Sending is paused until Nexus reconnects. Your draft is kept.");
            _offlineNoticeShown = true;
        }
        else if (_offlineNoticeShown)
        {
            _offlineNoticeShown = false;
            SetComposerStatus("");
        }
        _harness.IsEnabled = !offline;
        _model.IsEnabled = !offline;
        ToolTipService.SetToolTip(_sendHere, _legacy ? "Archived snapshots are read-only" : !System.IO.Directory.Exists(_workspace.Text.Trim()) ? "Choose a local project folder first" : "Send to " + selected + " using this project's context");
        ToolTipService.SetToolTip(_resume, _legacy ? "Older saved chats can only be read" : _nativeID.Length == 0 ? "This chat can't be picked up in the agent itself, but you can continue it here" : "Copies a command that picks this chat up in the agent itself");
        ToolTipService.SetToolTip(_teleport, _legacy ? "Archived snapshots cannot be teleported" : !_hasTransferableVersion ? "A complete or transcript-only version is required" : _workspaceHint.Length == 0 ? "This chat has no mapped workspace to package code from" : null);
        ToolTipService.SetToolTip(_fork, _legacy ? "Archived snapshots cannot be forked" : "Clone the captured conversation into a separate chat. The next message starts a new local agent session.");
        UpdateComposerButton();
    }

    private async Task ForkChatAsync()
    {
        if (!_fork.IsEnabled) return;
        var parentID = _id;
        _fork.IsEnabled = false;
        var response = await Call(NxMethods.SessionsFork, new { session_id = parentID });
        if (_id != parentID) return;
        UpdateActions();
        if (!response.Ok || response.Result is not { } child)
        {
            _notice.Text = response.Error ?? "Could not fork this chat.";
            return;
        }
        var childID = NxJson.Text(child, "id", "session_id");
        if (childID.Length == 0)
        {
            _notice.Text = "The new chat was made, but Nexus couldn't open it. Find it at the top of the sidebar.";
            return;
        }
        MainWindow.Current?.OpenSession(childID);
        MainWindow.Current?.RefreshChats();
    }

    private static bool SupportsInAppReply(string agent) => agent is "claude" or "codex" or "cursor" or "opencode" or "gemini" or "kimi";

    private string SelectedHarness() => (_harness.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "";

    private string SelectedModel()
    {
        var selected = _model.SelectedItem as ComboBoxItem;
        return ModelPickerCatalog.ResolveValue(_model.Text, selected?.Tag as string, selected?.Content?.ToString());
    }

    private void ConfigureModel()
    {
        // Rebuilding the items raises SelectionChanged; that is not a user choice.
        _configuringModel = true;
        try
        {
            _model.Items.Clear();
            var catalog = _modelCatalogs.GetValueOrDefault(SelectedHarness());
            _model.Items.Add(new ComboBoxItem { Content = DefaultModelLabel(), Tag = "" });
            _refreshModels.Visibility = Visibility.Visible;
            _refreshModels.IsEnabled = !_loadingModelAgents.Contains(SelectedHarness()) && !_sendInFlight;
            _model.PlaceholderText = _loadingModelAgents.Contains(SelectedHarness()) && catalog is null ? "Loading models…" : DefaultModelLabel();
            foreach (var (header, choice) in catalog?.Rows() ?? Enumerable.Empty<(string, ModelPickerChoice?)>())
            {
                if (choice is null)
                {
                    // Provider group header: not a model, not selectable.
                    _model.Items.Add(new ComboBoxItem { Content = header, IsEnabled = false, FontSize = 11, FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, Padding = new Thickness(10, 8, 10, 2) });
                    continue;
                }
                var item = new ComboBoxItem { Content = choice.Label, Tag = choice.ID };
                ToolTipService.SetToolTip(item, choice.Description.Length > 0 ? choice.Description + "\n" + choice.ID : choice.ID);
                _model.Items.Add(item);
            }
            // Select the remembered model (Default included) so the list opens
            // at it; a typed ID the list doesn't have stays as text.
            var remembered = RememberedModel(SelectedHarness());
            var match = _model.Items.OfType<ComboBoxItem>().FirstOrDefault(i => i.IsEnabled && (i.Tag as string) == remembered);
            _model.SelectedIndex = match is null ? -1 : _model.Items.IndexOf(match);
            if (match is null) _model.Text = remembered;
        }
        finally { _configuringModel = false; }
        UpdateModelCatalogNotice();
    }

    private static readonly string[] HarnessNames = ["OpenCode", "Codex", "Cursor", "Claude Code", "Gemini", "Kimi"];
    private double _harnessFitWidth;

    // The agent picker's width for its longest name at the picker's font:
    // text + the 8/6 padding + the chevron column (32) + a little slack.
    private double HarnessFitWidth()
    {
        if (_harnessFitWidth > 0) return _harnessFitWidth;
        var widest = 0.0;
        foreach (var name in HarnessNames)
        {
            var probe = new TextBlock { Text = name, FontSize = _harness.FontSize > 0 ? _harness.FontSize : 12.5 };
            probe.Measure(new Windows.Foundation.Size(double.PositiveInfinity, double.PositiveInfinity));
            widest = Math.Max(widest, probe.DesiredSize.Width);
        }
        // Before the font is known (no visual tree yet) fall back to a width
        // that holds "Claude Code" at 12.5.
        return _harnessFitWidth = widest > 0 ? Math.Ceiling(widest + 8 + 6 + 32 + 6) : 132;
    }

    // Only agents that can run here are offered (installed, startable and,
    // where that is cheap to tell, signed in). The list refreshes in the
    // background when the window regains focus; Settings › Agents shows the
    // others with a "Set up" action.
    private void ConfigureHarness()
    {
        var source = AgentAvailability.Normalize(_agent);
        var keep = SelectedHarness();
        var choices = AgentAvailabilityService.PickerChoices();
        var shown = _harness.Items.OfType<ComboBoxItem>().Select(i => i.Tag as string ?? "").ToList();
        var pick = AgentAvailability.Choose(choices, keep.Length > 0 ? keep : source);
        if (!shown.SequenceEqual(choices.Select(c => c.Value)))
        {
            _harness.Items.Clear();
            foreach (var (value, name) in choices) _harness.Items.Add(new ComboBoxItem { Content = name, Tag = value });
        }
        var index = choices.ToList().FindIndex(c => c.Value == pick);
        if (_harness.SelectedIndex != index) _harness.SelectedIndex = index;
        _harness.PlaceholderText = choices.Count == 0 ? "No agent ready" : "Agent";
        ToolTipService.SetToolTip(_harness, choices.Count == 0 ? AgentAvailability.NoneReady : "Agent that replies in this chat, on this computer");
        if (keep.Length > 0 && keep == pick) return;
        if (choices.Count == 0) { SetComposerStatus(AgentAvailability.NoneReady); return; }
        if (source.Length > 0 && pick != source)
        {
            var from = AgentAvailability.DisplayName(source);
            var to = AgentAvailability.DisplayName(pick);
            SetComposerStatus(AgentAvailabilityService.For(source) is { Usable: false }
                ? $"{from} isn't ready on this computer, so {to} will continue this chat."
                : $"{from} can't continue chats in Nexus yet, so {to} will.");
        }
        else if (keep.Length == 0) SetComposerStatus("");
    }

    private void OnAgentsChanged() => DispatcherQueue.TryEnqueue(() =>
    {
        if (_sendInFlight || _preparingHarness || _harness.Items.Count == 0 && _id.Length == 0) return;
        ConfigureHarness();
    });

    private static string BuildContinuationContext(JsonElement? turns, JsonElement? summary)
    {
        var parts = new List<string>();
        if (summary is { } body)
        {
            var overview = NxJson.Text(body, "summary", "text");
            if (overview.Length > 0) parts.Add("Summary: " + overview);
        }
        if (turns is { } turnBody)
        {
            foreach (var item in NxJson.Items(turnBody).TakeLast(12))
            {
                var role = NxJson.Text(item, "role");
                if (role is not ("user" or "assistant")) continue;
                var text = ReadableTurn(NxJson.Text(item, "text_preview", "text", "content", "message"), role);
                if (text.Length > 0) parts.Add(role + ": " + text);
            }
        }
        var joined = string.Join("\n\n", parts);
        return joined.Length > 6000 ? joined[^6000..] : joined;
    }

    /// <summary>Send from the composer (SessionPage.Composer.cs): the bubble first, the checks after.</summary>
    private Task SendHereAsync() => SendFromComposerAsync();

    private async Task NameNewChatAsync(string sessionID, string title)
    {
        var result = await Call(NxMethods.SessionsCreate, new
        {
            project_id = _projectID, harness = _agent, native_id = _nativeID,
            title, visibility = "private", workspace_root_hint = _workspace.Text.Trim()
        });
        if (_id == sessionID && result.Ok) MainWindow.Current?.RefreshChats();
    }

    private async Task CancelHereAsync()
    {
        // Stopped during the checks: the message is kept, not sent.
        if (_preparingHarness) { NoteStopRequested(); return; }
        if (!_sendInFlight || _streamRequestID.Length == 0) return;
        _cancelHere.IsEnabled = false;
        NoteStopRequested();
        if (_liveTurn is not null)
        {
            // Live chats stop the current turn but keep the session open.
            await InterruptLiveAsync();
            return;
        }
        try
        {
            var response = await Call(NxMethods.ContinueCancel, new { request_id = _streamRequestID });
            if (!response.Ok)
            {
                SetComposerStatus(response.Error ?? "Could not stop this reply.");
                if (_sendInFlight) _cancelHere.IsEnabled = true;
            }
        }
        catch (Exception ex)
        {
            SetComposerStatus(NxErrors.ForException("Nexus couldn't stop this reply. Try again.", ex, "chat"));
            if (_sendInFlight) _cancelHere.IsEnabled = true;
        }
    }

    private async Task RefreshActivityAsync(string sessionID)
    {
        var files = await Call(NxMethods.SessionsFiles, new { session_id = sessionID });
        var operations = await Call(NxMethods.SessionsOperations, new { session_id = sessionID });
        var continuations = await Call(NxMethods.ContinueLocalList, new { session_id = sessionID });
        if (_id == sessionID) { _savedContinuationResponse = continuations; FillActivity(files, operations, continuations); }
    }

    /// The read-only "Files changed" list under a reply. Files with nothing
    /// added or removed (+0 −0: touched, or no diff could be told apart) are
    /// left out; null when no file is left.
    private static Expander? FileChangesExpander(IReadOnlyList<string> changes, JsonElement exchange = default)
    {
        var shown = 0;
        var paths = new StackPanel { Spacing = 8, HorizontalAlignment = HorizontalAlignment.Stretch };
        var log = new LiveActivityLog();
        if (exchange.ValueKind == JsonValueKind.Object && exchange.TryGetProperty("activities", out var activities) && activities.ValueKind == JsonValueKind.Array)
            foreach (var activity in activities.EnumerateArray().Take(100)) log.Apply(activity.GetRawText());
        foreach (var path in changes)
        {
            var savedDiff = "";
            var notice = "";
            if (exchange.ValueKind == JsonValueKind.Object && exchange.TryGetProperty("file_diffs", out var diffs) && diffs.ValueKind == JsonValueKind.Array)
                foreach (var diff in diffs.EnumerateArray())
                    if (NxJson.Text(diff, "path").Replace('\\', '/').Equals(path.Replace('\\', '/'), StringComparison.OrdinalIgnoreCase))
                    { savedDiff = NxJson.Text(diff, "diff"); notice = NxJson.Text(diff, "notice"); break; }
            var matches = log.All.Where(tool => tool.Diff.Length > 0 && tool.Files.Any(file =>
                file.Replace('\\', '/').Equals(path.Replace('\\', '/'), StringComparison.OrdinalIgnoreCase) ||
                file.Replace('\\', '/').EndsWith("/" + path.Replace('\\', '/'), StringComparison.OrdinalIgnoreCase)));
            if (savedDiff.Length == 0 && notice.Length == 0) savedDiff = string.Join("\n\n", matches.Select(tool => tool.Diff));
            if (savedDiff.Length == 0 && notice.Length == 0) notice = "No text diff was captured for this file; it may be outside the preview limits.";
            var preview = new FileDiffPreview(path, savedDiff, notice);
            if (!preview.HasChanges) continue;
            paths.Children.Add(FileDiffView.Create(preview));
            shown++;
        }
        if (shown == 0) return null;
        return new Expander
        {
            Header = "Files changed · " + shown,
            IsExpanded = true,
            Content = paths,
            HorizontalAlignment = HorizontalAlignment.Stretch,
            HorizontalContentAlignment = HorizontalAlignment.Stretch
        };
    }

    private void HandleCoreEvent(string json)
    {
        if (HandleLiveEvent(json)) return;
        if (_streamRequestID.Length == 0 || _streamText is null) return;
        try
        {
            using var doc = JsonDocument.Parse(json);
            var root = doc.RootElement;
            if (NxJson.Text(root, "type") != "continue.progress" ||
                NxJson.Text(root, "request_id") != _streamRequestID) return;
            var text = NxJson.Text(root, "text");
            if (text.Length == 0) return;
            var kind = NxJson.Text(root, "kind");
            if (kind == "files")
            {
                using var changes = JsonDocument.Parse(text);
                var paths = StringArray(changes.RootElement, "file_changes");
                if (_streamFileChanges is not null && paths.Count > 0 && FileChangesExpander(paths, changes.RootElement) is { } filesList)
                { _streamFileChanges.Child = filesList; _streamFileChanges.Visibility = Visibility.Visible; }
                return;
            }
            if (kind == "activity")
            {
                _streamActivity?.Apply(text);
                TurnStatusProgress(kind, text);
                return;
            }
            if (kind == "status")
            {
                TurnStatusProgress(kind, text);
                return;
            }
            if (kind != "text") return;
            TurnStatusProgress(kind, text);
            // Each progress event carries the full text so far; render it
            // through the same throttle as live chats.
            _liveText.Clear();
            _liveText.Append(text);
            QueueLiveRender();
        }
        catch (JsonException) { }
    }

    private bool _offlineNoticeShown;

    private void HandleShellChanged(object? sender, EventArgs e) => DispatcherQueue?.TryEnqueue(UpdateActions);

    private void SetComposerStatus(string text)
    {
        _composerStatus.Text = text;
        _composerStatus.Visibility = string.IsNullOrWhiteSpace(text) ? Visibility.Collapsed : Visibility.Visible;
    }

    private void SetSending(bool sending)
    {
        _refreshModels.IsEnabled = !sending && !_loadingModelAgents.Contains(SelectedHarness());
        foreach (var remove in _imagePreviews.Children.OfType<Grid>().SelectMany(preview => preview.Children.OfType<Button>()))
            remove.IsEnabled = !sending;
        // Send and Stop morph in UpdateComposerButton (via UpdateActions).
        UpdateActions();
        if (sending)
        {
            _sendHere.IsEnabled = false;
            _harness.IsEnabled = false;
            _model.IsEnabled = false;
            _workspaceMenu.IsEnabled = false;
            _attachImage.IsEnabled = false;
            ApplyLiveComposerState();
            return;
        }
        _harness.IsEnabled = true;
        _model.IsEnabled = true;
        _workspaceMenu.IsEnabled = true;
        UpdateActions();
    }

    // ── Composer footer ──────────────────────────────────────────────────
    // [attach] [folder label ……… star] [agent] [model ↻ access] [stop] [send]
    // Every width change picks the roomiest arrangement that fits, giving
    // way in this order: the folder label trims, then becomes an icon, then
    // the pickers tighten, then the refresh button and the access picker
    // go. Send is never pushed off the edge.
    private readonly Grid _composerFooter = new();
    private readonly StackPanel _modelPickerRow = new() { Orientation = Orientation.Horizontal, Spacing = 3 };
    private const double FooterSpacing = 6;
    private const double FolderLabelMinWidth = 96;
    private const double FolderIconWidth = 36;
    private bool _footerLayoutQueued;

    private bool _footerLayoutRunning;

    private void QueueComposerFooterLayout()
    {
        // The layout's own changes (access picker shown or hidden) do not
        // queue another pass.
        if (_footerLayoutRunning || _footerLayoutQueued || DispatcherQueue is null) return;
        _footerLayoutQueued = true;
        DispatcherQueue.TryEnqueue(DispatcherQueuePriority.Low, () =>
        {
            _footerLayoutQueued = false;
            LayoutComposerFooter();
        });
    }

    private void LayoutComposerFooter()
    {
        var available = _composerFooter.ActualWidth;
        if (available <= 0) return;
        static double Desired(FrameworkElement element)
        {
            if (element.Visibility != Visibility.Visible) return 0;
            element.Measure(new Windows.Foundation.Size(double.PositiveInfinity, double.PositiveInfinity));
            return element.DesiredSize.Width;
        }
        var accessWanted = SupportsLive(SelectedHarness());
        var harnessFit = HarnessFitWidth();
        // Everything in the footer but the folder button, plus the gaps
        // between its seven columns.
        double Fixed()
        {
            var width = _composerFooter.ColumnSpacing * (_composerFooter.ColumnDefinitions.Count - 1);
            foreach (var child in _composerFooter.Children.OfType<FrameworkElement>())
                if (!ReferenceEquals(child, _workspaceMenu)) width += Desired(child);
            return width;
        }
        void Apply(int level)
        {
            // 0 roomy, 1 tight pickers, 2 no refresh button, 3 narrow model,
            // 4 narrow access picker, 5 no access picker, 6 no model picker,
            // 7 no agent picker (a very narrow window keeps attach, dictation
            // and Send usable).
            _harness.MinWidth = harnessFit;
            _harness.MaxWidth = Math.Max(level == 0 ? 160 : harnessFit, harnessFit);
            _model.MinWidth = level >= 3 ? 104 : level >= 1 ? 140 : 150;
            _model.MaxWidth = level >= 3 ? 140 : level >= 1 ? 180 : 260;
            _sendHere.MinWidth = 32;
            _sendHere.Padding = level == 0 ? new Thickness(10, 5, 12, 5) : new Thickness(7);
            _refreshModels.Visibility = level >= 2 ? Visibility.Collapsed : Visibility.Visible;
            _access.MaxWidth = level >= 4 ? 136 : double.PositiveInfinity;
            _access.Visibility = accessWanted && level < 5 ? Visibility.Visible : Visibility.Collapsed;
            _modelPickerRow.Visibility = level >= 6 ? Visibility.Collapsed : Visibility.Visible;
            _harness.Visibility = level >= 7 ? Visibility.Collapsed : Visibility.Visible;
            // Empty columns still take the gap; the buttons' own padding spaces them.
            _composerFooter.ColumnSpacing = level >= 6 ? 0 : FooterSpacing;
        }
        _footerLayoutRunning = true;
        try
        {
            var icon = true;
            for (var level = 0; level <= 7; level++)
            {
                Apply(level);
                var left = available - Fixed();
                if (left >= FolderLabelMinWidth) { icon = false; break; }
                if (left >= FolderIconWidth) break;
            }
            _workspaceMenu.MaxWidth = icon ? FolderIconWidth : double.PositiveInfinity;
            _workspaceMenu.HorizontalAlignment = HorizontalAlignment.Left;
            RefreshWorkspaceButton(icon);
        }
        finally { _footerLayoutRunning = false; }
    }

    private void RefreshWorkspaceButton(bool? compact = null)
    {
        var path = _workspace.Text.Trim();
        var hint = path.Length > 0 ? ShortWorkspacePath(path) : "Choose folder…";
        var isCompact = compact ?? _workspaceMenu.MaxWidth == 36;
        _workspaceMenu.Content = isCompact ? new FontIcon { Glyph = "\uE8B7", FontSize = 14 } : new TextBlock
        {
            Text = path.Length > 0 ? hint : "Choose folder…",
            TextTrimming = TextTrimming.CharacterEllipsis,
            FontSize = 12,
            Opacity = path.Length > 0 ? 0.86 : 0.62,
            VerticalAlignment = VerticalAlignment.Center
        };
        ToolTipService.SetToolTip(_workspaceMenu, path.Length > 0 ? path : "Pick the local project folder for this continuation");
    }

    private static string ShortWorkspacePath(string path)
    {
        if (path.Length <= 44) return path;
        var leaf = System.IO.Path.GetFileName(path.TrimEnd('\\', '/'));
        return leaf.Length > 0 && leaf.Length < 40 ? "…" + System.IO.Path.DirectorySeparatorChar + leaf : "…" + path[^41..];
    }

    private void AnimateInputFocus(bool focused) => AnimateComposerFocus(focused);

    private static Button SessionTabButton(string label) => new()
    {
        Content = label,
        MinHeight = 30,
        Padding = new Thickness(14, 5, 14, 5),
        CornerRadius = new CornerRadius(7),
        BorderThickness = new Thickness(0),
        Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
        FontSize = 13
    };

    private static void StyleSessionTab(Button tab, bool selected)
    {
        tab.Background = selected
            ? NexusPalette.AccentFill
            : new SolidColorBrush(Microsoft.UI.Colors.Transparent);
        tab.Foreground = selected ? NexusPalette.Accent : Surface("TextFillColorPrimaryBrush", Microsoft.UI.Colors.White);
        tab.Opacity = selected ? 1 : 0.66;
        tab.FontWeight = selected ? Microsoft.UI.Text.FontWeights.SemiBold : Microsoft.UI.Text.FontWeights.Normal;
    }

    private static string ExtractText(JsonElement value)
    {
        if (value.ValueKind == JsonValueKind.String) return value.GetString() ?? "";
        if (value.ValueKind == JsonValueKind.Array)
            return string.Join("\n", value.EnumerateArray().Select(ExtractText).Where(v => v.Length > 0));
        if (value.ValueKind != JsonValueKind.Object) return "";
        foreach (var key in new[] { "text", "content", "message", "user_query", "prompt" })
        {
            if (value.TryGetProperty(key, out var part))
            {
                var text = ExtractText(part);
                if (text.Length > 0) return text;
            }
        }
        return "";
    }

    private static string JoinArray(JsonElement item, string key)
    {
        if (item.ValueKind != JsonValueKind.Object || !item.TryGetProperty(key, out var values) || values.ValueKind != JsonValueKind.Array) return "";
        return string.Join(" · ", values.EnumerateArray().Select(v => v.ToString()).Where(v => v.Length > 0));
    }

    private static string Nonempty(string value, string fallback) => string.IsNullOrWhiteSpace(value) ? fallback : value;

    private async Task CopyResumeAsync()
    {
        var session = _id;
        var agent = _agent;
        var status = await HarnessStatusAsync(agent);
        if (status is null || _id != session) return;
        var response = await Call(NxMethods.ContinueStart, new { session_id = _id, mode = "open_in_agent", resume = "native", agent = _agent, native_id = _nativeID });
        if (_id != session) return;
        if (!response.Ok || response.Result is not { } result || !result.TryGetProperty("command", out var command) || command.ValueKind != JsonValueKind.Array)
        {
            _notice.Text = response.Error ?? "This chat can't be continued from a terminal.";
            return;
        }
        var argv = command.EnumerateArray().Select(v => v.ToString()).ToList();
        // Nexus may inherit a CLI path that a separately opened terminal does
        // not have. Use the resolved executable, not a bare command name.
        argv[0] = NxJson.Text(status.Value, "executable");
        if (status.Value.TryGetProperty("prefix", out var prefix) && prefix.ValueKind == JsonValueKind.Array)
            argv.InsertRange(1, prefix.EnumerateArray().Select(v => v.GetString() ?? ""));
        if (agent == "opencode" && SelectedHarness() == "opencode" && SelectedModel().Length > 0)
        {
            var selectedModel = SelectedModel();
            if (!Regex.IsMatch(selectedModel, @"^[A-Za-z0-9_.:-]+/[A-Za-z0-9_./:@+-]+$"))
            {
                _notice.Text = "Choose a valid provider/model before copying an OpenCode resume command.";
                return;
            }
            argv.Add("--model"); argv.Add(selectedModel);
        }
        var text = string.Join(" ", argv.Select(QuoteArgument));
        var data = new DataPackage();
        data.SetText(text);
        Clipboard.SetContent(data);
        _notice.Text = "Copied. Paste it into Command Prompt to pick this chat up in the agent itself. The agent needs its original chat on this computer.";
    }

    private static string QuoteArgument(string value) => value.Any(char.IsWhiteSpace) ? "\"" + value.Replace("\"", "\\\"") + "\"" : value;

    private async Task ShareAsync()
    {
        if (_id.Length == 0 || _projectID.Length == 0) return;
        var directory = await LoadShareDirectoryAsync();
        if (directory is null) return;
        var scope = new ComboBox { Header = "Share with", HorizontalAlignment = HorizontalAlignment.Stretch };
        scope.Items.Add(new ComboBoxItem { Content = "A person", Tag = "person" });
        scope.Items.Add(new ComboBoxItem { Content = "Everyone in this project", Tag = "project" });
        if (directory.OrganizationId.Length > 0)
            scope.Items.Add(new ComboBoxItem { Content = "Current members of " + directory.OrganizationName, Tag = "organization" });
        scope.SelectedIndex = 0;
        var recipient = RecipientPicker(directory);
        recipient.Header = "Person";
        var live = new CheckBox { Content = "Keep their access up to date", IsChecked = true };
        var scopeHint = Muted("Project sharing is live. Organization sharing grants its current members; new members are not added automatically.");
        scope.SelectionChanged += (_, _) =>
        {
            var person = (scope.SelectedItem as ComboBoxItem)?.Tag?.ToString() == "person";
            recipient.Visibility = person ? Visibility.Visible : Visibility.Collapsed;
            live.Visibility = person ? Visibility.Visible : Visibility.Collapsed;
        };
        var panel = new StackPanel { Spacing = 12, MinWidth = 360 };
        panel.Children.Add(Muted("Share this chat and its saved files. To send a restorable copy of the code, use Share chat + code."));
        panel.Children.Add(scope);
        if (directory.People.Count == 0)
            panel.Children.Add(Muted("Nobody else is in this project or its organization yet. Invite teammates in Settings › Organizations & members."));
        panel.Children.Add(recipient);
        panel.Children.Add(live);
        if (directory.OrganizationId.Length > 0) panel.Children.Add(scopeHint);
        // Who already has it, with a way to take it back.
        var access = new StackPanel { Spacing = 6, Margin = new Thickness(0, 6, 0, 0) };
        panel.Children.Add(access);
        await FillAccessAsync(access);
        var dialog = new ContentDialog
        {
            Title = "Share chat",
            Content = panel,
            PrimaryButtonText = "Share",
            CloseButtonText = "Cancel",
            XamlRoot = XamlRoot,
            DefaultButton = ContentDialogButton.Close
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary)
        {
            return;
        }
        var selectedScope = (scope.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "person";
        var selectedPerson = (recipient.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "";
        if (selectedScope == "person" && selectedPerson.Length == 0)
        {
            _notice.Text = "Choose a person to share with.";
            return;
        }
        if (selectedScope != "person")
        {
            var label = selectedScope == "project" ? "everyone in this project" : "the current members of " + directory.OrganizationName;
            var confirm = new ContentDialog
            {
                Title = "Share with " + label + "?",
                Content = "This grants access to the conversation and its saved files. " +
                    (selectedScope == "organization" ? "New organization members will not be added automatically." : "Project members receive live access."),
                PrimaryButtonText = "Share",
                CloseButtonText = "Cancel",
                XamlRoot = XamlRoot,
                DefaultButton = ContentDialogButton.Close
            };
            if (await confirm.ShowThemedAsync() != ContentDialogResult.Primary) return;
        }
        var response = selectedScope switch
        {
            "project" => await Call(NxMethods.SessionsShare, new { session_id = _id, team = true, confirm = true, live = true }),
            "organization" => await Call(NxMethods.SessionsShare, new { session_id = _id, organization_id = directory.OrganizationId, confirm = true }),
            _ => await Call(NxMethods.SessionsShare, new { session_id = _id, user_id = selectedPerson, live = live.IsChecked == true })
        };
        _notice.Text = response.Ok ? "Chat shared successfully." : response.Error ?? "Share failed.";
    }

    // "Who has access": each person (live or a fixed copy), the project team,
    // and a way to stop sharing with each or with everyone.
    private async Task FillAccessAsync(StackPanel host)
    {
        host.Children.Clear();
        var response = await Call(NxMethods.SessionsSharedWith, new { session_id = _id });
        if (!response.Ok || response.Result is not { } result)
        {
            host.Children.Add(Muted(response.Error ?? "Could not load who has access."));
            return;
        }
        var team = result.TryGetProperty("team", out var teamValue) && teamValue.ValueKind == JsonValueKind.True;
        var people = result.TryGetProperty("people", out var list) && list.ValueKind == JsonValueKind.Array
            ? list.EnumerateArray().ToList() : new List<JsonElement>();
        host.Children.Add(new TextBlock { Text = "Who has access", FontWeight = Microsoft.UI.Text.FontWeights.SemiBold, FontSize = 13 });
        if (!team && people.Count == 0)
        {
            host.Children.Add(Muted("Only you. This chat is private."));
            return;
        }
        UIElement Row(string label, string detail, Func<Task<NxResponse>> remove)
        {
            var row = new Grid { ColumnSpacing = 8 };
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = new GridLength(1, GridUnitType.Star) });
            row.ColumnDefinitions.Add(new ColumnDefinition { Width = GridLength.Auto });
            var text = new StackPanel();
            text.Children.Add(new TextBlock { Text = label, FontSize = 13, TextTrimming = TextTrimming.CharacterEllipsis });
            text.Children.Add(new TextBlock { Text = detail, FontSize = 11.5, Opacity = 0.55 });
            row.Children.Add(text);
            var button = new Button { Content = "Remove", Padding = new Thickness(10, 3, 10, 4), VerticalAlignment = VerticalAlignment.Center };
            button.Click += async (_, _) =>
            {
                button.IsEnabled = false;
                var done = await remove();
                if (!done.Ok) { _notice.Text = done.Error ?? "Could not remove access."; button.IsEnabled = true; return; }
                await FillAccessAsync(host);
            };
            Grid.SetColumn(button, 1);
            row.Children.Add(button);
            return row;
        }
        if (team)
            host.Children.Add(Row("Everyone in this project", "Live: sees new messages", () => Call(NxMethods.SessionsUnshare, new { session_id = _id, team = true })));
        foreach (var person in people)
        {
            var userID = NxJson.Text(person, "user_id");
            var name = Nonempty(NxJson.Text(person, "name"), userID);
            var isLive = person.TryGetProperty("live", out var liveValue) && liveValue.ValueKind == JsonValueKind.True;
            host.Children.Add(Row(name, isLive ? "Live: sees new messages" : "Sees the chat as it was when shared",
                () => Call(NxMethods.SessionsUnshare, new { session_id = _id, user_id = userID })));
        }
        var stopAll = new HyperlinkButton { Content = "Stop sharing with everyone", Padding = new Thickness(0, 4, 0, 0) };
        stopAll.Click += async (_, _) =>
        {
            stopAll.IsEnabled = false;
            var done = await Call(NxMethods.SessionsUnshare, new { session_id = _id });
            if (!done.Ok) { _notice.Text = done.Error ?? "Could not stop sharing."; stopAll.IsEnabled = true; return; }
            _notice.Text = "This chat is private again.";
            await FillAccessAsync(host);
        };
        host.Children.Add(stopAll);
    }

    private async Task<ShareDirectory?> LoadShareDirectoryAsync()
    {
        var response = await Call(NxMethods.SharingRecipients, new { project_id = _projectID });
        if (!response.Ok || response.Result is not { } result)
        {
            _notice.Text = response.Error ?? "Could not load people for this project.";
            return null;
        }
        var org = result.TryGetProperty("organization", out var orgValue) && orgValue.ValueKind == JsonValueKind.Object
            ? orgValue : default;
        var people = new List<SharePerson>();
        if (result.TryGetProperty("people", out var array) && array.ValueKind == JsonValueKind.Array)
        {
            foreach (var item in array.EnumerateArray())
            {
                var id = NxJson.Text(item, "user_id");
                if (id.Length == 0) continue;
                people.Add(new SharePerson(id, NxJson.Text(item, "name"),
                    NxJson.Text(item, "project_role"), NxJson.Text(item, "organization_role")));
            }
        }
        return new ShareDirectory(NxJson.Text(org, "id"), NxJson.Text(org, "name"), people);
    }

    private static ComboBox RecipientPicker(ShareDirectory directory)
    {
        var picker = new ComboBox { PlaceholderText = "Choose a teammate", HorizontalAlignment = HorizontalAlignment.Stretch };
        foreach (var person in directory.People)
        {
            var location = person.ProjectRole.Length > 0 ? "Project" : directory.OrganizationName;
            picker.Items.Add(new ComboBoxItem { Content = person.Name + " · " + location, Tag = person.Id });
        }
        return picker;
    }

    private async Task SendTeleportAsync()
    {
        if (_id.Length == 0)
        {
            return;
        }
        if (_projectID.Length == 0) return;
        var directory = await LoadShareDirectoryAsync();
        if (directory is null) return;
        var recipient = RecipientPicker(directory);
        recipient.Header = "Send to";
        if (recipient.Items.Count == 0)
        {
            _notice.Text = "No other project or organization members are available to receive this chat.";
            return;
        }
        var dialog = new ContentDialog
        {
            Title = "Send a teleport",
            Content = new StackPanel { Spacing = 10, Children = { new TextBlock { Text = "Send this chat and a verified copy of the mapped workspace (up to 128 MB). Known credential paths and generated dependencies are excluded, but secrets inside ordinary source files are not detected. Review your code before sending. The recipient restores to a new folder; existing files are never overwritten.", TextWrapping = TextWrapping.Wrap }, recipient } },
            PrimaryButtonText = "Send",
            CloseButtonText = "Cancel",
            DefaultButton = ContentDialogButton.Primary,
            XamlRoot = XamlRoot
        };
        if (await dialog.ShowThemedAsync() != ContentDialogResult.Primary)
        {
            return;
        }
        var recipientId = (recipient.SelectedItem as ComboBoxItem)?.Tag?.ToString() ?? "";
        if (recipientId.Length == 0)
        {
            _notice.Text = "Choose a teammate or organization member.";
            return;
        }
        _notice.Text = "Packaging code and sending teleport…";
        var response = await Call(NxMethods.TeleportSend, new { session_id = _id, to_user_id = recipientId, include_code = true, workspace_root = _workspaceHint });
        _notice.Text = response.Ok && response.Result is { } sent
            ? "Teleport sent with " + NxJson.Text(sent, "code_files") + " code files."
            : response.Error ?? "Teleport could not be sent.";
    }
}
