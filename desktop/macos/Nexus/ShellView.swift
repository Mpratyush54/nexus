import SwiftUI

struct ShellView: View {
    @EnvironmentObject private var model: ShellModel

    var body: some View {
        NavigationSplitView {
            List(selection: $model.section) {
                ForEach(NavSection.allCases) { section in
                    if section == .teleport && model.inboxCount > 0 {
                        Label(section.rawValue, systemImage: section.symbol)
                            .badge(model.inboxCount)
                            .tag(section)
                    } else {
                        Label(section.rawValue, systemImage: section.symbol)
                            .tag(section)
                    }
                }
            }
            .navigationTitle("Nexus")
        } detail: {
            VStack(spacing: 0) {
                if model.offline {
                    Text(model.banner)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                        .background(Color.orange.opacity(0.18))
                }
                if !model.pageError.isEmpty {
                    Text(model.pageError)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                }
                detail
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .onAppear {
            Task {
                await model.refreshBanner()
                await model.loadTimeline()
                await model.loadTeleports()
            }
        }
        .background {
            Button("") { model.searchOpen = true }
                .keyboardShortcut("k", modifiers: .command)
                .hidden()
            Button("") { model.searchOpen = true }
                .keyboardShortcut("k", modifiers: .control)
                .hidden()
        }
        .sheet(isPresented: $model.searchOpen) {
            SearchSheet()
                .environmentObject(model)
        }
        .alert("Share with the project", isPresented: $model.confirmTeam) {
            Button("Share with everyone") { Task { await model.share() } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("All members will see this chat and its files.")
        }
    }

    @ViewBuilder private var detail: some View {
        if let sessionId = model.sessionId, !sessionId.isEmpty {
            SessionScreen()
        } else {
            switch model.section ?? .timeline {
            case .timeline: TimelineScreen()
            case .memory: MemoryScreen()
            case .teleport: TeleportScreen()
            case .agents: AgentsScreen()
            case .settings: SettingsScreen()
            }
        }
    }
}

struct SearchSheet: View {
    @EnvironmentObject private var model: ShellModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Search").font(.title2)
            TextField("Sessions, memories, files, projects", text: $model.searchText)
                .textFieldStyle(.roundedBorder)
                .onSubmit { Task { await model.runSearch() } }
            List {
                Section("Sessions") {
                    ForEach(model.searchSessions) { item in
                        Button(item.title) {
                            dismiss()
                            model.openSession(item.key)
                        }
                    }
                }
                Section("Memories") {
                    ForEach(model.searchMemories) { item in
                        VStack(alignment: .leading) {
                            Text(item.title)
                            if !item.status.isEmpty {
                                Text(item.status).font(.caption)
                            }
                        }
                    }
                }
                Section("Projects") {
                    ForEach(model.searchProjects) { item in
                        Text(item.title)
                    }
                }
            }
            HStack {
                Button("Search") { Task { await model.runSearch() } }
                Button("Close") { dismiss() }
            }
        }
        .padding(20)
        .frame(minWidth: 520, minHeight: 420)
    }
}
