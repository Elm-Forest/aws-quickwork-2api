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
    @Published var lastError: String?
    @Published var notice: String?

    private var prepared = false
    private var serverProcess: Process?
    private var serverPipe: Pipe?

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
    }

    func saveSettings(address: String, model: String, reuse: Bool) -> Bool {
        let trimmed = address.trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = trimmed.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 2,
              ["127.0.0.1", "localhost"].contains(String(parts[0])),
              let port = Int(parts[1]), (1...65535).contains(port)
        else {
            lastError = "监听地址需要使用 127.0.0.1:端口 或 localhost:端口。"
            return false
        }

        do {
            guard let data = try? Data(contentsOf: configURL),
                  var json = try JSONSerialization.jsonObject(with: data) as? [String: Any]
            else {
                lastError = "配置文件尚未创建。请先刷新账号。"
                return false
            }
            json["addr"] = trimmed
            json["default_model"] = model
            json["session_reuse"] = reuse
            let updated = try JSONSerialization.data(
                withJSONObject: json,
                options: [.prettyPrinted, .sortedKeys]
            )
            try updated.write(to: configURL, options: .atomic)
            reloadConfig()
            notice = isRunning ? "设置已保存。重启代理后生效。" : "设置已保存。"
            lastError = nil
            return true
        } catch {
            lastError = "保存设置失败：\(error.localizedDescription)"
            return false
        }
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
                    if finished.terminationStatus != 0 {
                        self.lastError = "代理进程已退出。请查看运行日志。"
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

private struct Surface<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        content
            .padding(22)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.white)
            .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .stroke(Style.border, lineWidth: 1)
            )
    }
}

private struct Metric: View {
    let title: String
    let value: String
    let note: String
    let icon: String

    var body: some View {
        Surface {
            HStack {
                Text(title)
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(Style.secondary)
                Spacer()
                Image(systemName: icon)
                    .foregroundStyle(Style.accent)
            }
            Text(value)
                .font(.system(size: 26, weight: .semibold, design: .rounded))
                .foregroundStyle(Style.ink)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
                .padding(.top, 9)
            Text(note)
                .font(.system(size: 12))
                .foregroundStyle(Style.secondary)
                .padding(.top, 2)
        }
    }
}

private struct OverviewView: View {
    @ObservedObject var manager: ProxyManager

    var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Surface {
                HStack(alignment: .center, spacing: 18) {
                    ZStack {
                        RoundedRectangle(cornerRadius: 13)
                            .fill(manager.isRunning ? Style.success.opacity(0.12) : Style.border)
                            .frame(width: 56, height: 56)
                        Image(systemName: "point.3.connected.trianglepath.dotted")
                            .font(.system(size: 25))
                            .foregroundStyle(manager.isRunning ? Style.success : Style.secondary)
                    }
                    VStack(alignment: .leading, spacing: 6) {
                        Text(manager.isRunning ? "代理服务运行中" : "代理服务已停止")
                            .font(.system(size: 22, weight: .semibold))
                            .foregroundStyle(Style.ink)
                        Text("OpenAI 兼容接口 · 本机 HTTP 服务")
                            .font(.system(size: 13))
                            .foregroundStyle(Style.secondary)
                    }
                    Spacer()
                    Button {
                        manager.isRunning ? manager.stopServer() : manager.startServer()
                    } label: {
                        Label(
                            manager.isRunning ? "停止代理" : "启动代理",
                            systemImage: manager.isRunning ? "stop.fill" : "play.fill"
                        )
                        .font(.system(size: 13, weight: .semibold))
                        .frame(minWidth: 98)
                        .padding(.vertical, 7)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(manager.isRunning ? Style.ink : Style.accent)
                    .controlSize(.large)
                }
            }

            HStack(spacing: 16) {
                Metric(
                    title: "服务状态",
                    value: manager.isRunning ? "运行中" : "已停止",
                    note: manager.isRunning ? "监听本机请求" : "点击启动代理",
                    icon: "waveform.path"
                )
                Metric(
                    title: "已登录账号",
                    value: "\(manager.accounts.count)",
                    note: "支持多账号轮询",
                    icon: "person.2.fill"
                )
                Metric(
                    title: "默认模型",
                    value: manager.defaultModel,
                    note: "由 config.json 配置",
                    icon: "cpu"
                )
            }

            Surface {
                Text("API 接入")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(Style.ink)
                Text("Base URL")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(Style.secondary)
                    .padding(.top, 17)
                HStack {
                    Text(manager.endpoint)
                        .font(.system(size: 15, design: .monospaced))
                        .foregroundStyle(Style.ink)
                        .textSelection(.enabled)
                    Spacer()
                    Button {
                        manager.copyEndpoint()
                    } label: {
                        Label("复制地址", systemImage: "doc.on.doc")
                    }
                }
                .padding(14)
                .background(Style.background)
                .clipShape(RoundedRectangle(cornerRadius: 9))
                .padding(.top, 1)
                Text("接口：/chat/completions、/models")
                    .font(.system(size: 12))
                    .foregroundStyle(Style.secondary)
                    .padding(.top, 9)
            }
        }
    }
}

