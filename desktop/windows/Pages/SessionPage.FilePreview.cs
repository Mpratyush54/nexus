using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

// File links in this chat open a slide-in preview on the right of the chat,
// resolved against this chat's own workspace folder.
public sealed partial class SessionPage : IFilePreviewHost
{
    private FilePreviewPanel? _filePreview;
    private string _filePreviewSession = "";

    void IFilePreviewHost.ShowFilePreview(FileRef reference)
    {
        if (Content is not Grid root)
        {
            if (XamlRoot is { } xamlRoot) _ = FilePreviewPanel.ShowDialogAsync(xamlRoot, reference, ChatWorkspaceRoot());
            return;
        }
        if (_filePreview is null)
        {
            _filePreview = new FilePreviewPanel { HorizontalAlignment = HorizontalAlignment.Right, VerticalAlignment = VerticalAlignment.Stretch };
            Grid.SetRow(_filePreview, Math.Max(0, root.RowDefinitions.Count - 1));
            Canvas.SetZIndex(_filePreview, 20);
            _filePreview.CloseRequested += HideFilePreview;
            root.Children.Add(_filePreview);
            // Leaving the page or switching chats closes the preview.
            Unloaded += (_, _) => HideFilePreview();
            _header.RegisterPropertyChangedCallback(TextBlock.TextProperty, (_, _) =>
            {
                if (_filePreviewSession != _id) HideFilePreview();
            });
        }
        var available = root.ActualWidth - root.Padding.Left - root.Padding.Right;
        _filePreview.Width = available <= 0 ? 520 : available < 420 ? available : Math.Min(600, Math.Max(420, available * 0.55));
        _filePreview.Visibility = Visibility.Visible;
        _filePreviewSession = _id;
        _ = _filePreview.ShowAsync(reference, ChatWorkspaceRoot());
    }

    private void HideFilePreview()
    {
        if (_filePreview is not null) _filePreview.Visibility = Visibility.Collapsed;
    }

    // The folder this chat runs in: the composer's folder, the session's
    // recorded workspace, then the folder remembered for its project.
    private string ChatWorkspaceRoot()
    {
        var saved = AppPrefs.Data.ProjectFolders is { } folders && _projectID.Length > 0 && folders.TryGetValue(_projectID, out var folder) ? folder : "";
        foreach (var candidate in new[] { _workspace.Text.Trim(), _workspaceHint.Trim(), saved })
            if (candidate.Length > 0 && System.IO.Directory.Exists(candidate)) return candidate;
        return "";
    }
}
