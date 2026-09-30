import AppKit
import Foundation

enum NavSection: String, CaseIterable, Identifiable {
    case timeline = "Timeline"
    case memory = "Memory"
    case teleport = "Teleport"
    case agents = "Agents"
    case settings = "Settings"

    var id: String { rawValue }

    var symbol: String {
        switch self {
        case .timeline: return "clock"
        case .memory: return "brain"
        case .teleport: return "arrow.left.arrow.right"
        case .agents: return "cpu"
        case .settings: return "gearshape"
        }
    }
}

struct NxItem: Identifiable {
    var id: String
    var key: String
    var title: String
    var subtitle: String
    var status: String
}

@MainActor
final class ShellModel: ObservableObject {
    static let shared = ShellModel()

    @Published var section: NavSection? = .timeline
    @Published var sessionId: String?
    @Published var teleportId = ""
    @Published var offline = false
    @Published var banner = ""
    @Published var capturePaused = false
    @Published var inboxCount = 0
    @Published var searchOpen = false
    @Published var searchText = ""
    @Published var searchSessions: [NxItem] = []
    @Published var searchMemories: [NxItem] = []
    @Published var searchProjects: [NxItem] = []
    @Published var timeline: [NxItem] = []
    @Published var memories: [NxItem] = []
    @Published var teleports: [NxItem] = []
    @Published var projectFilter = ""
    @Published var agentFilter = ""
    @Published var machineFilter = ""
    @Published var personFilter = ""
    @Published var memoryQuery = ""
    @Published var memoryScope = "project"
    @Published var sessionTitle = ""
    @Published var sessionTurns: [NxItem] = []
    @Published var sessionFiles: [NxItem] = []
    @Published var sessionCommands: [NxItem] = []
    @Published var sessionMemories: [NxItem] = []
    @Published var sessionSummary = ""
    @Published var sessionContext = ""
    @Published var message = ""
    @Published var people = ""
    @Published var shareTeam = false
    @Published var shareLive = true
    @Published var confirmTeam = false
    @Published var resumeMode = "native"
    @Published var seedAgent = AgentCatalog.all[0].name
    @Published var statusText = ""
    @Published var preview = ""
    @Published var loginEnabled = false
    @Published var pageError = ""

    func ingest(_ json: String) {
        if json.localizedCaseInsensitiveContains("teleport") {
            Task { await loadTeleports() }
        }
        Task { await refreshBanner() }
    }

    func showWindow() {
        NSApp.activate(ignoringOtherApps: true)
        for window in NSApp.windows where window.canBecomeMain {
            window.makeKeyAndOrderFront(nil)
            return
        }
    }

    func openTeleport() {
        section = .teleport
        sessionId = nil
        showWindow()
    }

    func openSession(_ id: String) {
        sessionId = id
        showWindow()
        Task { await loadSession() }
    }

    func handle(_ url: URL) {
        let host = url.host?.lowercased() ?? ""
        let id = url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        switch host {
        case "session":
            openSession(id)
        case "teleport":
            teleportId = id
            openTeleport()
            Task { await loadTeleports() }
        case "auth":
            Task { _ = await NxClient.shared.call(NxMethods.authCallback, args: ["url": url.absoluteString]) }
        default:
            break
        }
    }

    func toggleCapture() {
        let method = capturePaused ? NxMethods.captureResume : NxMethods.capturePause
        Task {
            let response = await NxClient.shared.call(method)
            if let error = response.error, error.contains("handled by the app shell") {
                capturePaused.toggle()
                return
            }
            if !response.ok {
                pageError = response.error ?? "Capture toggle failed"
            }
        }
    }

    func quit() {
        NxClient.shared.shutdown()
        NSApp.terminate(nil)
    }

    func refreshBanner() async {
        let net = await NxClient.shared.call(NxMethods.netStatus)
        let uploads = await NxClient.shared.call(NxMethods.uploadsStatus)
        let timeline = await NxClient.shared.call(NxMethods.timelineList)
        let online = (NxJSON.object(net.raw)["online"] as? Bool) ?? true
        let pending = (NxJSON.object(uploads.raw)["pending"] as? NSNumber)?.intValue ?? 0
        let timelineOffline = timeline.error?.localizedCaseInsensitiveContains("offline") == true
        offline = !online || timelineOffline
        banner = offline
            ? "Offline: showing cached data, read-only. Capture continues (\(pending) items waiting to upload)."
            : ""
    }

