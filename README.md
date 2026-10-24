# StreamCap (Go)

抖音、B 站直播录制工具，带网页界面。后端用 Go 重写，自带界面，编译出来就是一个可执行文件。录制核心依赖 ffmpeg。

平台解析移植自 [streamget](https://github.com/ihmily/streamget)（MIT 许可），界面沿用 StreamCap 的 web UI。

## 运行

需要 ffmpeg 和 ffprobe：

- **Windows**：把 `ffmpeg.exe`、`ffprobe.exe` 放在 `StreamCap.exe` 同目录下（也可以放在同目录的 `ffmpeg\` 或 `ffmpeg\bin\` 子目录），或者执行 `winget install Gyan.FFmpeg` 安装。
- **macOS**：执行 `brew install ffmpeg`。

双击 `StreamCap.exe`（Windows）或 `StreamCap.app`（macOS）后，会打开一个独立的软件窗口，不经过浏览器：

| 操作 | 效果 |
|---|---|
| 关闭窗口 | 只隐藏窗口，**后台继续录制**。Windows 托盘 / macOS 菜单栏会留一个图标 |
| 点托盘或菜单栏图标，或再次双击程序 | 重新打开窗口（同一时间只会运行一个实例） |
| 托盘「退出」、界面左下角的退出按钮、macOS 上按 Cmd+Q | 停止所有录制，保存并合并文件后退出 |

界面也可以用浏览器访问：`http://127.0.0.1:6008`，托盘菜单里有「在浏览器中打开」。

### 数据保存位置

| | 设置、直播间列表 | 录像 | 日志 |
|---|---|---|---|
| Windows | 程序目录下的 `data\`（程序目录不可写时改到 `%LOCALAPPDATA%\StreamCap`） | `downloads\` | `logs\` |
| macOS（.app） | `~/Library/Application Support/StreamCap` | `~/Movies/StreamCap` | `~/Library/Logs/StreamCap` |

录像位置可以在设置里修改。排查断流问题时看 `streamcap.log`。

### 启动参数

```bash
StreamCap -browser    # 不开窗口，改用浏览器打开（开发界面时用）
StreamCap -devtools   # 在窗口里右键 →「检查」，打开开发者工具
StreamCap -headless   # 不开任何界面（服务器上用），Ctrl+C 退出
StreamCap -port 7000  # 换端口
```

> 界面没有登录保护。默认只监听 127.0.0.1，不要用 `-host 0.0.0.0` 暴露到不可信的网络。

### 退出与异常保护

- **任何方式退出都会先收尾**：Ctrl+C、关掉终端窗口、`kill`、托盘「退出」都会先让 ffmpeg 正常写完文件，再合并、转换后退出。
- **不会留下孤儿 ffmpeg**：
  - Windows 用作业对象（Job Object）把 ffmpeg 绑定在主程序上，主程序不管怎么结束，ffmpeg 都会一起结束。
  - macOS 和 Linux 会记录 ffmpeg 的进程号和启动时间。主程序被强制杀掉后，下次启动时会找到残留的 ffmpeg，让它正常收尾。
- **断电、崩溃后自动补整理**：每场直播都会记录到 `sessions.json`。上次没来得及合并、转换的录像，下次启动时会自动补做。

## 稳定性设计

针对的问题：直播并没有断，录制却断了，界面还显示"未开播"。

- **检测失败不等于未开播**：网络错误、平台风控等情况一律当作"状态未知"，不会把正在直播的房间标成"未开播"。
- **断流立即重连**：CDN 断开、ffmpeg 出错、45 秒没有写入任何数据，都会立刻重新获取一个新的拉流地址继续录。如果连续几次都很快失败，等待间隔会逐步加长。重连期间界面显示"重连中"。
- **确认下播再结束**：要连续 3 次检测到未开播，才认定这场直播结束。平台状态常常比 CDN 慢，只检测一次容易误判。
- **地址过期前无缝切换**：拉流地址带过期时间（比如 B 站约 1 小时）时，会在过期前 60 秒先用新地址开始录，再停掉旧的，中间没有空档。
- **一场直播一个文件**：同一场直播因重连产生的多段，下播后无损拼接成一个文件，再转成 MP4（只换封装，不重新编码）。
- **优雅停止**：停止录制时先给 ffmpeg 发 `q` 让它正常收尾，录出来的文件不会损坏。

## 弹幕

录制 B站 / 抖音直播时会同时保存弹幕（设置 → 录制 →「录制弹幕」，默认开启）。下播后在视频旁边生成同名的 `.ass` 文件，用 IINA、mpv、PotPlayer、VLC 打开视频会自动加载，可以随时开关；原视频不变。弹幕原始记录另存为同名的 `.danmaku.jsonl`。

- 时间对齐：每段录像以「最后收到数据的时刻」为终点、用实测时长倒推起点，再按合并顺序拼接；断流重连之间的空档里的弹幕会被丢掉，因为合并后的视频没有那段画面。精度约 ±2 秒。
- 屏幕放不下的弹幕会被丢弃（和 B站播放器一样），顶部/底部弹幕居中显示 4 秒。
- 应用内的播放器不显示弹幕。
- 抖音：弹幕的连接签名只有网页自己会生成，所以录制时会在窗口后台挂一个不可见的网页（macOS 用 WKWebView，Windows 用 WebView2），只把它收到的弹幕转发过来；不登录、不伪造签名。只在窗口和托盘 / 菜单栏模式下可用（`-headless` 不支持），方案见 [docs/douyin-danmaku.md](docs/douyin-danmaku.md)。
- 程序崩溃后自动补整理的录像不会生成 `.ass`（`.danmaku.jsonl` 还在）。

## B站投稿

可以把录像上传到自己的 B站账号：

1. 设置 →「B站投稿」→ 扫码登录：用哔哩哔哩 App 扫码确认，不需要输入密码。登录信息（token 和 Cookie）只保存在本机的 `uploads.json`，到期前会自动续期。
2. 手动投稿：在「录像」页把鼠标移到视频上，点上传按钮。标题、简介、标签、分区按设置里的模板预填，提交前可以改。
3. 自动投稿：在直播间设置里打开「下播后自动投稿到 B站」，录像合并、转换完成后会自动加入投稿队列。短于「自动投稿最短时长」（默认 10 分钟）的录像会跳过。自动投的稿件默认「仅自己可见」，可在设置里关掉。
4. 批量：「录像」页可以勾选多个文件（Shift 点击连选，表头复选框全选当前筛选出的文件），一起删除，或投成同一个稿件的多个分P。

上传进度在「投稿」页查看，一次传一个，失败的可以重试。退出时正在上传的任务会在下次启动后从头重传。分段录制产生的多个文件会投成同一个稿件的多个分P。封面从视频里自动截取一帧。

模板里可以用 `{anchor}` 主播、`{title}` 直播标题、`{date}` 日期、`{time}` 开播时间、`{platform}` 平台、`{url}` 直播间链接。录别人的直播请选「转载」，来源会自动填直播间链接。

登录和投稿走的是 B站 TV / App 端接口（与 [biliup](https://github.com/biliup/biliup-rs) 相同），不是官方开放接口，B站改接口后可能需要跟着更新。

## 已知限制

- **B 站画质**：不登录时，多数直播间最高只能录到 720p。在 设置 → 平台 Cookie 里填入带 `SESSDATA` 的登录 Cookie 可能解锁原画，这一点还没有用登录 Cookie 实际验证过。
- **抖音短链接**：`v.douyin.com/…` 的解析逻辑已经实现，但还没有用真实短链接测过。
- **抖音匿名 Cookie**：匿名访问依赖内置的 `ttwid`。如果抖音不再接受它，会表现为检测一直失败，这时在设置里填自己的 Cookie。

## 开发

开发界面时用浏览器调试（支持热更新），最终打包成独立窗口：

```bash
cd web && npm install && npm run build && cd ..   # 构建界面（会嵌入二进制）
go run ./cmd/streamcap -browser                    # 后端运行在 :6008，用浏览器打开
cd web && npm run dev                              # 界面热更新在 :5178，接口代理到 :6008
go run ./cmd/streamcap -devtools                   # 用独立窗口调试，右键可「检查」
go test ./...                                      # 加 STREAMCAP_LIVE_TEST=1 会跑连真实直播间的测试
scripts/build.sh                                   # 打包，产物在 dist/
```

`scripts/build.sh` 需要在 macOS 上运行（macOS 版依赖系统 SDK），产物有两个：

- `dist/StreamCap-windows-amd64.zip`：`StreamCap.exe`，不弹命令行窗口，带图标。
- `dist/StreamCap-macos.zip`：`StreamCap.app`，同时支持 Intel 和 Apple 芯片，已做 ad-hoc 签名。

图标改了以后需要重新生成：`(cd tools/genres && go run . -root ../..)`。会生成 exe 资源、`.ico`、菜单栏图标和 macOS 图标集。

代码结构：

```
cmd/streamcap         入口：启动参数、单实例、日志、HTTP 服务、统一的退出流程
internal/platform     平台解析接口；douyin/、bilibili/ 是各平台实现
internal/monitor      每个直播间一个 goroutine：检测、录制、重连、结束一场直播
internal/recorder     ffmpeg 进程管理、无数据看门狗、合并与转 MP4
internal/media        ffprobe 读分辨率、生成封面（只读本地文件，不碰直播流地址）
internal/upload       B站投稿：扫码登录、分片上传、提交稿件、投稿队列
internal/notify       开播/下播推送（钉钉、飞书、Bark、ntfy、Server酱、Telegram、邮件等）
internal/store        JSON 存储：原子写入，合并写盘
internal/api          HTTP 接口 + WebSocket 实时推送
internal/desktop      独立窗口（WebView2 / WKWebView）+ 托盘 / 菜单栏图标
tools/genres          生成图标和 Windows 资源（单独的 go module）
web/                  React 界面（Vite）
```
