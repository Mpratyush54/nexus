import SwiftUI

private struct OfflineHelp: ViewModifier {
    @EnvironmentObject private var model: ShellModel

    func body(content: Content) -> some View {
        content
            .disabled(model.offline)
            .help(model.offline ? "Available when online" : "")
    }
}

private extension View {
    func cloudOnly() -> some View { modifier(OfflineHelp()) }
}

struct TimelineScreen: View {
    @EnvironmentObject private var model: ShellModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Timeline").font(.title)
            HStack {
                TextField("Project", text: $model.projectFilter)
                TextField("Agent", text: $model.agentFilter)
                TextField("Machine", text: $model.machineFilter)
                TextField("Person", text: $model.personFilter)
                Button("Refresh") { Task { await model.loadTimeline() } }
            }
            List(model.timeline) { item in
                VStack(alignment: .leading, spacing: 6) {
                    Text(item.title).font(.headline)
                    if !item.subtitle.isEmpty {
                        Text(item.subtitle).foregroundStyle(.secondary)
                    }
                    HStack {
                        Button("Open") { model.openSession(item.key) }
                        Button("Continue") { model.openSession(item.key) }
                    }
                }
                .padding(.vertical, 4)
            }
        }
        .padding(24)
        .task { await model.loadTimeline() }
    }
}

struct SessionScreen: View {
    @EnvironmentObject private var model: ShellModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Button("Timeline") { model.sessionId = nil }
                Text(model.sessionTitle).font(.headline)
            }
            HStack {
                Button("Here in Nexus") {
                    Task { await model.continueSession(mode: "here", resume: "native") }
                }.cloudOnly()
                Button("Open in agent") {
                    Task { await model.continueSession(mode: "open_in_agent", resume: model.resumeMode) }
                }.cloudOnly()
                Picker("Resume", selection: $model.resumeMode) {
                    Text("native").tag("native")
                    Text("fork").tag("fork")
                    Text("seeded").tag("seeded")
                }
                .pickerStyle(.menu)
                Button("Continue in another agent") {
                    Task { await model.continueSession(mode: "open_in_agent", resume: "seeded", agent: model.seedAgent) }
                }.cloudOnly()
                Picker("Agent", selection: $model.seedAgent) {
                    ForEach(AgentCatalog.all) { harness in
                        Text(harness.name).tag(harness.name)
                    }
                }
                .pickerStyle(.menu)
                Button("Teleport") { model.openTeleport() }.cloudOnly()
            }
            HStack(alignment: .top, spacing: 16) {
                TabView {
                    itemList("Chat", model.sessionTurns)
                    itemList("Files", model.sessionFiles)
                    itemList("Commands", model.sessionCommands)
                    itemList("Memory", model.sessionMemories)
                    ScrollView { Text(model.sessionSummary).frame(maxWidth: .infinity, alignment: .leading) }
                        .tabItem { Text("Summary") }
                }
                VStack(alignment: .leading, spacing: 8) {
                    Text("Context").font(.headline)
                    Text(model.sessionContext)
                    TextField("Named people", text: $model.people)
                    Toggle("Everyone in this project", isOn: $model.shareTeam)
                    Toggle("Live", isOn: $model.shareLive)
                    Button("Share") { model.requestShare() }.cloudOnly()
                }
                .frame(width: 280, alignment: .leading)
            }
            TextField("Message the session", text: $model.message, axis: .vertical)
                .lineLimit(2...4)
            Button("Send") { Task { await model.send() } }.cloudOnly()
        }
        .padding(24)
        .task { await model.loadSession() }
    }

    private func itemList(_ title: String, _ items: [NxItem]) -> some View {
        List(items) { item in
            VStack(alignment: .leading) {
                Text(item.title)
                if !item.status.isEmpty {
                    Text(item.status).font(.caption)
                } else if !item.subtitle.isEmpty {
                    Text(item.subtitle).font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .tabItem { Text(title) }
    }
}

struct MemoryScreen: View {
    @EnvironmentObject private var model: ShellModel
    private let scopes = ["project", "session", "personal", "organization"]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Memory").font(.title)
            HStack {
                TextField("Search memory", text: $model.memoryQuery)
                Picker("Scope", selection: $model.memoryScope) {
                    ForEach(scopes, id: \.self) { Text($0) }
                }
                .pickerStyle(.menu)
                Button("Search") { Task { await model.loadMemories() } }
            }
            List(model.memories) { item in
                VStack(alignment: .leading, spacing: 6) {
                    Text(item.title).font(.headline)
                    Text(item.status.isEmpty ? "active" : item.status).font(.caption)
                    HStack {
                        Button("Pin") { Task { await model.pin(item.key) } }.cloudOnly()
                        Button("Forget") { Task { await model.forget(item.key) } }.cloudOnly()
                        ForEach(scopes, id: \.self) { scope in
                            Button(scope.capitalized) { Task { await model.scope(item.key, scope) } }.cloudOnly()
                        }
                    }
                }
                .padding(.vertical, 4)
            }
        }
        .padding(24)
        .task { await model.loadMemories() }
    }
}

struct TeleportScreen: View {
    @EnvironmentObject private var model: ShellModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Teleport").font(.title)
            HStack {
                Button("Inbox") { Task { await model.loadTeleports() } }
                Button("Sent") { Task { await model.loadSent() } }
            }
            HStack(alignment: .top) {
                List(model.teleports) { item in
                    Button {
                        model.teleportId = item.key
                        model.preview = item.title
                    } label: {
                        VStack(alignment: .leading) {
                            Text(item.title).font(.headline)
                            Text(item.subtitle).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                }
                VStack(alignment: .leading, spacing: 8) {
                    Text("Preview").font(.headline)
                    Text(model.preview).frame(maxWidth: .infinity, alignment: .leading)
                    HStack {
                        Button("Prepare") { Task { await model.prepareTeleport() } }.cloudOnly()
                        Button("Continue here") { Task { await model.applyTeleport() } }.cloudOnly()
                        Button("Open in agent") {
                            if !model.teleportId.isEmpty {
                                model.openSession(model.teleportId)
                            }
                        }
                        Button("Revoke") { Task { await model.revokeTeleport() } }.cloudOnly()
                    }
                }
            }
        }
        .padding(24)
        .task { await model.loadTeleports() }
    }
}

struct AgentsScreen: View {
    @EnvironmentObject private var model: ShellModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Agents").font(.title)
            if !model.statusText.isEmpty {
                Text(model.statusText).font(.callout)
            }
            List(AgentCatalog.all) { harness in
                VStack(alignment: .leading, spacing: 6) {
                    Text(harness.name).font(.headline)
                    Text(harness.ideHistory
                         ? "Doesn't support resume; restores into chat history"
                         : harness.resume)
                    Text("Capture follows the project switch. There is no per-agent switch.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Button("Configure MCP") { Task { await model.configureMCP(harness.name) } }
                        .cloudOnly()
                }
                .padding(.vertical, 4)
            }
        }
        .padding(24)
    }
}

struct SettingsScreen: View {
    @EnvironmentObject private var model: ShellModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Settings").font(.title)
            Toggle("Start at login", isOn: Binding(
                get: { model.loginEnabled },
                set: { model.setLogin($0) }
            ))
            HStack {
                Button("Clear cache") { Task { await model.clearCache() } }
                Button("Retry uploads") { Task { await model.retryUploads() } }.cloudOnly()
            }
            Text(model.statusText).frame(maxWidth: .infinity, alignment: .leading)
            Spacer()
        }
        .padding(24)
        .task { await model.loadStatus() }
    }
}
