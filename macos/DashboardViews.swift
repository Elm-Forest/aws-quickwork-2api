import AppKit
import Foundation
import SwiftUI

private enum Theme {
    static let canvas = Color(red: 0.965, green: 0.972, blue: 0.980)
    static let sidebar = Color(red: 0.090, green: 0.141, blue: 0.193)
    static let ink = Color(red: 0.117, green: 0.172, blue: 0.235)
    static let muted = Color(red: 0.429, green: 0.488, blue: 0.552)
    static let line = Color(red: 0.870, green: 0.901, blue: 0.925)
    static let accent = Color(red: 0.056, green: 0.451, blue: 0.621)
    static let accentBackground = Color(red: 0.907, green: 0.955, blue: 0.978)
    static let positive = Color(red: 0.103, green: 0.529, blue: 0.368)
}

private enum Page: String, CaseIterable, Identifiable {
    case overview = "概览"
    case accounts = "账号管理"
    case keys = "API 密钥"
    case settings = "代理设置"
    case logs = "运行日志"

    var id: String { rawValue }

    var icon: String {
        switch self {
        case .overview: return "square.grid.2x2"
        case .accounts: return "person.2"
        case .keys: return "key.horizontal"
        case .settings: return "slider.horizontal.3"
        case .logs: return "text.alignleft"
        }
    }

    var description: String {
        switch self {
        case .overview: return "查看服务状态和 API 接入信息"
        case .accounts: return "登录、刷新和管理上游账号"
        case .keys: return "设置本地 API 访问密钥"
        case .settings: return "修改监听端口、模型和会话配置"
        case .logs: return "查看代理进程输出"
        }
    }
}

private struct Panel<Content: View>: View {
    let content: Content

    init(@ViewBuilder content: () -> Content) {
        self.content = content()
    }

    var body: some View {
        content
            .padding(22)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.white)
            .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .stroke(Theme.line, lineWidth: 1)
            )
    }
}

private struct PanelTitle: View {
    let title: String
    let subtitle: String?

    init(_ title: String, subtitle: String? = nil) {
        self.title = title
        self.subtitle = subtitle
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(title)
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(Theme.ink)
            if let subtitle {
                Text(subtitle)
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.muted)
            }
        }
    }
}

private struct StatusTag: View {
    let text: String
    let active: Bool

    var body: some View {
        HStack(spacing: 6) {
            Circle()
                .fill(active ? Theme.positive : Theme.muted)
                .frame(width: 7, height: 7)
            Text(text)
                .font(.system(size: 11, weight: .medium))
        }
        .foregroundStyle(active ? Theme.positive : Theme.muted)
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
        .background(active ? Theme.positive.opacity(0.09) : Theme.canvas)
        .clipShape(Capsule())
    }
}

private struct Statistic: View {
    let label: String
    let value: String
    let detail: String
    let symbol: String

    var body: some View {
        Panel {
            VStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 7) {
                    Image(systemName: symbol)
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(Theme.accent)
                    Text(label)
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(Theme.muted)
                        .lineLimit(1)
                    Spacer(minLength: 0)
                }
                Text(value)
                    .font(.system(size: 26, weight: .semibold, design: .rounded))
                    .foregroundStyle(Theme.ink)
                    .lineLimit(1)
                    .minimumScaleFactor(0.75)
                Text(detail)
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.muted)
                    .lineLimit(1)
            }
            .frame(maxWidth: .infinity, minHeight: 92, alignment: .leading)
        }
    }
}

private struct OverviewPage: View {
    @ObservedObject var manager: ProxyManager

