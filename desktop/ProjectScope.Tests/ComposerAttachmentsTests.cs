#nullable enable
using System.Text.Json;
using Nexus.Core;
using Xunit;

public class ComposerAttachmentsTests
{
    [Theory]
    [InlineData("shot.PNG", true)]
    [InlineData("a.jpeg", true)]
    [InlineData("anim.gif", true)]
    [InlineData("pic.webp", true)]
    [InlineData("notes.txt", false)]
    [InlineData("vector.svg", false)]
    public void RecognisesSupportedImages(string path, bool image) => Assert.Equal(image, ComposerAttachments.IsImage(path));

    [Fact]
    public void ImageProblemsAreFriendly()
    {
        Assert.Null(ComposerAttachments.ImageProblem("claude", @"C:\a.png", 1024));
        // Big photos are shrunk, not refused; big GIFs can't be.
        Assert.Null(ComposerAttachments.ImageProblem("codex", @"C:\a.jpg", 12L * 1024 * 1024));
        Assert.Contains("GIFs can't be shrunk", ComposerAttachments.ImageProblem("claude", @"C:\a.gif", 6L * 1024 * 1024));
        Assert.Contains("over 50 MB", ComposerAttachments.ImageProblem("claude", @"C:\a.png", 60L * 1024 * 1024));
        Assert.Contains("Cursor can't take images", ComposerAttachments.ImageProblem("cursor", @"C:\a.png", 10));
        Assert.True(ComposerAttachments.NeedsShrink(6L * 1024 * 1024));
        Assert.False(ComposerAttachments.NeedsShrink(ComposerAttachments.MaxInlineImageBytes));
    }

    [Fact]
    public void FitWithinKeepsAspectRatio()
    {
        Assert.Equal((2048u, 1536u), ComposerAttachments.FitWithin(4032, 3024, 2048));
        Assert.Equal((800u, 600u), ComposerAttachments.FitWithin(800, 600, 2048));
    }

    [Fact]
    public void FilesAreNamedByPathAndImagesTagged()
    {
        var prompt = ComposerAttachments.WithFiles("check these", new[] { @"C:\work\log.txt" });
        Assert.Equal("check these\n\nAttached files:\n- C:\\work\\log.txt", prompt);
        var tagged = ComposerAttachments.WithImageTags("look", new[] { @"C:\Users\me\shot.png" });
        var parsed = HarnessMessage.Parse(tagged);
        Assert.Equal("look", parsed.Text);
        var image = Assert.Single(parsed.Attachments);
        Assert.True(image.IsImage);
        Assert.Equal("C:/Users/me/shot.png", image.Path);
        Assert.Equal("Image #1", image.Name);
    }

    [Fact]
    public void TranscriptImagesSitUnderTheirMessage()
    {
        using var doc = JsonDocument.Parse("""
            {"items":[
              {"offset":100,"position":250,"index":0,"role":"user","media_type":"image/png","bytes":10},
              {"offset":400,"position":520,"index":0,"role":"tool","media_type":"image/png","bytes":10},
              {"offset":5,"position":40,"index":0,"role":"user","media_type":"image/png","bytes":10}
            ]}
            """);
        var refs = TranscriptImagePlacement.Parse(doc.RootElement);
        Assert.Equal(3, refs.Count);
        // Shown messages at 250 (the image's own message) and 480; one Nexus
        // recorded message has a time-based index and is ignored.
        var placed = TranscriptImagePlacement.Place(new long[] { 250, 480, 17_000_000_000_000_000 }, refs);
        Assert.Equal(250, Assert.Single(placed[250]).Position);
        Assert.Equal("tool", Assert.Single(placed[480]).Role);
        // The image at 40 is older than every shown message: it waits.
        Assert.Equal(2, placed.Values.Sum(v => v.Count));
    }
}
