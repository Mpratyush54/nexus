using Xunit;

namespace Nexus.Core.Tests;

public class FileDiffPreviewTests
{
    [Fact]
    public void UnifiedDiffShowsChangedLinesAndRealLineNumbers()
    {
        var diff = new FileDiffPreview("app.cs", "--- a/app.cs\n+++ b/app.cs\n@@ -12,2 +12,2 @@\n context\n-old\n+new\n");
        Assert.Equal(1, diff.Added);
        Assert.Equal(1, diff.Removed);
        Assert.Equal(13, Assert.Single(diff.Lines, line => line.Kind == "removed").OldLine);
        Assert.Equal(13, Assert.Single(diff.Lines, line => line.Kind == "added").NewLine);
    }

    [Fact]
    public void CapturedCloudWorkspaceChangesRetainPatchNotJustPath()
    {
        var diff = FileDiffPreview.Captured("next-env.d.ts", """{"path":"next-env.d.ts","diff":"@@ -1 +1 @@\n-old\n+new"}""", "");
        Assert.Equal(1, diff.Added);
        Assert.Contains("-old", diff.Diff);
    }

    [Fact]
    public void SavedReplacementShowsBothSidesWithoutInventingLineNumbers()
    {
        var diff = FileDiffPreview.Captured("app.cs", """{"old_string":"before","new_string":"after"}""", "");
        Assert.Contains("-before", diff.Diff);
        Assert.Contains("+after", diff.Diff);
        Assert.Null(Assert.Single(diff.Lines, line => line.Kind == "removed").OldLine);
    }

    [Fact]
    public void FilenameOnlyHistoryDoesNotPretendToContainADiff()
    {
        var diff = FileDiffPreview.Captured("next-env.d.ts", """{"path":"next-env.d.ts"}""", "");
        Assert.Empty(diff.Diff);
        Assert.Contains("No patch", diff.Notice);
        Assert.Equal(0, diff.Added);
    }

    [Fact]
    public void PlusZeroMinusZeroHasNoChanges()
    {
        Assert.True(new FileDiffPreview("a.cs", "@@ -1 +1 @@\n-old\n+new").HasChanges);
        Assert.True(new FileDiffPreview("a.cs", "@@ -1,0 +1 @@\n+only").HasChanges);
        Assert.False(new FileDiffPreview("a.cs", "@@ -1 +1 @@\n same").HasChanges);
        Assert.False(new FileDiffPreview("a.cs", "", "No text diff was captured.").HasChanges);
    }
}
