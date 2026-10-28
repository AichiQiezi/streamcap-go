import type { BiliAccount, FilesResponse, LinkPreview, Partition, Rec, RecInput, SettingsResponse, StateResponse, UploadForm, UploadTask } from "./types";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body && typeof body.detail === "string" && body.detail) detail = body.detail;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(detail.trim() || "请求失败");
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

const get = <T>(path: string) => request<T>(path);
const post = <T>(path: string, body?: unknown) =>
  request<T>(path, { method: "POST", body: body === undefined ? undefined : JSON.stringify(body) });

export const api = {
  state: () => get<StateResponse>("/api/state"),

  add: (items: RecInput[]) => post<{ created: Rec[]; skipped: { url: string; reason: string }[] }>("/api/recordings", items),
  edit: (id: string, item: RecInput) =>
    request<Rec>(`/api/recordings/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(item) }),

  /** Who each link belongs to, whether they're live, and whether the list already has them. */
  preview: (urls: string[]) => post<{ previews: LinkPreview[] }>("/api/recordings/preview", { urls }),
  /** Live-room links on the clipboard (never the rest of its text); `sig` changes with its content. */
  clipboardLinks: () => get<{ sig: string; links: string[] }>("/api/clipboard/links"),

  monitor: (id: string, on: boolean) => post<Rec>(`/api/recordings/${encodeURIComponent(id)}/monitor`, { on }),
  record: (id: string, on: boolean) => post<Rec>(`/api/recordings/${encodeURIComponent(id)}/record`, { on }),
  check: (id: string) => post<{ ok: boolean }>(`/api/recordings/${encodeURIComponent(id)}/check`),
  /** Saves the room list's order (the default order in the UI). */
  reorder: (ids: string[]) => request<{ ok: boolean }>("/api/recordings/order", { method: "PUT", body: JSON.stringify({ ids }) }),
  batch: (ids: string[], action: "start" | "stop" | "delete" | "check") =>
    post<{ ok: boolean; count: number }>("/api/recordings/batch", { ids, action }),
  openFolder: (id: string) => post<{ ok: boolean; path: string }>(`/api/recordings/${encodeURIComponent(id)}/open-folder`),

  settings: () => get<SettingsResponse>("/api/settings"),
  saveSettings: (values: Record<string, unknown>) =>
    request<{ ok: boolean }>("/api/settings", { method: "PUT", body: JSON.stringify(values) }),
  saveCookies: (values: Record<string, string>) =>
    request<{ ok: boolean }>("/api/cookies", { method: "PUT", body: JSON.stringify(values) }),

  /** Sends a sample message through one push channel; `values` are unsaved settings edits. */
  testNotify: (channel: string, values: Record<string, unknown>) =>
    post<{ ok: boolean }>("/api/notify/test", { channel, values }),

  openNotificationSettings: () => post<{ ok: boolean }>("/api/notify/system-settings"),

  quit: () => post<{ ok: boolean }>("/api/app/quit", {}),

  files: () => get<FilesResponse>("/api/files"),
  deleteFile: (path: string) =>
    request<{ ok: boolean }>(`/api/files?path=${encodeURIComponent(path)}`, { method: "DELETE" }),
  revealFile: (path?: string) =>
    post<{ ok: boolean }>(`/api/files/reveal${path ? `?path=${encodeURIComponent(path)}` : ""}`),

  /** Interfaces recording traffic can be pinned to; `auto` is what "auto" resolves to now. */
  netInterfaces: () => get<{ interfaces: { value: string; label: string }[]; auto: string }>("/api/net/interfaces"),

  biliAccount: () => get<{ account: BiliAccount | null }>("/api/bili/account"),
  /** Starts a QR login: `qr` is a PNG data URL to scan with the Bilibili app. */
  biliLogin: () => post<{ auth_code: string; qr: string }>("/api/bili/login", {}),
  biliLoginPoll: (authCode: string) =>
    post<{ status: "waiting" | "scanned" | "expired" | "done"; account: BiliAccount | null }>("/api/bili/login/poll", { auth_code: authCode }),
  biliLogout: () => post<{ ok: boolean }>("/api/bili/logout", {}),
  biliPartitions: () => get<{ partitions: Partition[] }>("/api/bili/partitions"),

  uploads: () => get<{ tasks: UploadTask[] }>("/api/uploads"),
  uploadDraft: (path: string) => get<UploadForm>(`/api/uploads/draft?path=${encodeURIComponent(path)}`),
  /** `paths` are relative to the recordings folder, as `files()` lists them; each becomes a part (P). */
  addUpload: (paths: string[], form: UploadForm) => post<UploadTask>("/api/uploads", { paths, ...form }),
  retryUpload: (id: string) => post<{ ok: boolean }>(`/api/uploads/${encodeURIComponent(id)}/retry`, {}),
  cancelUpload: (id: string) => post<{ ok: boolean }>(`/api/uploads/${encodeURIComponent(id)}/cancel`, {}),
  removeUpload: (id: string) => request<{ ok: boolean }>(`/api/uploads/${encodeURIComponent(id)}`, { method: "DELETE" }),
};

/** Cover thumbnail for a recording, or null when no frame has been captured yet. */
export function thumbUrl(r: Rec): string | null {
  if (!r.stats?.thumb_version) return null;
  return `/api/recordings/${encodeURIComponent(r.id)}/thumbnail?v=${r.stats.thumb_version}`;
}

/** Direct (streamable / downloadable) URL for a recorded file. */
export function fileUrl(path: string): string {
  return `/api/files/raw?path=${encodeURIComponent(path)}`;
}
