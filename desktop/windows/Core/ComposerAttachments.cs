#nullable enable
using System.Text;
using System.Text.Json;

namespace Nexus.Core;

/// Rules for what the chat composer's 📎 accepts and how attachments go out.
/// Images go to the agent as images (Claude Code: base64 content blocks;
/// Codex: local image inputs; OpenCode: --file). Other files are named in
/// the message by path, which every agent can open itself.
internal static class ComposerAttachments
{
    public const int MaxImages = 4;
    public const int MaxFiles = 8;
    /// Images above this are shrunk before sending (Claude's per-image limit).
    public const long MaxInlineImageBytes = 5L * 1024 * 1024;
    /// Larger originals are refused outright rather than decoded.
    public const long MaxSourceImageBytes = 50L * 1024 * 1024;
    /// Longest side of a shrunk image.
    public const int ShrinkLongestSide = 2048;

    public static readonly string[] ImageExtensions = { ".png", ".jpg", ".jpeg", ".gif", ".webp" };

    public static bool IsImage(string path) =>
        ImageExtensions.Contains(System.IO.Path.GetExtension(path ?? "").ToLowerInvariant());

    /// Agents that take images from the composer.
    public static bool AcceptsImages(string agent) => agent is "claude" or "codex" or "opencode";

    /// Agents whose live session takes images (others send an image turn as
    /// a one-shot run).
    public static bool SendsImagesLive(string agent) => agent is "claude" or "codex";

    /// Why an image cannot be attached, or null when it can.
    public static string? ImageProblem(string agent, string path, long bytes)
    {
        var name = System.IO.Path.GetFileName(path);
        if (!AcceptsImages(agent))
            return ChatAgentName(agent) + " can't take images here. Switch to Claude Code, Codex, or OpenCode, or attach it as a file.";
        if (!IsImage(path)) return name + " isn't a PNG, JPEG, GIF, or WebP image.";
        if (bytes <= 0) return name + " is empty.";
        if (bytes > MaxSourceImageBytes) return name + " is too large (over 50 MB). Choose a smaller image.";
        if (bytes > MaxInlineImageBytes && System.IO.Path.GetExtension(path).Equals(".gif", StringComparison.OrdinalIgnoreCase))
            return name + " is over 5 MB. GIFs can't be shrunk; choose a smaller one.";
        return null;
    }

    /// True when an image must be shrunk before it is sent.
    public static bool NeedsShrink(long bytes) => bytes > MaxInlineImageBytes;

    /// Scales (width, height) so the longest side is at most max.
    public static (uint Width, uint Height) FitWithin(uint width, uint height, int max)
    {
        if (width == 0 || height == 0) return (width, height);
        var longest = Math.Max(width, height);
        if (longest <= max) return (width, height);
        var scale = (double)max / longest;
        return ((uint)Math.Max(1, Math.Round(width * scale)), (uint)Math.Max(1, Math.Round(height * scale)));
    }

    /// The message with non-image files named by path.
    public static string WithFiles(string prompt, IReadOnlyList<string> files)
    {
        if (files.Count == 0) return prompt;
        var b = new StringBuilder(prompt.TrimEnd());
        b.Append("\n\nAttached files:");
        foreach (var file in files) b.Append("\n- ").Append(file);
        return b.ToString();
    }

    /// The same image markers the core records (continuex.ImageTags), so the
    /// message shows its thumbnails before the saved copy comes back.
    public static string WithImageTags(string prompt, IReadOnlyList<string> images)
    {
        if (images.Count == 0) return prompt;
        var b = new StringBuilder(prompt);
        for (var i = 0; i < images.Count; i++)
            b.Append("\n<image name=[Image #").Append(i + 1).Append("] path=")
             .Append(JsonSerializer.Serialize(images[i].Replace('\\', '/'))).Append(">\n</image>");
        return b.ToString();
    }

    public static string FriendlySize(long bytes) =>
        bytes >= 1024 * 1024 ? $"{bytes / 1024.0 / 1024.0:0.#} MB" : $"{Math.Max(1, bytes / 1024)} KB";

    private static string ChatAgentName(string agent) => agent switch
    {
        "claude" => "Claude Code",
        "codex" => "Codex",
        "opencode" => "OpenCode",
        "cursor" => "Cursor",
        "gemini" => "Gemini",
        "kimi" => "Kimi",
        _ => agent.Length > 0 ? agent : "This agent",
    };
}

/// One image inside a local agent transcript (core transcript.images).
internal sealed record TranscriptImageRef(long Offset, long Position, int Index, string Role, string MediaType, long Bytes)
{
    public string Key => $"timg:{Offset}:{Index}";
}

/// Places transcript images next to the captured messages they belong to.
internal static class TranscriptImagePlacement
{
    private const long MaxPosition = 1_000_000_000_000;

    public static IReadOnlyList<TranscriptImageRef> Parse(JsonElement? result)
    {
        var refs = new List<TranscriptImageRef>();
        if (result is not { } body || body.ValueKind != JsonValueKind.Object || !body.TryGetProperty("items", out var items) || items.ValueKind != JsonValueKind.Array)
            return refs;
        foreach (var item in items.EnumerateArray())
        {
            long Long(string name) => item.TryGetProperty(name, out var v) && v.TryGetInt64(out var n) ? n : -1;
            var offset = Long("offset");
            var position = Long("position");
            var index = (int)Long("index");
            if (offset < 0 || index < 0) continue;
            refs.Add(new TranscriptImageRef(offset, position, index,
                item.TryGetProperty("role", out var r) ? r.GetString() ?? "" : "",
                item.TryGetProperty("media_type", out var m) ? m.GetString() ?? "" : "",
                Math.Max(0, Long("bytes"))));
        }
        return refs;
    }

    /// Groups images under the last shown message at or before their
    /// position. Images older than every shown message wait for an earlier
    /// page, so nothing lands next to the wrong message.
    public static Dictionary<long, List<TranscriptImageRef>> Place(IEnumerable<long> shownTurns, IEnumerable<TranscriptImageRef> refs)
    {
        // Byte positions of captured lines; time-based indexes (messages Nexus
        // recorded itself) never correspond to a transcript position.
        var turns = shownTurns.Where(t => t > 0 && t < MaxPosition).Distinct().OrderBy(t => t).ToArray();
        var placed = new Dictionary<long, List<TranscriptImageRef>>();
        if (turns.Length == 0) return placed;
        foreach (var image in refs)
        {
            var at = image.Position > 0 ? image.Position : image.Offset;
            var i = Array.BinarySearch(turns, at);
            if (i < 0) i = ~i - 1;
            if (i < 0) continue;
            if (!placed.TryGetValue(turns[i], out var list)) placed[turns[i]] = list = new List<TranscriptImageRef>();
            list.Add(image);
        }
        return placed;
    }
}
