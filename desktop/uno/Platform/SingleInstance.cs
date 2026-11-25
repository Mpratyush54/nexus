using System.Text;

namespace Nexus;

/// <summary>
/// Uno/macOS single instance: an exclusive lock on a file in the per-user
/// (or NEXUS_PROFILE) data folder. The OS drops the lock when the process
/// dies, so a crash never leaves a stale claim.
/// TODO(mac): forward the second launch's arguments (nexus:// links) to the
/// first instance over a Unix domain socket next to the lock. On macOS,
/// LaunchServices already routes nexus:// URLs and Dock clicks to the
/// running app, so this matters less than on Windows.
/// </summary>
public static class SingleInstance
{
    private static FileStream? _lock;

    public static bool ClaimOrForward(string[] argv)
    {
        var path = LockPath();
        try
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path)!);
            _lock = new FileStream(path, FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None);
            _lock.SetLength(0);
            _lock.Write(Encoding.UTF8.GetBytes(Environment.ProcessId.ToString()));
            _lock.Flush();
            return true;
        }
        catch (IOException)
        {
            // Another Nexus holds the lock.
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
    }

    public static void Stop()
    {
        var held = Interlocked.Exchange(ref _lock, null);
        held?.Dispose();
    }

    private static string LockPath()
    {
        var root = Nexus.Core.ProfilePaths.Folder(Environment.SpecialFolder.LocalApplicationData);
        return Path.Combine(root, "Nexus", "instance.lock");
    }
}
