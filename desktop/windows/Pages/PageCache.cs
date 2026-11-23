namespace Nexus.Pages;

/// <summary>
/// Reuses shell pages so Back/navigation restores scroll, filters, and chat without refetching.
/// </summary>
public static class PageCache
{
    public const int MaxCachedSessions = 8;

    private static HomeHubPage? _homeHub;
    private static DashboardPage? _dashboard;
    private static MemoryPage? _memory;
    private static SettingsPage? _settings;
    private static readonly Dictionary<string, SessionPage> _sessions = new(StringComparer.OrdinalIgnoreCase);
    private static readonly LinkedList<string> _sessionLru = new();
    private static string _timelineProjectId = "";

    public static HomeHubPage HomeHub => _homeHub ??= new HomeHubPage();
    public static DashboardPage Dashboard => _dashboard ??= new DashboardPage();

    public static MemoryPage Memory => _memory ??= new MemoryPage();

    public static SettingsPage Settings => _settings ??= new SettingsPage();
    private static AgentSetupPage? _agentSetup;
    public static AgentSetupPage AgentSetup => _agentSetup ??= new AgentSetupPage();

    public static SessionPage GetSession(string sessionId)
    {
        if (sessionId.Length == 0)
        {
            return new SessionPage();
        }
        if (_sessions.TryGetValue(sessionId, out var page))
        {
            TouchSession(sessionId);
            return page;
        }
        page = new SessionPage();
        _sessions[sessionId] = page;
        _sessionLru.AddFirst(sessionId);
        TrimSessions();
        return page;
    }

    public static void InvalidateProject(string? previousProjectId, string newProjectId)
    {
        if (string.Equals(previousProjectId ?? "", newProjectId, StringComparison.OrdinalIgnoreCase))
        {
            return;
        }
        _timelineProjectId = "";
        _homeHub?.InvalidateForProjectChange();
    }

    public static bool TimelineNeedsRefresh(string projectId) =>
        projectId.Length == 0 ||
        !string.Equals(_timelineProjectId, projectId, StringComparison.OrdinalIgnoreCase);

    public static void MarkTimelineLoaded(string projectId) => _timelineProjectId = projectId;

    private static void TouchSession(string sessionId)
    {
        _sessionLru.Remove(sessionId);
        _sessionLru.AddFirst(sessionId);
    }

    private static void TrimSessions()
    {
        while (_sessionLru.Count > MaxCachedSessions)
        {
            var evict = _sessionLru.Last!.Value;
            _sessionLru.RemoveLast();
            _sessions.Remove(evict);
        }
    }
}
