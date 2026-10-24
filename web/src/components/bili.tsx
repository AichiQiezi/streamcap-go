import { CircleAlert, LogOut, QrCode, RefreshCw, Smartphone, Upload } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { useStore } from "@/lib/store";
import type { MediaFile, Partition, UploadForm } from "@/lib/types";
import { Button, Confirm, cx, Dialog, Field, Input, Segmented, Switch } from "./ui";

type QRState = { qr: string; code: string; status: "waiting" | "scanned" | "expired" } | { error: string } | null;

/** Scan-to-login with the Bilibili app (TV QR login: no password ever goes through StreamCap). */
export function BiliLoginDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [state, setState] = useState<QRState>(null);
  const gen = useRef(0);

  const start = async () => {
    const my = ++gen.current;
    setState(null);
    try {
      const res = await api.biliLogin();
      if (my === gen.current) setState({ qr: res.qr, code: res.auth_code, status: "waiting" });
    } catch (e) {
      if (my === gen.current) setState({ error: (e as Error).message });
    }
  };

  useEffect(() => {
    if (open) start();
    else gen.current++; // stop polling
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const code = state && "code" in state && state.status !== "expired" ? state.code : null;
  useEffect(() => {
    if (!code) return;
    const my = gen.current;
    let stopped = false;
    const tick = async () => {
      try {
        const res = await api.biliLoginPoll(code);
        if (stopped || my !== gen.current) return;
        if (res.status === "done") {
          useStore.setState({ biliAccount: res.account });
          toast.success(`已登录 B站账号${res.account?.name ? `「${res.account.name}」` : ""}`);
          onOpenChange(false);
          return;
        }
        setState((s) => (s && "code" in s ? { ...s, status: res.status as "waiting" | "scanned" | "expired" } : s));
        if (res.status !== "expired") timer = setTimeout(tick, 2000);
      } catch (e) {
        if (!stopped && my === gen.current) setState({ error: (e as Error).message });
      }
    };
    let timer = setTimeout(tick, 2000);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [code]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title="登录 B站账号" description="用于把录像投稿到你的 B站账号" width={400}>
      <div className="flex flex-col items-center gap-4 pt-2">
        <div className="relative flex size-[220px] items-center justify-center overflow-hidden rounded-xl border border-line bg-white">
          {state && "qr" in state ? (
            <img src={state.qr} alt="登录二维码" className={cx("size-full p-2 [image-rendering:pixelated]", state.status !== "waiting" && "opacity-15")} />
          ) : state && "error" in state ? (
            <CircleAlert className="size-7 text-err" />
          ) : (
            <QrCode className="size-8 animate-pulse text-neutral-300" />
          )}
          {state && "status" in state && state.status === "scanned" && (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-neutral-900">
              <Smartphone className="size-7" />
              <span className="text-[13px] font-medium">已扫码，请在手机上确认</span>
            </div>
          )}
          {state && "status" in state && state.status === "expired" && (
            <button onClick={start} className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-neutral-900">
              <RefreshCw className="size-6" />
              <span className="text-[13px] font-medium">二维码已过期，点击刷新</span>
            </button>
          )}
        </div>
        {state && "error" in state ? (
          <div className="text-center text-[12.5px] text-err">
            {state.error}
            <Button size="sm" className="mt-3" onClick={start}>
              重试
            </Button>
          </div>
        ) : (
          <p className="text-center text-[12.5px] leading-relaxed text-fg-3">
            打开 <span className="font-medium text-fg-2">哔哩哔哩 App</span>，用首页左上角的扫一扫扫描二维码
            <br />
            登录信息只保存在本机的 data/uploads.json
          </p>
        )}
      </div>
    </Dialog>
  );
}

/** Account row for the settings page: who is logged in, or a login button. */
export function BiliAccountRow() {
  const account = useStore((s) => s.biliAccount);
  const [login, setLogin] = useState(false);
  const [logout, setLogout] = useState(false);
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 py-3">
      {account ? (
        <div className="flex min-w-0 items-center gap-3">
          {account.face ? (
            <img src={account.face} alt="" referrerPolicy="no-referrer" className="size-9 shrink-0 rounded-full border border-line object-cover" />
          ) : (
            <span className="size-9 shrink-0 rounded-full bg-surface-3" />
          )}
          <div className="min-w-0">
            <div className="truncate text-[13px] font-medium">{account.name || `UID ${account.mid}`}</div>
            <div className="text-[12px] text-fg-3">
              UID <span className="font-mono tabular">{account.mid}</span> · 登录有效期至 {new Date(account.expires_at).toLocaleDateString()}（到期前自动续期）
            </div>
          </div>
        </div>
      ) : (
        <div className="min-w-[200px] flex-1">
          <div className="text-[13px] font-medium">{account === undefined ? "正在读取…" : "未登录"}</div>
          <div className="mt-0.5 text-[12px] text-fg-3">用哔哩哔哩 App 扫码登录后，才能把录像投稿到 B站</div>
        </div>
      )}
      {account ? (
        <Button size="sm" variant="ghost" icon={<LogOut className="size-3.5" />} onClick={() => setLogout(true)}>
          退出登录
        </Button>
      ) : (
        <Button size="sm" variant="primary" icon={<QrCode className="size-3.5" />} onClick={() => setLogin(true)} disabled={account === undefined}>
          扫码登录
        </Button>
      )}
      <BiliLoginDialog open={login} onOpenChange={setLogin} />
      <Confirm
        open={logout}
        onOpenChange={setLogout}
        title="退出 B站账号？"
        body="退出后，排队中的投稿会因未登录而失败；重新登录后可以在「投稿」页重试。"
        confirmText="退出登录"
        danger
        onConfirm={async () => {
          try {
            await api.biliLogout();
            useStore.setState({ biliAccount: null });
          } catch (e) {
            toast.error((e as Error).message);
          }
        }}
      />
    </div>
  );
}

// Partitions rarely change; share one fetch between the settings page and dialogs.
let partitionsCache: Promise<Partition[]> | null = null;
function loadPartitions(): Promise<Partition[]> {
  partitionsCache ??= api.biliPartitions().then(
    (r) => r.partitions,
    (e) => {
      partitionsCache = null;
      throw e;
    },
  );
  return partitionsCache;
}

/** 分区 picker. Falls back to a number input when the list can't be loaded (e.g. not logged in yet). */
export function PartitionSelect({ value, onChange, className }: { value: number; onChange: (v: number) => void; className?: string }) {
  const account = useStore((s) => s.biliAccount);
  const [parts, setParts] = useState<Partition[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!account) return;
    loadPartitions().then(setParts, (e: Error) => setError(e.message));
  }, [account]);

  if (!parts)
    return (
      <div className="flex items-center gap-2 text-[12px] text-fg-3" title={error ?? undefined}>
        <span>分区 ID</span>
        <Input className="h-8 w-20 text-right font-mono" inputMode="numeric" value={value || ""} onChange={(e) => onChange(Number(e.target.value.replace(/\D/g, "")) || 0)} />
        {account === null && <span>登录后可从列表选择</span>}
        {error && <span className="text-warn">分区列表加载失败</span>}
      </div>
    );
  const known = parts.some((p) => p.children?.some((c) => c.id === value));
  return (
    <select
      value={known ? String(value) : ""}
      onChange={(e) => onChange(Number(e.target.value))}
      className={cx(
        "h-8.5 rounded-lg border border-line bg-surface px-2.5 text-[13px] text-fg focus:ring-3 focus:ring-[var(--ring)] focus:outline-none",
        className ?? "w-56",
      )}
    >
      {!known && <option value="">{value ? `分区 ${value}` : "选择分区"}</option>}
      {parts.map((p) => (
        <optgroup key={p.id} label={p.name}>
          {(p.children ?? []).map((c) => (
            <option key={c.id} value={c.id} title={c.desc}>
              {p.name} · {c.name}
            </option>
          ))}
        </optgroup>
      ))}
    </select>
  );
}

