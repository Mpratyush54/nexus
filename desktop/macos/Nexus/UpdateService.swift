import Foundation

/// Placeholder for Sparkle auto-update wiring (P5).
///
/// TODO(Sparkle): integrate Sparkle (`SPUStandardUpdaterController`) and set
/// `SUFeedURL` in Info.plist once the appcast feed is published. Until then
/// this service is intentionally a no-op so the Mac target builds without
/// linking Sparkle.
@MainActor
final class UpdateService {
    static let shared = UpdateService()

    private init() {}

    /// Checks for updates when Sparkle is linked. Currently a no-op.
    func checkForUpdates() {
        // TODO(Sparkle): updaterController.checkForUpdates()
    }

    /// Starts background update checks when Sparkle is linked. Currently a no-op.
    func start() {
        // TODO(Sparkle): updaterController.startUpdater()
    }
}