    private let statistics = [
        GridItem(.flexible(minimum: 160), spacing: 12),
        GridItem(.flexible(minimum: 160), spacing: 12),
        GridItem(.flexible(minimum: 160), spacing: 12)
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Panel {
                HStack(spacing: 17) {
                    Image(systemName: "point.3.connected.trianglepath.dotted")
                        .font(.system(size: 29, weight: .medium))
                        .foregroundStyle(manager.isRunning ? Theme.positive : Theme.accent)
                        .frame(width: 60, height: 60)
                        .background(manager.isRunning ? Theme.positive.opacity(0.11) : Theme.accentBackground)
                        .clipShape(RoundedRectangle(cornerRadius: 14))

                    VStack(alignment: .leading, spacing: 7) {
                        Text(manager.isRunning ? "代理服务运行中" : "代理服务已停止")
                            .font(.system(size: 21, weight: .semibold))
                            .foregroundStyle(Theme.ink)
                            .lineLimit(1)
                        Text(manager.isRunning ? "正在接收本机 API 请求" : "启动服务后即可使用 API 接口")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.muted)
                    }

                    Spacer(minLength: 14)

                    Button {
                        manager.isRunning ? manager.stopServer() : manager.startServer()
                    } label: {
                        Label(
                            manager.isRunning ? "停止代理" : "启动代理",
                            systemImage: manager.isRunning ? "stop.fill" : "play.fill"
                        )
                        .font(.system(size: 13, weight: .semibold))
                        .frame(minWidth: 90)
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.large)
                    .tint(manager.isRunning ? Theme.ink : Theme.accent)
                }
            }

            LazyVGrid(columns: statistics, spacing: 12) {
                Statistic(
                    label: "服务状态",
                    value: manager.isRunning ? "运行中" : "已停止",
                    detail: "监听 \(manager.listenHost):\(manager.listenPort)",
                    symbol: "antenna.radiowaves.left.and.right"
                )
                Statistic(
                    label: "上游账号",
                    value: "\(manager.accounts.count)",
                    detail: "支持多账号轮询",
                    symbol: "person.2.fill"
                )
                Statistic(
                    label: "默认模型",
                    value: manager.defaultModel,
                    detail: "当前配置",
                    symbol: "cpu"
                )
            }

            Panel {
                VStack(alignment: .leading, spacing: 16) {
                    HStack {
                        PanelTitle("API 接入", subtitle: "兼容 OpenAI Chat Completions")
                        Spacer()
                        StatusTag(text: manager.apiKeys.isEmpty ? "未启用鉴权" : "已启用鉴权", active: !manager.apiKeys.isEmpty)
                    }

                    Divider()

                    Text("BASE URL")
                        .font(.system(size: 10, weight: .semibold))
                        .tracking(1)
                        .foregroundStyle(Theme.muted)

                    HStack(spacing: 12) {
                        Text(manager.endpoint)
                            .font(.system(size: 15, weight: .medium, design: .monospaced))
                            .foregroundStyle(Theme.ink)
                            .lineLimit(1)
                            .textSelection(.enabled)
                        Spacer(minLength: 12)
                        Button {
                            manager.copyEndpoint()
                        } label: {
                            Label("复制地址", systemImage: "doc.on.doc")
                        }
                        .buttonStyle(.bordered)
                    }
                    .padding(.horizontal, 15)
                    .padding(.vertical, 13)
                    .background(Theme.canvas)
                    .clipShape(RoundedRectangle(cornerRadius: 10))

                    HStack(spacing: 8) {
                        Image(systemName: "terminal")
                            .foregroundStyle(Theme.muted)
                        Text("/v1/chat/completions")
                        Text("·")
                        Text("/v1/models")
                    }
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(Theme.muted)
                }
            }

            Panel {
                VStack(alignment: .leading, spacing: 13) {
                    HStack {
                        PanelTitle(
                            "已观测底层模型",
                            subtitle: "来自上游 turn_usage；选择时仍按对应档位路由"
                        )
                        Spacer(minLength: 12)
                        Button {
                            manager.reloadObservedModels()
                        } label: {
                            Label("刷新", systemImage: "arrow.clockwise")
                        }
                    }
                    Divider()
                    if manager.observedModels.isEmpty {
                        Text("尚无模型观测记录。成功调用模型并刷新后，将显示底层模型标识。")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.muted)
                            .padding(.vertical, 8)
                    } else {
                        ForEach(manager.observedModels) { item in
                            HStack(alignment: .top, spacing: 12) {
                                Text(item.id)
                                    .font(.system(size: 12, design: .monospaced))
                                    .foregroundStyle(Theme.ink)
                                    .textSelection(.enabled)
                                    .fixedSize(horizontal: false, vertical: true)
                                Spacer(minLength: 8)
                                Text(item.agentMode)
                                    .font(.system(size: 11, weight: .medium))
                                    .foregroundStyle(Theme.accent)
                                    .padding(.horizontal, 9)
                                    .padding(.vertical, 4)
                                    .background(Theme.accentBackground)
                                    .clipShape(Capsule())
                            }
                            .padding(.vertical, 4)
                        }
                    }
                }
            }

            if manager.accounts.isEmpty {
                HStack(spacing: 10) {
                    Image(systemName: "info.circle")
                        .foregroundStyle(Theme.accent)
                    Text("尚未添加上游账号。请前往“账号管理”完成登录。")
                        .font(.system(size: 12))
                        .foregroundStyle(Theme.ink)
                }
                .padding(.horizontal, 6)
            }
        }
    }
}

