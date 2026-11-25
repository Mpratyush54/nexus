namespace Nexus.Pages;

// Declarations of the message action row's composer hooks. feat/chat-rendering
// declares the same two in SessionPage.Messages.cs and calls them from Edit
// and Retry; SessionPage.Composer.cs implements them. Delete this file when
// that branch is merged (C# allows one declaration per partial method).
public sealed partial class SessionPage
{
    partial void ComposerEditHook(string text, ref bool handled);
    partial void ComposerRetryHook(string text, ref bool handled);
}
