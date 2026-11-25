// Prints the on-screen windows owned by a process id: "<count>" then one
// "id x y w h layer" line per window. CGWindowList needs no Accessibility
// permission, unlike System Events. Used by desktop-macos.yml.
import CoreGraphics
import Foundation

guard CommandLine.arguments.count > 1, let pid = Int(CommandLine.arguments[1]) else {
    print("usage: winlist <pid>"); exit(2)
}
let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
let mine = list.filter { ($0[kCGWindowOwnerPID as String] as? Int) == pid }
print(mine.count)
for w in mine {
    let id = w[kCGWindowNumber as String] as? Int ?? 0
    let layer = w[kCGWindowLayer as String] as? Int ?? 0
    let b = w[kCGWindowBounds as String] as? [String: Any] ?? [:]
    print(id, b["X"] ?? 0, b["Y"] ?? 0, b["Width"] ?? 0, b["Height"] ?? 0, layer)
}
