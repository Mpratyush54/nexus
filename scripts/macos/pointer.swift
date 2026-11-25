// Mouse input for the install checks (scripts/macos/install-update-checks.sh):
//   pointer click <x> <y>          left click at a global point
//   pointer scroll <x> <y> <lines> scroll wheel (negative = down) over a point
// Posting events needs Accessibility, which the macOS runner grants.
import CoreGraphics
import Foundation

let a = CommandLine.arguments
guard a.count >= 4, let x = Double(a[2]), let y = Double(a[3]) else {
    print("usage: pointer click|scroll x y [lines]"); exit(2)
}
let p = CGPoint(x: x, y: y)
let src = CGEventSource(stateID: .hidSystemState)
CGEvent(mouseEventSource: src, mouseType: .mouseMoved, mouseCursorPosition: p, mouseButton: .left)?.post(tap: .cghidEventTap)
usleep(150_000)
switch a[1] {
case "click":
    CGEvent(mouseEventSource: src, mouseType: .leftMouseDown, mouseCursorPosition: p, mouseButton: .left)?.post(tap: .cghidEventTap)
    usleep(80_000)
    CGEvent(mouseEventSource: src, mouseType: .leftMouseUp, mouseCursorPosition: p, mouseButton: .left)?.post(tap: .cghidEventTap)
case "scroll":
    let lines = Int32(a.count > 4 ? Int(a[4]) ?? -10 : -10)
    for _ in 0..<abs(lines) {
        CGEvent(scrollWheelEvent2Source: src, units: .line, wheelCount: 1, wheel1: lines < 0 ? -1 : 1, wheel2: 0, wheel3: 0)?.post(tap: .cghidEventTap)
        usleep(30_000)
    }
default:
    print("unknown action \(a[1])"); exit(2)
}
usleep(150_000)
