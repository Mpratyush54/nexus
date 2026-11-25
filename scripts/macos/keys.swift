// Keyboard input the way a hardware keyboard sends it, for the audit
// (scripts/macos/mac-audit.sh): a Shift flagsChanged event (key code 56)
// around each capital, then the letter with the Shift flag.
//   keys type "Hello World"   letters and spaces
//   keys key <code>            one key (53 = Esc, 36 = Return)
// Posting events needs Accessibility, which the macOS runner grants.
import CoreGraphics
import Foundation

let codes: [Character: CGKeyCode] = [
    "a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9, "b": 11, "q": 12,
    "w": 13, "e": 14, "r": 15, "y": 16, "t": 17, "o": 31, "u": 32, "i": 34, "p": 35, "l": 37, "j": 38,
    "k": 40, "n": 45, "m": 46, " ": 49,
]
let src = CGEventSource(stateID: .hidSystemState)
func post(_ code: CGKeyCode, _ down: Bool, _ flags: CGEventFlags = []) {
    let e = CGEvent(keyboardEventSource: src, virtualKey: code, keyDown: down)
    e?.flags = flags
    e?.post(tap: .cghidEventTap)
    usleep(12_000)
}
let a = CommandLine.arguments
guard a.count >= 3 else { print("usage: keys type <text> | keys key <code>"); exit(2) }
switch a[1] {
case "type":
    for ch in a[2] {
        let lower = Character(ch.lowercased())
        guard let code = codes[lower] else { continue }
        let shift = ch.isUppercase
        if shift { post(56, true, .maskShift) }
        post(code, true, shift ? .maskShift : [])
        post(code, false, shift ? .maskShift : [])
        if shift { post(56, false) }
    }
case "key":
    guard let code = CGKeyCode(a[2]) else { exit(2) }
    post(code, true); post(code, false)
default:
    print("unknown action \(a[1])"); exit(2)
}
usleep(100_000)