private struct AccountsView: View {
    @ObservedObject var manager: ProxyManager
    @State private var pendingRemoval: AccountEntry?

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Surface {
                HStack {
                    VStack(alignment: .leading, spacing: 5) {
                        Text("账号列表")
                            .font(.system(size: 18, weight: .semibold))
                        Text("账号由本机 SQLite 数据库保存。")
                            .font(.system(size: 12))
                            .foregroundStyle(Style.secondary)
                    }
                    Spacer()
                    Button("刷新令牌") { manager.refreshTokens() }
                        .disabled(manager.isWorking || manager.accounts.isEmpty)
                    Button {
                        manager.login()
                    } label: {
                        Label("添加账号", systemImage: "plus")
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Style.accent)
                    .disabled(manager.isWorking)
                }
            }

            if manager.accounts.isEmpty {
                Surface {
                    VStack(spacing: 14) {
                        Image(systemName: "person.crop.circle.badge.plus")
                            .font(.system(size: 36))
                            .foregroundStyle(Style.accent)
                        Text("尚未添加账号")
                            .font(.system(size: 17, weight: .medium))
                        Text("点击“添加账号”，在浏览器中完成 Amazon Quick 登录。")
                            .font(.system(size: 13))
                            .foregroundStyle(Style.secondary)
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 45)
                }
            } else {
                Surface {
                    ForEach(Array(manager.accounts.enumerated()), id: \.element.id) { index, account in
                        if index > 0 {
                            Divider().padding(.vertical, 12)
                        }
                        HStack(spacing: 14) {
                            ZStack {
                                Circle().fill(Style.accent.opacity(0.10))
                                    .frame(width: 40, height: 40)
                                Image(systemName: "person.crop.circle")
                                    .foregroundStyle(Style.accent)
                            }
                            VStack(alignment: .leading, spacing: 5) {
                                Text(account.email)
                                    .font(.system(size: 14, weight: .semibold))
                                    .foregroundStyle(Style.ink)
                                Text("\(account.provider) · 到期：\(account.expires) · 调用：\(account.uses)")
                                    .font(.system(size: 12))
                                    .foregroundStyle(Style.secondary)
                            }
                            Spacer()
                            Text(account.isReady ? "正常" : account.status)
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(account.isReady ? Style.success : Style.secondary)
                            Button("移除") { pendingRemoval = account }
                                .disabled(manager.isWorking)
                        }
                    }
                }
            }
        }
        .confirmationDialog(
            "移除账号",
            isPresented: Binding(
                get: { pendingRemoval != nil },
                set: { if !$0 { pendingRemoval = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("确认移除", role: .destructive) {
                if let account = pendingRemoval {
                    manager.removeAccount(account)
                }
                pendingRemoval = nil
            }
            Button("取消", role: .cancel) { pendingRemoval = nil }
        } message: {
            Text("这将从本机数据库删除所选账号的登录信息。")
        }
    }
}

private struct SettingsView: View {
    @ObservedObject var manager: ProxyManager
    @State private var address = "127.0.0.1:8080"
    @State private var model = "smart"
    @State private var reuse = true

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Surface {
                Text("代理配置")
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundStyle(Style.ink)

                VStack(alignment: .leading, spacing: 8) {
                    Text("监听地址")
                        .font(.system(size: 13, weight: .medium))
                    TextField("127.0.0.1:8080", text: $address)
                        .textFieldStyle(.roundedBorder)
                }
                .padding(.top, 17)

                VStack(alignment: .leading, spacing: 8) {
                    Text("默认模型")
                        .font(.system(size: 13, weight: .medium))
                    Picker("默认模型", selection: $model) {
                        ForEach(["smart", "fast", "balanced", "auto"], id: \.self) { item in
                            Text(item).tag(item)
                        }
                    }
                    .labelsHidden()
                    .frame(width: 210)
                }
                .padding(.top, 12)

                Toggle("复用上游会话", isOn: $reuse)
                    .padding(.top, 10)

                HStack {
                    Text("修改配置后，需要重新启动代理。")
                        .font(.system(size: 12))
                        .foregroundStyle(Style.secondary)
                    Spacer()
                    Button("保存设置") {
                        _ = manager.saveSettings(address: address, model: model, reuse: reuse)
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Style.accent)
                }
                .padding(.top, 14)
            }

            Surface {
                Text("数据与配置文件")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(Style.ink)
                Text(manager.supportURL.path)
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(Style.secondary)
                    .textSelection(.enabled)
                    .padding(.top, 10)
                HStack(spacing: 12) {
                    Button {
                        manager.openConfig()
                    } label: {
                        Label("打开 config.json", systemImage: "doc.text")
                    }
                    Button {
                        manager.openDataDirectory()
                    } label: {
                        Label("打开数据目录", systemImage: "folder")
                    }
                }
                .padding(.top, 14)
            }
        }
        .onAppear {
            manager.reloadConfig()
            let value = manager.endpoint
            address = value.replacingOccurrences(of: "http://", with: "")
                .replacingOccurrences(of: "/v1", with: "")
            model = manager.defaultModel
            reuse = manager.sessionReuse
        }
    }
}

