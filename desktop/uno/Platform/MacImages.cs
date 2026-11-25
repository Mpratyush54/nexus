using System.Diagnostics;
using Windows.Storage.Streams;

namespace Nexus.Pages;

/// <summary>
/// Image work for the chat composer on macOS. Uno implements neither
/// BitmapDecoder nor BitmapEncoder, so a pasted screenshot could not be
/// attached and a photo over 5 MB could not be shrunk. macOS ships sips,
/// which converts and resizes without any extra dependency.
/// </summary>
internal static class MacImages
{
    /// <summary>Saves pasteboard image bytes (PNG or TIFF) as a PNG in <paramref name="folder"/>.</summary>
    public static async Task<string> SavePastedAsync(IRandomAccessStreamWithContentType source, string folder)
    {
        Directory.CreateDirectory(folder);
        var id = Guid.NewGuid().ToString("N");
        var raw = Path.Combine(folder, "Pasted-" + id + ".raw");
        using (source)
        await using (var output = File.Create(raw))
        {
            await source.AsStreamForRead().CopyToAsync(output);
        }
        var png = Path.Combine(folder, "Pasted-" + id + ".png");
        if (IsPng(raw))
        {
            File.Move(raw, png);
            return png;
        }
        try
        {
            await SipsAsync("-s", "format", "png", raw, "--out", png);
            return png;
        }
        finally
        {
            File.Delete(raw);
        }
    }

    /// <summary>A JPEG copy with its longest side at each size in turn, until it fits.</summary>
    public static async Task<string> ShrinkAsync(string path, string folder, IEnumerable<int> sides, long maxBytes)
    {
        Directory.CreateDirectory(folder);
        var name = Path.GetFileNameWithoutExtension(path);
        foreach (var side in sides)
        {
            var target = Path.Combine(folder, name + "-resized-" + Guid.NewGuid().ToString("N")[..8] + ".jpg");
            await SipsAsync("-Z", side.ToString(), "-s", "format", "jpeg", "-s", "formatOptions", "85", path, "--out", target);
            if (File.Exists(target) && new FileInfo(target).Length <= maxBytes) return target;
            File.Delete(target);
        }
        throw new InvalidOperationException("the image is still over 5 MB after resizing");
    }

    private static bool IsPng(string path)
    {
        Span<byte> head = stackalloc byte[8];
        using var f = File.OpenRead(path);
        return f.Read(head) == 8 && head[0] == 0x89 && head[1] == (byte)'P' && head[2] == (byte)'N' && head[3] == (byte)'G';
    }

    private static async Task SipsAsync(params string[] args)
    {
        var info = new ProcessStartInfo("/usr/bin/sips") { UseShellExecute = false, RedirectStandardOutput = true, RedirectStandardError = true };
        foreach (var a in args) info.ArgumentList.Add(a);
        using var process = Process.Start(info) ?? throw new InvalidOperationException("sips did not start");
        var stderr = process.StandardError.ReadToEndAsync();
        _ = process.StandardOutput.ReadToEndAsync();
        using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(30));
        await process.WaitForExitAsync(timeout.Token);
        if (process.ExitCode != 0) throw new InvalidOperationException("sips failed: " + (await stderr).Trim());
    }
}
