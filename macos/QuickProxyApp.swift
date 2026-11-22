import AppKit
import Foundation
import SwiftUI

private enum Style {
    static let sidebar = Color(red: 0.105, green: 0.153, blue: 0.207)
    static let background = Color(red: 0.967, green: 0.973, blue: 0.979)
    static let border = Color(red: 0.885, green: 0.908, blue: 0.925)
    static let ink = Color(red: 0.115, green: 0.169, blue: 0.222)
    static let secondary = Color(red: 0.43, green: 0.49, blue: 0.55)
    static let accent = Color(red: 0.05, green: 0.47, blue: 0.60)
    static let success = Color(red: 0.10, green: 0.59, blue: 0.42)
}

private enum AppPage: String, CaseIterable, Identifiable {
    case overview = "概览"
    case accounts = "账号管理"
    case settings = "服务设置"
    case logs = "运行日志"

    var id: String { rawValue }

    var icon: String {
        switch self {
        case .overview: return "square.grid.2x2"
        case .accounts: return "person.2"
        case .settings: return "slider.horizontal.3"
        case .logs: return "text.alignleft"
        }
    }

    var subtitle: String {
        switch self {
        case .overview: return "本机代理状态与常用操作"
        case .accounts: return "管理 Amazon Quick 登录账号"
        case .settings: return "配置 API 地址和会话策略"
        case .logs: return "查看代理进程的运行输出"
        }
    }
}

struct AccountEntry: Identifiable {
    let id: String
    let email: String
    let provider: String
    let expires: String
    let uses: String
    let status: String

    var isReady: Bool { status == "ready" }
}

final class ProxyManager: ObservableObject {
    @Published private(set) var isRunning = false
    @Published private(set) var isWorking = false
    @Published private(set) var operation = ""
    @Published private(set) var accounts: [AccountEntry] = []
    @Published private(set) var logText = ""
    @Published private(set) var endpoint = "http://127.0.0.1:8080/v1"
    @Published private(set) var defaultModel = "smart"
    @Published private(set) var sessionReuse = true
    @Published private(set) var apiKeys: [String] = []
    @Published private(set) var modelOptions: [String] = ["smart", "fast", "balanced", "auto"]
    @Published var lastError: String?
    @Published var notice: String?

    private var prepared = false
    private var serverProcess: Process?
    private var serverPipe: Pipe?
    private var stopRequested = false
    private var restartPending = false

    let supportURL: URL = FileManager.default.urls(
        for: .applicationSupportDirectory,
        in: .userDomainMask
    )[0].appendingPathComponent("QuickProxy", isDirectory: true)

    var configURL: URL { supportURL.appendingPathComponent("config.json") }

    private var binaryURL: URL? {
        Bundle.main.resourceURL?.appendingPathComponent("quickproxy")
    }

    func prepare() {
        guard !prepared else { return }
        prepared = true
        do {
            try FileManager.default.createDirectory(
                at: supportURL,
                withIntermediateDirectories: true
            )
        } catch {
            lastError = "创建数据目录失败：\(error.localizedDescription)"
            return
        }
        reloadConfig()
        refreshAccounts()
    }

    func reloadConfig() {
        guard let data = try? Data(contentsOf: configURL),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return }

