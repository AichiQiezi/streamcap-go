import { CircleAlert, CircleCheck, Clock, ExternalLink, Loader2, RotateCw, Trash2, Upload, X } from "lucide-react";
import { useState } from "react";
import { useShallow } from "zustand/react/shallow";
import { toast } from "sonner";
import { BiliLoginDialog } from "@/components/bili";
import { Button, Confirm, cx, IconButton } from "@/components/ui";
import { api } from "@/lib/api";
import { ago, bytes, clock, rate } from "@/lib/format";
import { useStore } from "@/lib/store";
import type { UploadState, UploadTask } from "@/lib/types";

const STATES: Record<UploadState, { label: string; className: string }> = {
  queued: { label: "排队中", className: "bg-surface-3 text-fg-2" },
  uploading: { label: "上传中", className: "bg-primary/12 text-fg" },
  submitting: { label: "提交稿件", className: "bg-primary/12 text-fg" },
  done: { label: "已投稿", className: "bg-ok/12 text-ok" },
  failed: { label: "失败", className: "bg-err/12 text-err" },
  canceled: { label: "已取消", className: "bg-surface-3 text-fg-3" },
};

async function run(fn: () => Promise<unknown>) {
  try {
    await fn();
  } catch (e) {
    toast.error((e as Error).message);
  }
}

function TaskRow({ t, onRemove }: { t: UploadTask; onRemove: (t: UploadTask) => void }) {
  const st = STATES[t.state];
  const active = t.state === "uploading" || t.state === "submitting";
  const pct = t.total > 0 ? Math.min(100, (t.sent / t.total) * 100) : 0;
  const eta = t.state === "uploading" && t.speed_bps > 0 ? (t.total - t.sent) / t.speed_bps : null;
  const when = new Date(t.finished_at && !t.finished_at.startsWith("0001") ? t.finished_at : t.created_at).getTime() / 1000;
  return (
    <div className="group border-b border-line px-4 py-3 last:border-b-0 md:px-5">
      <div className="flex items-start gap-3">
        <span className={cx("mt-0.5 inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-px text-[11px] font-medium", st.className)}>
          {active && <Loader2 className="size-3 animate-spin" />}
          {t.state === "done" && <CircleCheck className="size-3" />}
          {t.state === "queued" && <Clock className="size-3" />}
          {t.state === "failed" && <CircleAlert className="size-3" />}
          {st.label}
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13.5px] font-medium" title={t.title}>
            {t.title}
          </div>
          <div className="mt-0.5 flex flex-wrap gap-x-3 text-[12px] text-fg-3">
            <span className="truncate font-mono" title={t.files.join("\n")}>
              {t.names.length > 1 ? `${t.names.length} 个分P` : t.names[0]}
            </span>
            <span className="font-mono tabular">{bytes(t.total)}</span>
            <span>{t.copyright === 2 ? "转载" : "自制"}</span>
            {t.only_self && <span>仅自己可见</span>}
            {t.room_id && <span>下播后自动投稿</span>}
            <span title={new Date(when * 1000).toLocaleString()}>{ago(when)}</span>
          </div>
        </div>
        <div className="flex shrink-0 gap-0.5">
          {t.state === "done" && t.bvid && (
            <IconButton label="在 B站打开稿件" onClick={() => window.open(`https://www.bilibili.com/video/${t.bvid}`, "_blank", "noopener")}>
              <ExternalLink className="size-3.5" />
            </IconButton>
          )}
          {(t.state === "failed" || t.state === "canceled") && (
            <IconButton label="重试" onClick={() => run(() => api.retryUpload(t.id))}>
              <RotateCw className="size-3.5" />
            </IconButton>
          )}
          {(t.state === "queued" || active) && (
            <IconButton label="取消上传" onClick={() => run(() => api.cancelUpload(t.id))}>
              <X className="size-3.5" />
            </IconButton>
          )}
          <IconButton label="删除记录（不删除文件）" tone="danger" onClick={() => onRemove(t)}>
            <Trash2 className="size-3.5" />
          </IconButton>
        </div>
      </div>
      {(t.state === "uploading" || t.state === "submitting") && (
        <div className="mt-2.5">
          <div className="h-1.5 overflow-hidden rounded-full bg-surface-3">
            <div className="h-full rounded-full bg-primary transition-[width] duration-500" style={{ width: `${t.state === "submitting" ? 100 : pct}%` }} />
          </div>
          <div className="mt-1 flex justify-between font-mono text-[11.5px] text-fg-3 tabular">
            <span>
              {t.state === "submitting" ? "上传完成，正在提交稿件…" : `${bytes(t.sent)} / ${bytes(t.total)} · ${pct.toFixed(1)}%`}
            </span>
            {t.state === "uploading" && <span>{rate(t.speed_bps)}{eta !== null && ` · 剩余 ${clock(eta)}`}</span>}
          </div>
        </div>
      )}
      {t.state === "failed" && t.error && <div className="mt-2 rounded-lg bg-err/8 px-3 py-2 text-[12px] leading-relaxed break-all text-err">{t.error}</div>}
      {t.state === "done" && (
        <div className="mt-1.5 text-[12px] text-fg-3">
          稿件 <span className="font-mono text-fg-2">{t.bvid}</span> 已提交，{t.only_self ? "审核通过后仅自己可见" : "B站审核通过后公开"}
        </div>
      )}
    </div>
  );
}

