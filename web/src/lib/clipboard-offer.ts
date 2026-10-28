import { useEffect } from "react";
import { toast } from "sonner";
import { api } from "./api";
import { platformName } from "./platforms";
import { useStore } from "./store";
import type { LinkPreview } from "./types";

// The clipboard is read by the backend (the WebView would prompt for permission) and only its
// live-room links come back. Each clipboard content is offered once, remembered across restarts.
// (Renamed from "sc-clip-sig": that version could mark content offered while the window was hidden.)
const KEY = "sc-clip-seen";
let lastSig: string | null = (() => {
  try {
    return localStorage.getItem(KEY);
  } catch {
    return null;
  }
})();
let busy = false;
let lastAt = 0;

// Inside the app window (WebView2) document.hasFocus() is always false, so there the server says
// when the window comes to the front ("app_active"); a browser tab uses its own focus.
const inAppWindow = Boolean((window as { chrome?: { webview?: unknown } }).chrome?.webview);
const looking = () => inAppWindow || (document.hasFocus() && document.visibilityState === "visible");

async function check() {
  if (busy || Date.now() - lastAt < 1500) return; // focus + visibilitychange fire together
  // Only while the user is looking: a notice shown to a hidden or background window would mark
  // the clipboard as offered without anyone seeing it.
  if (!looking()) return;
  if (document.querySelector("[role=dialog]")) return; // mid-edit: don't interrupt
  busy = true;
  lastAt = Date.now();
  try {
    const { sig, links } = await api.clipboardLinks();
    if (sig === lastSig) return;
    lastSig = sig;
    try {
      localStorage.setItem(KEY, sig);
    } catch {
      /* storage unavailable: offer again next start */
    }
    if (!links.length) return;
    const { previews } = await api.preview(links);
    const fresh = previews.filter((p) => p.platform && !p.existing);
    if (!fresh.length) return; // all already in the list
    if (!looking()) {
      // Switched away during the lookup: offer it again when the window is back.
      lastSig = null;
      try {
        localStorage.removeItem(KEY);
      } catch {
        /* storage unavailable */
      }
      return;
    }
    const first = fresh[0];
    const title =
      fresh.length === 1
        ? `检测到${platformName(first.platform)}「${first.anchor || "直播间"}」的直播间`
        : `检测到 ${fresh.length} 个直播间链接`;
    const description =
      fresh.length === 1
        ? first.error
          ? "暂时获取不到直播间信息，仍可添加"
          : first.is_live
            ? `正在直播${first.title ? `：${first.title}` : ""}`
            : "当前未开播"
        : fresh.map((p) => p.anchor || p.url).join("、");
    toast(title, {
      description,
      duration: 20000,
      // One click adds them with the default settings: the notice already says who it is.
      action: { label: "添加", onClick: () => void addNow(fresh) },
      cancel: { label: "调整设置", onClick: () => openDialog(fresh) },
    });
  } catch {
    /* clipboard busy or unsupported here: try on the next focus */
  } finally {
    busy = false;
  }
}

function openDialog(rooms: LinkPreview[]) {
  location.hash = "#/rooms";
  useStore.setState({ addPrefill: rooms.map((p) => p.url).join("\n") });
}

async function addNow(rooms: LinkPreview[]) {
  try {
    const res = await api.add(rooms.map((p) => ({ url: p.url })));
    useStore.getState().upsertMany(res.created);
    location.hash = "#/rooms";
    if (res.created.length) {
      // A new room's name is only learned on its first check; the preview already knows it.
      const skipped = new Set(res.skipped.map((s) => s.url));
      const added = rooms.filter((p) => !skipped.has(p.url)).slice(0, res.created.length);
      const names = added.map((p) => `「${p.anchor || "直播间"}」`).join("、");
      toast.success(`已添加${names}`, {
        description: res.created.some((r) => r.only_notify_no_record)
          ? "开播时会提醒你"
          : "开播后自动录制；画质等设置可在直播间卡片里修改",
      });
    }
    if (res.skipped.length) {
      toast.error(`有 ${res.skipped.length} 个没有添加`, { description: res.skipped.map((s) => s.reason).join("；") });
    }
  } catch (e) {
    toast.error("添加失败", { description: (e as Error).message, action: { label: "手动添加", onClick: () => openDialog(rooms) } });
  }
}

/** Offers to add live rooms whose share links are on the clipboard when the window gets focus. */
export function useClipboardOffer() {
  useEffect(() => {
    // App window: check each time it comes to the front.
    const unsub = useStore.subscribe((s, prev) => {
      if (inAppWindow && s.appActiveAt !== prev.appActiveAt) void check();
    });
    const onFocus = () => void check();
    const onVisible = () => document.visibilityState === "visible" && void check();
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisible);
    // The app window (WebView2) doesn't reliably fire focus when switched back to, so also watch
    // for the moment the page regains focus.
    let had = document.hasFocus();
    const poll = setInterval(() => {
      if (inAppWindow) return; // the server reports activation instead
      const has = document.hasFocus();
      if (has && !had) void check();
      had = has;
    }, 1500);
    void check();
    return () => {
      unsub();
      clearInterval(poll);
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);
}