private struct AccountsPage: View {
    @ObservedObject var manager: ProxyManager
    @State private var pendingRemoval: AccountEntry?

    var body: some View {
        Panel {
            VStack(alignment: .leading, spacing: 18) {
                HStack(spacing: 14) {
                    PanelTitle("上游账号", subtitle: "登录信息存储在本机数据库中")
                    Spacer()
                    Button {
                        manager.refreshAccounts()
                    } label: {
                        Label("刷新列表", systemImage: "arrow.clockwise")
                    }
                    .disabled(manager.isWorking)
                    Button("刷新令牌") { manager.refreshTokens() }
                        .disabled(manager.isWorking || manager.accounts.isEmpty)
                    Button {
                        manager.login()
                    } label: {
                        Label("添加账号", systemImage: "plus")
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(Theme.accent)
                    .disabled(manager.isWorking)
                }

                Divider()

                if manager.accounts.isEmpty {
                    VStack(spacing: 12) {
                        Image(systemName: "person.crop.circle.badge.plus")
                            .font(.system(size: 33))
                            .foregroundStyle(Theme.accent)
                        Text("尚未添加账号")
                            .font(.system(size: 15, weight: .medium))
                            .foregroundStyle(Theme.ink)
                        Text("点击“添加账号”，在浏览器中完成 Amazon Quick 登录。")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.muted)
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 44)
                } else {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(manager.accounts) { account in
                            HStack(spacing: 14) {
                                Image(systemName: "person.crop.circle.fill")
                                    .font(.system(size: 27))
                                    .foregroundStyle(Theme.accent)
                                    .frame(width: 36)
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(account.email)
                                        .font(.system(size: 13, weight: .semibold))
                                        .foregroundStyle(Theme.ink)
                                        .lineLimit(1)
                                    Text("\(account.provider) · 到期：\(account.expires) · 调用 \(account.uses) 次")
                                        .font(.system(size: 11))
                                        .foregroundStyle(Theme.muted)
                                        .lineLimit(1)
                                }
                                Spacer(minLength: 10)
                                StatusTag(text: account.isReady ? "正常" : account.status, active: account.isReady)
                                Button("移除") { pendingRemoval = account }
                                    .disabled(manager.isWorking)
                            }
                            .padding(.vertical, 11)
                            Divider()
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
            )
        ) {
            Button("确认移除", role: .destructive) {
                if let account = pendingRemoval {
                    manager.removeAccount(account)
                }
                pendingRemoval = nil
            }
            Button("取消", role: .cancel) { pendingRemoval = nil }
        } message: {
            Text("将从本机数据库删除该账号的登录记录。")
        }
    }
}

private struct KeysPage: View {
    @ObservedObject var manager: ProxyManager

    @State private var keys: [String] = []
    @State private var draftKey = ""
    @State private var feedback: String?
    @State private var loaded = false

    private var isDirty: Bool { keys != manager.apiKeys }

    private func copy(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
        feedback = "已复制到剪贴板。"
    }

    private func addKey() {
        let value = draftKey.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else {
            feedback = "请输入 API Key。"
            return
        }
        guard !value.contains(where: { $0.isWhitespace || $0.isNewline }) else {
            feedback = "API Key 不能包含空白字符。"
            return
        }
        guard !keys.contains(value) else {
            feedback = "该密钥已存在。"
            return
        }
        keys.append(value)
        draftKey = ""
        feedback = "已添加到列表，请保存配置。"
    }

    private func generateKey() {
        let value = "sk-qp-" +
            UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased() +
            UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased()
        keys.append(value)
        copy(value)
        feedback = "已生成并复制密钥，请保存配置。"
    }

