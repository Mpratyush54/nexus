using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Imaging;

namespace Nexus.Pages;

/// Image thumbnails in a chat and the full-size viewer they open. Bitmaps
/// are decoded at display size (DecodePixelWidth), never at full resolution
/// for a thumbnail, so a chat full of screenshots stays light.
internal static class ChatImages
{
    /// Longest side the viewer decodes to; enough for any screen.
    private const int ViewerMaxPixels = 2560;

    public static Button Thumbnail(string path, string label, double size = 64)
    {
        var button = Frame(label, size);
        SetImage(button, path, size);
        button.Click += async (_, _) => await ShowAsync(button.XamlRoot, path, label);
        return button;
    }

    /// A thumbnail whose file is produced on demand (a transcript image the
    /// core decodes to its cache only when the thumbnail is first shown).
    public static Button LazyThumbnail(Func<Task<(string? Path, string? Problem)>> resolve, string label, double size = 72)
    {
        var button = Frame(label, size);
        button.Content = new FontIcon { Glyph = "", FontSize = 18, Opacity = 0.45 };
        string? path = null;
        var started = false;
        async Task<string?> Load()
        {
            if (path is not null || started) return path;
            started = true;
            var (resolved, problem) = await resolve();
            if (resolved is null)
            {
                button.Content = new FontIcon { Glyph = "", FontSize = 16, Opacity = 0.5 };
                ToolTipService.SetToolTip(button, label + "\n" + (problem ?? "This image is no longer available."));
                return null;
            }
            path = resolved;
            SetImage(button, resolved, size);
            return path;
        }
        button.Loaded += async (_, _) => await Load();
        button.Click += async (_, _) =>
        {
            if (await Load() is { } file) await ShowAsync(button.XamlRoot, file, label);
        };
        return button;
    }

    private static Button Frame(string label, double size)
    {
        var button = new Button
        {
            Width = size, Height = size, MinWidth = 0, MinHeight = 0, Padding = new Thickness(0),
            CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1), BorderBrush = NexusPalette.AccentBorder,
            Background = new SolidColorBrush(Microsoft.UI.Colors.Transparent),
            HorizontalContentAlignment = HorizontalAlignment.Stretch, VerticalContentAlignment = VerticalAlignment.Stretch,
        };
        ToolTipService.SetToolTip(button, label + " · click to open");
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(button, "Open image " + label);
        return button;
    }

    private static void SetImage(Button button, string path, double size)
    {
        try
        {
            var bitmap = new BitmapImage { DecodePixelWidth = (int)(size * 2), UriSource = new Uri(System.IO.Path.GetFullPath(path)) };
            button.Content = new Image { Source = bitmap, Stretch = Stretch.UniformToFill };
        }
        catch
        {
            button.Content = new FontIcon { Glyph = "", FontSize = 16, Opacity = 0.5 };
        }
    }

    /// Full-size view: fits the window, scroll-wheel zoom, and a button to
    /// open the file in the default app.
    public static async Task ShowAsync(XamlRoot? root, string path, string label)
    {
        if (root is null || !File.Exists(path)) return;
        var bitmap = new BitmapImage();
        var image = new Image { Source = bitmap, Stretch = Stretch.Uniform };
        bitmap.ImageOpened += (_, _) =>
        {
            // Decode no larger than needed; huge screenshots are capped.
            if (bitmap.PixelWidth > ViewerMaxPixels) bitmap.DecodePixelWidth = ViewerMaxPixels;
        };
        bitmap.DecodePixelWidth = ViewerMaxPixels;
        bitmap.DecodePixelType = DecodePixelType.Logical;
        bitmap.UriSource = new Uri(System.IO.Path.GetFullPath(path));
        var maxHeight = Math.Max(240, root.Size.Height - 220);
        var maxWidth = Math.Max(320, root.Size.Width - 160);
        // Fits the window first; scroll-wheel / pinch zoom shows detail.
        image.MaxHeight = maxHeight;
        image.MaxWidth = maxWidth;
        var scroller = new ScrollViewer
        {
            Content = image, ZoomMode = ZoomMode.Enabled, MinZoomFactor = 0.5f, MaxZoomFactor = 6f,
            HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Auto,
            MaxHeight = maxHeight, MaxWidth = maxWidth,
        };
        var dialog = new ContentDialog
        {
            Title = label, Content = scroller, XamlRoot = root,
            PrimaryButtonText = "Open in app", CloseButtonText = "Close", DefaultButton = ContentDialogButton.Close,
        };
        dialog.Resources["ContentDialogMaxWidth"] = Math.Max(400, root.Size.Width - 80);
        dialog.Resources["ContentDialogMaxHeight"] = Math.Max(300, root.Size.Height - 80);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(image, label);
        try
        {
            if (await dialog.ShowThemedAsync() == ContentDialogResult.Primary)
                await PlatformText.OpenFileAsync(System.IO.Path.GetFullPath(path));
        }
        catch (Exception ex) when (ex is InvalidOperationException or System.Runtime.InteropServices.COMException or System.ComponentModel.Win32Exception)
        {
            // Another dialog is already open; the click does nothing.
        }
    }
}