    func loadTimeline() async {
        let response = await NxClient.shared.call(NxMethods.timelineList, args: [
            "project": projectFilter,
            "agent": agentFilter,
            "machine": machineFilter,
            "person": personFilter,
            "q": searchText
        ])
        pageError = response.ok ? "" : (response.error ?? "")
        timeline = Self.items(response, title: ["title", "summary", "preview", "kind"], subtitle: ["agent", "harness", "project"])
    }

    func loadMemories() async {
        let response = await NxClient.shared.call(NxMethods.memorySearch, args: [
            "q": memoryQuery,
            "scope": memoryScope
        ])
        pageError = response.ok ? "" : (response.error ?? "")
        memories = Self.items(response, title: ["text", "content", "title"], subtitle: ["scope"])
    }

    func loadTeleports() async {
        let response = await NxClient.shared.call(NxMethods.teleportInbox)
        pageError = response.ok ? "" : (response.error ?? "")
        teleports = Self.items(response, title: ["preview", "note", "summary"], subtitle: ["id"])
        inboxCount = teleports.count
    }

    func loadSent() async {
        let response = await NxClient.shared.call(NxMethods.teleportSent)
        pageError = response.ok ? "" : (response.error ?? "")
        teleports = Self.items(response, title: ["preview", "note", "summary"], subtitle: ["id"])
    }

    func loadSession() async {
        guard let sessionId, !sessionId.isEmpty else { return }
        let args: [String: Any] = ["session_id": sessionId, "id": sessionId]
        let session = await NxClient.shared.call(NxMethods.sessionsGet, args: args)
        let turns = await NxClient.shared.call(NxMethods.sessionsTurns, args: args)
        let files = await NxClient.shared.call(NxMethods.sessionsFiles, args: args)
        let ops = await NxClient.shared.call(NxMethods.sessionsOperations, args: args)
        let mems = await NxClient.shared.call(NxMethods.sessionsMemories, args: args)
        let summary = await NxClient.shared.call(NxMethods.sessionsSummary, args: args)
        let body = NxJSON.object(session.raw)
        let title = NxJSON.text(body, "title", "summary", "preview")
        let agent = NxJSON.text(body, "agent", "harness")
        sessionTitle = [agent, title, sessionId].filter { !$0.isEmpty }.joined(separator: " · ")
        if !session.ok {
            sessionTitle = session.error ?? sessionId
        }
        sessionTurns = Self.items(turns, title: ["text", "content", "message"], subtitle: ["role"])
        sessionFiles = Self.items(files, title: ["path", "name"], subtitle: [])
        sessionCommands = Self.items(ops, title: ["command", "cmd", "text"], subtitle: [])
        sessionMemories = Self.items(mems, title: ["text", "content", "title"], subtitle: ["status"])
        sessionSummary = summary.ok ? NxJSON.scrub(summary.raw) : (summary.error ?? "")
        sessionContext = [
            "Repo " + NxJSON.text(body, "repo", "project"),
            "Branch " + NxJSON.text(body, "branch"),
            "Visibility " + NxJSON.text(body, "visibility")
        ].joined(separator: "\n")
    }

    func continueSession(mode: String, resume: String, agent: String = "") async {
        guard let sessionId else { return }
        let response = await NxClient.shared.call(NxMethods.continueStart, args: [
            "session_id": sessionId,
            "mode": mode,
            "resume": resume,
            "agent": agent,
            "prompt": message
        ])
        sessionSummary = response.ok ? NxJSON.scrub(response.raw) : (response.error ?? "Continue failed")
    }

    func send() async {
        guard let sessionId else { return }
        let response = await NxClient.shared.call(NxMethods.runsMessage, args: [
            "session_id": sessionId,
            "prompt": message
        ])
        if response.ok {
            message = ""
            await loadSession()
        } else {
            pageError = response.error ?? "Send failed"
        }
    }

    func requestShare() {
        if shareTeam {
            confirmTeam = true
            return
        }
        Task { await share() }
    }

    func share() async {
        guard let sessionId else { return }
        let names = people.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
        let response = await NxClient.shared.call(NxMethods.sessionsShare, args: [
            "session_id": sessionId,
            "people": names,
            "team": shareTeam,
            "confirm": shareTeam,
            "live": shareLive
        ])
        pageError = response.ok ? "" : (response.error ?? "Share failed")
        if response.ok {
            sessionContext += "\nShare sent"
        }
    }

    func pin(_ id: String) async {
        await mutate(NxMethods.memoryPin, id: id)
    }

    func forget(_ id: String) async {
        await mutate(NxMethods.memoryForget, id: id)
    }

