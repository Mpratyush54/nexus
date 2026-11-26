using System.Text.Json;
using Xunit;

namespace Nexus.Core.Tests;

public class ChangeReviewTests
{
    private static JsonElement Json(string json) => JsonDocument.Parse(json).RootElement.Clone();

    private const string Review = """
    {"checkpoint_id":"cp1","total":3,"pending":2,"added":5,"removed":2,"files":[
      {"path":"src/app/retry.ts","status":"modified","added":4,"removed":2,"version":"v1","can_undo":true,
       "hunks":[{"index":0,"header":"@@ -1,3 +1,4 @@","added":1,"removed":0,"lines":[
         {"kind":" ","text":"a","old":1,"new":1},{"kind":"+","text":"b","new":2},{"kind":"-","text":"c","old":2}]}]},
      {"path":"logo.png","status":"added","binary":true,"notice":"Binary file.","added":0,"removed":0,"version":"v2","can_undo":true}
    ]}
    """;

    [Fact]
    public void ParsesFilesHunksAndLines()
    {
        var review = ChangeReview.Parse(Json(Review));
        Assert.Equal("cp1", review.CheckpointId);
        Assert.Equal(2, review.Files.Count);
        var file = review.Files[0];
        Assert.Equal("retry.ts", file.FileName);
        Assert.Equal("src/app", file.Folder);
        Assert.Equal("M", file.Letter);
        Assert.Equal("ts", file.Language);
        Assert.False(file.WholeFileOnly);
        var hunk = Assert.Single(file.Hunks);
        Assert.Equal("@@ -1,3 +1,4 @@", hunk.Header);
        Assert.True(hunk.Lines[1].Added);
        Assert.True(hunk.Lines[2].Removed);
        Assert.Equal(2, hunk.Lines[2].Old);
        var image = review.Files[1];
        Assert.True(image.Binary);
        Assert.True(image.WholeFileOnly);
        Assert.Equal("A", image.Letter);
        Assert.Equal("New file", image.StatusLabel);
    }

    [Fact]
    public void TitleSaysFilesAndLines()
    {
        var review = ChangeReview.Parse(Json(Review));
        Assert.False(review.Settled);
        Assert.Equal("Review changes (2 files, +5 −2)", review.Title);
        Assert.Equal("Review changes (1 file)", ChangeReview.Placeholder(1));
        var settled = ChangeReview.Parse(Json("""{"checkpoint_id":"x","total":3,"pending":0,"files":[]}"""));
        Assert.True(settled.Settled);
        Assert.Equal("Changes reviewed", settled.Title);
        Assert.Equal("3 files", settled.Summary);
    }

    [Theory]
    [InlineData("a/b/main.go", "go")]
    [InlineData("README.md", "")]
    [InlineData("ci.yml", "yaml")]
    [InlineData("Dockerfile", "dockerfile")]
    [InlineData("no_ext", "")]
    [InlineData("dir.v2/file", "")]
    public void LanguageFromPath(string path, string language) => Assert.Equal(language, ChangeReview.LanguageFor(path));

    [Fact]
    public void RewindPlanWordsAndRows()
    {
        var plan = RewindPlanView.Parse(Json("""
        {"checkpoint_id":"cp","from_idx":100,"later":2,"files":[
          {"path":"a.txt","action":"restore"},{"path":"b.txt","action":"delete"},
          {"path":"c.txt","action":"recreate"},{"path":"big.bin","action":"restore","problem":"Too large"}]}
        """));
        Assert.Equal(100, plan.FromIdx);
        Assert.Equal(3, plan.Restorable);
        Assert.Equal("Restore 3 files", plan.ConfirmLabel);
        Assert.Contains("this message and the 2 after it", plan.Lead);
        Assert.Equal("Removed · the agent created it", plan.Rows[1].Label);
        Assert.Equal("Brought back · the agent deleted it", plan.Rows[2].Label);
        Assert.False(plan.Rows[3].CanRestore);
        Assert.StartsWith("Can't restore", plan.Rows[3].Label);

        var empty = RewindPlanView.Parse(Json("""{"checkpoint_id":"cp","files":[]}"""));
        Assert.Equal("Go back in chat", empty.ConfirmLabel);
        Assert.Contains("No files need to change", empty.Lead);

        var one = RewindPlanView.Parse(Json("""{"checkpoint_id":"cp","later":0,"files":[{"path":"a","action":"restore"}]}"""));
        Assert.Equal("Restore 1 file", one.ConfirmLabel);
        Assert.Contains("1 file goes back to how it was before this message.", one.Lead);
    }