    private func save(restart: Bool) {
        guard manager.saveAPIKeys(keys) else { return }
        feedback = nil
        if restart { manager.restartServer() }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Panel {
                HStack(spacing: 16) {
                    Image(systemName: "key.horizontal.fill")
                        .font(.system(size: 21))
                        .foregroundStyle(Theme.accent)
                        .frame(width: 48, height: 48)
                        .background(Theme.accentBackground)
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                    VStack(alignment: .leading, spacing: 5) {
                        Text("API 访问鉴权")
                            .font(.system(size: 16, weight: .semibold))
                            .foregroundStyle(Theme.ink)
                        Text(keys.isEmpty ? "密钥列表为空，本机 API 无需密钥。" : "客户端请求需要携带配置的 API Key。")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.muted)
                    }
                    Spacer()
                    StatusTag(text: keys.isEmpty ? "关闭" : "启用", active: !keys.isEmpty)
                }
            }

            Panel {
                VStack(alignment: .leading, spacing: 17) {
                    HStack {
                        PanelTitle("密钥列表", subtitle: "已保存 \(manager.apiKeys.count) 条 · 当前列表 \(keys.count) 条")
                        Spacer()
                        if isDirty {
                            Text("有未保存的更改")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(Theme.accent)
                        }
                    }
                    Divider()

                    if keys.isEmpty {
                        Text("暂无 API Key")
                            .font(.system(size: 13))
                            .foregroundStyle(Theme.muted)
                            .frame(maxWidth: .infinity, alignment: .center)
                            .padding(.vertical, 20)
                    } else {
                        VStack(spacing: 0) {
                            ForEach(keys, id: \.self) { key in
                                HStack(spacing: 12) {
                                    Image(systemName: "key")
                                        .foregroundStyle(Theme.muted)
                                    Text("••••••••••••" + String(key.suffix(8)))
                                        .font(.system(size: 13, design: .monospaced))
                                        .foregroundStyle(Theme.ink)
                                    Spacer()
                                    Button("复制") { copy(key) }
                                    Button("移除") {
                                        keys.removeAll { $0 == key }
                                        feedback = "已从列表移除，请保存配置。"
                                    }
                                    .foregroundStyle(.red)
                                }
                                .padding(.vertical, 11)
                                Divider()
                            }
                        }
                    }

                    Divider()

                    Text("添加密钥")
                        .font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(Theme.ink)
                    HStack(spacing: 10) {
                        SecureField("输入 API Key", text: $draftKey)
                            .textFieldStyle(.roundedBorder)
                            .frame(maxWidth: .infinity)
                            .onSubmit(addKey)
                        Button("添加") { addKey() }
                            .disabled(draftKey.isEmpty)
                        Button {
                            generateKey()
                        } label: {
                            Label("生成密钥", systemImage: "arrow.triangle.2.circlepath")
                        }
                        .buttonStyle(.bordered)
                    }
                    Text("请求头：Authorization: Bearer <API Key>")
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(Theme.muted)
                        .textSelection(.enabled)

                    if let feedback {
                        Label(feedback, systemImage: "info.circle")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.accent)
                    }

                    HStack {
                        Text(manager.isRunning ? "服务运行中，保存后需重启生效。" : "保存后将在下次启动时生效。")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.muted)
                        Spacer()
                        Button("保存配置") { save(restart: false) }
                            .disabled(!isDirty)
                        if manager.isRunning {
                            Button("保存并重启") { save(restart: true) }
                                .buttonStyle(.borderedProminent)
                                .tint(Theme.accent)
                                .disabled(!isDirty)
                        }
                    }
                    .padding(.top, 5)
                }
            }
        }
        .onAppear {
            if !loaded {
                keys = manager.apiKeys
                loaded = true
            }
        }
    }
}

private struct SettingsPage: View {
    @ObservedObject var manager: ProxyManager

    @State private var host = "127.0.0.1"
    @State private var port = "8080"
    @State private var model = "smart"
    @State private var reuse = true

