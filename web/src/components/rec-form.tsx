import { ChevronDown, CircleAlert } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { detectPlatform, platformColor, platformName } from "@/lib/platforms";
import { useStore } from "@/lib/store";
import type { Rec, RecInput, SettingsValues } from "@/lib/types";
import { Button, cx, Dialog, Field, Input, Segmented, Select, Switch } from "./ui";

interface Form {
  urls: string;
  name: string;
  quality: string;
  format: string;
  monitor: boolean;
  segment: boolean;
  segmentTime: string;
  bitrate: string;
  dir: string;
  scheduled: boolean;
  schedStart: string;
  schedHours: string;
  push: boolean;
  notifyOnly: boolean;
  flvDirect: boolean;
}

const AUDIO = new Set(["MP3", "M4A", "AAC", "WAV", "WMA"]);

function fromRec(r: Rec): Form {
  return {
    urls: r.url,
    name: r.name,
    quality: r.quality,
    format: (r.format || "TS").toUpperCase(),
    monitor: r.monitor,
    segment: r.segment_record,
    segmentTime: r.segment_time || "1800",
    bitrate: r.video_bitrate ? String(r.video_bitrate) : "",
    dir: r.recording_dir || "",
    scheduled: r.scheduled_recording,
    schedStart: (r.scheduled_start_time || "").split(",")[0] || "18:00:00",
    schedHours: (r.monitor_hours || "").split(",")[0] || "5",
    push: r.enabled_message_push,
    notifyOnly: r.only_notify_no_record,
    flvDirect: r.flv_use_direct_download,
  };
}

