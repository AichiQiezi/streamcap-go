# 抖音弹幕录制方案

状态：已实现（macOS WKWebView + Windows WebView2），待真机验证（2026-10-19）

## 目标

录制抖音直播时同时保存弹幕，下播后和 B站一样在视频旁生成同名 `.ass`，播放器里可开关。只做个人录制，复用已有的弹幕管线（`internal/danmaku`：写 `.danmaku.jsonl` → 按录像时间轴生成 `.ass`）。

## 前期验证结论

| 验证项 | 结果 |
|---|---|
| 抖音网页的弹幕走 WebSocket（`webcast*-ws-web-*.douyin.com/webcast/im/push/v2/`），protobuf 编码、gzip 压缩 | 已确认 |
| 连接必须带 `signature` 参数，不带时服务器返回 HTTP 200、拒绝升级 | 已确认 |
| 签名与「直播间 ID + 用户编号」绑定，与 User-Agent 无关，每场直播需要一个新签名 | 已确认 |
| 不登录时，网页的连接能收到弹幕（用页面生成的签名直连，20 秒收到 15 条） | 已确认 |
| 不登录时，网页界面不显示弹幕（只显示「XXX 加入了直播间」），所以读页面上的文字不可行 | 已确认 |
| 页面在后台、只要有正常尺寸就会继续接收消息；尺寸为 0 时不渲染 | 已确认 |

## 方案比较

| 方案 | 做法 | 结论 |
|---|---|---|
| A. 模拟签名 | 把抖音的安全脚本 `webmssdk.es5.js` 放进 JS 沙箱，调用 `frontierSign` 生成签名再直连（biliup、F2、DouyinLiveWebFetcher 等的做法） | **不做**。属于在页面之外伪造平台的防爬签名，是在绕过抖音的反爬保护；而且抖音一改脚本就失效 |
| B. 代理截获 | 本地代理 + 根证书，截获浏览器 / 直播伴侣的连接（DouyinBarrageGrab，仅 Windows） | 不做。要对整台电脑做中间人拦截，安全代价大 |
| C. 官方接口 | 抖音开放平台「直播互动玩法」 | 不适用。需要主播在直播间挂上玩法 |
| D. 读页面上的文字 | 后台打开直播页，读取渲染出来的弹幕 | 不可行。访客看不到弹幕，要登录账号，有封号风险 |
| **E. 转交网页自己收到的数据** | 后台用内置浏览器正常打开直播页，页面自己完成签名和连接；注入脚本只把它收到的消息转给 StreamCap | **选定**。不伪造签名、不用登录、不装证书，效果相当于一个浏览器插件 |

## 选定方案 E 的设计

```
StreamCap (Go)                     原生层 (macOS / Windows)             抖音直播页
──────────────                     ─────────────────────────             ──────────
monitor：开始录制抖音直播间 ──开──▶ 屏幕外的网页（每场一个：WKWebView / WebView2）──加载──▶ live.douyin.com/<抖音号>
                                   │ 页面加载前注入脚本：                   │ 页面自己生成签名、
                                   │   包装 window.WebSocket，             │ 自己连接弹幕服务器
                                   │   只关注 /webcast/im/push/v2/ 的连接   │
◀──原始二进制帧（base64）── 原生消息通道 (macOS script message / Windows Bind) ◀── 收到的每一帧
解码 protobuf → 弹幕 → 现有的 danmaku.Writer → 下播后生成 .ass（与 B站相同）
monitor：这一场结束 ──关──▶ 关闭 WebView
```

### 1. 后台网页（`internal/desktop`，macOS / Windows）

- 新增 `desktop.OpenWatcher(url string, onFrame func([]byte)) (close func())`：
  - macOS（`darwin.m` / `desktop_darwin.go`）见下；
  - Windows（`watcher_windows.go`）用 WebView2：`NewWithOptions` 建一个 1280×800 的普通窗口，随后移到屏幕外
    （`SetWindowPos` 到 −32000,−32000）并加 `WS_EX_TOOLWINDOW` 从任务栏 / Alt+Tab 里去掉；每个网页一条独立的
    线程和消息循环（`runtime.LockOSThread` + `Run()`），WebView2 用户数据目录指向临时目录，用完删掉；
    `Init` 注入脚本，`Bind("__scDanmakuFrame")` 收 base64 帧；2 分钟没有帧就重新加载页面。
    每个 session 一个 WebView2 进程，比 macOS 重一些。
  - `desktop.WatcherSupported()` / `platform.OpenWatcher` 在 `main` 里按运行模式注入。

