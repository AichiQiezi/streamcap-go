import { useEffect, useRef, useState } from "react";
import type { Rec } from "./types";

const UNITS = ["B", "KB", "MB", "GB", "TB"];

/** "1536" -> "1.50 KB". Always returns "<number> <unit>" so callers can split on space. */
export function bytes(n: number | null | undefined, digits = 2): string {
  let v = Number(n);
  if (!Number.isFinite(v) || v < 0) v = 0;
  let u = 0;
  while (v >= 1024 && u < UNITS.length - 1) {
    v /= 1024;
    u += 1;
  }
  // Trim trailing zeros of the fraction only: with digits = 0 there is none, and "460" must stay 460.
  const fixed = u === 0 ? String(Math.round(v)) : v.toFixed(digits).replace(/\.(\d*?)0+$/, (_, d) => (d ? `.${d}` : ""));
  return `${fixed} ${UNITS[u]}`;
}

/** Bytes per second -> "1.5 MB/s". */
export function rate(bps: number | null | undefined): string {
  return `${bytes(bps)}/s`;
}

/** Seconds -> "H:MM:SS" (or "MM:SS" under an hour). */
export function clock(totalSeconds: number | null | undefined): string {
  const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const mm = h > 0 ? String(m).padStart(2, "0") : String(m);
  return `${h > 0 ? `${h}:` : ""}${mm}:${String(sec).padStart(2, "0")}`;
}

/** "1920×1080" from the live probe stats, or null when unknown. */
export function resolution(r: Rec): string | null {
  const { width, height } = r.stats ?? {};
  if (width && height) return `${width}×${height}`;
  return null;
}

/** Unix-seconds mtime -> relative Chinese label ("3分钟前", "昨天", "2024-05-01"). */
export function ago(mtime: number): string {
  const t = new Date(mtime * 1000);
  const diff = Date.now() - t.getTime();
  if (diff < 0) return "刚刚";
  const min = Math.floor(diff / 60000);
  if (min < 1) return "刚刚";
  if (min < 60) return `${min}分钟前`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}小时前`;
  const d = Math.floor(h / 24);
  if (d === 1) return "昨天";
  if (d < 30) return `${d}天前`;
  return `${t.getFullYear()}-${String(t.getMonth() + 1).padStart(2, "0")}-${String(t.getDate()).padStart(2, "0")}`;
}

interface DurationAnchor {
  key: string;
  base: number;
  at: number;
}

/**
 * Live-ticking duration for a recording card/row. Anchors on the server-reported
 * `duration_s` and advances locally while recording, so the clock moves every
 * second without waiting for the next WebSocket push.
 */
export function useDuration(r: Rec): number {
  const key = `${r.id}@${r.started_at ?? ""}@${r.state}`;
  const anchor = useRef<DurationAnchor>({ key, base: r.duration_s, at: Date.now() });
  if (anchor.current.key !== key) anchor.current = { key, base: r.duration_s, at: Date.now() };
  const [, setTick] = useState(0);

  useEffect(() => {
    if (r.state !== "recording") return;
    const t = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [r.state]);

  if (r.state !== "recording") return r.duration_s;
  return anchor.current.base + (Date.now() - anchor.current.at) / 1000;
}
