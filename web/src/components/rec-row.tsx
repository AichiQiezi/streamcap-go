import { memo } from "react";
import { bytes, clock, rate, resolution, useDuration } from "@/lib/format";
import { platformColor, platformName } from "@/lib/platforms";
import { recActions, RecMenu } from "./rec-actions";
import { Cover, NotifyOnlyBadge, qualityLabel, RecordThisLiveButton, SelectBox, type RecItemProps } from "./rec-card";
import { StatusPill } from "./status";
import { cx, Switch } from "./ui";

// Speed / size / quality only show once the list is wide enough; below that the row keeps the
// monitor switch and actions on screen instead of scrolling them out of view. Needs an
// `@container` ancestor (see RoomsPage).
export const ROW_GRID =
  "grid grid-cols-[28px_minmax(200px,2.4fr)_80px_120px_84px_56px_108px] @min-[1000px]:grid-cols-[28px_minmax(220px,2.4fr)_88px_120px_84px_96px_88px_72px_56px_108px] items-center gap-3";
const WIDE = "hidden @min-[1000px]:block";

function Duration({ r }: { r: RecItemProps["r"] }) {
  const d = useDuration(r);
  return <span className={cx("font-mono tabular", r.state === "recording" ? "text-fg" : "text-fg-3")}>{d ? clock(d) : "—"}</span>;
}

export const RecRow = memo(function RecRow({ r, selected, onSelect, onEdit, onDelete }: RecItemProps) {
  const recording = r.state === "recording";
  return (
    <div
      className={cx(
        ROW_GRID,
        "group border-b border-line px-4 py-2 text-[12.5px] transition last:border-b-0",
        selected ? "bg-surface-2" : "hover:bg-surface-2/60",
      )}
    >
      <div className={cx("transition", selected ? "opacity-100" : "opacity-40 group-hover:opacity-100")}>
        <SelectBox light checked={selected} onClick={() => onSelect(r.id)} />
      </div>
      <div className="flex min-w-0 items-center gap-3">
        <div className="relative aspect-video w-[72px] shrink-0 overflow-hidden rounded-md bg-black">
          <Cover r={r} compact />
          {recording && <span className="absolute top-1 left-1 size-1.5 rounded-full bg-rec animate-rec" />}
        </div>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-[13px] font-semibold">{r.name}</span>
            {r.only_notify_no_record && <NotifyOnlyBadge recording={r.is_recording} />}
          </div>
          <div className="flex min-w-0 items-center gap-1">
            <RecordThisLiveButton r={r} className="-ml-1.5" />
            <span className="truncate text-[12px] text-fg-3">{r.live_title || r.url.replace(/^https?:\/\/(www\.)?/, "")}</span>
            {recording && r.stats.danmaku != null && (
              <span className="shrink-0 text-[12px] text-fg-3 tabular" title="本场已录弹幕">
                · 弹幕 {r.stats.danmaku}
              </span>
            )}
          </div>
        </div>
      </div>
      <span className="inline-flex items-center gap-1.5 truncate text-fg-2">
        <span className="size-1.5 shrink-0 rounded-full" style={{ background: platformColor(r.platform_key) }} />
        {platformName(r.platform_key, r.platform)}
      </span>
      <StatusPill r={r} />
      <Duration r={r} />
      <span className={cx(WIDE, "font-mono tabular text-fg-2")}>{recording ? rate(r.stats.speed_bps) : "—"}</span>
      <span className={cx(WIDE, "font-mono tabular text-fg-2")}>{recording ? bytes(r.stats.size_bytes) : "—"}</span>
      <span className={cx(WIDE, "truncate font-mono text-fg-3")}>
        {r.only_notify_no_record && !recording ? "—" : (recording && resolution(r)) || qualityLabel(r.quality)}
      </span>
      <Switch checked={r.monitor} onChange={(v) => recActions.setMonitor(r, v)} label={`监控 ${r.name}`} />
      <div className="justify-self-end">
        <RecMenu r={r} onEdit={() => onEdit(r)} onDelete={() => onDelete(r)} />
      </div>
    </div>
  );
});

export function RowHeader() {
  return (
    <div className={cx(ROW_GRID, "border-b border-line px-4 py-2 text-[11.5px] font-medium text-fg-3")}>
      <span />
      <span>直播间</span>
      <span>平台</span>
      <span>状态</span>
      <span>时长</span>
      <span className={WIDE}>速度</span>
      <span className={WIDE}>大小</span>
      <span className={WIDE}>画质</span>
      <span>监控</span>
      <span />
    </div>
  );
}
