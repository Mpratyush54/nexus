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
        Assert.Equal("Friendly A", catalog.Choices[0].Label);
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
        Assert.True(catalog.RefreshFailed);
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

    [Fact]
    public void DefaultEntryNamesTheAgentsConfiguredModel()
    {
        using var json = JsonDocument.Parse("""{"items":[{"id":"claude-fable-5-2","name":"Claude Fable 5.2","is_default":true}],"default_model":"claude-fable-5-2","default_name":"Claude Fable 5.2"}""");
        var catalog = ModelPickerCatalog.Parse(json.RootElement);
        Assert.Equal("Default (Claude Fable 5.2)", catalog.DefaultLabel);
        Assert.Equal("", ModelPickerCatalog.ResolveValue("Default (Claude Fable 5.2)", "", "Default (Claude Fable 5.2)"));
        Assert.Equal("", ModelPickerCatalog.ResolveValue("Default (Claude Fable 5.2)", null, null));
        using var bare = JsonDocument.Parse("""{"items":[{"id":"a"}]}""");
        Assert.Equal("Default model", ModelPickerCatalog.Parse(bare.RootElement).DefaultLabel);
    }

    [Fact]
    public void RowsGroupByProviderOnlyWhenThereIsMoreThanOne()
    {
        using var many = JsonDocument.Parse("""{"items":[{"id":"openai/gpt-6","name":"GPT-6","provider":"OpenAI"},{"id":"anthropic/claude-opus-5-5","name":"Claude Opus 5.5","provider":"Anthropic"},{"id":"openai/gpt-5","name":"GPT-5","provider":"OpenAI"}]}""");
        var rows = ModelPickerCatalog.Parse(many.RootElement).Rows().Select(r => r.Choice is null ? "#" + r.Header : r.Choice.Label).ToList();
        Assert.Equal(new[] { "#OpenAI", "GPT-6", "GPT-5", "#Anthropic", "Claude Opus 5.5" }, rows);
        using var one = JsonDocument.Parse("""{"items":[{"id":"opus","name":"Opus","provider":"Anthropic"},{"id":"sonnet","name":"Sonnet","provider":"Anthropic"}]}""");
        Assert.All(ModelPickerCatalog.Parse(one.RootElement).Rows(), r => Assert.NotNull(r.Choice));
    }

    [Fact]
    public void FailedRefreshIsAFlagNotASentence()
    {
        using var json = JsonDocument.Parse("""{"items":[{"id":"a"}],"refresh_failed":true}""");
        var catalog = ModelPickerCatalog.Parse(json.RootElement);
        Assert.True(catalog.RefreshFailed);
        Assert.Equal("", catalog.Warning);
    }
}
