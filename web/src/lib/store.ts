import { toast } from "sonner";
import { create } from "zustand";
import { api } from "./api";
import type { DiskInfo, OptionsInfo, Rec, RecStats } from "./types";

export type ConnState = "open" | "connecting" | "closed";

const EMPTY_STATS: RecStats = {
  file: null,
  size_bytes: 0,
  speed_bps: 0,
  width: null,
  height: null,
  fps: null,
  vcodec: null,
  thumb_version: 0,
  danmaku: null,
};

const EMPTY_OPTIONS: OptionsInfo = { qualities: [], video_formats: [], audio_formats: [] };

function normalize(r: Rec): Rec {
  return { ...r, stats: { ...EMPTY_STATS, ...(r.stats ?? {}) } };
}

interface Store {
  recs: Record<string, Rec>;
  order: string[];
  disk: DiskInfo | null;
  options: OptionsInfo;
  loaded: boolean;
  /** graceful shutdown in progress (server is stopping recordings and saving files) */
  quitting: boolean;
  recordingEnabled: boolean;
  conn: ConnState;
  setConn: (c: ConnState) => void;
  upsert: (r: Rec) => void;
  upsertMany: (rs: Rec[]) => void;
  drop: (ids: string[]) => void;
}

export const useStore = create<Store>()((set) => ({
  recs: {},
  order: [],
  disk: null,
  options: EMPTY_OPTIONS,
  loaded: false,
  quitting: false,
  recordingEnabled: true,
  conn: "connecting",
  setConn: (conn) => set({ conn }),
  upsert: (r) =>
    set((s) => {
      const rec = normalize(r);
      return {
        recs: { ...s.recs, [rec.id]: rec },
        order: s.order.includes(rec.id) ? s.order : [...s.order, rec.id],
      };
    }),
  upsertMany: (rs) =>
    set((s) => {
      if (rs.length === 0) return s;
      const recs = { ...s.recs };
      let order = s.order;
      for (const r of rs) {
        const rec = normalize(r);
        recs[rec.id] = rec;
        if (!order.includes(rec.id)) order = [...order, rec.id];
      }
      return { recs, order };
    }),
  drop: (ids) =>
    set((s) => {
      const gone = new Set(ids);
      const recs = { ...s.recs };
      gone.forEach((id) => delete recs[id]);
      return { recs, order: s.order.filter((id) => !gone.has(id)) };
    }),
}));

function wsUrl(): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}/api/ws`;
}

interface WsMessage {
  event: "recording" | "removed" | "toast" | "quitting" | "order";
  data: unknown;
}

function handleMessage(msg: WsMessage) {
  const st = useStore.getState();
  if (msg.event === "recording") queueUpsert(msg.data as Rec);
  else if (msg.event === "removed") st.drop(msg.data as string[]);
  else if (msg.event === "quitting") useStore.setState({ quitting: true });
  else if (msg.event === "order") {
    // Reordered (here or in another window): adopt it, keeping any room the list doesn't know yet.
    const ids = (msg.data as string[]).filter((id) => id in st.recs);
    const rest = st.order.filter((id) => !ids.includes(id));
    useStore.setState({ order: [...ids, ...rest] });
  }
  else if (msg.event === "toast" && msg.data && typeof (msg.data as { text?: unknown }).text === "string") {
    toast.message((msg.data as { text: string }).text);
  }
}

// Media pushes arrive in bursts (one message per recording room every sample
// interval). Coalesce them into a single store update per microtask so N
// rooms don't cause N re-renders.
let flushQueued = false;
const pendingUpserts = new Map<string, Rec>();

function queueUpsert(r: Rec) {
  pendingUpserts.set(r.id, r);
  if (flushQueued) return;
  flushQueued = true;
  queueMicrotask(() => {
    flushQueued = false;
    if (pendingUpserts.size === 0) return;
    const batch = [...pendingUpserts.values()];
    pendingUpserts.clear();
    useStore.getState().upsertMany(batch);
  });
}

// Module-level singleton so React StrictMode's double-mount shares one connection.
let refs = 0;
let stopCurrent: (() => void) | null = null;

async function loadInitial() {
  const st = await api.state();
  const recs: Record<string, Rec> = {};
  st.recordings.forEach((r) => {
    recs[r.id] = normalize(r);
  });
  useStore.setState({
    recs,
    order: st.recordings.map((r) => r.id),
    disk: st.disk,
    options: st.options,
    recordingEnabled: st.recording_enabled,
    loaded: true,
  });
}

function start(): () => void {
  let ws: WebSocket | null = null;
  let ping: ReturnType<typeof setInterval> | null = null;
  let retry: ReturnType<typeof setTimeout> | null = null;
  let attempts = 0;
  let alive = true;
  const setConn = (c: ConnState) => useStore.getState().setConn(c);

  loadInitial().catch((e: Error) => {
    console.warn("initial state failed:", e);
  });

  const connect = () => {
    if (!alive) return;
    setConn(attempts === 0 ? "connecting" : "closed");
    let socket: WebSocket;
    try {
      socket = new WebSocket(wsUrl());
    } catch {
      scheduleRetry();
      return;
    }
    ws = socket;

    socket.onopen = () => {
      attempts = 0;
      setConn("open");
      // The server treats inbound text as keep-alive pings.
      if (ping) clearInterval(ping);
      ping = setInterval(() => {
        if (socket.readyState === WebSocket.OPEN) socket.send("ping");
      }, 25000);
      // Re-sync in case anything changed while disconnected.
      loadInitial().catch(() => undefined);
    };
    socket.onmessage = (ev) => {
      try {
        handleMessage(JSON.parse(String(ev.data)) as WsMessage);
      } catch {
        /* ignore malformed frames */
      }
    };
    socket.onclose = () => {
      if (ws === socket) ws = null;
      if (ping) {
        clearInterval(ping);
        ping = null;
      }
      scheduleRetry();
    };
    socket.onerror = () => {
      try {
        socket.close();
      } catch {
        /* already closed */
      }
    };
  };

  const scheduleRetry = () => {
    if (!alive) return;
    setConn("closed");
    attempts += 1;
    const delay = Math.min(1000 * 2 ** Math.min(attempts, 5), 15000);
    if (retry) clearTimeout(retry);
    retry = setTimeout(connect, delay);
  };

  connect();

  return () => {
    alive = false;
    if (retry) clearTimeout(retry);
    if (ping) clearInterval(ping);
    try {
      ws?.close();
    } catch {
      /* ignore */
    }
    ws = null;
  };
}

/**
 * Open the shared state/WebSocket connection. Safe to call from an effect
 * (reference-counted for StrictMode); returns a cleanup function.
 */
export function connectLive(): () => void {
  refs += 1;
  if (!stopCurrent) stopCurrent = start();
  let released = false;
  return () => {
    if (released) return;
    released = true;
    refs -= 1;
    if (refs <= 0) {
      refs = 0;
      stopCurrent?.();
      stopCurrent = null;
    }
  };
}
