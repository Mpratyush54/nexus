using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Microsoft.UI.Xaml.Media;
using Microsoft.UI.Xaml.Media.Imaging;
using Nexus.Core;
using Windows.ApplicationModel.DataTransfer;
using Windows.Storage;
using Windows.Storage.Pickers;
using WinRT.Interop;

namespace Nexus.Pages;

// The composer's 📎: images (sent to the agent as images) and other files
// (named in the message by path). Pick, paste (Ctrl+V / Cmd+V) or drop them; each
// shows as a thumbnail or chip with a remove ×.
public sealed partial class SessionPage
{
    private readonly List<string> _files = new();

    private static string ChatImageCache()
    {
        var path = System.IO.Path.Combine(ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus", "Chat images");
        System.IO.Directory.CreateDirectory(path);
        return path;
    }

    private void WireAttachmentDrop()
    {
        // The whole composer is a drop target; the text box keeps its own
        // text drops.
        _composer.AllowDrop = true;
        _composer.DragOver += (_, e) =>
        {
            if (!e.DataView.Contains(StandardDataFormats.StorageItems) && !e.DataView.Contains(StandardDataFormats.Bitmap)) return;
            e.AcceptedOperation = _attachImage.IsEnabled ? DataPackageOperation.Copy : DataPackageOperation.None;
            if (e.DragUIOverride is { } ui) ui.Caption = _attachImage.IsEnabled ? "Attach" : "Can't attach right now";
            e.Handled = true;
        };
        _composer.Drop += async (_, e) =>
        {
            if (!_attachImage.IsEnabled) return;
            var deferral = e.GetDeferral();
            try { await AddFromDataAsync(e.DataView); }
            catch (Exception ex) { SetComposerStatus(NxErrors.ForException("Nexus couldn't attach that. Try attaching the file instead.", ex, "attach")); }
            finally { deferral.Complete(); }
        };
    }

    private async Task AttachImageAsync()
    {
        if (MainWindow.Current is not { } window) return;
        var picker = new FileOpenPicker { SuggestedStartLocation = PickerLocationId.PicturesLibrary, ViewMode = PickerViewMode.Thumbnail };
        // Images first so the picker opens on them; "*" keeps other files reachable.
        foreach (var ext in ComposerAttachments.ImageExtensions) picker.FileTypeFilter.Add(ext);
        picker.FileTypeFilter.Add("*");
        InitializeWithWindow.Initialize(picker, WindowNative.GetWindowHandle(window));
        var files = await picker.PickMultipleFilesAsync();
        if (files is null) return;
        foreach (var file in files)
        {
            try { await AddAttachmentAsync(file); }
            catch (Exception ex) { SetComposerStatus(NxErrors.ForException("Nexus couldn't attach " + file.Name + ". Check that it isn't open in another app, then try again.", ex, "attach")); }
        }
    }

    private static bool ClipboardHasImage(DataPackageView content) => content.Contains(StandardDataFormats.Bitmap) ||
        (content.Contains(StandardDataFormats.StorageItems) && !content.Contains(StandardDataFormats.Text));

    private async void Prompt_Paste(object sender, TextControlPasteEventArgs e)
    {
        try
        {
            var content = Clipboard.GetContent();
            if (!ClipboardHasImage(content)) return;
            e.Handled = true;
            await PasteClipboardImageAsync(content);
        }
        catch (Exception ex) { SetComposerStatus(NxErrors.ForException("Nexus couldn't paste that image. Try saving it and attaching the file.", ex, "attach")); }
    }

    /// True when the clipboard held an image and the composer took it. The
    /// macOS Edit menu's Paste (and Cmd+V there) comes through this.
    private async Task<bool> TryPasteClipboardImageAsync()
    {
        try
        {
            var content = Clipboard.GetContent();
            if (!ClipboardHasImage(content)) return false;
            await PasteClipboardImageAsync(content);
        }
        catch (Exception ex) { SetComposerStatus(NxErrors.ForException("Nexus couldn't paste that image. Try saving it and attaching the file.", ex, "attach")); }
        return true;
    }

    private async Task PasteClipboardImageAsync(DataPackageView content)
    {
        try { await AddFromDataAsync(content); }
        catch (Exception ex) { SetComposerStatus(NxErrors.ForException("Nexus couldn't paste that image. Try saving it and attaching the file.", ex, "attach")); }
    }

    // A screenshot (bitmap) or copied/dropped files.
    private async Task AddFromDataAsync(DataPackageView content)
    {
        if (_sendInFlight) { SetComposerStatus("Wait for this reply before attaching more."); return; }
        if (content.Contains(StandardDataFormats.StorageItems))
        {
            foreach (var item in await content.GetStorageItemsAsync())
                if (item is StorageFile file) await AddAttachmentAsync(file);
            return;
        }
        if (!content.Contains(StandardDataFormats.Bitmap)) return;
        var problem = ComposerAttachments.ImageProblem(SelectedHarness(), "Pasted.png", 1);
        if (problem is not null) { SetComposerStatus(problem); return; }
        var cache = await StorageFolder.GetFolderFromPathAsync(ChatImageCache());
#if HAS_UNO
        // Uno has no BitmapDecoder/BitmapEncoder: save the pasteboard's bytes
        // and let macOS's own sips make a PNG of them.
        await AddAttachmentAsync(await StorageFile.GetFileFromPathAsync(
            await MacImages.SavePastedAsync(await (await content.GetBitmapAsync()).OpenReadAsync(), ChatImageCache())));
        return;
#else
        var pasted = await cache.CreateFileAsync("Pasted-" + Guid.NewGuid().ToString("N") + ".png");
        using (var source = await (await content.GetBitmapAsync()).OpenReadAsync())
        {
            var decoder = await Windows.Graphics.Imaging.BitmapDecoder.CreateAsync(source);
            using var bitmap = await decoder.GetSoftwareBitmapAsync();
            using var target = await pasted.OpenAsync(FileAccessMode.ReadWrite);
            var encoder = await Windows.Graphics.Imaging.BitmapEncoder.CreateAsync(Windows.Graphics.Imaging.BitmapEncoder.PngEncoderId, target);
            encoder.SetSoftwareBitmap(bitmap);
            await encoder.FlushAsync();
        }
        await AddAttachmentAsync(pasted);
#endif
    }

    private Task AddAttachmentAsync(StorageFile file) =>
        ComposerAttachments.IsImage(file.Path) ? AddImageAsync(file) : AddFileAsync(file);

    private async Task AddFileAsync(StorageFile file)
    {
        if (_files.Contains(file.Path, StringComparer.OrdinalIgnoreCase)) return;
        if (_files.Count >= ComposerAttachments.MaxFiles) { SetComposerStatus($"Attach at most {ComposerAttachments.MaxFiles} files."); return; }
        if (file.Path.Length == 0) { SetComposerStatus(file.Name + " is not a file on this computer."); return; }
        var size = (await file.GetBasicPropertiesAsync()).Size;
        _files.Add(file.Path);
        var chip = new Border
        {
            CornerRadius = new CornerRadius(10), Padding = new Thickness(8, 4, 4, 4),
            BorderThickness = new Thickness(1), BorderBrush = NexusPalette.AccentBorder, VerticalAlignment = VerticalAlignment.Center,
        };
        var remove = RemoveButton(file.Name);
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        row.Children.Add(new FontIcon { Glyph = "", FontSize = 12, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(new TextBlock { Text = file.Name, FontSize = 12, MaxWidth = 200, TextTrimming = TextTrimming.CharacterEllipsis, VerticalAlignment = VerticalAlignment.Center });
        row.Children.Add(new TextBlock { Text = ComposerAttachments.FriendlySize((long)size), FontSize = 11, Opacity = 0.6, VerticalAlignment = VerticalAlignment.Center });
        remove.Width = remove.Height = remove.MinWidth = remove.MinHeight = 20;
        row.Children.Add(remove);
        chip.Child = row;
        ToolTipService.SetToolTip(chip, file.Path + "\nThe agent gets this path and opens the file itself.");
        remove.Click += (_, _) =>
        {
            _files.Remove(file.Path);
            _imagePreviews.Children.Remove(chip);
            RefreshImageAttachments();
            _prompt.Focus(FocusState.Programmatic);
        };
        _imagePreviews.Children.Add(chip);
        RefreshImageAttachments();
    }

    private async Task AddImageAsync(StorageFile file)
    {
        var session = _id;
        var agent = SelectedHarness();
        if (_images.Count >= ComposerAttachments.MaxImages) { SetComposerStatus($"Attach at most {ComposerAttachments.MaxImages} images."); return; }
        if (_images.Contains(file.Path, StringComparer.OrdinalIgnoreCase)) return;
        var size = (long)(await file.GetBasicPropertiesAsync()).Size;
        var problem = ComposerAttachments.ImageProblem(agent, file.Path, size);
        if (problem is not null) { SetComposerStatus(problem); return; }
        var note = "";
        if (ComposerAttachments.NeedsShrink(size))
        {
            var original = file.Name;
            SetComposerStatus("Resizing " + original + "…");
            file = await ShrinkImageAsync(file);
            note = $"{original} was {ComposerAttachments.FriendlySize(size)}, over the 5 MB limit, so a resized copy will be sent.";
        }
        var bitmap = new BitmapImage { DecodePixelWidth = 128 };
        using (var stream = await file.OpenAsync(FileAccessMode.Read)) await bitmap.SetSourceAsync(stream);
        // Loading is asynchronous: do not attach into a different chat or
        // mutate a request that has already started sending.
        // A sent message took its attachments with it, so these belong to the next one.
        if (_id != session || !ComposerAttachments.AcceptsImages(SelectedHarness())) return;
        if (_images.Contains(file.Path, StringComparer.OrdinalIgnoreCase)) return;
        if (_images.Count >= ComposerAttachments.MaxImages) { SetComposerStatus($"Attach at most {ComposerAttachments.MaxImages} images."); return; }
        _images.Add(file.Path);
        var name = System.IO.Path.GetFileName(file.Path);
        var preview = new Grid { Width = 64, Height = 64, Tag = "image" };
        preview.Children.Add(new Border { CornerRadius = new CornerRadius(8), BorderThickness = new Thickness(1), BorderBrush = NexusPalette.AccentBorder, Child = new Image { Source = bitmap, Width = 62, Height = 62, Stretch = Stretch.UniformToFill } });
        ToolTipService.SetToolTip(preview, name);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(preview, "Attached image " + name);
        var remove = RemoveButton(name);
        remove.HorizontalAlignment = HorizontalAlignment.Right;
        remove.VerticalAlignment = VerticalAlignment.Top;
        remove.Click += (_, _) =>
        {
            _images.Remove(file.Path);
            _imagePreviews.Children.Remove(preview);
            RefreshImageAttachments();
            _prompt.Focus(FocusState.Programmatic);
        };
        preview.Children.Add(remove);
        _imagePreviews.Children.Add(preview);
        RefreshImageAttachments();
        SetComposerStatus(note);
    }

    private static Button RemoveButton(string name)
    {
        var remove = new Button
        {
            Content = new FontIcon { Glyph = "", FontSize = 10 },
            Width = 24, Height = 24, MinWidth = 24, MinHeight = 24,
            Padding = new Thickness(0), CornerRadius = new CornerRadius(12),
            Background = new SolidColorBrush(Windows.UI.Color.FromArgb(255, 28, 36, 48)),
            Foreground = new SolidColorBrush(Microsoft.UI.Colors.White),
            BorderBrush = NexusPalette.AccentBorder, BorderThickness = new Thickness(1)
        };
        ToolTipService.SetToolTip(remove, "Remove " + name);
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(remove, "Remove attachment " + name);
        return remove;
    }

    // Re-encodes a big photo as JPEG with its longest side at most 2048 px,
    // stepping down until it fits the 5 MB inline limit. The original file
    // is never changed.
    private static async Task<StorageFile> ShrinkImageAsync(StorageFile file)
    {
#if HAS_UNO
        return await StorageFile.GetFileFromPathAsync(await MacImages.ShrinkAsync(file.Path, ChatImageCache(),
            new[] { ComposerAttachments.ShrinkLongestSide, 1600, 1200 }, ComposerAttachments.MaxInlineImageBytes));
#else
        var cache = await StorageFolder.GetFolderFromPathAsync(ChatImageCache());
        var baseName = System.IO.Path.GetFileNameWithoutExtension(file.Name);
        foreach (var side in new[] { ComposerAttachments.ShrinkLongestSide, 1600, 1200 })
        {
            var target = await cache.CreateFileAsync(baseName + "-resized-" + Guid.NewGuid().ToString("N")[..8] + ".jpg");
            using (var source = await file.OpenAsync(FileAccessMode.Read))
            {
                var decoder = await Windows.Graphics.Imaging.BitmapDecoder.CreateAsync(source);
                var (width, height) = ComposerAttachments.FitWithin(decoder.OrientedPixelWidth, decoder.OrientedPixelHeight, side);
                using var output = await target.OpenAsync(FileAccessMode.ReadWrite);
                var properties = new Windows.Graphics.Imaging.BitmapPropertySet
                {
                    { "ImageQuality", new Windows.Graphics.Imaging.BitmapTypedValue(0.85, Windows.Foundation.PropertyType.Single) }
                };
                var encoder = await Windows.Graphics.Imaging.BitmapEncoder.CreateAsync(Windows.Graphics.Imaging.BitmapEncoder.JpegEncoderId, output, properties);
                using var pixels = await decoder.GetSoftwareBitmapAsync(Windows.Graphics.Imaging.BitmapPixelFormat.Bgra8, Windows.Graphics.Imaging.BitmapAlphaMode.Ignore,
                    new Windows.Graphics.Imaging.BitmapTransform { ScaledWidth = width, ScaledHeight = height, InterpolationMode = Windows.Graphics.Imaging.BitmapInterpolationMode.Fant },
                    Windows.Graphics.Imaging.ExifOrientationMode.RespectExifOrientation, Windows.Graphics.Imaging.ColorManagementMode.DoNotColorManage);
                encoder.SetSoftwareBitmap(pixels);
                await encoder.FlushAsync();
            }
            if ((long)(await target.GetBasicPropertiesAsync()).Size <= ComposerAttachments.MaxInlineImageBytes) return target;
            await target.DeleteAsync();
        }
        throw new InvalidOperationException("the image is still over 5 MB after resizing");
#endif
    }

    private void ClearImageAttachments()
    {
        _images.Clear();
        _files.Clear();
        _imagePreviews.Children.Clear();
        RefreshImageAttachments();
    }

    // Switching to an agent that can't take images drops only the images;
    // attached files still go as paths.
    private void DropImagesFor(string agent)
    {
        if (_images.Count == 0 || ComposerAttachments.AcceptsImages(agent)) return;
        _images.Clear();
        foreach (var preview in _imagePreviews.Children.OfType<Grid>().Where(g => Equals(g.Tag, "image")).ToList())
            _imagePreviews.Children.Remove(preview);
        RefreshImageAttachments();
        SetComposerStatus(ChatTitles.AgentName(agent) + " can't take images, so they were removed. Attached files stay.");
    }

    private void RefreshImageAttachments()
    {
        _attachmentsLabel.Text = string.Join(" · ", _images.Concat(_files).Select(System.IO.Path.GetFileName));
        _attachmentStrip.Visibility = _images.Count + _files.Count > 0 ? Visibility.Visible : Visibility.Collapsed;
    }
}