- macOS 实现：
  - 屏幕外的 `WKWebView`（窗口放在可视区域外、不最小化），尺寸 1280×800，因为尺寸为 0 时页面不工作。
  - 页面开始加载前（`WKUserScript`, `AtDocumentStart`）注入脚本：包装 `window.WebSocket`；URL 含 `/webcast/im/push/v2/` 时监听它的 `message` 事件，把 `ArrayBuffer` 转 base64，通过 `webkit.messageHandlers.streamcapDanmaku.postMessage` 交给原生层。**只读，不改动、不额外发送任何数据**，心跳和应答仍由页面自己处理。
  - 用 `WKContentRuleList` 屏蔽视频流（`.flv` / `.m3u8` / `.ts` / `.m4s`）和大图，并把视频静音，节省带宽和 CPU。
  - 独立的非持久化会话（`WKWebsiteDataStore.nonPersistent`），不带用户的任何账号信息。
- macOS 还会屏蔽视频流省带宽；Windows 的 go-webview2 封装没有暴露资源拦截回调，只能静音，视频仍会下载
  （带宽和 CPU 高于 macOS，后续可以换成 `pkg/edge` 直接用 `AddWebResourceRequestedFilter`）。
- 可用性：窗口模式和浏览器模式（菜单栏 / 托盘 App 也有运行循环）下为 true；`-headless` 下 `main` 不注入，
  抖音弹幕会被跳过（B站不受影响）。

### 2. 抖音弹幕源（`internal/platform/douyin/danmaku.go`）

- 实现 `platform.DanmakuSource`：用现有解析拿到抖音号（`web_rid`），拼出 `https://live.douyin.com/<web_rid>`，再调用注入进来的 `OpenWatcher`。`platform` 包不直接依赖 `desktop`，由 `main` 注入（和系统通知的做法一样）。
- protobuf 解码（只解需要的字段，不引入 protobuf 依赖）：
  - `PushFrame`：`payloadEncoding`(6)、`payload`(8)；`payloadEncoding == "gzip"` 时先解压
  - `Response`：`messages`(1)
  - `Message`：`method`(1)、`payload`(2)；只处理 `WebcastChatMessage`
  - `ChatMessage`：`user`(2) → `User.nickName`(3)；`content`(3)
- 弹幕到达时间用本机时间，和 B站一致，交给现有的 `.ass` 时间对齐逻辑（以每段录像最后收到数据的时刻倒推起点）。
- 页面意外关闭、长时间没有消息时（比如 2 分钟），重新加载页面。

### 3. 设置与界面

- 「录制弹幕」说明里写明抖音需要在窗口或托盘 / 菜单栏模式下运行（`-headless` 不可用）。
- 抖音直播间开始录制并开启弹幕时，后台打开网页；这一场结束（含续播等待结束）时关闭。
- 只提醒不录制的直播间不开网页。

### 4. 平台范围

- macOS（WKWebView）与 Windows（WebView2）都已实现；Windows 侧只做过交叉编译，还没在真机上跑过。
- `-headless` / Linux 不支持（没有内置浏览器），这两处的抖音弹幕会被跳过，B站弹幕照常。

## 原型验证（待做，macOS / Windows 各跑一遍）

用一个正在直播的大号直播间（如「交个朋友直播间」）验证：

| # | 验证项 | 通过标准 |
|---|---|---|
| 1 | 屏幕外 WKWebView 以访客身份打开直播页，注入的脚本能拿到弹幕连接的数据帧 | 能解出 `WebcastChatMessage`，数量与此前直连时同量级（约每 20 秒十几条） |
| 2 | 屏蔽视频后弹幕仍然正常 | 同上 |
| 3 | 连续运行 30 分钟 | 不断线；断线后页面能自己重连；记录单个页面的 CPU / 内存占用 |
| 4 | 与录制结合跑一场 | 生成的 `.ass` 时间与画面对得上 |
| 5 | Windows：窗口移到屏幕外、不在任务栏和 Alt+Tab 里，右键菜单和开发者工具已关 | 看不到窗口，弹幕照常 |

任意一项不通过就停下来重新评估，不进入完整开发。

## 风险

- **访客连接拿不到弹幕**：直连测试里能收到，但抖音可能对访客做限制。到时只剩「在后台网页里登录小号」一条路，账号有被风控的风险，需要重新决定。
- **抖音改版**：页面结构、连接地址、protobuf 字段变化都可能导致失效，需要跟着改。好处是签名始终由页面自己生成，不受签名算法更新的影响。
- **资源占用**：每个正在录、并开启弹幕的抖音直播间挂一个后台网页，同时录多个时要关注内存。
- **使用条款**：仍然属于自动化读取抖音页面的数据，是否符合抖音的使用条款需要自行判断；只用于个人录制，不做传播和盈利。

## 不做的事

- 不伪造、不模拟抖音的任何签名（方案 A）。
- 不安装证书、不拦截整台电脑的网络流量（方案 B）。
- 不使用、不保存用户的抖音账号。