export const textareaClass =
  "w-full rounded-lg border border-line bg-surface px-3 py-2 text-[13px] leading-relaxed text-fg placeholder:text-fg-3 transition focus:border-line-strong focus:outline-none focus:ring-3 focus:ring-[var(--ring)]";

/**
 * The submit form, prefilled from the settings' templates (using the first file). Several files
 * become the parts (P1, P2, …) of one submission, in the order given.
 */
export function UploadDialog({ files, onClose }: { files: MediaFile[] | null; onClose: () => void }) {
  const file = files?.[0] ?? null;
  const account = useStore((s) => s.biliAccount);
  const [form, setForm] = useState<UploadForm | null>(null);
  const [saving, setSaving] = useState(false);
  const [login, setLogin] = useState(false);

  useEffect(() => {
    if (!file) return;
    setForm(null);
    api.uploadDraft(file.path).then(setForm, (e: Error) => {
      toast.error(e.message);
      onClose();
    });
  }, [file]); // eslint-disable-line react-hooks/exhaustive-deps

  const set = <K extends keyof UploadForm>(k: K, v: UploadForm[K]) => setForm((f) => (f ? { ...f, [k]: v } : f));
  const titleLen = form ? [...form.title].length : 0;

  async function submit() {
    if (!file || !form) return;
    setSaving(true);
    try {
      await api.addUpload(files!.map((f) => f.path), form);
      toast.success("已加入投稿队列", {
        description: "上传进度可以在「投稿」页查看",
        action: { label: "查看", onClick: () => (location.hash = "#/uploads") },
      });
      onClose();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog
      open={!!file}
      onOpenChange={(o) => !o && onClose()}
      title="投稿到 B站"
      description={files && files.length > 1 ? `${files.length} 个文件，投成同一个稿件的 ${files.length} 个分P（按时间顺序）` : file?.name}
      width={600}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            取消
          </Button>
          {account ? (
            <Button variant="primary" icon={<Upload className="size-3.5" />} loading={saving} disabled={!form} onClick={submit}>
              开始上传
            </Button>
          ) : (
            <Button variant="primary" onClick={() => setLogin(true)} disabled={account === undefined}>
              先扫码登录
            </Button>
          )}
        </>
      }
    >
      {!form ? (
        <div className="space-y-3 pt-1">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="h-9 animate-pulse rounded-lg bg-surface-2" />
          ))}
        </div>
      ) : (
        <div className="space-y-4 pt-1">
          {account && (
            <div className="flex items-center gap-2 text-[12.5px] text-fg-3">
              投稿账号
              {account.face && <img src={account.face} alt="" referrerPolicy="no-referrer" className="size-5 rounded-full" />}
              <span className="font-medium text-fg-2">{account.name || account.mid}</span>
            </div>
          )}
          <Field label={<span className="flex justify-between">标题<span className={cx("font-mono tabular", titleLen > 80 ? "text-err" : "text-fg-3")}>{titleLen}/80</span></span>}>
            <Input value={form.title} onChange={(e) => set("title", e.target.value)} />
          </Field>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-[auto_minmax(0,1fr)]">
            {/* not a <Field>: its <label> would forward clicks on the caption to the first button */}
            <div className="space-y-1.5">
              <span className="block text-[12.5px] font-medium text-fg-2">类型</span>
              <Segmented
                value={String(form.copyright) as "1" | "2"}
                onChange={(v) => set("copyright", Number(v) as 1 | 2)}
                options={[
                  { value: "1", label: "自制", title: "自己的直播" },
                  { value: "2", label: "转载", title: "录的是别人的直播，需要注明来源" },
                ]}
              />
            </div>
            {form.copyright === 2 && (
              <Field label="转载来源">
                <Input value={form.source} onChange={(e) => set("source", e.target.value)} placeholder="直播间链接" className="font-mono text-[12px]" />
              </Field>
            )}
          </div>
          <Field label="分区">
            <PartitionSelect value={form.tid} onChange={(v) => set("tid", v)} className="w-full" />
          </Field>
          <Field label="标签" hint="用逗号分隔，最多 10 个">
            <Input value={form.tags} onChange={(e) => set("tags", e.target.value)} />
          </Field>
          <Field label="简介">
            <textarea rows={4} value={form.desc} onChange={(e) => set("desc", e.target.value)} className={textareaClass} />
          </Field>
          <Field inline label="仅自己可见" hint="审核通过后也只有你自己能看到，之后可以在 B站稿件管理里改成公开">
            <Switch checked={form.only_self} onChange={(v) => set("only_self", v)} label="仅自己可见" />
          </Field>
          <p className="text-[12px] leading-relaxed text-fg-3">
            封面会从视频里自动截取一帧。投稿后需要 B站审核，通过后才会公开。
          </p>
        </div>
      )}
      <BiliLoginDialog open={login} onOpenChange={setLogin} />
    </Dialog>
  );
}
