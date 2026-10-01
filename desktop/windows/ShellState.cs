using CommunityToolkit.Mvvm.ComponentModel;

namespace Nexus;

public partial class ShellState : ObservableObject
{
    public static ShellState Current { get; } = new();

    public static event EventHandler? Changed;

    [ObservableProperty] private string banner = "";
    [ObservableProperty] private bool offline;
    [ObservableProperty] private int pendingUploads;
    [ObservableProperty] private bool capturePaused;

    partial void OnOfflineChanged(bool value) => Changed?.Invoke(this, EventArgs.Empty);
}