        let address = json["addr"] as? String ?? "127.0.0.1:8080"
        endpoint = "http://\(address)/v1"
        defaultModel = json["default_model"] as? String ?? "smart"
        sessionReuse = json["session_reuse"] as? Bool ?? true
        apiKeys = json["api_keys"] as? [String] ?? []
        let models = json["models"] as? [String] ?? []
        modelOptions = models.isEmpty ? ["smart", "fast", "balanced", "auto"] : models
    }

    var listenHost: String {
        let hostAndPort = endpoint
            .replacingOccurrences(of: "http://", with: "")
            .replacingOccurrences(of: "/v1", with: "")
        return String(hostAndPort.split(separator: ":", omittingEmptySubsequences: false).first ?? "127.0.0.1")
    }

    var listenPort: String {
        let hostAndPort = endpoint
            .replacingOccurrences(of: "http://", with: "")
            .replacingOccurrences(of: "/v1", with: "")
        let fields = hostAndPort.split(separator: ":", omittingEmptySubsequences: false)
        return fields.count == 2 ? String(fields[1]) : "8080"
    }

    private func persistConfig(_ change: (inout [String: Any]) -> Void) -> Bool {
        do {
            var json: [String: Any] = [:]
            if FileManager.default.fileExists(atPath: configURL.path) {
                let data = try Data(contentsOf: configURL)
                guard let loaded = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                    lastError = "配置文件不是有效的 JSON 对象。"
                    return false
                }
                json = loaded
            }
            change(&json)
            let data = try JSONSerialization.data(
                withJSONObject: json,
                options: [.prettyPrinted, .sortedKeys]
            )
            try data.write(to: configURL, options: .atomic)
            try FileManager.default.setAttributes(
                [.posixPermissions: 0o600],
                ofItemAtPath: configURL.path
            )
            reloadConfig()
            notice = isRunning ? "配置已保存。请重启代理使设置生效。" : "配置已保存。"
            lastError = nil
            return true
        } catch {
            lastError = "保存失败：\(error.localizedDescription)"
            return false
        }
    }

    func saveSettings(host: String, port: String, model: String, reuse: Bool) -> Bool {
        let value = port.trimmingCharacters(in: .whitespacesAndNewlines)
        guard ["127.0.0.1", "localhost"].contains(host),
              value.allSatisfy({ $0.isNumber }),
              let numericPort = Int(value),
              (1...65535).contains(numericPort)
        else {
            lastError = "端口必须是 1–65535 之间的整数。"
            return false
        }
        guard modelOptions.contains(model) else {
            lastError = "请选择已有的模型别名。"
            return false
        }
        return persistConfig { json in
            json["addr"] = "\(host):\(numericPort)"
            json["default_model"] = model
            json["session_reuse"] = reuse
        }
    }

    func saveAPIKeys(_ keys: [String]) -> Bool {
        let cleaned = keys.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
        guard cleaned.allSatisfy({
            !$0.isEmpty &&
            !$0.contains(where: { $0.isWhitespace || $0.isNewline })
        }), Set(cleaned).count == cleaned.count else {
            lastError = "API Key 不能为空、包含空白字符或重复。"
            return false
        }
        return persistConfig { json in
            json["api_keys"] = cleaned
        }
    }

    func restartServer() {
        guard isRunning else {
            startServer()
            return
        }
        restartPending = true
        stopServer()
    }

    private func makeProcess(command: String, arguments: [String] = []) throws -> Process {
        guard let executable = binaryURL,
              FileManager.default.isExecutableFile(atPath: executable.path)
        else {
            throw NSError(
                domain: "QuickProxy", code: 1,
                userInfo: [NSLocalizedDescriptionKey: "应用内缺少 quickproxy 核心程序。"]
            )
        }
        let process = Process()
        process.executableURL = executable
        process.arguments = [command, "-config", configURL.path] + arguments
        process.currentDirectoryURL = supportURL
        return process
    }

    func startServer() {
        guard !isRunning else { return }
        lastError = nil
        notice = nil
        stopRequested = false
        do {
            let process = try makeProcess(command: "server")
            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            process.terminationHandler = { [weak self] finished in
                DispatchQueue.main.async {
                    guard let self = self,
                          self.serverProcess?.processIdentifier == finished.processIdentifier
                    else { return }
                    self.serverPipe?.fileHandleForReading.readabilityHandler = nil
                    self.serverPipe = nil
                    self.serverProcess = nil
                    self.isRunning = false
                    self.appendLog("代理进程已退出，退出码：\(finished.terminationStatus)\n")
                    if finished.terminationStatus != 0 && !self.stopRequested {
                        self.lastError = "代理进程已退出。请查看运行日志。"
                    }
                    self.stopRequested = false
                    if self.restartPending {
                        self.restartPending = false
                        self.startServer()
                    }
                }
            }

            try process.run()
            serverProcess = process
            serverPipe = pipe
            isRunning = true
            appendLog("启动代理：\(endpoint)\n")
            pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
                let data = handle.availableData
                if data.isEmpty {
                    handle.readabilityHandler = nil
                    return
                }
                let output = String(decoding: data, as: UTF8.self)
                DispatchQueue.main.async { self?.appendLog(output) }
            }
        } catch {
            lastError = "启动失败：\(error.localizedDescription)"
            appendLog("启动失败：\(error.localizedDescription)\n")
        }
    }

    func stopServer() {
        guard let process = serverProcess else { return }
        if process.isRunning {
            stopRequested = true
            appendLog("正在停止代理…\n")
            process.terminate()
        }
    }

    func shutdown() {
        guard let process = serverProcess, process.isRunning else { return }
        process.terminate()
    }

    private func appendLog(_ message: String) {
        logText += message
        if logText.count > 80_000 {
            logText = String(logText.suffix(60_000))
        }
    }

    private func runCLI(
        _ command: String,
        arguments: [String] = [],
        title: String,
        completion: @escaping (Bool, String) -> Void
    ) {
        guard !isWorking else { return }
        isWorking = true
        operation = title
        notice = nil
        lastError = nil

        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self = self else { return }
            var successful = false
            var output = ""
            do {
                let process = try self.makeProcess(command: command, arguments: arguments)
                let pipe = Pipe()
                process.standardOutput = pipe
                process.standardError = pipe
                try process.run()
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                process.waitUntilExit()
                successful = process.terminationStatus == 0
                output = String(decoding: data, as: UTF8.self)
            } catch {
                output = error.localizedDescription
            }
            let success = successful
            let result = output
            DispatchQueue.main.async {
                self.isWorking = false
                self.operation = ""
                completion(success, result)
            }
        }
    }

    func refreshAccounts() {
        runCLI("accounts", title: "正在读取账号…") { [weak self] success, output in
            guard let self = self else { return }
            if success {
                self.accounts = Self.parseAccounts(output)
                self.reloadConfig()
            } else {
                self.lastError = "读取账号失败：\(output.trimmingCharacters(in: .whitespacesAndNewlines))"
            }
        }
    }

    func login() {
        runCLI("login", arguments: ["-open"], title: "等待浏览器完成登录…") {
            [weak self] success, output in
            guard let self = self else { return }
            if success {
                self.notice = "账号登录完成。"
                self.refreshAccounts()
            } else {
                self.lastError = "登录失败：\(output.trimmingCharacters(in: .whitespacesAndNewlines))"
            }
        }
    }

    func refreshTokens() {
        runCLI("refresh", title: "正在刷新账号令牌…") {
            [weak self] success, output in
            guard let self = self else { return }
            if success {
                self.notice = "账号令牌已刷新。"
                self.refreshAccounts()
            } else {
                self.lastError = "刷新失败：\(output.trimmingCharacters(in: .whitespacesAndNewlines))"
            }
        }
    }

    func removeAccount(_ account: AccountEntry) {
        runCLI("logout", arguments: ["-account", account.id], title: "正在移除账号…") {
            [weak self] success, output in
            guard let self = self else { return }
            if success {
                self.notice = "账号已移除。"
                self.refreshAccounts()
            } else {
                self.lastError = "移除失败：\(output.trimmingCharacters(in: .whitespacesAndNewlines))"
            }
        }
    }

    func openConfig() {
        guard FileManager.default.fileExists(atPath: configURL.path) else {
            refreshAccounts()
            notice = "配置文件初始化完成后，可再次点击打开。"
            return
        }
        NSWorkspace.shared.open(configURL)
    }

    func openDataDirectory() {
        NSWorkspace.shared.open(supportURL)
    }

    func copyEndpoint() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(endpoint, forType: .string)
        notice = "API 地址已复制。"
    }

    private static func parseAccounts(_ raw: String) -> [AccountEntry] {
        var entries: [AccountEntry] = []
        for line in raw.components(separatedBy: .newlines) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("ID"),
                  !trimmed.hasPrefix("no accounts") else { continue }
            let columns = trimmed.replacingOccurrences(
                of: "\\s{2,}", with: "\t", options: .regularExpression
            ).components(separatedBy: "\t")
            guard columns.count >= 8, Int(columns[0]) != nil else { continue }
            entries.append(AccountEntry(
                id: columns[0],
                email: columns[1],
                provider: columns[2],
                expires: columns[5],
                uses: columns[6],
                status: columns[7]
            ))
        }
        return entries
    }
}
