import Foundation
import ServiceManagement

struct NxResponse: Sendable {
    var ok: Bool
    var error: String?
    var raw: String
}

/// In-process core. nx_call runs off the main actor. The subscribe thunk
/// copies the C string and hops to the main actor; it does not call nx_call.
final class NxClient: @unchecked Sendable {
    static let shared = NxClient()

    func start() {
        _ = nx_init("{\"online\":true}")
        nx_subscribe(nxEventThunk)
        if !LoginStart.isEnabled {
            try? LoginStart.setEnabled(true)
        }
    }

    func call(_ method: String, args: [String: Any] = [:]) async -> NxResponse {
        let json = NxClient.encode(args)
        return await Task.detached(priority: .userInitiated) {
            NxClient.invoke(method, json)
        }.value
    }

    func shutdown() {
        _ = nx_shutdown()
    }

    private static func encode(_ args: [String: Any]) -> String {
        guard JSONSerialization.isValidJSONObject(args),
              let data = try? JSONSerialization.data(withJSONObject: args),
              let text = String(data: data, encoding: .utf8) else {
            return "{}"
        }
        return text
    }

    private static func invoke(_ method: String, _ json: String) -> NxResponse {
        let ptr = method.withCString { name in
            json.withCString { body in
                nx_call(name, body)
            }
        }
        defer {
            if let ptr {
                nx_free(ptr)
            }
        }
        guard let ptr else {
            return NxResponse(ok: false, error: "empty core response", raw: "")
        }
        let text = String(cString: ptr)
        guard let data = text.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return NxResponse(ok: false, error: "core response was not JSON", raw: text)
        }
        let ok = object["ok"] as? Bool ?? false
        let error = object["error"] as? String
        let raw: String
        if let result = object["result"],
           JSONSerialization.isValidJSONObject(result),
           let encoded = try? JSONSerialization.data(withJSONObject: result) {
            raw = String(data: encoded, encoding: .utf8) ?? ""
        } else if object["result"] is NSNull {
            raw = "null"
        } else {
            raw = ""
        }
        return NxResponse(ok: ok, error: error, raw: raw)
    }
}

private func nxEventThunk(_ raw: UnsafePointer<CChar>?) {
    guard let raw else { return }
    let text = String(cString: raw)
    Task { @MainActor in
        ShellModel.shared.ingest(text)
    }
}

enum LoginStart {
    static var isEnabled: Bool {
        SMAppService.mainApp.status == .enabled
    }

    static func setEnabled(_ on: Bool) throws {
        if on {
            try SMAppService.mainApp.register()
        } else {
            try SMAppService.mainApp.unregister()
        }
    }
}

enum NxJSON {
    static func object(_ raw: String) -> [String: Any] {
        guard let data = raw.data(using: .utf8),
              let value = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return [:]
        }
        return value
    }

    static func items(_ raw: String) -> [[String: Any]] {
        guard let data = raw.data(using: .utf8),
              let value = try? JSONSerialization.jsonObject(with: data) else {
            return []
        }
        if let list = value as? [[String: Any]] {
            return list
        }
        guard let object = value as? [String: Any] else {
            return []
        }
        let keys = ["items", "events", "sessions", "memories", "teleports", "agents", "results", "projects", "turns", "files", "versions"]
        for key in keys {
            if let list = object[key] as? [[String: Any]] {
                return list
            }
        }
        return []
    }

    static func text(_ item: [String: Any], _ keys: String...) -> String {
        for key in keys {
            if let value = item[key] as? String, !value.isEmpty {
                return value
            }
            if let value = item[key] as? NSNumber {
                return value.stringValue
            }
        }
        return ""
    }

    static func publicStatus(_ raw: String) -> String {
        switch raw.trimmingCharacters(in: .whitespacesAndNewlines).uppercased() {
        case "", "PROPOSED", "CONFIRMED", "ACTIVE":
            return "active"
        case "REJECTED", "FORGOTTEN":
            return "forgotten"
        case "SUPERSEDED":
            return "superseded"
        default:
            return raw.lowercased()
        }
    }

    static func scrub(_ text: String) -> String {
        text
            .replacingOccurrences(of: "PROPOSED", with: "active")
            .replacingOccurrences(of: "CONFIRMED", with: "active")
            .replacingOccurrences(of: "REJECTED", with: "forgotten")
            .replacingOccurrences(of: "SUPERSEDED", with: "superseded")
    }
}