export function RecFormDialog({ open, onOpenChange, editing }: { open: boolean; onOpenChange: (o: boolean) => void; editing?: Rec | null }) {
  const options = useStore((s) => s.options);
  const [f, setF] = useState<Form | null>(null);
  const [advanced, setAdvanced] = useState(false);
  const [saving, setSaving] = useState(false);
  const [defaultDir, setDefaultDir] = useState("");

  useEffect(() => {
    if (!open) return;
    setAdvanced(false);
    if (editing) {
      setF(fromRec(editing));
      // only for the 保存目录 placeholder
      api.settings().then((st) => setDefaultDir(st.default_save_path), () => undefined);
      return;
    }
    setF(null);
    const init = (v: SettingsValues, defaultSavePath = "") => {
      setDefaultDir(defaultSavePath);
      setF({
        urls: "",
        name: "",
        quality: String(v.record_quality || "OD"),
        format: String(v.video_format || "TS").toUpperCase(),
        monitor: true,
        segment: Boolean(v.segmented_recording_enabled),
        segmentTime: String(v.video_segment_time || "1800"),
        bitrate: "",
        dir: "",
        scheduled: false,
        schedStart: "18:00:00",
        schedHours: "5",
        push: false,
        notifyOnly: Boolean(v.only_notify_no_record),
        flvDirect: Boolean(v.flv_use_direct_download),
      });
    };
    // Without settings the form still works with built-in defaults, instead of a skeleton forever.
    api.settings().then(
      (st) => init(st.values, st.default_save_path),
      () => init({}),
    );
  }, [open, editing]);

  const lines = useMemo(
    () =>
      (f?.urls ?? "")
        .split(/[\s,，]+/)
        .map((s) => s.trim())
        .filter(Boolean)
        .map((url) => ({ url, platform: detectPlatform(url) })),
    [f?.urls],
  );
  const valid = lines.filter((l) => l.platform);
  const single = editing || lines.length <= 1;

  const set = <K extends keyof Form>(k: K, v: Form[K]) => setF((p) => (p ? { ...p, [k]: v } : p));

  async function submit() {
    if (!f) return;
    const bitrate = f.bitrate.trim() ? Number(f.bitrate) : null;
    if (bitrate !== null && (!Number.isInteger(bitrate) || bitrate <= 0)) {
      toast.error("码率需要是大于 0 的整数（kbps）");
      return;
    }
    const base: Omit<RecInput, "url"> = {
      quality: f.quality,
      record_format: f.format,
      segment_record: f.segment,
      segment_time: f.segmentTime,
      video_bitrate: AUDIO.has(f.format) ? null : bitrate,
      recording_dir: f.dir.trim() || null,
      scheduled_recording: f.scheduled,
      scheduled_start_time: f.scheduled ? f.schedStart : null,
      monitor_hours: f.scheduled ? f.schedHours : null,
      enabled_message_push: f.push,
      only_notify_no_record: f.notifyOnly,
      flv_use_direct_download: f.flvDirect,
    };
    setSaving(true);
    try {
      if (editing) {
        const res = await api.edit(editing.id, { ...base, url: f.urls.trim(), streamer_name: f.name.trim() || editing.name });
        useStore.getState().upsert(res);
        toast.success("已保存");
      } else {
        const res = await api.add(
          valid.map(({ url }) => ({ ...base, url, monitor_status: f.monitor, streamer_name: single ? f.name.trim() || null : null })),
        );
        res.created.forEach((r) => useStore.getState().upsert(r));
        if (res.created.length) toast.success(`已添加 ${res.created.length} 个直播间${f.monitor ? "，正在检测开播状态" : ""}`);
        res.skipped.forEach((s) => toast.warning(`${s.reason}：${s.url}`));
        if (!res.created.length) return;
      }
      onOpenChange(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  const formatOptions = [
    ...options.video_formats.map((v) => ({ value: v, label: v === "TS" ? "TS（推荐，断电不损坏）" : v })),
    ...options.audio_formats.map((v) => ({ value: v, label: `${v}（仅音频）` })),
  ];

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={editing ? `编辑「${editing.name}」` : "添加直播间"}
      description={editing ? undefined : "粘贴直播间链接，一行一个，可以一次添加多个"}
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button variant="primary" loading={saving} disabled={!f || (!editing && !valid.length)} onClick={submit}>
            {editing ? "保存" : valid.length > 1 ? `添加 ${valid.length} 个` : "添加"}
          </Button>
        </>
      }
    >
      {!f ? (
        <div className="h-64 animate-pulse rounded-xl bg-surface-2" />
      ) : (
        <div className="space-y-5">
          <div className="space-y-2">
            {editing ? (
              <Field label="直播间链接">
                <Input autoFocus value={f.urls} onChange={(e) => set("urls", e.target.value)} />
              </Field>
            ) : (
              <>
                <textarea
                  autoFocus
                  value={f.urls}
                  onChange={(e) => set("urls", e.target.value)}
                  onKeyDown={(e) => (e.metaKey || e.ctrlKey) && e.key === "Enter" && submit()}
                  rows={3}
                  placeholder={"https://live.douyin.com/123456\nhttps://live.bilibili.com/22603"}
                  className="w-full resize-none rounded-xl border border-line bg-surface px-3.5 py-3 font-mono text-[12.5px] leading-6 text-fg placeholder:text-fg-3 focus:border-line-strong focus:ring-3 focus:ring-[var(--ring)] focus:outline-none"
                />
                {lines.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">
                    {lines.map((l, i) => (
                      <span
                        key={i}
                        className={cx(
                          "inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-0.5 text-[11.5px]",
                          l.platform ? "border-line bg-surface-2 text-fg-2" : "border-err/30 bg-err/8 text-err",
                        )}
                      >
                        {l.platform ? (
                          <span className="size-1.5 rounded-full" style={{ background: platformColor(l.platform) }} />
                        ) : (
                          <CircleAlert className="size-3" />
                        )}
                        <span className="font-medium">{l.platform ? platformName(l.platform) : "不支持"}</span>
                        <span className="truncate opacity-70">{l.url.replace(/^https?:\/\/(www\.)?/, "").slice(0, 32)}</span>
                      </span>
                    ))}
                  </div>
                )}
              </>
            )}
          </div>

          {single && (
            <Field label="主播名称" hint={editing ? undefined : "可不填，开播后会自动获取"}>
              <Input value={f.name} onChange={(e) => set("name", e.target.value)} placeholder="自动获取" />
            </Field>
          )}

          <div className="grid grid-cols-[1fr_auto] items-end gap-4">
            <Field label="画质">
              <Segmented value={f.quality} onChange={(v) => set("quality", v)} options={options.qualities} />
            </Field>
            <Field label="格式">
              <Select value={f.format} onChange={(v) => set("format", v)} options={formatOptions} className="w-[190px]" />
            </Field>
          </div>

          {!editing && (
            <div className="rounded-xl border border-line px-4 py-1">
              <Field inline label="添加后立即开始监控" hint="开播即自动录制；关闭则只添加到列表">
                <Switch checked={f.monitor} onChange={(v) => set("monitor", v)} />
              </Field>
            </div>
          )}

          <div>
            <button
              type="button"
              onClick={() => setAdvanced((a) => !a)}
              className="inline-flex items-center gap-1 text-[12.5px] font-medium text-fg-2 hover:text-fg"
            >
              <ChevronDown className={cx("size-3.5 transition", advanced && "rotate-180")} />
              更多选项
            </button>
            {advanced && (
              <div className="mt-3 divide-y divide-line rounded-xl border border-line px-4">
                <Field inline label="分段录制" hint="按时长切成多个文件，长时间录制更安全">
                  <div className="flex items-center gap-2">
                    {f.segment && (
                      <div className="flex items-center gap-1.5 text-[12px] text-fg-3">
                        <Input className="h-7 w-20 text-right font-mono" value={f.segmentTime} onChange={(e) => set("segmentTime", e.target.value.replace(/\D/g, ""))} />
                        秒
                      </div>
                    )}
                    <Switch checked={f.segment} onChange={(v) => set("segment", v)} />
                  </div>
                </Field>
                {!AUDIO.has(f.format) && (
                  <Field inline label="自定义码率" hint="留空则原样保存（推荐）；填写会重新编码，占用较多 CPU">
                    <div className="flex items-center gap-1.5 text-[12px] text-fg-3">
                      <Input className="h-7 w-24 text-right font-mono" placeholder="原始" value={f.bitrate} onChange={(e) => set("bitrate", e.target.value.replace(/\D/g, ""))} />
                      kbps
                    </div>
                  </Field>
                )}
                <div className="py-3">
                  <Field label="保存目录" hint="留空使用设置里的默认目录">
                    <Input value={f.dir} onChange={(e) => set("dir", e.target.value)} placeholder={defaultDir || "默认目录"} className="font-mono text-[12px]" />
                  </Field>
                </div>
                <Field inline label="定时监控" hint="只在每天指定时段内检测开播">
                  <Switch checked={f.scheduled} onChange={(v) => set("scheduled", v)} />
                </Field>
                {f.scheduled && (
                  <div className="flex items-center gap-3 py-3 text-[12.5px] text-fg-2">
                    每天
                    <Input type="time" step={1} className="h-7 w-32 font-mono" value={f.schedStart} onChange={(e) => set("schedStart", e.target.value.length === 5 ? `${e.target.value}:00` : e.target.value)} />
                    开始，持续
                    <Input className="h-7 w-14 text-right font-mono" value={f.schedHours} onChange={(e) => set("schedHours", e.target.value.replace(/[^\d.]/g, ""))} />
                    小时
                  </div>
                )}
                <Field inline label="开播消息推送" hint="需在设置里配置推送渠道">
                  <Switch checked={f.push} onChange={(v) => set("push", v)} />
                </Field>
                <Field inline label="仅通知，不录制">
                  <Switch checked={f.notifyOnly} onChange={(v) => set("notifyOnly", v)} />
                </Field>
              </div>
            )}
          </div>
        </div>
      )}
    </Dialog>
  );
}
