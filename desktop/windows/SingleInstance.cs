using System.IO.Pipes;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Text;

namespace Nexus;

public static class SingleInstance
{
    private static Mutex? _mutex;
    private static NamedPipeServerStream? _server;
    private static string _token = "";
    private static bool _stopping;

    public static void Stop()
    {
        _stopping = true;
        Interlocked.Exchange(ref _server, null)?.Dispose();
        var mutex = Interlocked.Exchange(ref _mutex, null);
        if (mutex is null) return;
        try { mutex.ReleaseMutex(); } catch (ApplicationException) { }
        mutex.Dispose();
    }

    public static bool ClaimOrForward(string[] argv)
    {
        try
        {
            _mutex = new Mutex(initiallyOwned: true, MutexName(), out var createdNew);
            if (createdNew)
            {
                WriteToken();
                return true;
            }
        }
        catch (UnauthorizedAccessException)
        {
            return true;
        }

        var deadline = DateTime.UtcNow.AddSeconds(5);
        while (DateTime.UtcNow < deadline)
        {
            if (TryForward(argv))
            {
                return false;
            }
            Thread.Sleep(120);
        }

        try
        {
            if (_mutex.WaitOne(0))
            {
                WriteToken();
                return true;
            }
        }
        catch (AbandonedMutexException)
        {
            WriteToken();
            return true;
        }
        return false;
    }

    public static void Listen(Action<string> onCommand)
    {
        _token = ReadToken();
        if (_token.Length == 0)
        {
            WriteToken();
            _token = ReadToken();
        }
        try
        {
            _server = new NamedPipeServerStream(
                PipeName(),
                PipeDirection.In,
                1,
                PipeTransmissionMode.Byte,
                PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);
        }
        catch (IOException)
        {
            return;
        }
        _ = Task.Run(() => AcceptLoop(onCommand));
    }

    private static async Task AcceptLoop(Action<string> onCommand)
    {
        var server = _server;
        if (server is null)
        {
            return;
        }
        var buffer = new byte[8192];
        try
        {
        while (!_stopping)
        {
            await server.WaitForConnectionAsync().ConfigureAwait(false);
            using var body = new MemoryStream();
            int read;
            while ((read = await server.ReadAsync(buffer).ConfigureAwait(false)) > 0)
            {
                body.Write(buffer, 0, read);
            }
            var text = Encoding.UTF8.GetString(body.ToArray());
            server.Disconnect();
            var newline = text.IndexOf('\n');
            if (newline <= 0)
            {
                continue;
            }
            var token = text[..newline].TrimEnd('\r');
            if (!TokenMatches(token))
            {
                continue;
            }
            var payload = text[(newline + 1)..];
            if (payload.Length > 0)
            {
                onCommand(payload);
            }
        }
        }
        catch (ObjectDisposedException) when (_stopping) { }
        catch (IOException) when (_stopping) { }
    }

    private static bool TryForward(string[] argv)
    {
        var token = ReadToken();
        if (token.Length == 0)
        {
            return false;
        }
        try
        {
            using var client = new NamedPipeClientStream(".", PipeName(), PipeDirection.Out);
            client.Connect(400);
            using var writer = new StreamWriter(client, new UTF8Encoding(false)) { AutoFlush = true };
            writer.Write(token);
            writer.Write('\n');
            writer.Write(string.Join("\n", argv));
            return true;
        }
        catch (TimeoutException)
        {
            return false;
        }
        catch (IOException)
        {
            return false;
        }
    }

    private static bool TokenMatches(string candidate)
    {
        if (_token.Length == 0)
        {
            return false;
        }
        var actual = Encoding.UTF8.GetBytes(candidate);
        var expected = Encoding.UTF8.GetBytes(_token);
        return CryptographicOperations.FixedTimeEquals(actual, expected);
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
            File.Move(temp, path, true);
        }
        catch (IOException)
        {
        }
        catch (UnauthorizedAccessException)
        {
        }
    }

    private static string ReadToken()
    {
        try
        {
            var path = TokenPath();
            return File.Exists(path) ? File.ReadAllText(path).Trim() : "";
        }
        catch (IOException)
        {
            return "";
        }
        catch (UnauthorizedAccessException)
        {
            return "";
        }
    }

    private static string TokenPath()
    {
        var root = Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData);
        return Path.Combine(root, "Nexus", "instance.token");
    }

    private static string MutexName()
    {
        var sid = WindowsIdentity.GetCurrent().User?.Value ?? "user";
        return "Local\\Nexus-" + sid;
    }

    private static string PipeName()
    {
        var sid = WindowsIdentity.GetCurrent().User?.Value ?? "user";
        return "nexus-" + sid;
    }
}
