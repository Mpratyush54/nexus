using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;

namespace Nexus;

/// <summary>
/// Uno/macOS single instance.
/// - Ownership: an exclusive lock (flock) on instance.lock in the per-user (or
///   NEXUS_PROFILE) data folder. The OS drops it when the process dies, so a
///   crash never leaves a stale claim.
/// - Hand-off: the owner listens on a Unix domain socket, instance.sock, next
///   to the lock (mode 0600). A second launch connects, sends the token from
///   instance.token and its arguments, one per line, and exits. That is the
///   same payload the Windows named pipe carries, and the owner handles it
///   with MainWindow.HandleRemote (nexus:// links, --quit).
/// nexus:// links opened from other apps arrive as Apple events instead
/// (libnexusmac); this path covers a second launch of the executable.
/// </summary>
public static class SingleInstance
{
    private const int MaxSocketPath = 100; // sun_path is 104 bytes on macOS
    private static FileStream? _lock;
    private static Socket? _server;
    private static string _token = "";
    private static volatile bool _stopping;

    public static bool ClaimOrForward(string[] argv)
    {
        if (_lock is not null) return true;
        if (TryLock()) return true;

        var deadline = DateTime.UtcNow.AddSeconds(5);
        while (DateTime.UtcNow < deadline)
        {
            if (TryForward(argv)) return false;
            // The owner may have just exited.
            if (TryLock()) return true;
            Thread.Sleep(150);
        }
        // Another Nexus holds the lock but does not answer; do not run twice.
        return false;
    }

    private static bool TryLock()
    {
        var path = Path.Combine(Folder(), "instance.lock");
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            _lock = new FileStream(path, FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None);
            _lock.SetLength(0);
            _lock.Write(Encoding.UTF8.GetBytes(Environment.ProcessId.ToString()));
            _lock.Flush();
            WriteToken();
            return true;
        }
        catch (IOException)
        {
            return false;
        }
        catch (UnauthorizedAccessException)
        {
            // Cannot tell; running twice beats not running.
            return true;
        }
    }

    public static void Listen(Action<string> onCommand)
    {
        if (_server is not null) return;
        _token = ReadToken();
        if (_token.Length == 0)
        {
            WriteToken();
            _token = ReadToken();
        }
        var path = SocketPath();
        try
        {
            // We hold the lock, so any socket file left here is stale.
            if (File.Exists(path)) File.Delete(path);
            var server = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
            server.Bind(new UnixDomainSocketEndPoint(path));
            if (!OperatingSystem.IsWindows())
            {
                File.SetUnixFileMode(path, UnixFileMode.UserRead | UnixFileMode.UserWrite);
            }
            server.Listen(4);
            _server = server;
        }
        catch (Exception ex) when (ex is SocketException or IOException or UnauthorizedAccessException)
        {
            Nexus.Core.Diagnostics.DiagLog.Warn("startup", "single-instance socket unavailable; a second launch cannot hand off", ex);
            return;
        }
        _ = Task.Run(() => AcceptLoop(onCommand));
    }

    private static async Task AcceptLoop(Action<string> onCommand)
    {
        var server = _server;
        if (server is null) return;
        var buffer = new byte[8192];
        while (!_stopping)
        {
            Socket client;
            try
            {
                client = await server.AcceptAsync().ConfigureAwait(false);
            }
            catch (Exception) when (_stopping)
            {
                return;
            }
            catch (SocketException)
            {
                continue;
            }
            catch (ObjectDisposedException)
            {
                return;
            }
            try
            {
                using (client)
                {
                    using var body = new MemoryStream();
                    using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(3));
                    int read;
                    while ((read = await client.ReceiveAsync(buffer, SocketFlags.None, cts.Token).ConfigureAwait(false)) > 0)
                    {
                        body.Write(buffer, 0, read);
                        if (body.Length > 64 * 1024) break;
                    }
                    var text = Encoding.UTF8.GetString(body.ToArray());
                    var newline = text.IndexOf('\n');
                    if (newline <= 0 || !TokenMatches(text[..newline].TrimEnd('\r'))) continue;
                    var payload = text[(newline + 1)..];
                    Nexus.Core.Diagnostics.DiagLog.Info("startup", "second launch handed over its arguments");
                    Console.Error.WriteLine("[nexus-mac] second launch handed over its arguments");
                    if (payload.Length > 0) onCommand(payload);
                }
            }
            catch (Exception ex) when (ex is SocketException or OperationCanceledException or IOException)
            {
            }
        }
    }

    private static bool TryForward(string[] argv)
    {
        var token = ReadToken();
        if (token.Length == 0) return false;
        try
        {
            using var client = new Socket(AddressFamily.Unix, SocketType.Stream, ProtocolType.Unspecified);
            client.Connect(new UnixDomainSocketEndPoint(SocketPath()));
            var bytes = Encoding.UTF8.GetBytes(token + "\n" + string.Join("\n", argv));
            client.Send(bytes);
            client.Shutdown(SocketShutdown.Send);
            return true;
        }
        catch (SocketException)
        {
            return false;
        }
    }

    public static void Stop()
    {
        _stopping = true;
        var server = Interlocked.Exchange(ref _server, null);
        if (server is not null)
        {
            try { server.Dispose(); } catch (SocketException) { }
            try { File.Delete(SocketPath()); } catch (IOException) { } catch (UnauthorizedAccessException) { }
        }
        Interlocked.Exchange(ref _lock, null)?.Dispose();
    }

    private static bool TokenMatches(string candidate)
    {
        if (_token.Length == 0) return false;
        return CryptographicOperations.FixedTimeEquals(Encoding.UTF8.GetBytes(candidate), Encoding.UTF8.GetBytes(_token));
    }

    private static void WriteToken()
    {
        try
        {
            var path = TokenPath();
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            var token = Convert.ToHexString(RandomNumberGenerator.GetBytes(32));
            var temp = path + "." + Environment.ProcessId;
            File.WriteAllText(temp, token, new UTF8Encoding(false));
            if (!OperatingSystem.IsWindows())
            {
                File.SetUnixFileMode(temp, UnixFileMode.UserRead | UnixFileMode.UserWrite);
            }
            File.Move(temp, path, true);
        }
        catch (IOException) { }
        catch (UnauthorizedAccessException) { }
    }

    private static string ReadToken()
    {
        try
        {
            var path = TokenPath();
            return File.Exists(path) ? File.ReadAllText(path).Trim() : "";
        }
        catch (IOException) { return ""; }
        catch (UnauthorizedAccessException) { return ""; }
    }

    private static string Folder() =>
        Path.Combine(Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData), "Nexus");

    private static string TokenPath() => Path.Combine(Folder(), "instance.token");

    internal static string SocketPath()
    {
        var path = Path.Combine(Folder(), "instance.sock");
        if (Encoding.UTF8.GetByteCount(path) <= MaxSocketPath) return path;
        // A very long home folder: fall back to the per-user temp folder, keyed
        // by the data folder so profiles stay apart.
        var key = Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(Folder())))[..16].ToLowerInvariant();
        return Path.Combine(Path.GetTempPath(), $"nexus-{key}.sock");
    }
}