    private func save(restart: Bool) {
        if manager.saveSettings(host: host, port: port, model: model, reuse: reuse),
           restart {
            manager.restartServer()
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Panel {
                VStack(alignment: .leading, spacing: 18) {
                    PanelTitle("网络设置", subtitle: "修改 API 服务监听的主机与端口")
                    Divider()
                    HStack(spacing: 18) {
                        VStack(alignment: .leading, spacing: 7) {
                            Text("监听主机")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(Theme.ink)
                            Picker("监听主机", selection: $host) {
                                Text("127.0.0.1").tag("127.0.0.1")
                                Text("localhost").tag("localhost")
                            }
                            .labelsHidden()
                            .frame(width: 210)
                        }
                        VStack(alignment: .leading, spacing: 7) {
                            Text("监听端口")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(Theme.ink)
                            TextField("8080", text: $port)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 145)
                        }
                        Spacer()
                    }
                    Text("当前地址：http://\(host):\(port)/v1")
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(Theme.muted)
                        .textSelection(.enabled)
                }
            }

            Panel {
                VStack(alignment: .leading, spacing: 18) {
                    PanelTitle("模型与会话", subtitle: "选择默认模型并配置上游会话复用")
                    Divider()
                    HStack(spacing: 24) {
                        VStack(alignment: .leading, spacing: 7) {
                            Text("默认模型")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(Theme.ink)
                            Picker("默认模型", selection: $model) {
                                ForEach(manager.modelOptions, id: \.self) { value in
                                    Text(value).tag(value)
                                }
                            }
                            .labelsHidden()
                            .frame(width: 210)
                        }
                        Spacer()
                        Toggle("复用上游会话", isOn: $reuse)
                            .toggleStyle(.switch)
                            .fixedSize()
                    }
                    HStack {
                        Text(manager.isRunning ? "修改后需重启代理才能生效。" : "保存后将在下次启动时生效。")
                            .font(.system(size: 12))
                            .foregroundStyle(Theme.muted)
                        Spacer()
                        Button("保存设置") { save(restart: false) }
                        if manager.isRunning {
                            Button("保存并重启") { save(restart: true) }
                                .buttonStyle(.borderedProminent)
                                .tint(Theme.accent)
                        }
                    }
                    .padding(.top, 6)
                }
            }

            Panel {
                VStack(alignment: .leading, spacing: 12) {
                    PanelTitle("配置文件", subtitle: "高级配置可以直接编辑 config.json")
                    Text(manager.supportURL.path)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(Theme.muted)
                        .textSelection(.enabled)
                    HStack(spacing: 10) {
                        Button {
                            manager.openConfig()
                        } label: {
                            Label("打开配置文件", systemImage: "doc.text")
                        }
                        Button {
                            manager.openDataDirectory()
                        } label: {
                            Label("打开数据目录", systemImage: "folder")
                        }
                    }
                }
            }
        }
        .onAppear {
            manager.reloadConfig()
            host = manager.listenHost
            port = manager.listenPort
            model = manager.defaultModel
            reuse = manager.sessionReuse
        }
    }
}

private struct LogsPage: View {
    @ObservedObject var manager: ProxyManager

    var body: some View {
        Panel {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    PanelTitle("进程输出", subtitle: "查看代理启动与请求处理日志")
                    Spacer()
                    Button {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(manager.logText, forType: .string)
                    } label: {
                        Label("复制日志", systemImage: "doc.on.doc")
                    }
                    .disabled(manager.logText.isEmpty)
                }
                Divider()
                ScrollView {
                    Text(manager.logText.isEmpty ? "启动代理后，日志将显示在这里。" : manager.logText)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(Theme.ink)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(16)
                }
                .frame(minHeight: 360)
                .background(Theme.canvas)
                .clipShape(RoundedRectangle(cornerRadius: 10))
            }
        }
    }
}