private struct LogsView: View {
    @ObservedObject var manager: ProxyManager

    var body: some View {
        Surface {
            HStack {
                Text("进程输出")
                    .font(.system(size: 18, weight: .semibold))
                Spacer()
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(manager.logText, forType: .string)
                } label: {
                    Label("复制日志", systemImage: "doc.on.doc")
                }
            }
            ScrollView {
                Text(manager.logText.isEmpty ? "启动代理后，运行日志将显示在这里。" : manager.logText)
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(Style.ink)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(14)
            }
            .frame(minHeight: 360)
            .background(Style.background)
            .clipShape(RoundedRectangle(cornerRadius: 9))
            .padding(.top, 14)
        }
    }
}

private struct RootView: View {
    @ObservedObject var manager: ProxyManager
    @State private var selection: AppPage = .overview

    var body: some View {
        HStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 10) {
                    Image(systemName: "point.3.connected.trianglepath.dotted")
                        .font(.system(size: 23, weight: .semibold))
                        .foregroundStyle(Color.white)
                        .frame(width: 37, height: 37)
                        .background(Style.accent)
                        .clipShape(RoundedRectangle(cornerRadius: 11))
                    VStack(alignment: .leading, spacing: 2) {
                        Text("QuickProxy")
                            .font(.system(size: 17, weight: .semibold))
                            .foregroundStyle(Color.white)
                        Text("Amazon Quick API")
                            .font(.system(size: 10))
                            .foregroundStyle(Color.white.opacity(0.6))
                    }
                }
                .padding(.bottom, 36)

                ForEach(AppPage.allCases) { page in
                    Button {
                        selection = page
                    } label: {
                        HStack(spacing: 12) {
                            Image(systemName: page.icon)
                                .frame(width: 20)
                            Text(page.rawValue)
                            Spacer()
                        }
                        .font(.system(size: 13, weight: selection == page ? .semibold : .medium))
                        .foregroundStyle(selection == page ? Color.white : Color.white.opacity(0.66))
                        .padding(.horizontal, 13)
                        .padding(.vertical, 12)
                        .background(
                            selection == page ? Color.white.opacity(0.13) : Color.clear
                        )
                        .clipShape(RoundedRectangle(cornerRadius: 9))
                    }
                    .buttonStyle(.plain)
                    .padding(.bottom, 5)
                }