export function UploadsPage() {
  const tasks = useStore(useShallow((s) => s.uploadOrder.map((id) => s.uploads[id]).filter(Boolean)));
  const account = useStore((s) => s.biliAccount);
  const [removing, setRemoving] = useState<UploadTask | null>(null);
  const [login, setLogin] = useState(false);
  const finished = tasks.filter((t) => t.state === "done");

  return (
    <div className="mx-auto w-full max-w-[1080px] px-5 pt-7 pb-16 md:px-8">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-[22px] font-semibold tracking-[-0.02em]">投稿</h1>
          <p className="mt-1 text-[12.5px] text-fg-3">
            在「录像」页选择文件投稿，或在直播间设置里打开「下播后自动投稿到 B站」
          </p>
        </div>
        <div className="flex items-center gap-2">
          {account ? (
            <span className="flex items-center gap-2 text-[12.5px] text-fg-2">
              {account.face && <img src={account.face} alt="" referrerPolicy="no-referrer" className="size-6 rounded-full border border-line" />}
              {account.name || `UID ${account.mid}`}
            </span>
          ) : (
            account === null && (
              <Button variant="primary" icon={<Upload className="size-3.5" />} onClick={() => setLogin(true)}>
                扫码登录 B站
              </Button>
            )
          )}
          {account && (
            <Button icon={<ExternalLink className="size-3.5" />} onClick={() => window.open("https://member.bilibili.com/platform/upload-manager/article", "_blank", "noopener")}>
              稿件管理
            </Button>
          )}
        </div>
      </header>

      <div className="mt-6 overflow-hidden rounded-xl border border-line bg-surface">
        {tasks.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-16 text-center">
            <span className="flex size-11 items-center justify-center rounded-xl border border-line bg-surface-2 text-fg-3">
              <Upload className="size-4.5" />
            </span>
            <div>
              <div className="text-[13.5px] font-medium text-fg">还没有投稿</div>
              <div className="mt-1 text-[12.5px] text-fg-3">到「录像」页，把鼠标移到文件上，点上传按钮</div>
            </div>
          </div>
        ) : (
          tasks.map((t) => <TaskRow key={t.id} t={t} onRemove={setRemoving} />)
        )}
      </div>
      {finished.length > 0 && (
        <p className="mt-3 text-right text-[12px] text-fg-3">
          已投稿 <span className="font-mono tabular">{finished.length}</span> 个
        </p>
      )}

      <BiliLoginDialog open={login} onOpenChange={setLogin} />
      <Confirm
        open={!!removing}
        onOpenChange={(o) => !o && setRemoving(null)}
        title="删除这条投稿记录？"
        body={
          removing?.state === "uploading" || removing?.state === "queued"
            ? "上传会被取消。本地录像文件不会被删除。"
            : "只删除列表里的记录，本地录像文件和已投稿的稿件都不受影响。"
        }
        confirmText="删除记录"
        danger
        onConfirm={() => run(() => api.removeUpload(removing!.id))}
      />
    </div>
  );
}
