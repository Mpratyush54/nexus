#nullable enable
using Nexus.Core;
using Xunit;

namespace Nexus.Core.Tests;

public class ProfilePathsTests
{
    private const string Temp = @"C:\t\";

    [Theory]
    [InlineData(null)]
    [InlineData("")]
    [InlineData("   ")]
    public void UnsetOrBlankNameMeansNoProfile(string? name)
    {
        Assert.Null(ProfilePaths.Resolve(name, @"D:\ignored", Temp));
    }

    [Fact]
    public void DefaultRootIsUnderTemp()
    {
        var profile = ProfilePaths.Resolve(" demo ", null, Temp);
        Assert.NotNull(profile);
        Assert.Equal("demo", profile!.Name);
        Assert.Equal(@"C:\t\nexus-profile-demo", profile.Root);
    }

    [Fact]
    public void ExplicitRootWins()
    {
        var profile = ProfilePaths.Resolve("demo", @"D:\Users\maya", Temp);
        Assert.Equal(@"D:\Users\maya", profile!.Root);
    }

    [Theory]
    [InlineData("a b")]
    [InlineData(@"..\x")]
    [InlineData("x/y")]
    [InlineData("12345678901234567890123456789012345678901")]
    public void UnsafeNamesAreRejected(string name)
    {
        Assert.Throws<ArgumentException>(() => ProfilePaths.Resolve(name, null, Temp));
    }

    [Fact]
    public void FoldersMapUnderTheRoot()
    {
        const string root = @"D:\Users\maya";
        Assert.Equal(root, ProfilePaths.FolderIn(root, Environment.SpecialFolder.UserProfile));
        Assert.Equal(@"D:\Users\maya\AppData\Roaming", ProfilePaths.FolderIn(root, Environment.SpecialFolder.ApplicationData));
        Assert.Equal(@"D:\Users\maya\AppData\Local", ProfilePaths.FolderIn(root, Environment.SpecialFolder.LocalApplicationData));
        Assert.Equal(@"D:\Users\maya\Documents", ProfilePaths.FolderIn(root, Environment.SpecialFolder.MyDocuments));
        Assert.Throws<ArgumentOutOfRangeException>(() => ProfilePaths.FolderIn(root, Environment.SpecialFolder.Windows));
    }

    [Fact]
    public void InstanceNamesAreUnchangedWithoutAProfile()
    {
        Assert.Equal(@"Local\Nexus-S-1-5", ProfilePaths.InstanceNameFor(null, @"Local\Nexus-S-1-5"));
        var profile = ProfilePaths.Resolve("demo", null, Temp);
        Assert.Equal(@"Local\Nexus-S-1-5-profile-demo", ProfilePaths.InstanceNameFor(profile, @"Local\Nexus-S-1-5"));
        Assert.Equal("nexus-S-1-5-profile-demo", ProfilePaths.InstanceNameFor(profile, "nexus-S-1-5"));
    }

    [Fact]
    public void EnvironmentPointsEveryHomeVariableInsideTheRoot()
    {
        const string root = @"D:\Users\maya";
        var env = ProfilePaths.EnvironmentFor(root);
        foreach (var key in new[] { "USERPROFILE", "HOME", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP" })
        {
            Assert.True(ProfilePaths.IsInside(root, env[key]!), key);
        }
        foreach (var key in new[] { "NEXUS_CONFIG_DIR", "NEXUS_CACHE_DIR", "CENTRAL_MEMORY_CONFIG_DIR", "CODEX_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME" })
        {
            Assert.Null(env[key]);
        }
    }

    [Fact]
    public void WithoutAProfileFolderMatchesTheOs()
    {
        // The test host never sets NEXUS_PROFILE, so Folder must be a pure
        // pass-through to Environment.GetFolderPath.
        if (ProfilePaths.Active) return;
        foreach (var folder in new[] { Environment.SpecialFolder.UserProfile, Environment.SpecialFolder.ApplicationData, Environment.SpecialFolder.LocalApplicationData, Environment.SpecialFolder.MyDocuments })
        {
            Assert.Equal(Environment.GetFolderPath(folder), ProfilePaths.Folder(folder));
        }
    }

    [Theory]
    [InlineData(@"D:\Users\maya", @"D:\Users\maya\code\x", true)]
    [InlineData(@"D:\Users\maya", @"D:\Users\maya", true)]
    [InlineData(@"D:\Users\maya", @"D:\Users\maya2\x", false)]
    [InlineData(@"D:\Users\maya", @"C:\Users\someone", false)]
    public void IsInsideComparesWholeSegments(string root, string path, bool expected)
    {
        Assert.Equal(expected, ProfilePaths.IsInside(root, path));
    }
}
