import { CircleAlert, Download, FolderOpen } from "lucide-react";
import { useEffect, useState } from "react";
import { api, fileUrl } from "@/lib/api";
import { bytes } from "@/lib/format";
import type { MediaFile } from "@/lib/types";
import { Button, buttonClass, Dialog } from "./ui";

const NATIVE = new Set(["mp4", "mov", "m4a", "mp3", "aac", "wav", "mkv"]);

export function PlayerDialog({ file, onClose }: { file: MediaFile | null; onClose: () => void }) {
  // Callback ref: the dialog content mounts in a portal after `file` is set, so a plain ref is
  // still null when the effect first runs.
  const [el, setEl] = useState<HTMLMediaElement | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    setError(null);
    if (!file || !el) return;
    const url = fileUrl(file.path);
    if (NATIVE.has(file.ext)) {
      el.src = url;
      el.play().catch(() => undefined);
      return () => {
        el.removeAttribute("src");
        el.load();
      };
    }
    // TS / FLV: browsers can't play these containers directly; mpegts.js remuxes them to MSE.
    let player: { destroy: () => void } | null = null;
    let cancelled = false;
    import("mpegts.js").then(({ default: mpegts }) => {
      if (cancelled) return;
      if (!mpegts.isSupported()) {
        setError("当前浏览器不支持播放该格式");
        return;
      }
      // The loader runs in a Web Worker, which can't resolve relative URLs.
      const abs = new URL(url, location.href).href;
      const p = mpegts.createPlayer({ type: file.ext === "flv" ? "flv" : "mpegts", url: abs, isLive: false }, { enableWorker: true });
      p.attachMediaElement(el);
      p.load();
      p.on(mpegts.Events.ERROR, () => setError("播放失败：文件可能仍在写入或已损坏"));
      Promise.resolve(p.play()).catch(() => undefined);
      player = p;
    });
    return () => {
      cancelled = true;
      player?.destroy();
    };
  }, [file, el]);

  return (
    <Dialog
      open={!!file}
      onOpenChange={(o) => !o && onClose()}
      title={<span className="block max-w-[760px] truncate">{file?.name}</span>}
      description={
        file
          ? `${file.folder || "根目录"} · ${bytes(file.size)} · ${file.ext.toUpperCase()}` +
            (file.danmaku ? " · 有弹幕文件，这里不显示，用 IINA / mpv / PotPlayer 打开可看弹幕" : "")
          : undefined
      }
      width={1000}
      footer={
        file && (
          <>
            <Button variant="ghost" icon={<FolderOpen className="size-3.5" />} onClick={() => api.revealFile(file.path)}>
              在文件夹中显示
            </Button>
            <a href={fileUrl(file.path)} download={file.name} className={buttonClass("secondary")}>
              <Download className="size-3.5" />
              下载
            </a>
          </>
        )
      }
    >
      <div className="relative overflow-hidden rounded-xl bg-black">
        {file?.kind === "audio" ? (
          <audio ref={setEl} controls className="w-full" />
        ) : (
          <video ref={setEl} controls playsInline className="aspect-video max-h-[62vh] w-full bg-black" />
        )}
        {error && (
          // Covers the native controls too: the browser keeps spinning on a stream it can't decode.
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/80 px-6 text-center text-white backdrop-blur-sm">
            <CircleAlert className="size-6 text-white/70" />
            <div className="text-[13.5px] font-medium">{error}</div>
            <div className="max-w-sm text-[12px] leading-relaxed text-white/60">
              正在录制的文件要等录完才能完整播放；也可以点下方「在文件夹中显示」用本地播放器打开。
            </div>
          </div>
        )}
      </div>
    </Dialog>
  );
}
