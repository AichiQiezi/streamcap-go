// Command genres draws the StreamCap icon and generates every platform asset from it:
//
//	internal/desktop/assets/app.ico, app.png, tray_template.png   (embedded at runtime)
//	cmd/streamcap/rsrc_windows_{amd64,arm64}.syso                 (exe icon, manifest, version)
//	build/macos/AppIcon.iconset/*.png                             (fed to iconutil for the .app)
//
// Run from the repository root:  (cd tools/genres && go run . -root ../..)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
	xdraw "golang.org/x/image/draw"
)

var (
	bg   = color.NRGBA{0x11, 0x12, 0x14, 0xff}
	ring = color.NRGBA{0xf4, 0xf4, 0xf5, 0xff}
	rec  = color.NRGBA{0xff, 0x3b, 0x30, 0xff}
)

func main() {
	root := flag.String("root", "../..", "repository root")
	ver := flag.String("version", "0.1.0", "product version")
	flag.Parse()

	app := drawApp(1024)
	assets := filepath.Join(*root, "internal/desktop/assets")
	must(os.MkdirAll(assets, 0o755))
	writePNG(filepath.Join(assets, "app.png"), resize(app, 256))
	writePNG(filepath.Join(assets, "tray_template.png"), drawTemplate(44))

	icon, err := winres.NewIconFromResizedImage(app, []int{16, 20, 24, 32, 40, 48, 64, 128, 256})
	must(err)
	var ico bytes.Buffer
	must(icon.SaveICO(&ico))
	must(os.WriteFile(filepath.Join(assets, "app.ico"), ico.Bytes(), 0o644))

	// Windows resources: icon #1 (the window class loads it by ID), manifest, version info.
	rs := &winres.ResourceSet{}
	must(rs.SetIcon(winres.ID(1), icon))
	rs.SetManifest(winres.AppManifest{
		Identity:            winres.AssemblyIdentity{Name: "StreamCap"},
		Description:         "StreamCap live recorder",
		DPIAwareness:        winres.DPIPerMonitorV2,
		UseCommonControlsV6: true,
		ExecutionLevel:      winres.AsInvoker,
	})
	var maj, min, pat uint16
	fmt.Sscanf(*ver, "%d.%d.%d", &maj, &min, &pat)
	vi := version.Info{FileVersion: [4]uint16{maj, min, pat, 0}, ProductVersion: [4]uint16{maj, min, pat, 0}}
	vi.Set(0, version.ProductName, "StreamCap")
	vi.Set(0, version.FileDescription, "StreamCap 直播录制")
	vi.Set(0, version.ProductVersion, *ver)
	vi.Set(0, version.CompanyName, "StreamCap")
	vi.Set(0, version.OriginalFilename, "streamcap.exe")
	rs.SetVersionInfo(vi)
	for arch, a := range map[string]winres.Arch{"amd64": winres.ArchAMD64, "arm64": winres.ArchARM64} {
		f, err := os.Create(filepath.Join(*root, "cmd/streamcap", "rsrc_windows_"+arch+".syso"))
		must(err)
		must(rs.WriteObject(f, a))
		f.Close()
	}

	// macOS iconset (the build script turns it into AppIcon.icns with iconutil).
	set := filepath.Join(*root, "build/macos/AppIcon.iconset")
	must(os.MkdirAll(set, 0o755))
	for _, s := range []int{16, 32, 128, 256, 512} {
		writePNG(filepath.Join(set, fmt.Sprintf("icon_%dx%d.png", s, s)), resize(drawApp(s*4), s))
		writePNG(filepath.Join(set, fmt.Sprintf("icon_%dx%d@2x.png", s, s)), resize(drawApp(s*8), s*2))
	}
	log.Println("assets generated")
}

// drawApp renders the app icon (dark rounded square, light ring, red record dot) at n×n,
// supersampled 4× for smooth edges.
func drawApp(n int) image.Image {
	ss := 4
	N := n * ss
	img := image.NewNRGBA(image.Rect(0, 0, N, N))
	c := float64(N) / 2
	radius := float64(N) * 0.225 // corner radius of the rounded square (macOS-like)
	margin := float64(N) * 0.06
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if !inRoundRect(fx, fy, margin, float64(N)-margin, radius) {
				continue
			}
			col := bg
			d := math.Hypot(fx-c, fy-c)
			if math.Abs(d-float64(N)*0.255) <= float64(N)*0.035 {
				col = ring
			}
			if d <= float64(N)*0.125 {
				col = rec
			}
			img.SetNRGBA(x, y, col)
		}
	}
	return resize(img, n)
}

// drawTemplate renders the menu-bar template icon: black ring + dot on transparent (macOS tints it).
func drawTemplate(n int) image.Image {
	ss := 4
	N := n * ss
	img := image.NewNRGBA(image.Rect(0, 0, N, N))
	c := float64(N) / 2
	for y := 0; y < N; y++ {
		for x := 0; x < N; x++ {
			d := math.Hypot(float64(x)+0.5-c, float64(y)+0.5-c)
			if math.Abs(d-float64(N)*0.36) <= float64(N)*0.06 || d <= float64(N)*0.17 {
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0xff})
			}
		}
	}
	return resize(img, n)
}

func inRoundRect(x, y, lo, hi, r float64) bool {
	if x < lo || y < lo || x > hi || y > hi {
		return false
	}
	cx := math.Max(lo+r, math.Min(x, hi-r))
	cy := math.Max(lo+r, math.Min(y, hi-r))
	return math.Hypot(x-cx, y-cy) <= r
}

func resize(src image.Image, n int) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst
}

func writePNG(path string, img image.Image) {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	must(png.Encode(f, img))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
