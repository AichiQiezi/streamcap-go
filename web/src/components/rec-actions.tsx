import {
  CircleStop,
  Copy,
  ExternalLink,
  FolderOpen,
  MoreHorizontal,
  Pencil,
  Play,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { useStore } from "@/lib/store";
import type { Rec } from "@/lib/types";
import { IconButton, Menu, MenuContent, MenuItem, MenuSep, MenuTrigger } from "./ui";

async function run<T>(p: Promise<T>, ok?: string): Promise<T | undefined> {
  try {
    const res = await p;
    if (ok) toast.success(ok);
    return res;
  } catch (e) {
    toast.error((e as Error).message);
  }
}

export const recActions = {
  setMonitor: async (r: Rec, on: boolean) => {
    const res = await run(api.monitor(r.id, on), on ? `已开始监控「${r.name}」` : `已暂停「${r.name}」`);
    if (res) useStore.getState().upsert(res);
  },
  check: (r: Rec) => run(api.check(r.id), "正在检测直播状态…"),
  openFolder: (r: Rec) => run(api.openFolder(r.id)),
  stopRecording: async (r: Rec) => {
    const res = await run(api.record(r.id, false), "已停止本次录制，下次检测到开播会继续录");
    if (res) useStore.getState().upsert(res);
  },
  startRecording: async (r: Rec) => {
    const res = await run(api.record(r.id, true));
    if (res) useStore.getState().upsert(res);
  },
  copyLink: (r: Rec) => navigator.clipboard.writeText(r.url).then(() => toast.success("已复制直播间链接")),
};

export function RecMenu({ r, onEdit, onDelete }: { r: Rec; onEdit: () => void; onDelete: () => void }) {
  return (
    <div className="flex items-center gap-0.5">
      <IconButton label="立即检测" onClick={() => recActions.check(r)} disabled={!r.monitor || r.state === "checking"}>
        <RefreshCw className="size-3.5" />
      </IconButton>
      <IconButton label="打开文件夹" onClick={() => recActions.openFolder(r)}>
        <FolderOpen className="size-3.5" />
      </IconButton>
      <Menu>
        <MenuTrigger asChild>
          <button
            aria-label="更多操作"
            className="inline-flex size-7.5 items-center justify-center rounded-md text-fg-3 transition hover:bg-surface-2 hover:text-fg data-[state=open]:bg-surface-2 data-[state=open]:text-fg"
          >
            <MoreHorizontal className="size-4" />
          </button>
        </MenuTrigger>
        <MenuContent>
          <MenuItem icon={<Pencil />} onSelect={onEdit}>
            编辑
          </MenuItem>
          {r.is_recording ? (
            <MenuItem icon={<CircleStop />} onSelect={() => recActions.stopRecording(r)}>
              停止本次录制
            </MenuItem>
          ) : (
            <MenuItem icon={<Play />} onSelect={() => recActions.startRecording(r)} disabled={r.state === "checking"}>
              {r.monitor ? "立即尝试录制" : "开始监控并录制"}
            </MenuItem>
          )}
          <MenuSep />
          <MenuItem icon={<ExternalLink />} onSelect={() => window.open(r.url, "_blank", "noopener")}>
            打开直播间
          </MenuItem>
          <MenuItem icon={<Copy />} onSelect={() => recActions.copyLink(r)}>
            复制链接
          </MenuItem>
          <MenuSep />
          <MenuItem icon={<Trash2 />} danger onSelect={onDelete}>
            删除
          </MenuItem>
        </MenuContent>
      </Menu>
    </div>
  );
}
