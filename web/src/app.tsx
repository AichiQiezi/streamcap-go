import { Check, Film, Loader2, Monitor, Moon, Power, Radio, Settings2, Sun, Upload } from "lucide-react";
import { useEffect, useState, useSyncExternalStore } from "react";
import { Toaster, toast } from "sonner";
import { Confirm, cx, Menu, MenuContent, MenuItem, MenuTrigger, Tip, TipProvider } from "./components/ui";
import { api } from "./lib/api";
import { useClipboardOffer } from "./lib/clipboard-offer";
import { bytes } from "./lib/format";
import { connectLive, useStore } from "./lib/store";
import { type ThemeMode, useTheme } from "./lib/theme";
import { LibraryPage } from "./pages/library";
import { RoomsPage } from "./pages/rooms";
import { SettingsPage } from "./pages/settings";
import { UploadsPage } from "./pages/uploads";

const ROUTES = {
  rooms: { label: "直播间", icon: Radio, Page: RoomsPage },
  library: { label: "录像", icon: Film, Page: LibraryPage },
  uploads: { label: "投稿", icon: Upload, Page: UploadsPage },
  settings: { label: "设置", icon: Settings2, Page: SettingsPage },
} as const;
type Route = keyof typeof ROUTES;

const subscribeHash = (cb: () => void) => {
  window.addEventListener("hashchange", cb);
  return () => window.removeEventListener("hashchange", cb);
};
const readRoute = (): Route => {
  const r = location.hash.replace(/^#\/?/, "") as Route;
  return r in ROUTES ? r : "rooms";
};

function Logo() {
  return (
    <svg viewBox="0 0 32 32" className="size-7" aria-hidden>
      <rect width="32" height="32" rx="8" className="fill-fg" />
      <circle cx="16" cy="16" r="8.5" fill="none" className="stroke-bg" strokeWidth="2" />
      <circle cx="16" cy="16" r="4" fill="var(--rec)" />
    </svg>
  );
}

const THEMES: { value: ThemeMode; label: string; icon: typeof Sun }[] = [
  { value: "system", label: "跟随系统", icon: Monitor },
  { value: "light", label: "浅色", icon: Sun },
  { value: "dark", label: "深色", icon: Moon },
];

function ThemeMenu() {
  const { mode, resolved, setMode } = useTheme();
  const Icon = mode === "system" ? Monitor : resolved === "dark" ? Moon : Sun;
  return (
    // A native title rather than <Tip>: focus returns to the trigger when the menu closes,
    // which would reopen a Radix tooltip and leave it stuck on screen.
    <Menu>
      <MenuTrigger asChild>
        <button
          aria-label="切换外观"
          title={`外观：${THEMES.find((t) => t.value === mode)!.label}`}
          className="rounded-md p-1.5 text-fg-3 transition hover:bg-surface-2 hover:text-fg data-[state=open]:bg-surface-2 data-[state=open]:text-fg"
        >
          <Icon className="size-3.5" />
        </button>
      </MenuTrigger>
      <MenuContent align="start" side="top">
        {THEMES.map((t) => (
          <MenuItem key={t.value} icon={<t.icon />} onSelect={() => setMode(t.value)}>
            <span className="flex-1">{t.label}</span>
            {mode === t.value && <Check className="size-3.5 text-fg" />}
          </MenuItem>
        ))}
      </MenuContent>
    </Menu>
  );
}

function Sidebar({ route }: { route: Route }) {
  const conn = useStore((s) => s.conn);
  const disk = useStore((s) => s.disk);
  const recordingCount = useStore((s) => Object.values(s.recs).filter((r) => r.state === "recording").length);
  const uploadCount = useStore((s) => Object.values(s.uploads).filter((t) => t.state === "queued" || t.state === "uploading" || t.state === "submitting").length);
  const [quitOpen, setQuitOpen] = useState(false);
  const used = disk ? 1 - disk.free / disk.total : 0;

  return (
    <aside className="flex w-[60px] shrink-0 flex-col border-r border-line bg-surface/60 px-2.5 pt-4 pb-3 lg:w-[216px] lg:px-3">
      <div className="flex items-center gap-2.5 px-1 pb-6 lg:px-2">
        <Logo />
        <span className="text-[15px] font-semibold tracking-[-0.02em] max-lg:hidden">StreamCap</span>
      </div>
      <nav className="space-y-0.5">
        {(Object.keys(ROUTES) as Route[]).map((key) => {
          const { label, icon: Icon } = ROUTES[key];
          return (
            <a
              key={key}
              href={`#/${key}`}
              title={label}
              className={cx(
                "relative flex h-9 items-center gap-2.5 rounded-lg px-2.5 text-[13.5px] transition max-lg:justify-center",
                route === key ? "bg-surface-2 font-medium text-fg" : "text-fg-2 hover:bg-surface-2/60 hover:text-fg",
              )}
            >
              <Icon className="size-4 shrink-0" strokeWidth={route === key ? 2.2 : 1.8} />
              <span className="max-lg:hidden">{label}</span>
              {key === "rooms" && recordingCount > 0 && (
                <span className="ml-auto inline-flex items-center gap-1 rounded-full bg-rec/12 px-1.5 py-px font-mono text-[11px] font-medium text-rec tabular max-lg:absolute max-lg:-top-1 max-lg:-right-1 max-lg:px-1">
                  <span className="size-1.5 rounded-full bg-rec animate-rec" />
                  {recordingCount}
                </span>
              )}
              {key === "uploads" && uploadCount > 0 && (
                <span className="ml-auto inline-flex items-center gap-1 rounded-full bg-surface-3 px-1.5 py-px font-mono text-[11px] font-medium text-fg-2 tabular max-lg:absolute max-lg:-top-1 max-lg:-right-1 max-lg:px-1">
                  <Loader2 className="size-2.5 animate-spin" />
                  {uploadCount}
                </span>
              )}
            </a>
          );
        })}
      </nav>

      <div className="mt-auto space-y-3">
        {disk && (
          <Tip label={disk.path} side="right">
            <div className="rounded-lg border border-line bg-surface px-3 py-2.5 max-lg:hidden">
              <div className="flex justify-between text-[11.5px]">
                <span className="text-fg-3">磁盘</span>
                <span className="font-mono text-fg-2 tabular">{bytes(disk.free)} 可用</span>
              </div>
              <div className="mt-2 h-1 overflow-hidden rounded-full bg-surface-3">
                <div
                  className="h-full rounded-full"
                  style={{ width: `${Math.round(used * 100)}%`, background: used > 0.9 ? "var(--warn)" : "var(--fg-3)" }}
                />
              </div>
            </div>
          </Tip>
        )}
        <div className="flex items-center justify-between px-1 max-lg:flex-col max-lg:gap-3">
          <Tip label={conn === "open" ? "已连接录制服务" : conn === "connecting" ? "正在连接…" : "已断开，正在重连"} side="right">
            <span className="inline-flex items-center gap-1.5 text-[11.5px] text-fg-3">
              <span className={cx("size-1.5 rounded-full", conn === "open" ? "bg-ok" : conn === "connecting" ? "bg-warn animate-rec" : "bg-err")} />
              <span className="max-lg:hidden">{conn === "open" ? "在线" : conn === "connecting" ? "连接中" : "离线"}</span>
            </span>
          </Tip>
          <div className="flex items-center gap-0.5 max-lg:flex-col">
            <ThemeMenu />
            <Tip label="退出 StreamCap" side="right">
              <button
                onClick={() => setQuitOpen(true)}
                aria-label="退出 StreamCap"
                className="rounded-md p-1.5 text-fg-3 transition hover:bg-err/10 hover:text-err"
              >
                <Power className="size-3.5" />
              </button>
            </Tip>
          </div>
        </div>
        <Confirm
          open={quitOpen}
          onOpenChange={setQuitOpen}
          title="退出 StreamCap？"
          body={
            recordingCount > 0 ? (
              <>
                正在录制的 <span className="font-mono text-fg">{recordingCount}</span> 个直播间会停止录制，已录内容会先保存好、合并成完整文件再退出。
                <span className="mt-2 block text-fg-3">只想关掉窗口、继续后台录制的话，直接关闭窗口即可。</span>
              </>
            ) : (
              "退出后不再监控开播。只想关掉窗口、继续后台监控的话，直接关闭窗口即可。"
            )
          }
          confirmText="退出"
          danger
          onConfirm={async () => {
            try {
              await api.quit();
            } catch (e) {
              toast.error((e as Error).message);
            }
          }}
        />
      </div>
    </aside>
  );
}

/** Shown while the server stops recordings and saves files, then once it has exited. */
function QuittingOverlay() {
  const quitting = useStore((s) => s.quitting);
  const conn = useStore((s) => s.conn);
  if (!quitting) return null;
  const gone = conn !== "open";
  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-bg/90 backdrop-blur-sm animate-fade">
      <div className="flex flex-col items-center gap-4 text-center">
        {gone ? (
          <Power className="size-8 text-fg-3" />
        ) : (
          <Loader2 className="size-8 animate-spin text-fg-3" />
        )}
        <div>
          <div className="text-[16px] font-semibold">{gone ? "StreamCap 已安全退出" : "正在停止录制并保存文件…"}</div>
          <div className="mt-1.5 text-[13px] text-fg-3">
            {gone ? "录像都已保存，可以关闭此页面了" : "正在合并分段、转换格式，完成后会自动退出，请稍候"}
          </div>
        </div>
      </div>
    </div>
  );
}

export function App() {
  const { resolved } = useTheme();
  const route = useSyncExternalStore(subscribeHash, readRoute);
  useEffect(() => connectLive(), []);
  useClipboardOffer();
  const { Page, label } = ROUTES[route];

  useEffect(() => {
    document.title = `${label} · StreamCap`;
  }, [label]);

  return (
    <TipProvider>
      <div className="flex h-full">
        <Sidebar route={route} />
        <main className="min-w-0 flex-1 overflow-y-auto">
          <Page />
        </main>
      </div>
      <QuittingOverlay />
      <Toaster
        position="bottom-right"
        theme={resolved}
        toastOptions={{
          className: "!bg-surface !text-fg !border-line !shadow-xl !rounded-xl !text-[13px] !font-sans",
        }}
      />
    </TipProvider>
  );
}
