using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using Microsoft.Win32;

namespace Nexus.Installer;

/// <summary>What the bundled release is: written by package-windows.ps1.</summary>
internal sealed class Payload
{
    public string Version = "";
    public long Bytes;
    public int Files;

    public static Payload Read()
    {
        var p = new Payload();
        using var s = Assembly.GetExecutingAssembly().GetManifestResourceStream("payload.json");
        if (s is null) return p;
        var json = new StreamReader(s).ReadToEnd();
        p.Version = Match(json, "version");
        long.TryParse(Match(json, "bytes"), out p.Bytes);
        int.TryParse(Match(json, "files"), out p.Files);
        return p;
    }

    private static string Match(string json, string key) =>
        Regex.Match(json, "\"" + key + "\"\\s*:\\s*\"?([^\",}]*)").Groups[1].Value.Trim();

    public static bool HasSetup => Assembly.GetExecutingAssembly().GetManifestResourceInfo("payload.Setup.exe") is not null;
}

/// <summary>An existing installation (from Velopack's uninstall entry).</summary>
internal sealed class Existing
{
    public string Version = "";
    public string Root = "";

    public static Existing? Find()
    {
        using var key = Registry.CurrentUser.OpenSubKey(InstallEngine.UninstallKey);
        if (key is null) return null;
        var root = key.GetValue("InstallLocation") as string ?? "";
        if (root.Length == 0 || !Directory.Exists(root)) return null;
        // The version Velopack keeps with the app (full, e.g. 0.4.0-beta.6);
        // DisplayVersion was written without the prerelease label by old installs.
        var version = "";
        try
        {
            var manifest = Path.Combine(root, "current", "sq.version");
            if (File.Exists(manifest)) version = Regex.Match(File.ReadAllText(manifest), "<version>([^<]+)</version>").Groups[1].Value.Trim();
        }
        catch (IOException) { }
        catch (UnauthorizedAccessException) { }
        if (version.Length == 0) version = key.GetValue("DisplayVersion") as string ?? "";
        return new Existing { Version = version, Root = root };
    }

    public IEnumerable<Process> Running() => InstallEngine.ProcessesUnder(Root);
}

internal sealed record Progress(double Percent, string Status);

/// <summary>
/// Runs Velopack's Setup.exe silently and reports real progress from what it
/// does on disk, in the order Velopack does it: Update.exe (5%), the package
/// copied (10%), the app extracted into current\ (10-90%, by bytes), Start
/// menu shortcut, nexus:// registered by the app's install hook, uninstall
/// entry, exit code 0 (100%).
/// </summary>
internal sealed class InstallEngine
{
    public const string PackId = "NexusDesktop";
    public const string UninstallKey = @"Software\Microsoft\Windows\CurrentVersion\Uninstall\" + PackId;

