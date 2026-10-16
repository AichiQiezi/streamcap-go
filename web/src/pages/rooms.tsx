import { LayoutGrid, List, Pause, Play, Plus, RefreshCw, Search, Trash2, X } from "lucide-react";
import { closestCenter, DndContext, type DragEndEvent, PointerSensor, useSensor, useSensors } from "@dnd-kit/core";
import { arrayMove, rectSortingStrategy, SortableContext, useSortable, verticalListSortingStrategy } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { RecCard } from "@/components/rec-card";
import { RecFormDialog } from "@/components/rec-form";
import { RecRow, RowHeader } from "@/components/rec-row";
import { Button, Confirm, cx, Input, Kbd, Segmented, Select } from "@/components/ui";
import { api } from "@/lib/api";
import { bytes } from "@/lib/format";
import { platformName } from "@/lib/platforms";
import { useStore } from "@/lib/store";
import type { Rec } from "@/lib/types";

type Filter = "all" | "recording" | "live" | "offline" | "paused" | "error" | "notify";

const FILTERS: { value: Filter; label: string; match: (r: Rec) => boolean }[] = [
  { value: "all", label: "全部", match: () => true },
  { value: "recording", label: "录制中", match: (r) => r.state === "recording" || r.state === "reconnecting" },
  { value: "live", label: "直播中", match: (r) => r.state === "live" },
  { value: "offline", label: "未开播", match: (r) => ["offline", "checking", "scheduled"].includes(r.state) },
  { value: "paused", label: "已暂停", match: (r) => r.state === "paused" },
  { value: "error", label: "异常", match: (r) => r.state === "error" },
  { value: "notify", label: "仅提醒", match: (r) => r.only_notify_no_record },
];

type SortMode = "custom" | "status" | "name";

const SORT_OPTIONS: { value: SortMode; label: string }[] = [
  { value: "custom", label: "自定义顺序" },
  { value: "status", label: "直播中优先" },
  { value: "name", label: "按主播名" },
];

/** 直播中优先: recording first, then live, then problems, then the rest — each group in saved order. */
function statusRank(r: Rec): number {
  if (r.state === "recording" || r.state === "reconnecting") return 0;
  if (r.state === "live") return 1;
  if (r.state === "error") return 2;
  return 3;
}

/** A card or row that can be dragged to a new place (only while the sort is 自定义顺序). */
function SortableItem({ id, enabled, children }: { id: string; enabled: boolean; children: React.ReactNode }) {
  const { listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id, disabled: !enabled });
  return (
    <div
      ref={setNodeRef}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      className={cx(enabled && "cursor-grab touch-none", isDragging && "relative z-20 cursor-grabbing opacity-90 [&>*]:shadow-2xl")}
      {...listeners}
    >
      {children}
    </div>
  );
}

function readPref<T extends string>(key: string, fallback: T): T {
  try {
    return (localStorage.getItem(key) as T) || fallback;
  } catch {
    return fallback;
  }
}

function Stat({ label, value, sub, accent }: { label: string; value: React.ReactNode; sub?: React.ReactNode; accent?: string }) {
  return (
    <div className="rounded-xl border border-line bg-surface px-4 py-3">
      <div className="text-[12px] text-fg-3">{label}</div>
      <div className="mt-1 flex items-baseline gap-2">
        <span className="font-mono text-[22px] font-medium tracking-tight tabular" style={accent ? { color: accent } : undefined}>
          {value}
        </span>
        {sub && <span className="text-[12px] text-fg-3">{sub}</span>}
      </div>
    </div>
  );
}

