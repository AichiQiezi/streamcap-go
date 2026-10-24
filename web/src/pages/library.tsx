import { ArrowDown, ArrowUp, FileAudio, FileVideo, Film, FolderOpen, Play, RefreshCw, Search, Trash2, Upload } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { UploadDialog } from "@/components/bili";
import { PlayerDialog } from "@/components/player";
import { Button, Confirm, cx, IconButton, Input, Select, Tip } from "@/components/ui";
import { api } from "@/lib/api";
import { ago, bytes } from "@/lib/format";
import { platformColor, platformName } from "@/lib/platforms";
import { useStore } from "@/lib/store";
import type { MediaFile } from "@/lib/types";

/** Top-level folder names the backend uses with "按平台建文件夹" (display names, or keys with 英文路径命名). */
const PLATFORM_DIRS: Record<string, string> = { 抖音: "douyin", douyin: "douyin", B站: "bilibili", bilibili: "bilibili" };

/** Recordings are named `主播_2026-10-14_19-58-02[_标题][_p2][_001].ext` (internal/monitor/paths.go). */
const NAME_RE = /^(.+?)_(\d{4}-\d{2}-\d{2})_(\d{2})-(\d{2})-(\d{2})(.*)$/;

interface Desc {
  streamer: string;
  platformKey: string | null;
  original: boolean;
  /** start time (+ live title) parsed from the file name, or the bare name when it doesn't match */
  label: string;
  title: string | null;
  /** "p2" for a part split off by a reconnect, "#001" for a timed segment */
  tags: string[];
}

function describe(f: MediaFile): Desc {
  const parts = f.folder.split("/").filter(Boolean);
  const original = parts[parts.length - 1] === "original";
  const platformKey = PLATFORM_DIRS[parts[0]] ?? null;
  const stem = f.ext ? f.name.slice(0, -(f.ext.length + 1)) : f.name;
  const m = NAME_RE.exec(stem);
  if (!m) {
    // Not one of ours: fall back to the folder layout (platform / streamer / …).
    const core = parts.slice(platformKey ? 1 : 0, original ? -1 : undefined);
    return { streamer: core[0] || "未分组", platformKey, original, label: stem, title: null, tags: [] };
  }
  let rest = m[6];
  const tags: string[] = [];
  const seg = /_(\d{3})$/.exec(rest);
  if (seg) {
    tags.unshift(`#${seg[1]}`);
    rest = rest.slice(0, seg.index);
  }
  const part = /_p(\d+)$/.exec(rest);
  if (part) {
    tags.unshift(`p${part[1]}`);
    rest = rest.slice(0, part.index);
  }
  return {
    streamer: m[1],
    platformKey,
    original,
    label: `${m[2]} ${m[3]}:${m[4]}:${m[5]}`,
    title: rest.replace(/^_/, "") || null,
    tags,
  };
}

type SortKey = "time" | "size" | "name";

function SortHead({
  k,
  sort,
  onSort,
  children,
  className,
}: {
  k: SortKey;
  sort: { key: SortKey; desc: boolean };
  onSort: (k: SortKey) => void;
  children: React.ReactNode;
  className?: string;
}) {
  const active = sort.key === k;
  const Arrow = sort.desc ? ArrowDown : ArrowUp;
  return (
    <button
      type="button"
      onClick={() => onSort(k)}
      aria-sort={active ? (sort.desc ? "descending" : "ascending") : undefined}
      className={cx("inline-flex items-center gap-1 transition hover:text-fg", active && "text-fg-2", className)}
    >
      {children}
      <Arrow className={cx("size-3", active ? "opacity-100" : "opacity-0")} />
    </button>
  );
}

