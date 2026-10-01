using System.IO.Pipes;
using System.Security.Principal;
using System.Text;

namespace Nexus;

public static class SingleInstance
{
    private static NamedPipeServerStream? _server;

    public static bool ClaimOrForward(string[] argv)
    {
        try
        {
            using var client = new NamedPipeClientStream(".", PipeName(), PipeDirection.Out);
            client.Connect(250);
            using var writer = new StreamWriter(client, Encoding.UTF8) { AutoFlush = true };
            writer.Write(string.Join("\n", argv));
            return false;
        }
        catch (TimeoutException)
        {
            return true;
        }
        catch (IOException)
        {
            return true;
        }
    }

    public static void Listen(Action<string> onCommand)
    {
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
        while (true)
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
            if (text.Length > 0)
            {
                onCommand(text);
            }
        }
    }

    private static string PipeName()
    {
        var sid = WindowsIdentity.GetCurrent().User?.Value ?? "user";
        return "nexus-" + sid;
    }
}
