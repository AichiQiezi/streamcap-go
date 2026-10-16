import { Bell, Check, Loader2 } from "lucide-react";
import { memo, useEffect, useState } from "react";
import { thumbUrl } from "@/lib/api";
import { bytes, clock, rate, resolution, useDuration } from "@/lib/format";
import { platformColor, platformName } from "@/lib/platforms";
import { useStore } from "@/lib/store";
import type { Rec } from "@/lib/types";
import { recActions, RecMenu } from "./rec-actions";
import { StatusPill, stateLabel } from "./status";
import { cx, Switch, Tip } from "./ui";

export interface RecItemProps {
  r: Rec;
  selected: boolean;
  onSelect: (id: string) => void;
  onEdit: (r: Rec) => void;
  onDelete: (r: Rec) => void;
}

export function qualityLabel(q: string) {
  return useStore.getState().options.qualities.find((o) => o.value === q)?.label ?? q;
}

/**
 * The newest thumbnail that has finished loading. A new frame arrives every ~30s; pointing <img> at
 * it directly blanks the cover while it downloads, so the previous frame stays up until then.
 */
function useLoadedImage(src: string | null): string | null {
  const [shown, setShown] = useState<string | null>(null);
  useEffect(() => {
    if (!src) return;
    let cancelled = false;
    const img = new Image();
    img.onload = () => !cancelled && setShown(src);
    img.src = src; // on error: keep whatever is shown (a stale frame beats a blank one)
    return () => {
      cancelled = true;
    };
  }, [src]);
  return src ? shown : null;
}

export function Cover({ r, compact }: { r: Rec; compact?: boolean }) {
  // Only live visuals get a real thumbnail: offline/paused cards render the
  // placeholder, so scrolling a long list doesn't fire thumbnail requests
  // for rooms that have nothing new to show.
  const live = r.state === "recording" || r.state === "reconnecting" || r.state === "live";
  const src = useLoadedImage(live ? thumbUrl(r) : null);
  const color = platformColor(r.platform_key);
  const dim = !live;
  if (!src) {
    // Idle rooms get a quiet placeholder so the few live ones stand out in a long grid.
    const initial = (r.name || "?").slice(0, 1);
    return (
      <div
        className="absolute inset-0 flex items-center justify-center"
        style={{
          background: `radial-gradient(120% 90% at 30% 20%, color-mix(in srgb, ${color} var(${dim ? "--tint-idle" : "--tint"}), transparent), transparent 65%), var(--surface-2)`,
        }}
      >
        {compact ? (
          <span className="text-[13px] font-semibold text-fg-2 select-none">{initial}</span>
        ) : (
          <span
            className={cx(
              "flex items-center justify-center rounded-full font-semibold tracking-tight select-none",
              dim ? "size-14 bg-surface/70 text-[22px] text-fg-3 ring-1 ring-line" : "size-16 bg-surface/80 text-[26px] text-fg ring-1 ring-line-strong",
            )}
          >
            {initial}
          </span>
        )}
      </div>
    );
  }
  return (
    <>
      <img src={src} alt="" aria-hidden decoding="async" className="absolute inset-0 size-full scale-125 object-cover opacity-50 blur-xl" />
      <img
        src={src}
        alt={`${r.name} 画面`}
        decoding="async"
        className="absolute inset-0 size-full object-contain"
      />
    </>
  );
}

export function NotifyOnlyBadge({ recording }: { recording?: boolean }) {
  return (
    <Tip label={recording ? "这一场在录，下播后恢复只提醒" : "只在开播时提醒，不录制"}>
      <span className="inline-flex shrink-0 items-center gap-1 rounded bg-surface-3 px-1.5 py-px text-[11px] font-medium text-fg-2">
        <Bell className="size-3" />
        {recording ? "仅提醒 · 录这一场" : "仅提醒"}
      </span>
    </Tip>
  );
}

/** For a notify-only room that is live: record this live only (shown on cards and list rows). */
export function RecordThisLiveButton({ r, className }: { r: Rec; className?: string }) {
  if (!r.only_notify_no_record || r.state !== "live") return null;
  return (
    <Tip label="只录这一场，下播后恢复只提醒">
      <button
        type="button"
        onClick={() => recActions.recordThisLive(r)}
        className={cx(
          "inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-[11.5px] font-medium text-rec transition hover:bg-rec/10",
          className,
        )}
      >
        <span className="size-1.5 rounded-full bg-rec" />
        录这一场
      </button>
    </Tip>
  );
}

function RecPill({ r }: { r: Rec }) {
  const d = useDuration(r);
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md bg-rec px-1.5 py-0.5 text-[11px] font-semibold text-white shadow-sm">
      <span className="size-1.5 rounded-full bg-white animate-rec" />
      REC
      <span className="font-mono font-medium tabular opacity-95">{clock(d)}</span>
    </span>
  );
}

export function SelectBox({ checked, onClick, light }: { checked: boolean; onClick: () => void; light?: boolean }) {
  return (
    <button
      type="button"
      aria-label={checked ? "取消选择" : "选择"}
      aria-pressed={checked}
      onClick={onClick}
      className={cx(
        "flex size-5 items-center justify-center rounded-md border transition",
        checked
          ? "border-transparent bg-primary text-primary-fg"
          : light
            ? "border-line-strong bg-surface text-transparent hover:border-fg-3"
            : "border-white/60 bg-black/30 text-transparent backdrop-blur-sm hover:border-white",
      )}
    >
      <Check className="size-3.5" strokeWidth={3} />
    </button>
  );
}

