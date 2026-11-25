#nullable enable
using System.Globalization;

namespace Nexus.Core;

/// <summary>One way to say "when" everywhere: "just now", "5 minutes ago", "1 hour ago", "yesterday", "3 days ago", then a date.</summary>
internal static class FriendlyTime
{
    public static string Relative(DateTimeOffset at, DateTimeOffset? now = null)
    {
        var current = now ?? DateTimeOffset.Now;
        var local = at.ToLocalTime();
        var age = current - local;
        if (age < TimeSpan.FromMinutes(1)) return "just now";
        if (age < TimeSpan.FromHours(1)) return Count((int)age.TotalMinutes, "minute") + " ago";
        if (age < TimeSpan.FromDays(1) && local.Date == current.ToLocalTime().Date) return Count((int)age.TotalHours, "hour") + " ago";
        var days = (current.ToLocalTime().Date - local.Date).Days;
        if (days <= 1) return "yesterday";
        if (days < 7) return Count(days, "day") + " ago";
        return local.ToString(local.Year == current.Year ? "MMM d" : "MMM d, yyyy", CultureInfo.CurrentCulture);
    }

    public static string Relative(string raw)
    {
        if (!DateTimeOffset.TryParse(raw, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var at) || at.Year < 2000) return "";
        return Relative(at);
    }

    /// <summary>"1 device", "2 devices".</summary>
    public static string Count(long n, string singular, string? plural = null) =>
        $"{n:N0} {(n == 1 ? singular : plural ?? singular + "s")}";
}
