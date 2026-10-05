import AppKit
import WebKit
import Foundation
import Security
import Darwin

private struct DesktopError: LocalizedError {
    let message: String
    var errorDescription: String? { message }
}

private final class DownloadRecord {
    let download: WKDownload
    var destination: URL?
    var temporary: URL?
    init(_ download: WKDownload) { self.download = download }
}

final class DBXMCPApp: NSObject, NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate, WKDownloadDelegate {
    private var window: NSWindow!
    private var webView: WKWebView!
    private var statusLabel: NSTextField!
    private var backend: Process?
    private var backendInput: FileHandle?
    private var backendLog: FileHandle?
    private var lockDescriptor: Int32 = -1
    private var startupTask: URLSessionDataTask?
    private var startupGeneration = UUID()
    private var quitting = false
    private var stopping = false
    private var shutdownGeneration: UUID?
    private var shutdownCompletions: [() -> Void] = []
    private var failurePresented = false
    private var port = 4227
    private var dataDirectory: URL?
    private var backendExecutable: URL?
    private var password = ""
    private var downloads: [ObjectIdentifier: DownloadRecord] = [:]
    private let httpSession = URLSession(configuration: .ephemeral)
    private var baseURL: URL { URL(string: "http://127.0.0.1:\(port)")! }

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildMenu()
        buildWindow()
        startBackend()
    }

    private func buildWindow() {
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1280, height: 820), styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "数据库工作台"
        window.minSize = NSSize(width: 960, height: 640)
        window.delegate = self
        window.setFrameAutosaveName("DBXMCPMainWindow")
        window.center()

        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .default()
        webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.translatesAutoresizingMaskIntoConstraints = false
        webView.allowsBackForwardNavigationGestures = false
        let content = NSView(frame: window.contentView!.bounds)
        content.addSubview(webView)
        NSLayoutConstraint.activate([
            webView.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            webView.topAnchor.constraint(equalTo: content.topAnchor),
            webView.bottomAnchor.constraint(equalTo: content.bottomAnchor)
        ])
        statusLabel = NSTextField(wrappingLabelWithString: "正在启动数据库工作台…")
        statusLabel.alignment = .center
        statusLabel.font = .systemFont(ofSize: 16)
        statusLabel.translatesAutoresizingMaskIntoConstraints = false
        content.addSubview(statusLabel)
        NSLayoutConstraint.activate([
            statusLabel.centerXAnchor.constraint(equalTo: content.centerXAnchor),
            statusLabel.centerYAnchor.constraint(equalTo: content.centerYAnchor),
            statusLabel.widthAnchor.constraint(lessThanOrEqualToConstant: 640)
        ])
        webView.isHidden = true
        window.contentView = content
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func menuItem(_ title: String, action: Selector, key: String = "", target: AnyObject? = nil) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
        item.target = target
        return item
    }

    private func buildMenu() {
        let main = NSMenu()
        let appMenu = NSMenu(title: "Database Workbench")
        appMenu.addItem(menuItem("关于数据库工作台", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), target: NSApp))
        appMenu.addItem(.separator())
        appMenu.addItem(menuItem("隐藏数据库工作台", action: #selector(NSApplication.hide(_:)), key: "h", target: NSApp))
        let hideOthers = menuItem("隐藏其他应用", action: #selector(NSApplication.hideOtherApplications(_:)), key: "h", target: NSApp)
        hideOthers.keyEquivalentModifierMask = [.command, .option]
        appMenu.addItem(hideOthers)
        appMenu.addItem(menuItem("显示全部", action: #selector(NSApplication.unhideAllApplications(_:)), target: NSApp))
        appMenu.addItem(.separator())
        appMenu.addItem(menuItem("退出数据库工作台", action: #selector(NSApplication.terminate(_:)), key: "q", target: NSApp))
        let appItem = NSMenuItem()
        appItem.submenu = appMenu
        main.addItem(appItem)

        let edit = NSMenu(title: "编辑")
        edit.addItem(menuItem("撤销", action: Selector(("undo:")), key: "z"))
        let redo = menuItem("重做", action: Selector(("redo:")), key: "z")
        redo.keyEquivalentModifierMask = [.command, .shift]
        edit.addItem(redo)
        edit.addItem(.separator())
        edit.addItem(menuItem("剪切", action: #selector(NSText.cut(_:)), key: "x"))
        edit.addItem(menuItem("复制", action: #selector(NSText.copy(_:)), key: "c"))
        edit.addItem(menuItem("粘贴", action: #selector(NSText.paste(_:)), key: "v"))
        edit.addItem(menuItem("全选", action: #selector(NSText.selectAll(_:)), key: "a"))
        let editItem = NSMenuItem(title: "编辑", action: nil, keyEquivalent: "")
        editItem.submenu = edit
        main.addItem(editItem)

        let workspace = NSMenu(title: "工作台")
        let reload = menuItem("重新加载工作台", action: #selector(reloadWorkspace), key: "r", target: self)
        reload.keyEquivalentModifierMask = [.control, .command]
        workspace.addItem(reload)
        workspace.addItem(menuItem("重新启动后端", action: #selector(restartBackend), target: self))
        workspace.addItem(.separator())
        workspace.addItem(menuItem("显示数据文件夹", action: #selector(revealDataDirectory), target: self))
        workspace.addItem(menuItem("显示后端日志", action: #selector(revealBackendLog), target: self))
        workspace.addItem(menuItem("显示 MCP 客户端配置", action: #selector(revealMCPConfiguration), target: self))
        let workspaceItem = NSMenuItem(title: "工作台", action: nil, keyEquivalent: "")
        workspaceItem.submenu = workspace
        main.addItem(workspaceItem)

        let windowMenu = NSMenu(title: "窗口")
        windowMenu.addItem(menuItem("最小化", action: #selector(NSWindow.performMiniaturize(_:)), key: "m"))
        windowMenu.addItem(menuItem("缩放", action: #selector(NSWindow.performZoom(_:))))
        let windowItem = NSMenuItem(title: "窗口", action: nil, keyEquivalent: "")
        windowItem.submenu = windowMenu
        main.addItem(windowItem)
        NSApp.mainMenu = main
        NSApp.windowsMenu = windowMenu
    }

    private func configureStorage() throws {
        let env = ProcessInfo.processInfo.environment
        if let configured = env["DBX_DESKTOP_PORT"] {
            guard let value = Int(configured), (1024...65535).contains(value) else {
                throw DesktopError(message: "DBX_DESKTOP_PORT 必须是 1024 到 65535 之间的端口。")
            }
            port = value
        }
        let path = env["DBX_DESKTOP_DATA_DIR"] ?? NSHomeDirectory() + "/Library/Application Support/DBX MCP Desktop"
        let expanded = (path as NSString).expandingTildeInPath
        guard expanded.hasPrefix("/") else { throw DesktopError(message: "DBX_DESKTOP_DATA_DIR 必须是绝对路径。") }
        let directory = URL(fileURLWithPath: expanded, isDirectory: true).standardizedFileURL
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)
        dataDirectory = directory
        if lockDescriptor == -1 {
            let descriptor = Darwin.open(directory.appendingPathComponent("desktop.lock").path, O_CREAT | O_RDWR | O_NOFOLLOW | O_CLOEXEC, 0o600)
            guard descriptor >= 0 else { throw DesktopError(message: "无法创建工作台锁文件。") }
            guard flock(descriptor, LOCK_EX | LOCK_NB) == 0 else {
                Darwin.close(descriptor)
                throw DesktopError(message: "此数据目录已有数据库工作台实例在运行。请切换到已有窗口，或使用另一个数据目录。")
            }
            lockDescriptor = descriptor
        }
        password = try persistentSecret(named: "web-password", in: directory)
        let token = try persistentSecret(named: "mcp-token", in: directory)
        let configuration: [String: Any] = ["mcpServers": ["dbx": ["url": baseURL.appendingPathComponent("mcp").absoluteString, "headers": ["Authorization": "Bearer \(token)"]]]]
        try writePrivateJSON(configuration, to: directory.appendingPathComponent("mcp-client.json"))
    }

    private func persistentSecret(named name: String, in directory: URL) throws -> String {
        let url = directory.appendingPathComponent(name)
        var info = stat()
        if lstat(url.path, &info) == 0 {
            guard info.st_mode & S_IFMT == S_IFREG, info.st_uid == getuid(), info.st_size > 0, info.st_size <= 4096 else {
                throw DesktopError(message: "凭据文件 \(name) 不是有效的当前用户私有文件。")
            }
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
            let value = try String(contentsOf: url, encoding: .utf8).trimmingCharacters(in: .newlines)
            guard !value.isEmpty, value.rangeOfCharacter(from: .whitespacesAndNewlines) == nil else {
                throw DesktopError(message: "凭据文件 \(name) 内容无效。")
            }
            return value
        }
        guard errno == ENOENT else { throw DesktopError(message: "无法读取凭据文件 \(name)。") }
        var bytes = [UInt8](repeating: 0, count: 32)
        guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else {
            throw DesktopError(message: "无法生成本地访问凭据。")
        }
        let value = bytes.map { String(format: "%02x", $0) }.joined()
        try writePrivate(Data(value.utf8), to: url)
        return value
    }

    private func writePrivateJSON(_ value: [String: Any], to url: URL) throws {
        try writePrivate(JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys]), to: url)
    }

    private func writePrivate(_ data: Data, to url: URL) throws {
        let temporary = url.deletingLastPathComponent().appendingPathComponent(".\(url.lastPathComponent).\(UUID().uuidString)")
        let descriptor = Darwin.open(temporary.path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC, 0o600)
        guard descriptor >= 0 else { throw DesktopError(message: "无法写入 \(url.lastPathComponent)。") }
        defer { Darwin.close(descriptor); try? FileManager.default.removeItem(at: temporary) }
        try data.withUnsafeBytes { buffer in
            var offset = 0
            while offset < buffer.count {
                let count = Darwin.write(descriptor, buffer.baseAddress!.advanced(by: offset), buffer.count - offset)
                if count < 0 && errno == EINTR { continue }
                guard count > 0 else { throw DesktopError(message: "写入 \(url.lastPathComponent) 失败。") }
                offset += count
            }
        }
        guard fsync(descriptor) == 0, Darwin.rename(temporary.path, url.path) == 0 else {
            throw DesktopError(message: "保存 \(url.lastPathComponent) 失败。")
        }
    }

    private func checkPortAvailable() throws {
        let descriptor = socket(AF_INET, SOCK_STREAM, 0)
        guard descriptor >= 0 else { throw DesktopError(message: "无法检查本地服务端口。") }
        defer { Darwin.close(descriptor) }
        var reuse: Int32 = 1
        _ = setsockopt(descriptor, SOL_SOCKET, SO_REUSEADDR, &reuse, socklen_t(MemoryLayout.size(ofValue: reuse)))
        var address = sockaddr_in()
        address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        address.sin_family = sa_family_t(AF_INET)
        address.sin_port = UInt16(port).bigEndian
        address.sin_addr.s_addr = inet_addr("127.0.0.1")
        let result = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(descriptor, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        guard result == 0 else {
            throw DesktopError(message: "本地端口 \(port) 已被占用，工作台不会连接已有的未知服务。请关闭占用服务，或设置 DBX_DESKTOP_PORT 后重启。")
        }
    }

    private func startBackend() {
        guard !quitting, !stopping else { return }
        startupGeneration = UUID()
        let generation = startupGeneration
        failurePresented = false
        statusLabel.stringValue = "正在启动数据库工作台…"
        statusLabel.isHidden = false
        webView.isHidden = true
        do {
            try configureStorage()
            try checkPortAvailable()
            guard let resources = Bundle.main.resourceURL, let directory = dataDirectory else { throw DesktopError(message: "找不到应用资源目录。") }
            let executable = resources.appendingPathComponent("dbx-web")
            let assets = resources.appendingPathComponent("web-dist", isDirectory: true)
            guard FileManager.default.isExecutableFile(atPath: executable.path), FileManager.default.fileExists(atPath: assets.appendingPathComponent("index.html").path) else {
                throw DesktopError(message: "应用包缺少 dbx-web 或 web-dist/index.html，请重新打包。")
            }
            backendExecutable = executable
            let logURL = directory.appendingPathComponent("backend.log")
            let logDescriptor = Darwin.open(logURL.path, O_WRONLY | O_CREAT | O_APPEND | O_NOFOLLOW | O_CLOEXEC, 0o600)
            guard logDescriptor >= 0 else { throw DesktopError(message: "无法打开后端日志文件。") }
            _ = fchmod(logDescriptor, 0o600)
            let log = FileHandle(fileDescriptor: logDescriptor, closeOnDealloc: true)
            backendLog = log
            let process = Process()
            process.executableURL = executable
            process.currentDirectoryURL = directory
            var environment = ProcessInfo.processInfo.environment.filter { !$0.key.hasPrefix("DBX_") }
            environment["DBX_DATA_DIR"] = directory.path
            environment["DBX_STATIC_DIR"] = assets.path
            environment["DBX_BIND_ADDR"] = "127.0.0.1"
            environment["DBX_PORT"] = String(port)
            environment["DBX_PASSWORD"] = password
            environment["DBX_DISABLE_PASSWORD"] = "0"
            environment["DBX_DEMO_MODE"] = "0"
            environment["DBX_PUBLIC_BASE_PATH"] = ""
            environment["DBX_WEB_MCP_TOKEN_FILE"] = directory.appendingPathComponent("mcp-token").path
            environment["DBX_WEB_MCP_ALLOWED_HOSTS"] = "127.0.0.1:\(port)"
            environment["DBX_EXIT_ON_STDIN_CLOSE"] = "1"
            environment["DBX_MCP_PLUGIN_TOOLS"] = "lazy"
            process.environment = environment
            process.standardOutput = log
            process.standardError = log
            let lifetime = Pipe()
            _ = fcntl(lifetime.fileHandleForWriting.fileDescriptor, F_SETFD, FD_CLOEXEC)
            _ = fcntl(lifetime.fileHandleForReading.fileDescriptor, F_SETFD, FD_CLOEXEC)
            process.standardInput = lifetime.fileHandleForReading
            backendInput = lifetime.fileHandleForWriting
            process.terminationHandler = { [weak self] child in
                DispatchQueue.main.async {
                    guard let self, self.startupGeneration == generation, !self.quitting, !self.stopping else { return }
                    self.fail("数据库后端已退出（状态 \(child.terminationStatus)）。可以重试，或查看后端日志。")
                }
            }
            backend = process
            try process.run()
            try? lifetime.fileHandleForReading.close()
            writeRuntime(state: "starting")
            awaitReadiness(generation: generation, deadline: Date().addingTimeInterval(45))
        } catch {
            let message = error.localizedDescription
            stopBackend { [weak self] in self?.fail(message) }
        }
    }

    private func writeRuntime(state: String) {
        // A second instance that failed to acquire the lock must not overwrite
        // the running instance's diagnostic file while handling that failure.
        guard lockDescriptor >= 0, let directory = dataDirectory else { return }
        let info: [String: Any] = [
            "state": state, "port": port, "pid": ProcessInfo.processInfo.processIdentifier,
            "backendPid": backend?.isRunning == true ? backend!.processIdentifier : NSNull(),
            "dataDirectory": directory.path, "backendExecutable": backendExecutable?.path ?? "",
            "webURL": baseURL.absoluteString + "/", "mcpURL": baseURL.appendingPathComponent("mcp").absoluteString,
            "mcpClientConfig": directory.appendingPathComponent("mcp-client.json").path,
            "logFile": directory.appendingPathComponent("backend.log").path,
            "updatedAt": ISO8601DateFormatter().string(from: Date())
        ]
        try? writePrivateJSON(info, to: directory.appendingPathComponent("runtime.json"))
    }

    private func awaitReadiness(generation: UUID, deadline: Date) {
        guard generation == startupGeneration, !quitting else { return }
        guard backend?.isRunning == true else { fail("数据库后端未能启动，请查看日志后重试。"); return }
        guard Date() < deadline else { fail("等待数据库后端启动超时。请查看日志后重试。"); return }
        var request = URLRequest(url: baseURL.appendingPathComponent("api/auth/check"))
        request.timeoutInterval = 2
        startupTask = httpSession.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self, generation == self.startupGeneration, !self.quitting else { return }
                guard self.backend?.isRunning == true else { self.fail("数据库后端在启动期间退出。"); return }
                if (response as? HTTPURLResponse)?.statusCode == 200 {
                    self.authenticate(generation: generation)
                } else {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [weak self] in self?.awaitReadiness(generation: generation, deadline: deadline) }
                }
            }
        }
        startupTask?.resume()
    }

    private func authenticate(generation: UUID) {
        guard !quitting, !stopping, backend?.isRunning == true, generation == startupGeneration else { return }
        statusLabel.stringValue = "正在打开工作台…"
        var request = URLRequest(url: baseURL.appendingPathComponent("api/auth/login"))
        request.httpMethod = "POST"
        request.timeoutInterval = 15
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONSerialization.data(withJSONObject: ["password": password])
        startupTask = httpSession.dataTask(with: request) { [weak self] _, response, _ in
            DispatchQueue.main.async {
                guard let self, generation == self.startupGeneration, !self.quitting else { return }
                guard self.backend?.isRunning == true, let response = response as? HTTPURLResponse, response.statusCode == 200 else {
                    self.fail("无法建立本地工作台会话，请重试或查看日志。")
                    return
                }
                var headers: [String: String] = [:]
                for (key, value) in response.allHeaderFields { headers[String(describing: key)] = String(describing: value) }
                let cookies = HTTPCookie.cookies(withResponseHeaderFields: headers, for: self.baseURL).filter { $0.name == "dbx_session" }
                guard !cookies.isEmpty else { self.fail("后端没有返回有效的本地会话。"); return }
                let group = DispatchGroup()
                for cookie in cookies {
                    group.enter()
                    self.webView.configuration.websiteDataStore.httpCookieStore.setCookie(cookie) { group.leave() }
                }
                group.notify(queue: .main) { [weak self] in
                    guard let self, generation == self.startupGeneration, self.backend?.isRunning == true, !self.quitting else { return }
                    self.webView.load(URLRequest(url: self.baseURL.appendingPathComponent("")))
                    self.writeRuntime(state: "ready")
                }
            }
        }
        startupTask?.resume()
    }

    private func stopBackend(completion: @escaping () -> Void) {
        shutdownCompletions.append(completion)
        if stopping { return }
        stopping = true
        let generation = UUID()
        shutdownGeneration = generation
        startupGeneration = UUID()
        startupTask?.cancel()
        startupTask = nil
        let child = backend
        child?.terminationHandler = nil
        if child?.isRunning == true {
            statusLabel.stringValue = "正在停止数据库后端，等待进行中的操作结束…"
            statusLabel.isHidden = false
            webView.isHidden = true
            writeRuntime(state: "stopping")
        }
        // Closing the only parent writer requests the backend's ordinary
        // graceful shutdown. Its HTTP, backup, and connection cleanup budgets
        // total up to 13 seconds; keep the UI responsive throughout that window.
        try? backendInput?.close()
        backendInput = nil

        guard let child, child.processIdentifier > 0 else {
            // Defer even the no-process case so applicationShouldTerminate can
            // return terminateLater before its completion sends the reply.
            DispatchQueue.main.async { [weak self] in self?.finishStopping(generation: generation) }
            return
        }

        DispatchQueue.main.asyncAfter(deadline: .now() + 15) { [weak self] in
            guard let self, self.shutdownGeneration == generation, child.isRunning else { return }
            child.terminate()
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 18) { [weak self] in
            guard let self, self.shutdownGeneration == generation, child.isRunning else { return }
            Darwin.kill(child.processIdentifier, SIGKILL)
        }
        // Reap off the main thread. Never restart against the same data files
        // merely because a signal was sent: the old process must have exited.
        DispatchQueue.global(qos: .utility).async { [weak self] in
            child.waitUntilExit()
            DispatchQueue.main.async { [weak self] in self?.finishStopping(generation: generation) }
        }
    }

    private func finishStopping(generation: UUID) {
        guard shutdownGeneration == generation else { return }
        shutdownGeneration = nil
        backend = nil
        try? backendLog?.close()
        backendLog = nil
        writeRuntime(state: "stopped")
        stopping = false
        let completions = shutdownCompletions
        shutdownCompletions.removeAll()
        completions.forEach { $0() }
    }

    private func fail(_ message: String) {
        guard !quitting, !stopping, !failurePresented else { return }
        failurePresented = true
        startupTask?.cancel()
        statusLabel.stringValue = message
        statusLabel.isHidden = false
        webView.isHidden = true
        let alert = NSAlert()
        alert.messageText = "数据库工作台无法继续"
        alert.informativeText = message
        alert.alertStyle = .warning
        alert.addButton(withTitle: "重试")
        alert.addButton(withTitle: "查看日志")
        alert.addButton(withTitle: "退出")
        alert.beginSheetModal(for: window) { [weak self] response in
            guard let self else { return }
            self.failurePresented = false
            if response == .alertFirstButtonReturn {
                self.restartBackend()
            } else if response == .alertSecondButtonReturn {
                self.revealBackendLog()
            } else {
                NSApp.terminate(nil)
            }
        }
    }

    @objc private func reloadWorkspace() {
        guard !quitting, !stopping else { return }
        if backend?.isRunning == true { authenticate(generation: startupGeneration) } else { restartBackend() }
    }

    @objc private func restartBackend() {
        guard !quitting, !stopping else { return }
        stopBackend { [weak self] in
            guard let self, !self.quitting else { return }
            self.startBackend()
        }
    }

    @objc private func revealDataDirectory() {
        if let directory = dataDirectory { NSWorkspace.shared.open(directory) }
    }

    @objc private func revealBackendLog() {
        if let directory = dataDirectory { NSWorkspace.shared.activateFileViewerSelecting([directory.appendingPathComponent("backend.log")]) }
    }

    @objc private func revealMCPConfiguration() {
        if let directory = dataDirectory { NSWorkspace.shared.activateFileViewerSelecting([directory.appendingPathComponent("mcp-client.json")]) }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if quitting { return .terminateLater }
        quitting = true
        stopBackend { sender.reply(toApplicationShouldTerminate: true) }
        return .terminateLater
    }

    func applicationWillTerminate(_ notification: Notification) {
        if lockDescriptor >= 0 { flock(lockDescriptor, LOCK_UN); Darwin.close(lockDescriptor); lockDescriptor = -1 }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        window.makeKeyAndOrderFront(nil)
        return true
    }

    private func isLocal(_ url: URL) -> Bool {
        url.scheme == "http" && url.host == "127.0.0.1" && url.port == port && url.user == nil && url.password == nil
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url else { decisionHandler(.cancel); return }
        if isLocal(url) || (url.scheme == "blob" && url.absoluteString.hasPrefix("blob:\(baseURL.absoluteString)/")) {
            if navigationAction.shouldPerformDownload { decisionHandler(.download) } else { decisionHandler(.allow) }
            return
        }
        if navigationAction.navigationType == .linkActivated, ["http", "https"].contains(url.scheme ?? "") {
            NSWorkspace.shared.open(url)
        }
        decisionHandler(.cancel)
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationResponse: WKNavigationResponse, decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void) {
        guard let url = navigationResponse.response.url, isLocal(url) || url.scheme == "blob" else { decisionHandler(.cancel); return }
        let disposition = (navigationResponse.response as? HTTPURLResponse)?.value(forHTTPHeaderField: "Content-Disposition") ?? ""
        decisionHandler(!navigationResponse.canShowMIMEType || disposition.lowercased().hasPrefix("attachment") ? .download : .allow)
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        guard !quitting, !stopping else { return }
        statusLabel.isHidden = true
        webView.isHidden = false
        window.makeFirstResponder(webView)
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        let failure = error as NSError
        // WebKit interrupts the original navigation when it becomes a
        // download or our navigation policy cancels it. Neither is a failure
        // of the workbench; WKDownloadDelegate reports actual download errors.
        if failure.code == NSURLErrorCancelled || (failure.domain == "WebKitErrorDomain" && failure.code == 102) { return }
        fail("无法载入工作台页面：\(error.localizedDescription)")
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) { fail("工作台页面进程已退出，请重新加载。") }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = navigationAction.request.url, isLocal(url) { webView.load(navigationAction.request) }
        return nil
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping ([URL]?) -> Void) {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = parameters.allowsDirectories
        panel.allowsMultipleSelection = parameters.allowsMultipleSelection
        panel.beginSheetModal(for: window) { response in completionHandler(response == .OK ? panel.urls : nil) }
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let alert = NSAlert()
        alert.messageText = "数据库工作台"
        alert.informativeText = message
        alert.addButton(withTitle: "确定")
        alert.beginSheetModal(for: window) { _ in completionHandler() }
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let alert = NSAlert()
        alert.messageText = "数据库工作台"
        alert.informativeText = message
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")
        alert.beginSheetModal(for: window) { completionHandler($0 == .alertFirstButtonReturn) }
    }

    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) {
        let alert = NSAlert()
        alert.messageText = "数据库工作台"
        alert.informativeText = prompt
        let field = NSTextField(string: defaultText ?? "")
        field.frame = NSRect(x: 0, y: 0, width: 360, height: 24)
        alert.accessoryView = field
        alert.addButton(withTitle: "确定")
        alert.addButton(withTitle: "取消")
        alert.beginSheetModal(for: window) { completionHandler($0 == .alertFirstButtonReturn ? field.stringValue : nil) }
    }

    private func trackDownload(_ download: WKDownload) {
        downloads[ObjectIdentifier(download)] = DownloadRecord(download)
        download.delegate = self
    }

    func webView(_ webView: WKWebView, navigationAction: WKNavigationAction, didBecome download: WKDownload) { trackDownload(download) }
    func webView(_ webView: WKWebView, navigationResponse: WKNavigationResponse, didBecome download: WKDownload) { trackDownload(download) }

    func download(_ download: WKDownload, decideDestinationUsing response: URLResponse, suggestedFilename: String, completionHandler: @escaping (URL?) -> Void) {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = URL(fileURLWithPath: suggestedFilename).lastPathComponent
        panel.canCreateDirectories = true
        panel.beginSheetModal(for: window) { [weak self] result in
            guard let self, result == .OK, let destination = panel.url, let record = self.downloads[ObjectIdentifier(download)] else { completionHandler(nil); return }
            record.destination = destination
            record.temporary = destination.deletingLastPathComponent().appendingPathComponent(".dbx-download-\(UUID().uuidString)")
            completionHandler(record.temporary)
        }
    }

    func downloadDidFinish(_ download: WKDownload) {
        guard let record = downloads.removeValue(forKey: ObjectIdentifier(download)), let temporary = record.temporary, let destination = record.destination else { return }
        do {
            if FileManager.default.fileExists(atPath: destination.path) {
                _ = try FileManager.default.replaceItemAt(destination, withItemAt: temporary)
            } else {
                try FileManager.default.moveItem(at: temporary, to: destination)
            }
        } catch { showDownloadError(error) }
    }

    func download(_ download: WKDownload, didFailWithError error: Error, resumeData: Data?) {
        if let record = downloads.removeValue(forKey: ObjectIdentifier(download)), let temporary = record.temporary { try? FileManager.default.removeItem(at: temporary) }
        if (error as NSError).code != NSURLErrorCancelled { showDownloadError(error) }
    }

    private func showDownloadError(_ error: Error) {
        let alert = NSAlert()
        alert.messageText = "文件未能保存"
        alert.informativeText = error.localizedDescription
        alert.addButton(withTitle: "确定")
        alert.beginSheetModal(for: window)
    }
}

let application = NSApplication.shared
let delegate = DBXMCPApp()
application.setActivationPolicy(.regular)
application.delegate = delegate
application.run()
