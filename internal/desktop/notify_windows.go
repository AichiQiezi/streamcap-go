//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Windows PowerShell's AppUserModelID. A toast needs a registered AUMID and StreamCap has no
// installer to register its own, so the toast is attributed to PowerShell.
const psAUMID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// The text travels in environment variables and is XML-escaped inside PowerShell: nothing from a
// live title is ever spliced into the script itself.
const toastScript = `
$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$e = { param($s) [Security.SecurityElement]::Escape($s) }
$launch = ''
if ($env:SC_URL) { $launch = ' activationType="protocol" launch="' + (& $e $env:SC_URL) + '"' }
$xml = '<toast' + $launch + '><visual><binding template="ToastGeneric"><text>' + (& $e $env:SC_TITLE) +
  '</text><text>' + (& $e $env:SC_BODY) + '</text></binding></visual></toast>'
$doc = New-Object Windows.Data.Xml.Dom.XmlDocument
$doc.LoadXml($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:SC_AUMID).Show(
  [Windows.UI.Notifications.ToastNotification]::new($doc))
`

// OpenNotificationSettings opens Settings → System → Notifications.
func OpenNotificationSettings() error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", "ms-settings:notifications").Start()
}

// Notify shows a Windows toast; clicking it opens url.
func Notify(title, body, url string) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "SC_TITLE="+title, "SC_BODY="+body, "SC_URL="+url, "SC_AUMID="+psAUMID)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("发送系统通知失败：%v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