    [Fact]
    public void CheckpointIndexKnowsRestorableMessages()
    {
        var index = CheckpointIndex.Parse(Json("""
        {"checkpoints":[{"id":"a","user_idx":10,"changed":2,"done":true},{"id":"b","user_idx":20,"changed":0,"done":true,"rewound":true},
                        {"id":"c","user_idx":0,"changed":1,"done":false}],
         "rewinds":[{"from_idx":20,"cutoff_idx":35,"undo_id":"u1","files":2,"at":"2026-11-26T10:00:00Z"}]}
        """));
        Assert.True(index.CanRestore(10));
        Assert.False(index.CanRestore(20)); // hidden by a restore
        Assert.False(index.CanRestore(99));
        Assert.Equal(new[] { "a" }, index.Reviewable.Select(i => i.Id));
        var rewind = Assert.Single(index.Rewinds);
        Assert.Equal(35, rewind.CutoffIdx);
        Assert.Equal("u1", rewind.UndoId);
        index.Note("d", 40, 3);
        Assert.True(index.CanRestore(40));
        Assert.Contains(index.Reviewable, i => i.Id == "d");
    }

    [Fact]
    public void RewindHidesFromTheMessageToTheFirstLaterMessage()
    {
        var elements = new List<(string? Key, long? Order)>
        {
            ("turn:5:user", 5), ("turn:6", 6),
            ("turn:10:user", 10), ("turn:11", 11), (null, null), // the restored message, its reply and a card
            ("turn:20:user", 20), ("turn:21", 21),
            ("turn:40:user", 40), ("turn:41", 41),               // sent after the restore
        };
        var rewind = new ChatRewind(10, 30, "u", 2, DateTimeOffset.Now);
        Assert.Equal((2, 7), ChatRewinds.Range(elements, rewind));
        // Nothing sent since: hide to the end.
        Assert.Equal((2, 9), ChatRewinds.Range(elements, rewind with { CutoffIdx = 99 }));
        Assert.Null(ChatRewinds.Range(elements, rewind with { FromIdx = 7 }));
        Assert.Equal("Restored checkpoint · 2 files", ChatRewinds.Divider(rewind));
        Assert.Equal("Restored checkpoint", ChatRewinds.Divider(rewind with { Files = 0 }));
    }

    [Fact]
    public void BackToBackRestoresKeepOnlyTheLastDivider()
    {
        // message, divider, (hidden message), divider, divider, message, divider
        var elements = new List<(bool IsDivider, bool Visible)>
        {
            (false, true), (true, true), (false, false), (true, true), (true, true), (false, true), (true, true),
        };
        Assert.Equal(new[] { 1, 3 }, ChatRewinds.StackedDividers(elements));
        // A divider that is already hidden is neither hidden again nor counted.
        elements[3] = (true, false);
        Assert.Equal(new[] { 1 }, ChatRewinds.StackedDividers(elements));
        Assert.Empty(ChatRewinds.StackedDividers(new List<(bool, bool)> { (true, true), (false, true), (true, true) }));
    }

    [Fact]
    public void RewindCommandIsOfferedOnlyWithARestorableMessage()
    {
        var index = CheckpointIndex.Parse(Json("""{"checkpoints":[{"id":"a","user_idx":4,"changed":1,"done":true,"rewound":true}]}"""));
        Assert.DoesNotContain(index.Items, i => !i.Rewound && i.UserIdx > 0);
        index.Note("b", 8, 0);
        Assert.Contains(index.Items, i => !i.Rewound && i.UserIdx > 0);
    }
}