private struct RootView: View {
    @ObservedObject var manager: ProxyManager
    @State private var page: Page = .overview

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 11) {
                Image(systemName: "point.3.connected.trianglepath.dotted")
                    .font(.system(size: 22))
                    .frame(width: 37, height: 37)
                    .background(Theme.accent)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
                VStack(alignment: .leading, spacing: 3) {
                    Text("QuickProxy")
                        .font(.system(size: 16, weight: .bold))
                    Text("API 代理管理")
                        .font(.system(size: 11))
                        .foregroundStyle(Color.white.opacity(0.57))
                }
            }
            .foregroundStyle(Color.white)
            .padding(.bottom, 32)

            ForEach(Page.allCases) { item in
                Button {
                    page = item
                } label: {
                    HStack(spacing: 12) {
                        Image(systemName: item.icon)
                            .frame(width: 19)
                        Text(item.rawValue)
                        Spacer(minLength: 0)
                    }
                    .font(.system(size: 13, weight: page == item ? .semibold : .medium))
                    .foregroundStyle(page == item ? Color.white : Color.white.opacity(0.70))
                    .padding(.horizontal, 13)
                    .padding(.vertical, 12)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(page == item ? Color.white.opacity(0.14) : Color.clear)
                    .clipShape(RoundedRectangle(cornerRadius: 9))
                }
                .buttonStyle(.plain)
                .padding(.bottom, 5)
            }

            Spacer(minLength: 20)

            HStack(spacing: 8) {
                Circle()
                    .fill(manager.isRunning ? Theme.positive : Color.gray)
                    .frame(width: 8, height: 8)
                Text(manager.isRunning ? "服务运行中" : "服务已停止")
                    .font(.system(size: 12))
                    .foregroundStyle(Color.white.opacity(0.78))
            }
            .padding(.horizontal, 12)
            .padding(.bottom, 12)
        }
        .padding(20)
        .frame(width: 218)
        .background(Theme.sidebar)
    }

    private var heading: some View {
        HStack(alignment: .center) {
            VStack(alignment: .leading, spacing: 6) {
                Text(page.rawValue)
                    .font(.system(size: 23, weight: .bold))
                    .foregroundStyle(Theme.ink)
                Text(page.description)
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.muted)
            }
            Spacer(minLength: 15)
            if manager.isWorking {
                ProgressView()
                    .controlSize(.small)
                Text(manager.operation)
                    .font(.system(size: 11))
                    .foregroundStyle(Theme.muted)
            } else {
                StatusTag(
                    text: manager.isRunning ? "服务运行中" : "服务已停止",
                    active: manager.isRunning
                )
            }
        }
        .padding(.horizontal, 28)
        .padding(.vertical, 22)
        .background(Color.white)
    }

    @ViewBuilder
    private var pageContent: some View {
        switch page {
        case .overview: OverviewPage(manager: manager)
        case .accounts: AccountsPage(manager: manager)
        case .keys: KeysPage(manager: manager)
        case .settings: SettingsPage(manager: manager)
        case .logs: LogsPage(manager: manager)
        }
    }

    var body: some View {
        HStack(spacing: 0) {
            sidebar
            VStack(spacing: 0) {
                heading
                Rectangle().fill(Theme.line).frame(height: 1)
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        if let error = manager.lastError {
                            Label(error, systemImage: "exclamationmark.triangle")
                                .font(.system(size: 12))
                                .foregroundStyle(Color(red: 0.71, green: 0.24, blue: 0.18))
                                .padding(13)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Color(red: 1, green: 0.944, blue: 0.93))
                                .clipShape(RoundedRectangle(cornerRadius: 9))
                        }
                        if let notice = manager.notice {
                            Label(notice, systemImage: "checkmark.circle")
                                .font(.system(size: 12))
                                .foregroundStyle(Theme.positive)
                                .padding(13)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(Theme.positive.opacity(0.08))
                                .clipShape(RoundedRectangle(cornerRadius: 9))
                        }
                        pageContent
                    }
                    .frame(maxWidth: 1040, alignment: .leading)
                    .padding(26)
                    .frame(maxWidth: .infinity, alignment: .top)
                }
                .background(Theme.canvas)
            }
        }
        .frame(minWidth: 980, minHeight: 680)
    }
}

private final class AppDelegate: NSObject, NSApplicationDelegate {
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
                .preferredColorScheme(.light)
                .onAppear {
                    appDelegate.manager = manager
                    manager.prepare()
                }
        }
        .defaultSize(width: 1080, height: 720)

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
            OpenWindowButton()
            Button("退出 QuickProxy") { NSApplication.shared.terminate(nil) }
        }
        .menuBarExtraStyle(.menu)
    }
}

private struct OpenWindowButton: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Button("打开管理窗口") { openWindow(id: "main") }
    }
}
