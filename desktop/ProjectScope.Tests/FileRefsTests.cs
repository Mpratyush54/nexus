using Xunit;

namespace Nexus.Core.Tests;

public class FileRefsTests
{
    [Theory]
    [InlineData("Full setup is in NOTIFICATIONS.md.", "NOTIFICATIONS.md", null, null)]
    [InlineData("See docs/NOTIFICATIONS.md for details", "docs/NOTIFICATIONS.md", null, null)]
    [InlineData("crash at src/app.ts:42 today", "src/app.ts", 42, null)]
    [InlineData("crash at src/app.ts:42:7, see", "src/app.ts", 42, 7)]
    [InlineData(@"open C:\path\file.cs now", @"C:\path\file.cs", null, null)]
    [InlineData("(see ./scripts/build.ps1)", "./scripts/build.ps1", null, null)]
    [InlineData("edit package.json and run", "package.json", null, null)]
    [InlineData("the Makefile target", "Makefile", null, null)]
    [InlineData("in lib/widget.foo the", "lib/widget.foo", null, null)]
    [InlineData("README.md#L12", "README.md", 12, null)]
    public void FindsFileReferences(string text, string path, int? line, int? column)
    {
        var span = Assert.Single(FileRefs.Find(text));
        Assert.Equal(path, span.Ref.Path);
        Assert.Equal(line, span.Ref.Line);
        Assert.Equal(column, span.Ref.Column);
        Assert.Equal(text.IndexOf(path, StringComparison.Ordinal), span.Index);
    }

    [Theory]
    [InlineData("Hello there, this is a normal sentence.")]
    [InlineData("Use Node.js and Next.js for this.")]
    [InlineData("e.g. this, i.e. that, at 10 a.m. today")]
    [InlineData("version 1.2.3 and pi is 3.14")]
    [InlineData("client/server and and/or TCP/IP")]
    [InlineData("call res.json() then console.log(x)")]
    [InlineData("visit https://example.com/docs/readme.md now")]
    [InlineData("go to example.com/docs/readme.md")]
    [InlineData("the src/components folder")]
    [InlineData("meet at 12:30")]
    public void IgnoresOrdinaryProse(string text)
    {
        Assert.Empty(FileRefs.Find(text));
    }

    [Fact]
    public void FindsSeveralReferencesInOneLine()
    {
        var spans = FileRefs.Find("Changed src/a.ts:3 and tests/a.test.ts.");
        Assert.Equal(new[] { "src/a.ts", "tests/a.test.ts" }, spans.Select(s => s.Ref.Path));
    }

    [Fact]
    public void InlineCodeSpanIsParsedWhole()
    {
        Assert.Equal("src/app.ts", FileRefs.Parse(" src/app.ts ")?.Path);
        Assert.Null(FileRefs.Parse("npm run build"));
    }

    [Theory]
    [InlineData("docs/NOTIFICATIONS.md", "docs/NOTIFICATIONS.md", null)]
    [InlineData("src/app.ts#L42", "src/app.ts", 42)]
    [InlineData("src/app.ts:42", "src/app.ts", 42)]
    [InlineData("my%20file.md", "my file.md", null)]
    [InlineData(@"C:\repo\x.cs", @"C:\repo\x.cs", null)]
    public void MarkdownLinkTargetsResolveToFiles(string target, string path, int? line)
    {
        var reference = FileRefs.ParseLinkTarget(target);
        Assert.NotNull(reference);
        Assert.Equal(path, reference!.Path);
        Assert.Equal(line, reference.Line);
    }

    [Theory]
    [InlineData("https://example.com/a.md")]
    [InlineData("mailto:someone@example.com")]
    [InlineData("#section")]
    public void MarkdownWebLinksAreNotFiles(string target)
    {
        Assert.Null(FileRefs.ParseLinkTarget(target));
    }

    [Fact]
    public void ResolvesExactRelativePathBeforeSearching()
    {
        using var temp = new TempTree("docs/NOTIFICATIONS.md", "other/NOTIFICATIONS.md");
        var result = FileRefs.Resolve(new FileRef("docs/NOTIFICATIONS.md"), temp.Root, Array.Empty<string>());
        Assert.Equal(FileRefStatus.Found, result.Status);
        Assert.Equal(temp.Full("docs/NOTIFICATIONS.md"), Assert.Single(result.Matches));
    }

    [Fact]
    public void BareNameFallsBackToBoundedWorkspaceSearch()
    {
        using var temp = new TempTree("docs/NOTIFICATIONS.md", "node_modules/pkg/NOTIFICATIONS.md");
        var result = FileRefs.Resolve(new FileRef("NOTIFICATIONS.md"), temp.Root, Array.Empty<string>());
        Assert.Equal(FileRefStatus.Found, result.Status);
        Assert.Equal(temp.Full("docs/NOTIFICATIONS.md"), Assert.Single(result.Matches));
    }

    [Fact]
    public void SearchPrefersMatchesEndingWithTheMentionedFolders()
    {
        using var temp = new TempTree("a/app.ts", "b/src/app.ts");
        var result = FileRefs.Resolve(new FileRef("src/app.ts"), temp.Root, Array.Empty<string>());
        Assert.Equal(FileRefStatus.Found, result.Status);
        Assert.Equal(temp.Full("b/src/app.ts"), result.Matches[0]);
        Assert.Equal(2, result.Matches.Count);
    }

    [Fact]
    public void AbsolutePathOutsideWorkspaceIsNotPreviewed()
    {
        using var workspace = new TempTree("x.md");
        using var elsewhere = new TempTree("secret.txt");
        var result = FileRefs.Resolve(new FileRef(elsewhere.Full("secret.txt")), workspace.Root, Array.Empty<string>());
        Assert.Equal(FileRefStatus.OutsideRoots, result.Status);
        Assert.Empty(result.Matches);
    }

    [Fact]
    public void AbsolutePathInsideAnotherKnownFolderIsAllowed()
    {
        using var workspace = new TempTree("x.md");
        using var known = new TempTree("notes.txt");
        var result = FileRefs.Resolve(new FileRef(known.Full("notes.txt")), workspace.Root, new[] { known.Root });
        Assert.Equal(FileRefStatus.Found, result.Status);
    }

    [Fact]
    public void RelativePathCannotEscapeTheWorkspace()
    {
        using var temp = new TempTree("inner/x.md");
        var result = FileRefs.Resolve(new FileRef("../../../Windows/win.ini"), temp.Full("inner"), Array.Empty<string>());
        Assert.Equal(FileRefStatus.OutsideRoots, result.Status);
    }

    [Fact]
    public void RelativePathWithoutWorkspaceSaysSo()
    {
        var result = FileRefs.Resolve(new FileRef("docs/a.md"), "", Array.Empty<string>());
        Assert.Equal(FileRefStatus.NoWorkspace, result.Status);
    }

    private sealed class TempTree : IDisposable
    {
        public string Root { get; } = Path.Combine(Path.GetTempPath(), "nexus-filerefs-" + Guid.NewGuid().ToString("N"));

        public TempTree(params string[] files)
        {
            FileRefs.ClearSearchCache();
            foreach (var file in files)
            {
                var full = Full(file);
                Directory.CreateDirectory(Path.GetDirectoryName(full)!);
                File.WriteAllText(full, "x");
            }
            Directory.CreateDirectory(Root);
        }

        public string Full(string relative) => Path.GetFullPath(Path.Combine(Root, relative));

        public void Dispose()
        {
            try { Directory.Delete(Root, recursive: true); } catch (IOException) { }
        }
    }
}
