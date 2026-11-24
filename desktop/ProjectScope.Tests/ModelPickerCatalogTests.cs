using System.Text.Json;
using Xunit;

namespace Nexus.Core.Tests;

public class ModelPickerCatalogTests
{
    [Fact]
    public void FriendlyNameResolvesToExactCliID()
    {
        Assert.Equal("model-id", ModelPickerCatalog.ResolveValue("Friendly name · model-id", "model-id", "Friendly name · model-id"));
        Assert.Equal("custom/model", ModelPickerCatalog.ResolveValue("custom/model", "model-id", "Friendly name · model-id"));
        Assert.Equal("", ModelPickerCatalog.ResolveValue("Default model", "model-id", "Friendly name"));
        Assert.Equal("", ModelPickerCatalog.ResolveValue("", null, null));
    }

    [Fact]
    public void CatalogKeepsNamesIDsAndSourceWithoutDuplicates()
    {
        using var json = JsonDocument.Parse("""{"models":["a","b"],"items":[{"id":"a","name":"Friendly A","description":"Description"}],"source":"Installed CLI","warning":"Catalog only"}""");
        var catalog = ModelPickerCatalog.Parse(json.RootElement);
        Assert.Equal(2, catalog.Choices.Count);
        Assert.Equal("Friendly A · a", catalog.Choices[0].Label);
        Assert.Equal("a", catalog.Choices[0].ID);
        Assert.Equal("Installed CLI", catalog.Source);
        Assert.Equal("Catalog only", catalog.Warning);
    }

    [Fact]
    public void EmptyOrInvalidCatalogStillAllowsManualSelection()
    {
        using var json = JsonDocument.Parse("null");
        var catalog = ModelPickerCatalog.Parse(json.RootElement);
        Assert.Empty(catalog.Choices);
        Assert.NotEmpty(catalog.Warning);
        Assert.Equal("saved-model", ModelPickerCatalog.ResolveValue("saved-model", null, null));
    }

    [Fact]
    public void BackgroundRefreshWithSameListIsRecognised()
    {
        static ModelPickerCatalog Parse(string text) { using var json = JsonDocument.Parse(text); return ModelPickerCatalog.Parse(json.RootElement); }
        var cached = Parse("""{"items":[{"id":"a","name":"A"}],"source":"CLI","cached":true,"fetched_at":"2026-01-01T00:00:00Z"}""");
        var fresh = Parse("""{"items":[{"id":"a","name":"A"}],"source":"CLI","fetched_at":"2026-01-02T00:00:00Z"}""");
        var changed = Parse("""{"items":[{"id":"a","name":"A"},{"id":"b"}],"source":"CLI"}""");
        Assert.True(cached.SameAs(fresh));
        Assert.False(cached.SameAs(changed));
    }
}
