import { FileAudio, FileVideo, FolderOpen, Play, RefreshCw, Search, Trash2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { PlayerDialog } from "@/components/player";
import { Button, Confirm, cx, IconButton, Input, Select } from "@/components/ui";
import { api } from "@/lib/api";
import { useStore } from "@/lib/store";
import { ago, bytes } from "@/lib/format";
import type { MediaFile } from "@/lib/types";

/** "斗鱼直播/玩机器/original" -> { group: "玩机器", platform: "斗鱼直播", original: true } */
function describe(folder: string) {
  const parts = folder.split("/").filter(Boolean);
  const original = parts[parts.length - 1] === "original";
  const core = original ? parts.slice(0, -1) : parts;
  return { group: core[core.length - 1] || "未分组", platform: core.length > 1 ? core[0] : "", original };
}

export function LibraryPage() {
  const [files, setFiles] = useState<MediaFile[] | null>(null);
  const [root, setRoot] = useState("");
  const [query, setQuery] = useState("");
  const [group, setGroup] = useState("all");
  const [showOriginal, setShowOriginal] = useState(false);
  const [playing, setPlaying] = useState<MediaFile | null>(null);
  const [deleting, setDeleting] = useState<MediaFile | null>(null);
  const [loading, setLoading] = useState(false);
  const activeFiles = useStore((st) =>
    Object.values(st.recs)
      .filter((r) => r.state === "recording" && r.stats.file)
      .map((r) => r.stats.file!.replace(/\\/g, "/"))
      .join("\n"),
  );
  const activeSet = useMemo(() => {
    const act = activeFiles.split("\n").filter(Boolean);
    const m = new Map<string, boolean>();
    (files ?? []).forEach((f) => m.set(f.path, act.some((a) => a.endsWith(`/${f.path}`))));
    return m;
  }, [files, activeFiles]);
  const isActive = (f: MediaFile) => activeSet.get(f.path) ?? false;

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const res = await api.files();
      setFiles(res.files);
      setRoot(res.root);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const groups = useMemo(() => {
    const m = new Map<string, number>();
    (files ?? []).forEach((f) => {
      const g = describe(f.folder).group;
      m.set(g, (m.get(g) ?? 0) + 1);
    });
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [files]);

  const originals = (files ?? []).filter((f) => describe(f.folder).original).length;

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (files ?? []).filter((f) => {
      const d = describe(f.folder);
      if (!showOriginal && d.original) return false;
      if (group !== "all" && d.group !== group) return false;
      return !q || f.name.toLowerCase().includes(q) || f.folder.toLowerCase().includes(q);
    });
  }, [files, query, group, showOriginal]);

  const total = visible.reduce((s, f) => s + f.size, 0);

  async function confirmDelete() {
    if (!deleting) return;
    try {
      await api.deleteFile(deleting.path);
      setFiles((fs) => fs?.filter((f) => f.path !== deleting.path) ?? null);
      toast.success("已删除文件");
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <div className="mx-auto w-full max-w-[1480px] px-5 pt-7 md:px-8 pb-16">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-[22px] font-semibold tracking-[-0.02em]">录像</h1>
          <p className="mt-1 truncate font-mono text-[12px] text-fg-3" title={root}>
            {root}
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="ghost" icon={<RefreshCw className={cx("size-3.5", loading && "animate-spin")} />} onClick={load}>
            刷新
          </Button>
          <Button icon={<FolderOpen className="size-3.5" />} onClick={() => api.revealFile()}>
            打开录像文件夹
          </Button>
        </div>
      </header>

      <div className="mt-6 flex flex-wrap items-center gap-3">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-fg-3" />
          <Input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索文件名" className="w-64 pl-8" />
        </div>
        <Select
          value={group}
          onChange={setGroup}
          options={[{ value: "all", label: `全部主播（${groups.length}）` }, ...groups.map(([g, n]) => ({ value: g, label: `${g}（${n}）` }))]}
        />
        {originals > 0 && (
          <label className="flex cursor-pointer items-center gap-2 text-[12.5px] text-fg-2">
            <input type="checkbox" checked={showOriginal} onChange={(e) => setShowOriginal(e.target.checked)} className="accent-[var(--fg)]" />
            显示转码前的原始文件（{originals}）
          </label>
        )}
        <span className="ml-auto text-[12.5px] text-fg-3">
          <span className="font-mono tabular">{visible.length}</span> 个文件 · <span className="font-mono tabular">{bytes(total)}</span>
        </span>
      </div>

      <div className="mt-4 overflow-hidden rounded-xl border border-line bg-surface">
        <div className="grid grid-cols-[minmax(0,1fr)_80px_96px] md:grid-cols-[minmax(0,1fr)_200px_90px_110px_112px] gap-4 border-b border-line px-4 py-2 text-[11.5px] font-medium text-fg-3">
          <span>文件</span>
          <span className="max-md:hidden">主播</span>
          <span className="text-right">大小</span>
          <span className="max-md:hidden">时间</span>
          <span />
        </div>
        {files === null ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="h-9 animate-pulse rounded-lg bg-surface-2" />
            ))}
          </div>
        ) : visible.length === 0 ? (
          <div className="py-16 text-center text-[13px] text-fg-3">{files.length ? "没有符合条件的文件" : "还没有录像，开始录制后会出现在这里"}</div>
        ) : (
          visible.map((f) => {
            const d = describe(f.folder);
            const Icon = f.kind === "audio" ? FileAudio : FileVideo;
            return (
              <div
                key={f.path}
                onDoubleClick={() => setPlaying(f)}
                className="group grid grid-cols-[minmax(0,1fr)_80px_96px] md:grid-cols-[minmax(0,1fr)_200px_90px_110px_112px] items-center gap-4 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0 hover:bg-surface-2/60"
              >
                <div className="flex min-w-0 items-center gap-3">
                  <button
                    onClick={() => setPlaying(f)}
                    aria-label={`播放 ${f.name}`}
                    className="relative flex size-8 shrink-0 items-center justify-center rounded-lg border border-line bg-surface-2 text-fg-3 transition group-hover:border-transparent group-hover:bg-primary group-hover:text-primary-fg"
                  >
                    <Icon className="size-3.5 group-hover:hidden" />
                    <Play className="hidden size-3.5 group-hover:block" fill="currentColor" />
                  </button>
                  <span className="truncate font-medium text-fg" title={f.name}>
                    {f.name}
                  </span>
                  <span className="shrink-0 rounded border border-line px-1 font-mono text-[10.5px] text-fg-3 uppercase">{f.ext}</span>
                  {d.original && <span className="shrink-0 rounded bg-surface-3 px-1.5 text-[10.5px] text-fg-3">原始</span>}
                  {isActive(f) && (
                    <span className="inline-flex shrink-0 items-center gap-1 rounded bg-rec/12 px-1.5 text-[10.5px] font-medium text-rec">
                      <span className="size-1.5 rounded-full bg-rec animate-rec" />
                      录制中
                    </span>
                  )}
                </div>
                <span className="truncate text-fg-2 max-md:hidden" title={f.folder}>
                  {d.group}
                  {d.platform && <span className="text-fg-3"> · {d.platform.replace(/直播$/, "")}</span>}
                </span>
                <span className="text-right font-mono tabular text-fg-2">{bytes(f.size)}</span>
                <span className="text-fg-3 max-md:hidden" title={new Date(f.mtime * 1000).toLocaleString()}>
                  {ago(f.mtime)}
                </span>
                <div className="flex justify-end gap-0.5 opacity-0 transition group-hover:opacity-100 focus-within:opacity-100">
                  <IconButton label="播放" onClick={() => setPlaying(f)}>
                    <Play className="size-3.5" />
                  </IconButton>
                  <IconButton label="在文件夹中显示" onClick={() => api.revealFile(f.path)}>
                    <FolderOpen className="size-3.5" />
                  </IconButton>
                  <IconButton label={isActive(f) ? "正在录制，不能删除" : "删除"} tone="danger" disabled={isActive(f)} onClick={() => setDeleting(f)}>
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
              </div>
            );
          })
        )}
      </div>

      <PlayerDialog file={playing} onClose={() => setPlaying(null)} />
      <Confirm
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="删除这个文件？"
        body={
          <>
            <span className="font-mono text-[12px] break-all text-fg">{deleting?.name}</span>
            <span className="mt-2 block">文件会被直接删除，不经过废纸篓，无法恢复。</span>
          </>
        }
        confirmText="永久删除"
        danger
        onConfirm={confirmDelete}
      />
    </div>
  );
}
