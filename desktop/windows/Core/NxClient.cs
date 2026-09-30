using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using System.Text.Json;
using Microsoft.UI.Dispatching;

namespace Nexus.Core;

public sealed class NxClient
{
    private static DispatcherQueue? _queue;
    private static Action<string>? _onEvent;

    public void Init(string configJson) => NxNative.nx_init(configJson);

    public void Shutdown() => NxNative.nx_shutdown();

    /// <summary>
    /// Registers the core callback. The shim copies the payload and posts it
    /// to the UI queue. It must not call nx_call on the Go thread.
    /// </summary>
    public unsafe void Subscribe(DispatcherQueue queue, Action<string> onEvent)
    {
        _queue = queue;
        _onEvent = onEvent;
        NxNative.nx_subscribe(&OnCoreEvent);
    }

    [UnmanagedCallersOnly(CallConvs = [typeof(CallConvCdecl)])]
    private static void OnCoreEvent(nint json)
    {
        var copy = Marshal.PtrToStringUTF8(json) ?? "";
        _queue?.TryEnqueue(() => _onEvent?.Invoke(copy));
    }

    public Task<NxResponse> CallAsync(string method, object? args = null, CancellationToken ct = default)
    {
        var json = args is null ? "{}" : JsonSerializer.Serialize(args);
        return Task.Run(() => Call(method, json), ct);
    }

    private static NxResponse Call(string method, string json)
    {
        var ptr = NxNative.nx_call(method, json);
        try
        {
            if (ptr == 0)
            {
                return new NxResponse(false, "empty core response", null);
            }
            var text = System.Runtime.InteropServices.Marshal.PtrToStringUTF8(ptr) ?? "";
            using var doc = JsonDocument.Parse(text);
            var root = doc.RootElement;
            var ok = root.TryGetProperty("ok", out var okEl) && okEl.ValueKind == JsonValueKind.True;
            string? error = root.TryGetProperty("error", out var err) ? err.GetString() : null;
            JsonElement? result = root.TryGetProperty("result", out var value) ? value.Clone() : null;
            return new NxResponse(ok, error, result);
        }
        finally
        {
            if (ptr != 0)
            {
                NxNative.nx_free(ptr);
            }
        }
    }
}

public sealed record NxResponse(bool Ok, string? Error, JsonElement? Result);
