import AppKit
import CoreServices
import SwiftUI

@main
struct NexusApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var model = ShellModel.shared

    var body: some Scene {
        WindowGroup {
            ShellView()
                .environmentObject(model)
                .onOpenURL { url in model.handle(url) }
        }
        .defaultSize(width: 1100, height: 720)
        MenuBarExtra("Nexus", systemImage: "circle.hexagongrid.fill") {
            Button("Open") { model.showWindow() }
            Button(model.capturePaused ? "Resume capture" : "Pause capture") { model.toggleCapture() }
            Button("Teleport inbox") { model.openTeleport() }
            Divider()
            Button("Quit") { model.quit() }
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NxClient.shared.start()
        UpdateService.shared.start()
        Task { @MainActor in
            await ShellModel.shared.refreshBanner()
        }
        if CommandLine.arguments.contains("--background") {
            for window in NSApp.windows where window.canBecomeMain {
                window.close()
            }
        }
        NSAppleEventManager.shared().setEventHandler(
            self,
            andSelector: #selector(handleURLEvent(_:withReply:)),
            forEventClass: AEEventClass(kInternetEventClass),
            andEventID: AEEventID(kAEGetURL))
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    @objc func handleURLEvent(_ event: NSAppleEventDescriptor, withReply reply: NSAppleEventDescriptor) {
        guard let text = event.paramDescriptor(forKeyword: keyDirectObject)?.stringValue,
              let url = URL(string: text) else {
            return
        }
        Task { @MainActor in
            ShellModel.shared.handle(url)
        }
    }
}
