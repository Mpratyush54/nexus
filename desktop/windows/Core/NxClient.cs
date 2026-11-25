using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;
using System.Text.Json;
using Microsoft.UI.Dispatching;

namespace Nexus.Core;

public sealed class NxClient
{
    private static DispatcherQueue? _queue;
    private static Action<string>? _onEvent;
    private static NxClient? _subscribedClient;
    public event Action<string>? EventReceived;

    // Each nx_call blocks a pool thread for the whole network round trip.
    // Opening a chat starts about ten at once; with the default minimum (one
    // thread per core) the rest queued behind the pool's slow thread injection
    // and the chat waited seconds before its requests were even sent.
    static NxClient()
    {
        ThreadPool.GetMinThreads(out var workers, out var io);
        if (workers < 32) ThreadPool.SetMinThreads(32, io);
    }

    public bool Init(string configJson) => NxNative.nx_init(configJson) == 0;

    public void Shutdown() => NxNative.nx_shutdown();

    /// <summary>
    /// Registers the core callback. The shim copies the payload and posts it
    /// to the UI queue. It must not call nx_call on the Go thread.
    /// </summary>
    public unsafe void Subscribe(DispatcherQueue queue, Action<string> onEvent)
    {
        _queue = queue;
        _onEvent = onEvent;
        _subscribedClient = this;
        NxNative.nx_subscribe(&OnCoreEvent);
    }

    [UnmanagedCallersOnly(CallConvs = [typeof(CallConvCdecl)])]
    private static void OnCoreEvent(nint json)
    {
        var copy = Marshal.PtrToStringUTF8(json) ?? "";
        _queue?.TryEnqueue(() => { _subscribedClient?.EventReceived?.Invoke(copy); _onEvent?.Invoke(copy); });
    }

    public Task<NxResponse> CallAsync(string method, object? args = null, CancellationToken ct = default)
    {
        var json = args is null ? "{}" : JsonSerializer.Serialize(args);
        return Task.Run(() => Call(method, json), ct);
    }

    /// <summary>Raw call without error mapping or reachability tracking (the reachability probe itself).</summary>
    internal static (bool Ok, JsonElement? Result) CallDirect(string method, string json)
    {
        var (ok, _, result) = Invoke(method, json);
        return (ok, result);
    }

    private static NxResponse Call(string method, string json)
    {
        var (ok, raw, result) = Invoke(method, json);
        if (ok)
        {
            if (Connectivity.IsServerMethod(method)) Connectivity.NoteReachable();
            return new NxResponse(true, raw, result, raw);
        }
        // People see a plain sentence with a reference id; the raw engine
        // text goes to the log under that id (method and error only:
        // arguments may hold chat text).
        string shown;
        if (NxErrors.IsSlow(raw))
        {
            // No answer in time (or cut off): this call was slow, which says
            // nothing about the server being down. The probe checks; the
            // card that asked shows its own calm state.
            var reference = Connectivity.NoteUnreachable(method, raw ?? "", slow: true);
            shown = NxErrors.SlowMessage + " (ref " + reference + ")";
        }
        else if (NxErrors.IsUnreachable(raw))
        {
            var reference = Connectivity.NoteUnreachable(method, raw ?? "");
            shown = NxErrors.UnreachableMessage + " (ref " + reference + ")";
        }
        else
        {
            var (message, reference) = NxErrors.Friendly(raw);
            AppDiagnostics.NoteCallFailure(method, reference.Length > 0 ? $"{raw} [ref {reference}]" : raw);
            shown = message.Length > 0 ? message : "Something went wrong.";
        }
        return new NxResponse(false, shown, result, raw);
    }

    private static (bool Ok, string? Error, JsonElement? Result) Invoke(string method, string json)
    {
        var ptr = NxNative.nx_call(method, json);
        try
        {
            if (ptr == 0) return (false, "empty core response", null);
            var text = System.Runtime.InteropServices.Marshal.PtrToStringUTF8(ptr) ?? "";
            JsonDocument doc;
            try { doc = JsonDocument.Parse(text); }
            catch (JsonException ex)
            {
                Diagnostics.DiagLog.Error("nx", $"{method}: unreadable core response", ex);
                return (false, "unreadable core response", null);
            }
            using (doc)
            {
                var root = doc.RootElement;
                var ok = root.TryGetProperty("ok", out var okEl) && okEl.ValueKind == JsonValueKind.True;
                string? error = root.TryGetProperty("error", out var err) && err.ValueKind == JsonValueKind.String ? err.GetString() : null;
                JsonElement? result = root.TryGetProperty("result", out var value) ? value.Clone() : null;
                return (ok, error, result);
            }
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

/// <summary>
/// One nx_call answer. <see cref="Error"/> is safe to show (a plain sentence,
/// with a reference id when it stands for an engine error);
/// <see cref="RawError"/> is the engine's text, for logic only.
/// </summary>
public sealed record NxResponse(bool Ok, string? Error, JsonElement? Result, string? RawError = null);