                Spacer()
                HStack(spacing: 8) {
                    Circle()
                        .fill(manager.isRunning ? Style.success : Color.gray)
                        .frame(width: 8, height: 8)
                    Text(manager.isRunning ? "服务运行中" : "服务已停止")
                        .font(.system(size: 12))
                        .foregroundStyle(Color.white.opacity(0.75))
                }
                .padding(.bottom, 10)
                Text("数据仅保存在本机")
                    .font(.system(size: 11))
                    .foregroundStyle(Color.white.opacity(0.42))
            }
            .padding(22)
            .frame(width: 226)
            .background(Style.sidebar)

            VStack(spacing: 0) {
                HStack {
                    VStack(alignment: .leading, spacing: 5) {
                        Text(selection.rawValue)
                            .font(.system(size: 23, weight: .bold))
                            .foregroundStyle(Style.ink)
                        Text(selection.subtitle)
                            .font(.system(size: 12))
                            .foregroundStyle(Style.secondary)
                    }
                    Spacer()
                    if manager.isWorking {
                        ProgressView()
                            .controlSize(.small)
                        Text(manager.operation)
                            .font(.system(size: 12))
                            .foregroundStyle(Style.secondary)
                    } else {
                        Button {
                            manager.refreshAccounts()
                        } label: {
                            Label("刷新账号", systemImage: "arrow.clockwise")
                        }
                        .disabled(manager.isWorking)
                    }
                }
                .padding(.horizontal, 28)
                .padding(.vertical, 25)

                Rectangle().fill(Style.border).frame(height: 1)

                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        if let error = manager.lastError {
                            Label(error, systemImage: "exclamationmark.triangle")
                                .font(.system(size: 12))
                                .foregroundStyle(Color(red: 0.66, green: 0.21, blue: 0.14))
                                .padding(12)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Color(red: 1, green: 0.94, blue: 0.91))
                                .clipShape(RoundedRectangle(cornerRadius: 9))
                        }
                        if let notice = manager.notice {
                            Label(notice, systemImage: "checkmark.circle")
                                .font(.system(size: 12))
                                .foregroundStyle(Style.success)
                                .padding(12)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Style.success.opacity(0.08))
                                .clipShape(RoundedRectangle(cornerRadius: 9))
                        }
                        switch selection {
                        case .overview: OverviewView(manager: manager)
                        case .accounts: AccountsView(manager: manager)
                        case .settings: SettingsView(manager: manager)
                        case .logs: LogsView(manager: manager)
                        }
                    }
                    .padding(27)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .background(Style.background)
        }
        .frame(minWidth: 940, minHeight: 640)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    weak var manager: ProxyManager?

    func applicationWillTerminate(_ notification: Notification) {
        manager?.shutdown()
    }
}

@main
struct QuickProxyApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var manager = ProxyManager()

    var body: some Scene {
        WindowGroup("QuickProxy", id: "main") {
            RootView(manager: manager)
                .onAppear {
                    appDelegate.manager = manager
                    manager.prepare()
                }
        }
        .defaultSize(width: 1010, height: 680)

        MenuBarExtra("QuickProxy", systemImage: "point.3.connected.trianglepath.dotted") {
            Text(manager.isRunning ? "代理服务运行中" : "代理服务已停止")
            Text(manager.endpoint)
            Divider()
            Button(manager.isRunning ? "停止代理" : "启动代理") {
                manager.isRunning ? manager.stopServer() : manager.startServer()
            }
            Button("刷新账号") { manager.refreshAccounts() }
                .disabled(manager.isWorking)
            Divider()
            OpenMainWindowButton()
            Button("退出 QuickProxy") {
                manager.shutdown()
                NSApplication.shared.terminate(nil)
            }
        }
        .menuBarExtraStyle(.menu)
    }
}

private struct OpenMainWindowButton: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Button("打开管理窗口") { openWindow(id: "main") }
    }
}
