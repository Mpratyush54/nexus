using Nexus.Core;
using Xunit;

public class TranscriptTextTests
{
    [Fact]
    public void EncryptedMetadataIsHiddenButNormalCommandsRemainReadable()
    {
        var blob = "gAAAA" + new string('A', 180);
        Assert.True(TranscriptText.IsOpaque(blob));
        Assert.True(TranscriptText.IsOpaque("{\"message\":\"" + blob + "\"}"));
        Assert.False(TranscriptText.IsOpaque("{\"command\":\"echo hello\"}"));
        Assert.False(TranscriptText.IsOpaque("This example mentions gAAAA but is normal prose."));
    }

    [Fact]
    public void ToolArgumentsKeepLongCommandsAndDecodeEscapedQuotes()
    {
        var command = "gh api user --jq \".login\"; " + new string('x', 2000);
        var json = System.Text.Json.JsonSerializer.Serialize(new { command });
        Assert.Equal(command, TranscriptText.Argument(json, "command"));
        Assert.Contains(new string('x', 2000), TranscriptText.FormatArguments(json));
    }

    [Fact]
    public void ToolFieldsDecodeCodeAndPatchesIntoRealLines()
    {
        var patch = "*** Begin Patch\n*** Add File: hello.txt\n+hello \"world\"\n*** End Patch";
        var json = System.Text.Json.JsonSerializer.Serialize(new { patch, workdir = @"D:\project" });
        var fields = TranscriptText.ToolFields(json);
        Assert.Equal(patch, fields[0].Text);
        Assert.True(fields[0].Code);
        Assert.Equal("Working directory", fields[1].Label);
        Assert.Equal(@"D:\project", fields[1].Text);
    }

    [Fact]
    public void ToolFieldsAlsoDecodeStringEncodedArguments()
    {
        var inner = System.Text.Json.JsonSerializer.Serialize(new { command = "first\nsecond" });
        var fields = TranscriptText.ToolFields(System.Text.Json.JsonSerializer.Serialize(inner));
        Assert.Single(fields);
        Assert.Equal("first\nsecond", fields[0].Text);
    }
}
