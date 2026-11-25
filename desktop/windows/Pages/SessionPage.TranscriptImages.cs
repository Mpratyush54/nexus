using Microsoft.UI.Xaml;
using Microsoft.UI.Xaml.Controls;
using Nexus.Core;

namespace Nexus.Pages;

// Images inside a captured chat (screenshots a person pasted into Claude
// Code or Codex, images a tool returned). Capture never uploads image bytes,
// so they come from the agent's transcript on this computer: the core lists
// where they are without decoding, and each thumbnail decodes one image,
// size-capped, only when it is first shown.
public sealed partial class SessionPage
{
    private IReadOnlyList<TranscriptImageRef> _transcriptImages = Array.Empty<TranscriptImageRef>();
    private string _transcriptImagesFor = "";
    private const int MaxImagesPerStrip = 12;

    private static string TranscriptAgent(string agent) => agent.Trim().ToLowerInvariant() switch
    {
        "claude" or "claude-code" or "claude_code" => "claude",
        "codex" => "codex",
        _ => "",
    };

    private async Task LoadTranscriptImagesAsync()
    {
        var sessionId = _id;
        var agent = TranscriptAgent(_agent);
        var native = _nativeID;
        if (agent.Length == 0 || native.Length == 0) return;
        var key = sessionId + "|" + agent + "|" + native;
        if (_transcriptImagesFor != key)
        {
            _transcriptImagesFor = key;
            _transcriptImages = Array.Empty<TranscriptImageRef>();
            var response = await Call(NxMethods.TranscriptImages, new { agent, native_id = native });
            if (_id != sessionId || _transcriptImagesFor != key) return;
            _transcriptImages = TranscriptImagePlacement.Parse(response.Ok ? response.Result : null);
        }
        PlaceTranscriptImages();
    }

    private void PlaceTranscriptImages()
    {
        if (_transcriptImages.Count == 0) return;
        // Shown captured messages by index; when a prompt and reply share an
        // index, the one lower in the chat wins.
        var shown = new Dictionary<long, FrameworkElement>();
        foreach (var (key, element) in _messageAnchors)
        {
            if (!key.StartsWith("turn:", StringComparison.Ordinal)) continue;
            var idxText = key["turn:".Length..];
            var colon = idxText.IndexOf(':');
            if (colon >= 0) idxText = idxText[..colon];
            if (!long.TryParse(idxText, out var idx)) continue;
            if (ChatChild(element) is not { } child) continue;
            if (!shown.TryGetValue(idx, out var current) || _chat.Children.IndexOf(child) > _chat.Children.IndexOf(current))
                shown[idx] = child;
        }
        var agent = TranscriptAgent(_agent);
        var native = _nativeID;
        foreach (var (idx, refs) in TranscriptImagePlacement.Place(shown.Keys, _transcriptImages))
        {
            var tag = "timgs:" + idx;
            if (_chat.Children.OfType<FrameworkElement>().Any(e => Equals(e.Tag, tag))) continue;
            var at = _chat.Children.IndexOf(shown[idx]);
            if (at < 0) continue;
            _chat.Children.Insert(at + 1, TranscriptImageStrip(refs, tag, agent, native));
        }
    }

    // The direct child of the chat column that holds an element.
    private FrameworkElement? ChatChild(FrameworkElement element)
    {
        // Compared by membership, not by the Parent wrapper's identity.
        DependencyObject? node = element;
        for (var depth = 0; node is FrameworkElement fe && depth < 8; depth++)
        {
            if (_chat.Children.IndexOf(fe) >= 0) return fe;
            node = fe.Parent ?? Microsoft.UI.Xaml.Media.VisualTreeHelper.GetParent(fe);
        }
        return null;
    }

    private FrameworkElement TranscriptImageStrip(List<TranscriptImageRef> refs, string tag, string agent, string native)
    {
        var fromUser = refs.All(r => r.Role == "user");
        var row = new StackPanel { Orientation = Orientation.Horizontal, Spacing = 6 };
        foreach (var image in refs.Take(MaxImagesPerStrip))
        {
            var label = (image.Role switch { "user" => "Image in your message", "tool" => "Image from a tool result", _ => "Image from the agent" })
                + (image.Bytes > 0 ? " · " + ComposerAttachments.FriendlySize(image.Bytes) : "");
            var sessionId = _id;
            row.Children.Add(ChatImages.LazyThumbnail(async () =>
            {
                var response = await Call(NxMethods.TranscriptImage, new { agent, native_id = native, offset = image.Offset, index = image.Index });
                if (_id != sessionId) return (null, null);
                var path = response.Ok && response.Result is { } body ? NxJson.Text(body, "path") : "";
                return path.Length > 0 ? (path, null) : (null, response.Error);
            }, label, 96));
        }
        if (refs.Count > MaxImagesPerStrip)
            row.Children.Add(new TextBlock { Text = $"+{refs.Count - MaxImagesPerStrip} more", Opacity = 0.6, FontSize = 12, VerticalAlignment = VerticalAlignment.Center });
        var scroll = new ScrollViewer
        {
            Content = row, HorizontalScrollBarVisibility = ScrollBarVisibility.Auto, VerticalScrollBarVisibility = ScrollBarVisibility.Disabled,
            HorizontalScrollMode = ScrollMode.Auto, VerticalScrollMode = ScrollMode.Disabled,
            HorizontalAlignment = fromUser ? HorizontalAlignment.Right : HorizontalAlignment.Left,
            Tag = tag,
        };
        Microsoft.UI.Xaml.Automation.AutomationProperties.SetName(scroll, refs.Count == 1 ? "1 image" : refs.Count + " images");
        return scroll;
    }
}
