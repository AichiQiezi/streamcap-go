import { useEffect } from "react";
import { toast } from "sonner";
import { api } from "./api";
import { platformName } from "./platforms";
import { useStore } from "./store";

// The clipboard is read by the backend (the WebView would prompt for permission) and only its
// live-room links come back. Each clipboard content is offered once, remembered across restarts.
const KEY = "sc-clip-sig";
let lastSig: string | null = (() => {
  try {
    return localStorage.getItem(KEY);
  } catch {
    return null;
  }
})();
let busy = false;
let lastAt = 0;

async function check() {
  if (busy || Date.now() - lastAt < 1500) return; // focus + visibilitychange fire together
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
      action: {
        label: "添加",
        onClick: () => {
          location.hash = "#/rooms";
          useStore.setState({ addPrefill: fresh.map((p) => p.url).join("\n") });
        },
      },
    });
  } catch {
    /* clipboard busy or unsupported here: try on the next focus */
  } finally {
    busy = false;
  }
}

/** Offers to add live rooms whose share links are on the clipboard when the window gets focus. */
export function useClipboardOffer() {
  useEffect(() => {
    const onFocus = () => void check();
    const onVisible = () => document.visibilityState === "visible" && void check();
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisible);
    void check();
    return () => {
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);
}
