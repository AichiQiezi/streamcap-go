import type { FilesResponse, Rec, RecInput, SettingsResponse, StateResponse } from "./types";

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

  monitor: (id: string, on: boolean) => post<Rec>(`/api/recordings/${encodeURIComponent(id)}/monitor`, { on }),
  record: (id: string, on: boolean) => post<Rec>(`/api/recordings/${encodeURIComponent(id)}/record`, { on }),
  check: (id: string) => post<{ ok: boolean }>(`/api/recordings/${encodeURIComponent(id)}/check`),
  batch: (ids: string[], action: "start" | "stop" | "delete" | "check") =>
    post<{ ok: boolean; count: number }>("/api/recordings/batch", { ids, action }),
  openFolder: (id: string) => post<{ ok: boolean; path: string }>(`/api/recordings/${encodeURIComponent(id)}/open-folder`),

  settings: () => get<SettingsResponse>("/api/settings"),
  saveSettings: (values: Record<string, unknown>) =>
    request<{ ok: boolean }>("/api/settings", { method: "PUT", body: JSON.stringify(values) }),
  saveCookies: (values: Record<string, string>) =>
    request<{ ok: boolean }>("/api/cookies", { method: "PUT", body: JSON.stringify(values) }),

  quit: () => post<{ ok: boolean }>("/api/app/quit", {}),

  files: () => get<FilesResponse>("/api/files"),
  deleteFile: (path: string) =>
    request<{ ok: boolean }>(`/api/files?path=${encodeURIComponent(path)}`, { method: "DELETE" }),
  revealFile: (path?: string) =>
    post<{ ok: boolean }>(`/api/files/reveal${path ? `?path=${encodeURIComponent(path)}` : ""}`),
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