export function LibraryPage() {
  const [files, setFiles] = useState<MediaFile[] | null>(null);
  const [root, setRoot] = useState("");
  const [query, setQuery] = useState("");
  const [group, setGroup] = useState("all");
  const [showOriginal, setShowOriginal] = useState(false);
  const [playing, setPlaying] = useState<MediaFile | null>(null);
  const [deleting, setDeleting] = useState<MediaFile | null>(null);
  const [uploading, setUploading] = useState<MediaFile | null>(null);
  const [loading, setLoading] = useState(false);
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: "time", desc: true });
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

  const described = useMemo(() => new Map((files ?? []).map((f) => [f.path, describe(f)])), [files]);
  const desc = (f: MediaFile) => described.get(f.path)!;

  const groups = useMemo(() => {
    const m = new Map<string, number>();
    (files ?? []).forEach((f) => {
      const g = described.get(f.path)!.streamer;
      m.set(g, (m.get(g) ?? 0) + 1);
    });
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [files, described]);

  const originals = (files ?? []).filter((f) => described.get(f.path)?.original).length;

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = (files ?? []).filter((f) => {
      const d = described.get(f.path)!;
      if (!showOriginal && d.original) return false;
      if (group !== "all" && d.streamer !== group) return false;
      return !q || f.name.toLowerCase().includes(q) || f.folder.toLowerCase().includes(q);
    });
    const by: Record<SortKey, (a: MediaFile, b: MediaFile) => number> = {
      time: (a, b) => a.mtime - b.mtime,
      size: (a, b) => a.size - b.size,
      name: (a, b) => a.name.localeCompare(b.name, "zh-CN"),
    };
    return list.sort((a, b) => (sort.desc ? -1 : 1) * by[sort.key](a, b));
  }, [files, described, query, group, showOriginal, sort]);

  const onSort = (key: SortKey) =>
    setSort((s) => (s.key === key ? { key, desc: !s.desc } : { key, desc: key !== "name" }));

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
        <div className="grid grid-cols-[minmax(0,1fr)_80px_128px] md:grid-cols-[minmax(0,1fr)_200px_90px_110px_144px] gap-4 border-b border-line px-4 py-2 text-[11.5px] font-medium text-fg-3">
          <SortHead k="name" sort={sort} onSort={onSort}>
            文件
          </SortHead>
          <span className="max-md:hidden">主播</span>
          <SortHead k="size" sort={sort} onSort={onSort} className="flex-row-reverse justify-self-end">
            大小
          </SortHead>
          <SortHead k="time" sort={sort} onSort={onSort} className="max-md:hidden">
            修改时间
          </SortHead>
          <span />
        </div>
        {files === null ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="h-9 animate-pulse rounded-lg bg-surface-2" />
            ))}
          </div>
        ) : visible.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-16 text-center">
            <span className="flex size-11 items-center justify-center rounded-xl border border-line bg-surface-2 text-fg-3">
              {files.length ? <Search className="size-4.5" /> : <Film className="size-4.5" />}
            </span>
            <div>
              <div className="text-[13.5px] font-medium text-fg">{files.length ? "没有符合条件的文件" : "还没有录像"}</div>
              <div className="mt-1 text-[12.5px] text-fg-3">{files.length ? "换个关键词或主播试试" : "开始录制后，录好的文件会出现在这里"}</div>
            </div>
          </div>
        ) : (
          visible.map((f) => {
            const d = desc(f);
            const Icon = f.kind === "audio" ? FileAudio : FileVideo;
            return (
              <div
                key={f.path}
                onDoubleClick={() => setPlaying(f)}
                className="group grid grid-cols-[minmax(0,1fr)_80px_128px] md:grid-cols-[minmax(0,1fr)_200px_90px_110px_144px] items-center gap-4 border-b border-line px-4 py-2 text-[12.5px] last:border-b-0 hover:bg-surface-2/60"
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
                  <span className="min-w-0 truncate" title={f.name}>
                    <span className="font-mono font-medium text-fg tabular">{d.label}</span>
                    {d.title && <span className="text-fg-2"> · {d.title}</span>}
                  </span>
                  <span className="shrink-0 rounded border border-line px-1 font-mono text-[10.5px] text-fg-3 uppercase">{f.ext}</span>
                  {f.danmaku && (
                    <Tip label="有同名 .ass 弹幕文件：用 IINA、mpv、PotPlayer、VLC 播放会显示弹幕（应用内播放器不显示）">
                      <span className="shrink-0 rounded bg-live/12 px-1.5 text-[10.5px] font-medium text-live">弹幕</span>
                    </Tip>
                  )}
                  {d.tags.map((t) => (
                    <span key={t} className="shrink-0 rounded bg-surface-3 px-1 font-mono text-[10.5px] text-fg-2" title={t.startsWith("p") ? "断流重连后的续录部分" : "分段录制的分段"}>
                      {t}
                    </span>
                  ))}
                  {d.original && <span className="shrink-0 rounded bg-surface-3 px-1.5 text-[10.5px] text-fg-2">原始</span>}
                  {isActive(f) && (
                    <span className="inline-flex shrink-0 items-center gap-1 rounded bg-rec/12 px-1.5 text-[10.5px] font-medium text-rec">
                      <span className="size-1.5 rounded-full bg-rec animate-rec" />
                      录制中
                    </span>
                  )}
                </div>
                <span className="flex min-w-0 items-center gap-1.5 text-fg-2 max-md:hidden" title={f.folder}>
                  {d.platformKey && <span className="size-1.5 shrink-0 rounded-full" style={{ background: platformColor(d.platformKey) }} />}
                  <span className="truncate">{d.streamer}</span>
                  {d.platformKey && <span className="shrink-0 text-fg-3">{platformName(d.platformKey)}</span>}
                </span>
                <span className="text-right font-mono tabular text-fg-2">{bytes(f.size)}</span>
                <span className="text-fg-3 tabular max-md:hidden" title={new Date(f.mtime * 1000).toLocaleString()}>
                  {ago(f.mtime)}
                </span>
                <div className="flex justify-end gap-0.5 opacity-0 transition group-hover:opacity-100 focus-within:opacity-100">
                  <IconButton label="播放" onClick={() => setPlaying(f)}>
                    <Play className="size-3.5" />
                  </IconButton>
                  {f.kind === "video" && (
                    <IconButton label={isActive(f) ? "正在录制，录完后才能投稿" : "投稿到 B站"} disabled={isActive(f)} onClick={() => setUploading(f)}>
                      <Upload className="size-3.5" />
                    </IconButton>
                  )}
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
      <UploadDialog file={uploading} onClose={() => setUploading(null)} />
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