export const RecCard = memo(function RecCard({ r, selected, onSelect, onEdit, onDelete }: RecItemProps) {
  const res = resolution(r);
  const recording = r.state === "recording";
  const showCover = (r.state === "recording" || r.state === "reconnecting" || r.state === "live") && !!r.stats.thumb_version;
  return (
    <article
      className={cx(
        "group relative flex flex-col overflow-hidden rounded-xl border bg-surface shadow-card transition [content-visibility:auto] [contain-intrinsic-size:auto_340px]",
        selected ? "border-fg/40 ring-1 ring-fg/25" : "border-line hover:border-line-strong",
      )}
    >
      <div className="relative aspect-video overflow-hidden bg-black">
        <Cover r={r} />
        {showCover && (
          // scrims keep the overlay chips legible on bright frames; placeholders don't need them
          <>
            <div className="pointer-events-none absolute inset-x-0 top-0 h-14 bg-gradient-to-b from-black/45 to-transparent" />
            <div className="pointer-events-none absolute inset-x-0 bottom-0 h-14 bg-gradient-to-t from-black/50 to-transparent" />
          </>
        )}
        <div className={cx("absolute top-2.5 left-2.5 transition", selected ? "opacity-100" : "opacity-0 group-hover:opacity-100 focus-within:opacity-100")}>
          <SelectBox checked={selected} onClick={() => onSelect(r.id)} />
        </div>
        <span className="absolute top-2.5 right-2.5 inline-flex items-center gap-1.5 rounded-md bg-black/50 px-1.5 py-0.5 text-[11px] font-medium text-white backdrop-blur-md">
          <span className="size-1.5 rounded-full" style={{ background: platformColor(r.platform_key) }} />
          {platformName(r.platform_key, r.platform)}
        </span>
        <div className="absolute bottom-2.5 left-2.5">
          {recording ? (
            <RecPill r={r} />
          ) : r.state === "reconnecting" ? (
            <span className="inline-flex items-center gap-1.5 rounded-md bg-warn px-1.5 py-0.5 text-[11px] font-semibold text-on-warn">
              <Loader2 className="size-3 animate-spin" />
              重连中
            </span>
          ) : r.state === "live" ? (
            <span className="rounded-md bg-live px-1.5 py-0.5 text-[11px] font-semibold text-white">LIVE</span>
          ) : null}
        </div>
        {recording && res && (
          <span className="absolute right-2.5 bottom-2.5 rounded-md bg-black/55 px-1.5 py-0.5 font-mono text-[10.5px] font-medium text-white/90 backdrop-blur-md">
            {res}
          </span>
        )}
      </div>

      <div className="flex min-h-0 flex-1 flex-col gap-1 px-3.5 pt-3 pb-2.5">
        <div className="flex items-baseline gap-2">
          <h3 className="min-w-0 truncate text-[14px] font-semibold tracking-[-0.01em]" title={r.name}>
            {r.name}
          </h3>
          {r.only_notify_no_record ? (
            <NotifyOnlyBadge recording={r.is_recording} />
          ) : (
            <span className="shrink-0 text-[11.5px] text-fg-3">
              {qualityLabel(r.quality)} · {r.format}
            </span>
          )}
        </div>
        <p className="truncate text-[12.5px] text-fg-2" title={r.live_title ?? undefined}>
          {r.live_title || <span className="text-fg-3">{r.url.replace(/^https?:\/\/(www\.)?/, "")}</span>}
        </p>
        <div className="mt-1 flex h-5 min-w-0 items-center gap-3 overflow-hidden text-[12px] whitespace-nowrap text-fg-3">
          {recording ? (
            <>
              <Tip label="下载速度">
                <span className="shrink-0 font-mono tabular text-fg-2">{rate(r.stats.speed_bps)}</span>
              </Tip>
              <span className="h-3 w-px shrink-0 bg-line-strong" />
              <Tip label="本次已录大小">
                <span className="shrink-0 font-mono tabular">{bytes(r.stats.size_bytes)}</span>
              </Tip>
              {r.reconnects > 0 ? (
                <>
                  <span className="h-3 w-px shrink-0 bg-line-strong" />
                  <Tip label={r.last_error ? `上次中断：${r.last_error}` : "本场直播中断后已自动接上"}>
                    <span className="min-w-0 truncate text-warn">已重连 {r.reconnects} 次</span>
                  </Tip>
                </>
              ) : (
                r.stats.vcodec && (
                  <>
                    <span className="h-3 w-px shrink-0 bg-line-strong" />
                    <span className="min-w-0 truncate font-mono uppercase">{r.stats.vcodec}</span>
                  </>
                )
              )}
            </>
          ) : (
            <>
              <StatusPill r={r} />
              {(r.state === "reconnecting" || r.state === "error") && r.last_error && (
                <span className="min-w-0 truncate text-fg-3" title={r.last_error}>
                  {r.last_error}
                </span>
              )}
              <RecordThisLiveButton r={r} className="ml-auto" />
            </>
          )}
        </div>
      </div>

      <div className="flex items-center justify-between border-t border-line px-2.5 py-1.5">
        <Tip label={r.monitor ? "暂停监控（会停止正在进行的录制）" : "开始监控：开播自动录制"}>
          <label className="flex cursor-pointer items-center gap-2 rounded-md px-1 py-1 text-[12px] text-fg-2">
            <Switch checked={r.monitor} onChange={(v) => recActions.setMonitor(r, v)} label={`监控 ${r.name}`} />
            {r.monitor ? "监控中" : "已暂停"}
          </label>
        </Tip>
        <RecMenu r={r} onEdit={() => onEdit(r)} onDelete={() => onDelete(r)} />
      </div>
      <span className="sr-only">{stateLabel(r)}</span>
    </article>
  );
});
