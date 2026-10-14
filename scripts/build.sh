#!/usr/bin/env bash
# Build the web UI, then:
#   dist/StreamCap-windows-amd64.zip   StreamCap.exe (GUI, no console window; icon/manifest from rsrc_windows_*.syso)
#   dist/StreamCap-macos.zip           StreamCap.app (universal arm64+amd64, ad-hoc signed)
# Must run on macOS: the macOS build needs cgo (WKWebView) and the system SDK.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local

VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 0.1.0)}"
VERSION="${VERSION#v}"
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "==> web UI"
(cd web && npm ci --silent && npm run build >/dev/null)

rm -rf dist && mkdir -p dist

echo "==> Windows amd64"
mkdir -p dist/windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS} -H windowsgui" \
  -o dist/windows/StreamCap.exe ./cmd/streamcap
cat > dist/windows/使用说明.txt <<'EOF'
StreamCap 直播录制（抖音 / B站）

1. 需要 ffmpeg：把 ffmpeg.exe 和 ffprobe.exe 放到本文件夹（或 ffmpeg\bin\ 子文件夹），
   或在命令行执行 winget install Gyan.FFmpeg。
2. 双击 StreamCap.exe 打开。关闭窗口后程序在右下角托盘继续录制；
   托盘图标右键「退出」才会真正退出（会先保存正在录制的文件）。
3. 录像默认保存在本文件夹的 downloads 里，设置、日志在 data、logs 里。
EOF
(cd dist/windows && zip -qr ../StreamCap-windows-amd64.zip .)

echo "==> macOS universal"
for arch in arm64 amd64; do
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "${LDFLAGS}" \
    -o "dist/streamcap-darwin-${arch}" ./cmd/streamcap 2>&1 | grep -v "duplicate libraries" || true
done
APP=dist/macos/StreamCap.app
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
lipo -create -output "$APP/Contents/MacOS/StreamCap" dist/streamcap-darwin-arm64 dist/streamcap-darwin-amd64
rm dist/streamcap-darwin-*
iconutil -c icns build/macos/AppIcon.iconset -o "$APP/Contents/Resources/AppIcon.icns"
cat > "$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>StreamCap</string>
  <key>CFBundleIdentifier</key><string>io.github.aichiqiezi.streamcap</string>
  <key>CFBundleName</key><string>StreamCap</string>
  <key>CFBundleDisplayName</key><string>StreamCap</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>LSApplicationCategoryType</key><string>public.app-category.video</string>
  <!-- The UI is served from 127.0.0.1 over plain HTTP. -->
  <key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
EOF
# Go's cgo link leaves an invalid ad-hoc signature with newer Xcode linkers; re-sign the bundle.
codesign --force --deep --sign - "$APP"
codesign --verify --deep "$APP"
(cd dist/macos && ditto -c -k --keepParent StreamCap.app ../StreamCap-macos.zip)

ls -la dist