    func scope(_ id: String, _ scope: String) async {
        let response = await NxClient.shared.call(NxMethods.memoryScope, args: ["id": id, "scope": scope])
        if response.ok {
            await loadMemories()
        } else {
            pageError = response.error ?? "Scope change failed"
        }
    }

    func prepareTeleport() async {
        let response = await NxClient.shared.call(NxMethods.teleportPrepare, args: [
            "id": teleportId,
            "text": preview
        ])
        preview = response.ok ? NxJSON.scrub(response.raw) : (response.error ?? "Prepare failed")
    }

    func applyTeleport() async {
        let response = await NxClient.shared.call(NxMethods.teleportApply, args: ["id": teleportId])
        preview = response.ok ? "Done" : (response.error ?? "Apply failed")
    }

    func revokeTeleport() async {
        let response = await NxClient.shared.call(NxMethods.teleportRevoke, args: ["id": teleportId])
        preview = response.ok ? "Revoked" : (response.error ?? "Revoke failed")
        if response.ok {
            await loadTeleports()
        }
    }

    func configureMCP(_ agent: String) async {
        let response = await NxClient.shared.call(NxMethods.agentsConfigureMcp, args: ["agent": agent])
        pageError = response.ok ? "" : (response.error ?? "Configure failed")
        if response.ok {
            statusText = agent + " MCP config requested\n" + statusText
        }
    }

    func clearCache() async {
        let response = await NxClient.shared.call(NxMethods.cacheClear)
        statusText = response.ok ? "Cache cleared" : (response.error ?? "Clear failed")
    }

    func retryUploads() async {
        let response = await NxClient.shared.call(NxMethods.uploadsRetry)
        statusText = response.ok ? NxJSON.scrub(response.raw) : (response.error ?? "Retry failed")
    }

    func loadStatus() async {
        loginEnabled = LoginStart.isEnabled
        let status = await NxClient.shared.call(NxMethods.statusGet)
        let diag = await NxClient.shared.call(NxMethods.diagnosticsGet)
        let auth = await NxClient.shared.call(NxMethods.authStatus)
        statusText = [status, diag, auth].map { item in
            item.ok ? NxJSON.scrub(item.raw) : (item.error ?? "failed")
        }.joined(separator: "\n")
        statusText += "\nSign-in returns through nexus://auth/callback. Tokens stay in the core."
    }

    func setLogin(_ on: Bool) {
        do {
            try LoginStart.setEnabled(on)
            loginEnabled = LoginStart.isEnabled
        } catch {
            pageError = error.localizedDescription
            loginEnabled = LoginStart.isEnabled
        }
    }

    func runSearch() async {
        let sessions = await NxClient.shared.call(NxMethods.timelineList, args: ["q": searchText])
        let memories = await NxClient.shared.call(NxMethods.memorySearch, args: ["q": searchText])
        let projects = await NxClient.shared.call(NxMethods.projectsList, args: ["q": searchText])
        searchSessions = Self.items(sessions, title: ["title", "summary", "preview"], subtitle: ["agent", "harness"])
        searchMemories = Self.items(memories, title: ["text", "content", "title"], subtitle: ["status"])
        searchProjects = Self.items(projects, title: ["name", "title"], subtitle: ["id"])
    }

    private func mutate(_ method: String, id: String) async {
        let response = await NxClient.shared.call(method, args: ["id": id])
        if response.ok {
            await loadMemories()
        } else {
            pageError = response.error ?? "Memory update failed"
        }
    }

    private static func items(_ response: NxResponse, title: [String], subtitle: [String]) -> [NxItem] {
        NxJSON.items(response.raw).enumerated().map { index, item in
            let key = NxJSON.text(item, "id", "session_id", "project_id", "teleport_id")
            let heading = NxJSON.text(item, title)
            let detail = subtitle.map { NxJSON.text(item, $0) }.filter { !$0.isEmpty }.joined(separator: " · ")
            let status = NxJSON.publicStatus(NxJSON.text(item, "status"))
            return NxItem(
                id: "\(key)#\(index)",
                key: key,
                title: NxJSON.scrub(heading.isEmpty ? key : heading),
                subtitle: NxJSON.scrub(detail),
                status: NxJSON.text(item, "status").isEmpty ? "" : status
            )
        }
    }
}

private extension NxJSON {
    static func text(_ item: [String: Any], _ keys: [String]) -> String {
        text(item, keys[0], keys.count > 1 ? keys[1] : "", keys.count > 2 ? keys[2] : "", keys.count > 3 ? keys[3] : "")
    }
}