export function RoomsPage() {
  const recs = useStore((s) => s.recs);
  const order = useStore((s) => s.order);
  const disk = useStore((s) => s.disk);
  const loaded = useStore((s) => s.loaded);
  const recordingEnabled = useStore((s) => s.recordingEnabled);

  const [filter, setFilter] = useState<Filter>("all");
  const [platform, setPlatform] = useState("all");
  const [query, setQuery] = useState("");
  const [view, setView] = useState<"grid" | "list">(() => readPref("sc-view", "grid"));
  const [sortMode, setSortMode] = useState<SortMode>(() => readPref("sc-sort", "custom"));
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<Rec | null>(null);
  const [deleting, setDeleting] = useState<Rec[] | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const all = useMemo(() => order.map((id) => recs[id]).filter(Boolean), [order, recs]);

  useEffect(() => {
    try {
      localStorage.setItem("sc-view", view);
      localStorage.setItem("sc-sort", sortMode);
    } catch {
      /* storage unavailable */
    }
  }, [view, sortMode]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target;
      if (t instanceof Element && t.closest("input,textarea,select,[role=dialog]")) return;
      if (e.key === "n" && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        setEditing(null);
        setFormOpen(true);
      } else if (e.key === "/") {
        e.preventDefault();
        searchRef.current?.focus();
      } else if (e.key === "Escape") setSelected(new Set());
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const counts = useMemo(() => Object.fromEntries(FILTERS.map((f) => [f.value, all.filter(f.match).length])), [all]);
  const platforms = useMemo(() => {
    const m = new Map<string, string>();
    all.forEach((r) => r.platform_key && m.set(r.platform_key, platformName(r.platform_key, r.platform)));
    return [...m.entries()];
  }, [all]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    const match = FILTERS.find((f) => f.value === filter)!.match;
    const list = all
      .filter(match)
      .filter((r) => platform === "all" || r.platform_key === platform)
      .filter((r) => !q || [r.name, r.live_title, r.url].some((s) => s?.toLowerCase().includes(q)));
    // `all` is in the saved order; the other modes sort stably on top of it.
    if (sortMode === "status") list.sort((a, b) => statusRank(a) - statusRank(b));
    else if (sortMode === "name") list.sort((a, b) => a.name.localeCompare(b.name, "zh-CN"));
    return list;
  }, [all, filter, platform, query, sortMode]);

  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 6 } }));
  const draggable = sortMode === "custom";

  // Moves the dragged room among the visible ones; rooms hidden by a filter keep their places.
  function onDragEnd({ active, over }: DragEndEvent) {
    if (!over || active.id === over.id) return;
    const ids = visible.map((r) => r.id);
    const moved = arrayMove(ids, ids.indexOf(String(active.id)), ids.indexOf(String(over.id)));
    const shown = new Set(ids);
    let k = 0;
    const prev = order;
    const next = order.map((id) => (shown.has(id) ? moved[k++] : id));
    useStore.setState({ order: next });
    api.reorder(next).catch((e: Error) => {
      useStore.setState({ order: prev });
      toast.error(`顺序没保存：${e.message}`);
    });
  }

  const recording = all.filter((r) => r.state === "recording");
  const totalSpeed = recording.reduce((s, r) => s + r.stats.speed_bps, 0);
  const monitoring = all.filter((r) => r.monitor).length;

  const toggle = useCallback((id: string) => {
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  }, []);
  const onEdit = useCallback((r: Rec) => {
    setEditing(r);
    setFormOpen(true);
  }, []);
  const onDelete = useCallback((r: Rec) => setDeleting([r]), []);

  async function batch(action: "start" | "stop" | "check") {
    const ids = [...selected];
    try {
      await api.batch(ids, action);
      toast.success({ start: "已开始监控", stop: "已暂停", check: "正在检测" }[action] + ` ${ids.length} 个直播间`);
      setSelected(new Set());
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function confirmDelete() {
    if (!deleting) return;
    try {
      await api.batch(
        deleting.map((r) => r.id),
        "delete",
      );
      useStore.getState().drop(deleting.map((r) => r.id));
      setSelected(new Set());
      toast.success(`已删除 ${deleting.length} 个直播间`);
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  const ItemComp = view === "grid" ? RecCard : RecRow;

  return (
    <div className="mx-auto w-full max-w-[1480px] px-5 pt-7 md:px-8 pb-28">
      <header className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-[22px] font-semibold tracking-[-0.02em]">直播间</h1>
          <p className="mt-1 text-[13px] text-fg-3">开播自动录制，原画直存不转码；也可以只提醒不录制</p>
        </div>
        <Button
          variant="primary"
          icon={<Plus className="size-4" />}
          onClick={() => {
            setEditing(null);
            setFormOpen(true);
          }}
        >
          添加直播间
          <Kbd inverted>N</Kbd>
        </Button>
      </header>

      {!recordingEnabled && (
        <div className="mt-5 rounded-xl border border-err/30 bg-err/8 px-4 py-3 text-[13px] text-err">
          磁盘可用空间低于设置的阈值，新的录制已暂停。清理空间或在设置中调低阈值后会自动恢复。
        </div>
      )}

      <section className="mt-6 grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat label="正在录制" value={recording.length} sub={`/ ${all.length} 个直播间`} accent={recording.length ? "var(--rec)" : undefined} />
        <Stat label="总下载速度" value={totalSpeed ? bytes(totalSpeed).split(" ")[0] : "0"} sub={`${totalSpeed ? bytes(totalSpeed).split(" ")[1] : "B"}/s`} />
        <Stat label="监控中" value={monitoring} sub={`${all.filter((r) => r.state === "live").length} 个开播未录`} />
        <Stat
          label="可用空间"
          value={disk ? bytes(disk.free, 1).split(" ")[0] : "—"}
          sub={disk ? `${bytes(disk.free).split(" ")[1]} · 共 ${bytes(disk.total, 0)}` : undefined}
          accent={disk && disk.free < 20 * 1024 ** 3 ? "var(--warn)" : undefined}
        />
      </section>

      <div className="sticky top-0 z-10 -mx-5 mt-6 flex flex-wrap items-center gap-3 bg-bg/85 px-5 py-3 backdrop-blur-md md:-mx-8 md:px-8">
        <Segmented
          value={filter}
          onChange={setFilter}
          options={FILTERS.filter((f) => f.value !== "notify" || counts.notify > 0 || filter === "notify").map((f) => ({
            value: f.value,
            label: (
              <>
                {f.label}
                <span className={cx("font-mono text-[11px] tabular", filter === f.value ? "text-fg-3" : "text-fg-3/70")}>{counts[f.value]}</span>
              </>
            ),
          }))}
        />
        <div className="ml-auto flex flex-wrap items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-fg-3" />
            <Input ref={searchRef} value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索主播、标题、链接" className="w-52 pl-8 xl:w-60" />
            {!query && <span className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2"><Kbd>/</Kbd></span>}
          </div>
          {platforms.length > 1 && (
            <Select
              value={platform}
              onChange={setPlatform}
              options={[{ value: "all", label: "全部平台" }, ...platforms.map(([value, label]) => ({ value, label }))]}
            />
          )}
          <Select
            value={sortMode}
            onChange={(v) => setSortMode(v as SortMode)}
            options={SORT_OPTIONS}
            title={draggable ? "拖动卡片调整顺序，会一直保持" : undefined}
          />
          <Segmented
            value={view}
            onChange={setView}
            options={[
              { value: "grid", label: <LayoutGrid className="size-3.5" />, title: "卡片视图" },
              { value: "list", label: <List className="size-3.5" />, title: "列表视图" },
            ]}
          />
        </div>
      </div>

      <section className="mt-2">
        {!loaded ? (
          <div className="grid grid-cols-[repeat(auto-fill,minmax(290px,1fr))] gap-4">
            {Array.from({ length: 4 }).map((_, i) => (
              <div key={i} className="h-[300px] animate-pulse rounded-xl border border-line bg-surface" />
            ))}
          </div>
        ) : all.length === 0 ? (
          <EmptyRooms onAdd={() => setFormOpen(true)} />
        ) : visible.length === 0 ? (
          <div className="rounded-xl border border-dashed border-line py-16 text-center text-[13px] text-fg-3">没有符合条件的直播间</div>
        ) : view === "grid" ? (
          <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
            <SortableContext items={visible.map((r) => r.id)} strategy={rectSortingStrategy}>
              <div className="grid grid-cols-[repeat(auto-fill,minmax(290px,1fr))] gap-4">
                {visible.map((r) => (
                  <SortableItem key={r.id} id={r.id} enabled={draggable}>
                    <ItemComp r={r} selected={selected.has(r.id)} onSelect={toggle} onEdit={onEdit} onDelete={onDelete} />
                  </SortableItem>
                ))}
              </div>
            </SortableContext>
          </DndContext>
        ) : (
          <div className="@container overflow-x-auto rounded-xl border border-line bg-surface">
            <div className="min-w-[720px]">
              <RowHeader />
              <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={onDragEnd}>
                <SortableContext items={visible.map((r) => r.id)} strategy={verticalListSortingStrategy}>
                  {visible.map((r) => (
                    <SortableItem key={r.id} id={r.id} enabled={draggable}>
                      <ItemComp r={r} selected={selected.has(r.id)} onSelect={toggle} onEdit={onEdit} onDelete={onDelete} />
                    </SortableItem>
                  ))}
                </SortableContext>
              </DndContext>
            </div>
          </div>
        )}
      </section>

      {selected.size > 0 && (
        <div className="fixed bottom-6 left-1/2 z-30 flex max-w-[calc(100vw-96px)] -translate-x-1/2 items-center gap-1 overflow-x-auto rounded-2xl border border-line bg-surface p-1.5 pl-4 whitespace-nowrap shadow-2xl animate-fade">
          <span className="mr-2 text-[13px] font-medium">
            已选 <span className="font-mono tabular">{selected.size}</span> 项
          </span>
          <button className="mr-1 text-[12.5px] text-fg-3 hover:text-fg" onClick={() => setSelected(new Set(visible.map((r) => r.id)))}>
            全选
          </button>
          <span className="mx-1 h-5 w-px bg-line" />
          <Button size="sm" variant="ghost" icon={<Play className="size-3.5" />} onClick={() => batch("start")}>
            开始监控
          </Button>
          <Button size="sm" variant="ghost" icon={<Pause className="size-3.5" />} onClick={() => batch("stop")}>
            暂停
          </Button>
          <Button size="sm" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={() => batch("check")}>
            检测
          </Button>
          <Button size="sm" variant="ghost" className="text-err hover:text-err" icon={<Trash2 className="size-3.5" />} onClick={() => setDeleting(all.filter((r) => selected.has(r.id)))}>
            删除
          </Button>
          <button aria-label="取消选择" className="ml-1 rounded-lg p-1.5 text-fg-3 hover:bg-surface-2 hover:text-fg" onClick={() => setSelected(new Set())}>
            <X className="size-4" />
          </button>
        </div>
      )}

      <RecFormDialog open={formOpen} onOpenChange={setFormOpen} editing={editing} />
      <Confirm
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={deleting && deleting.length > 1 ? `删除 ${deleting.length} 个直播间？` : `删除「${deleting?.[0]?.name}」？`}
        body={
          <>
            会先停止监控和正在进行的录制，再从列表中移除。
            <span className="mt-2 block text-fg-3">已经录好的视频文件不会被删除。</span>
          </>
        }
        confirmText="删除"
        danger
        onConfirm={confirmDelete}
      />
    </div>
  );
}

function EmptyRooms({ onAdd }: { onAdd: () => void }) {
  return (
    <div className="flex flex-col items-center rounded-2xl border border-dashed border-line-strong px-6 py-20 text-center">
      <div className="relative mb-6 flex size-14 items-center justify-center rounded-2xl border border-line bg-surface shadow-card">
        <span className="size-4 rounded-full bg-rec shadow-[0_0_0_6px_color-mix(in_srgb,var(--rec)_18%,transparent)]" />
      </div>
      <h2 className="text-[16px] font-semibold">还没有直播间</h2>
      <p className="mt-1.5 max-w-sm text-[13px] leading-relaxed text-fg-3">
        支持抖音和 B 站。添加后开播会自动开始录制，断流会自动重连，同一场直播结束后合并成一个文件。也可以设为「仅开播提醒」，只提醒不录制。
      </p>
      <Button variant="primary" className="mt-6" icon={<Plus className="size-4" />} onClick={onAdd}>
        添加第一个直播间
      </Button>
    </div>
  );
}