    public static string DefaultRoot =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), PackId);

    public string Root { get; }
    public string LogPath { get; } = Path.Combine(Path.GetTempPath(), "NexusSetup.log");
    public string MainExe => Path.Combine(Root, "current", "Nexus.exe");
    private readonly Payload _payload;

    public InstallEngine(string root, Payload payload)
    {
        Root = root;
        _payload = payload;
    }

    public async Task RunAsync(IProgress<Progress> progress, CancellationToken ct)
    {
        progress.Report(new Progress(1, "Preparing…"));
        var setup = await Task.Run(ExtractSetup, ct).ConfigureAwait(false);
        var args = $"--silent --log \"{LogPath}\"";
        if (!string.Equals(Path.GetFullPath(Root).TrimEnd('\\'), Path.GetFullPath(DefaultRoot).TrimEnd('\\'), StringComparison.OrdinalIgnoreCase))
            args += $" --installto \"{Root}\"";
        var psi = new ProcessStartInfo(setup, args) { UseShellExecute = false, CreateNoWindow = true };
        using var process = Process.Start(psi) ?? throw new InvalidOperationException("Setup could not start.");
        var last = 1.0;
        var status = "Unpacking Nexus…";
        while (!process.HasExited)
        {
            ct.ThrowIfCancellationRequested();
            var (pct, text) = Observe(status);
            status = text;
            last = Math.Max(last, pct);          // never moves backwards
            progress.Report(new Progress(last, status));
            await Task.Delay(120, ct).ConfigureAwait(false);
        }
        TryDelete(setup);
        if (process.ExitCode != 0)
            throw new InstallFailedException(LastLogError() ?? "Setup stopped with an error.", LogPath);
        if (!File.Exists(MainExe))
            throw new InstallFailedException("Setup finished, but Nexus.exe is missing.", LogPath);
        // The app's install hook also does this; doing it here too covers
        // installs whose hook could not run.
        RetireLegacyShortcuts();
        // Velopack writes DisplayVersion without the prerelease label.
        try
        {
            using var key = Registry.CurrentUser.OpenSubKey(UninstallKey, true);
            if (key is not null && _payload.Version.Length > 0) key.SetValue("DisplayVersion", _payload.Version);
        }
        catch (Exception ex) when (ex is UnauthorizedAccessException or System.Security.SecurityException or IOException) { }
        progress.Report(new Progress(100, "Done"));
    }

    private (double, string) Observe(string status)
    {
        var current = Path.Combine(Root, "current");
        var pct = 2.0;
        if (File.Exists(Path.Combine(Root, "Update.exe"))) { pct = 5; status = "Unpacking Nexus…"; }
        if (Directory.Exists(Path.Combine(Root, "packages")) && Directory.EnumerateFiles(Path.Combine(Root, "packages"), "*.nupkg").Any()) pct = 10;
        if (Directory.Exists(current))
        {
            long bytes = 0;
            try
            {
                foreach (var f in new DirectoryInfo(current).EnumerateFiles("*", SearchOption.AllDirectories)) bytes += f.Length;
            }
            catch (IOException) { }
            catch (UnauthorizedAccessException) { }
            var share = _payload.Bytes > 0 ? Math.Min(1.0, bytes / (double)_payload.Bytes) : 0;
            pct = Math.Max(pct, 10 + share * 80);
            if (File.Exists(Path.Combine(current, "Microsoft.UI.Xaml.dll"))) status = "Installing the Windows App runtime…";
            if (File.Exists(Path.Combine(current, "nexuscore.dll"))) status = "Setting up the capture engine…";
            if (File.Exists(Path.Combine(current, "cli", "nexus.exe"))) status = "Adding the nexus command line…";
        }
        var startMenu = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Programs), "Nexus.lnk");
        if (File.Exists(startMenu) && pct >= 89) { pct = 92; status = "Adding Nexus to Start…"; }
        if (ProtocolPointsInto(Root) && pct >= 89) { pct = 96; status = "Registering nexus:// links…"; }
        if (Registry.CurrentUser.OpenSubKey(UninstallKey) is { } key && pct >= 95)
        {
            key.Dispose();
            pct = 99;
            status = "Finishing up…";
        }
        return (pct, status);
    }

    private static bool ProtocolPointsInto(string root)
    {
        using var key = Registry.CurrentUser.OpenSubKey(@"Software\Classes\nexus\shell\open\command");
        return key?.GetValue(null) is string cmd && cmd.IndexOf(root, StringComparison.OrdinalIgnoreCase) >= 0;
    }

    private static string ExtractSetup()
    {
        var dir = Path.Combine(Path.GetTempPath(), "NexusSetup-" + Guid.NewGuid().ToString("N").Substring(0, 8));
        Directory.CreateDirectory(dir);
        var path = Path.Combine(dir, "Setup.exe");
        using var src = Assembly.GetExecutingAssembly().GetManifestResourceStream("payload.Setup.exe")
            ?? throw new InstallFailedException("This installer is missing its package. Download it again.", "");
        using var dst = File.Create(path);
        src.CopyTo(dst, 1 << 20);
        return path;
    }

    private string? LastLogError()
    {
        try
        {
            if (!File.Exists(LogPath)) return null;
            var line = File.ReadAllLines(LogPath).LastOrDefault(l => l.IndexOf("ERROR", StringComparison.OrdinalIgnoreCase) >= 0);
            if (line is null) return null;
            var i = line.IndexOf("ERROR", StringComparison.OrdinalIgnoreCase);
            return line.Substring(i + 5).Trim(' ', ']', ':');
        }
        catch (IOException) { return null; }
    }

    private static void TryDelete(string setup)
    {
        try { Directory.Delete(Path.GetDirectoryName(setup)!, true); } catch (IOException) { } catch (UnauthorizedAccessException) { }
    }

    public static IEnumerable<Process> ProcessesUnder(string root)
    {
        foreach (var p in Process.GetProcessesByName("Nexus"))
        {
            string path;
            try { path = p.MainModule?.FileName ?? ""; }
            catch (Exception) { continue; }
            if (path.StartsWith(root.TrimEnd('\\') + "\\", StringComparison.OrdinalIgnoreCase)) yield return p;
        }
    }

    // ---- shortcuts from older Nexus setups (same rules as the app's LegacyInstall)

    private static string LegacyBin =>
        Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "Nexus", "bin");

    internal static bool IsLegacyTarget(string target, string installRoot)
    {
        if (string.IsNullOrWhiteSpace(target)) return false;
        if (IsUnder(target, installRoot)) return false;
        if (IsUnder(target, LegacyBin)) return true;
        var name = Path.GetFileName(target);
        if (name.StartsWith("nexus-desktop", StringComparison.OrdinalIgnoreCase) || name.StartsWith("nexus-daemon", StringComparison.OrdinalIgnoreCase)) return true;
        return name.Equals("Nexus.exe", StringComparison.OrdinalIgnoreCase) && !File.Exists(target);
    }

    private static bool IsUnder(string path, string root)
    {
        if (path.Length == 0 || root.Length == 0) return false;
        try
        {
            var dir = Path.GetFullPath(root).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar;
            return Path.GetFullPath(path).StartsWith(dir, StringComparison.OrdinalIgnoreCase);
        }
        catch (ArgumentException) { return false; }
        catch (NotSupportedException) { return false; }
    }

    private static IEnumerable<string> Links(string dir, string pattern)
    {
        try { return Directory.Exists(dir) ? Directory.GetFiles(dir, pattern) : Array.Empty<string>(); }
        catch (IOException) { return Array.Empty<string>(); }
        catch (UnauthorizedAccessException) { return Array.Empty<string>(); }
    }

    public void RetireLegacyShortcuts()
    {
        bool Legacy(string link) => IsLegacyTarget(ShellLink.Target(link), Root);
        void Try(Action a) { try { a(); } catch (IOException) { } catch (UnauthorizedAccessException) { } catch (System.Runtime.InteropServices.COMException) { } }

        foreach (var link in Links(Environment.GetFolderPath(Environment.SpecialFolder.Startup), "*Nexus*.lnk").Where(Legacy))
        {
            Try(() => SetStartAtLogin(true));
            Try(() => File.Delete(link));
        }
        var programs = Environment.GetFolderPath(Environment.SpecialFolder.Programs);
        var folder = Path.Combine(programs, "Nexus");
        foreach (var link in Links(folder, "*.lnk").Concat(Links(programs, "Nexus Desktop.lnk")).Where(Legacy))
            Try(() => File.Delete(link));
        Try(() => { if (Directory.Exists(folder) && !Directory.EnumerateFileSystemEntries(folder).Any()) Directory.Delete(folder); });
        var hadDesktop = false;
        foreach (var link in Links(Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), "Nexus*.lnk").Where(Legacy))
        {
            hadDesktop = true;
            Try(() => File.Delete(link));
        }
        if (hadDesktop) Try(() => SetDesktopShortcut(true));
    }

    // ---- options applied after install (same formats the app itself uses)

    public void SetDesktopShortcut(bool on)
    {
        var link = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), "Nexus.lnk");
        if (!on) return;
        ShellLink.Write(link, MainExe, "Nexus: your coding agents' memory");
    }

    public void SetStartAtLogin(bool on)
    {
        using var key = Registry.CurrentUser.CreateSubKey(@"Software\Microsoft\Windows\CurrentVersion\Run");
        if (key is null) return;
        if (on) key.SetValue("Nexus", $"\"{MainExe}\" --background");
    }
}

internal sealed class InstallFailedException : Exception
{
    public string LogPath { get; }
    public InstallFailedException(string message, string logPath) : base(message) => LogPath = logPath;
}
