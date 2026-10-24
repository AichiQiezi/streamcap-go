/** Shared shapes for the StreamCap Go API (`internal/api`). */

export type RecState =
  | "recording"
  | "reconnecting"
  | "live"
  | "error"
  | "checking"
  | "scheduled"
  | "offline"
  | "paused";

export interface RecStats {
  file: string | null;
  size_bytes: number;
  speed_bps: number;
  width: number | null;
  height: number | null;
  fps: number | null;
  vcodec: string | null;
  thumb_version: number;
  /** Comments recorded so far this session; null when it records none. */
  danmaku: number | null;
}

export interface Rec {
  id: string;
  url: string;
  name: string;
  platform: string;
  platform_key: string;
  live_title: string | null;
  quality: string;
  format: string;
  state: RecState;
  status_info: string | null;
  monitor: boolean;
  is_live: boolean;
  is_recording: boolean;
  duration_s: number;
  started_at: string | null;
  recording_dir: string | null;
  segment_record: boolean;
  segment_time: string | null;
  video_bitrate: number | null;
  scheduled_recording: boolean;
  scheduled_start_time: string | null;
  monitor_hours: string | null;
  enabled_message_push: boolean;
  only_notify_no_record: boolean;
  /** submit the session's files to Bilibili after the live */
  auto_upload: boolean;
  flv_use_direct_download: boolean;
  checked_at: string | null;
  /** why the last check / part failed, shown while reconnecting or in error */
  last_error: string | null;
  /** drops recovered automatically in the current live session */
  reconnects: number;
  stats: RecStats;
}

/** Mirrors the backend `RecordingIn` model. All fields optional except `url`. */
export interface RecInput {
  url: string;
  streamer_name?: string | null;
  quality?: string;
  record_format?: string;
  segment_record?: boolean;
  segment_time?: string;
  video_bitrate?: number | null;
  recording_dir?: string | null;
  monitor_status?: boolean;
  scheduled_recording?: boolean;
  scheduled_start_time?: string | null;
  monitor_hours?: string | null;
  enabled_message_push?: boolean;
  only_notify_no_record?: boolean;
  auto_upload?: boolean;
  flv_use_direct_download?: boolean;
}

export interface MediaFile {
  path: string;
  name: string;
  folder: string;
  size: number;
  mtime: number;
  kind: "audio" | "video";
  ext: string;
  /** a same-named .ass comment track sits next to the file */
  danmaku?: boolean;
}

export type SettingsValues = Record<string, unknown>;

export interface DiskInfo {
  path: string;
  free: number;
  total: number;
}

export interface QualityOption {
  value: string;
  label: string;
}

export interface OptionsInfo {
  qualities: QualityOption[];
  video_formats: string[];
  audio_formats: string[];
}

export interface StateResponse {
  recordings: Rec[];
  recording_enabled: boolean;
  disk: DiskInfo;
  options: OptionsInfo;
}

export interface SettingsResponse {
  values: SettingsValues;
  default_save_path: string;
  cookies: Record<string, string>;
}

export interface FilesResponse {
  root: string;
  files: MediaFile[];
}

/** The Bilibili account videos are submitted to. */
export interface BiliAccount {
  mid: number;
  name: string;
  face: string;
  expires_at: string;
}

export interface Partition {
  id: number;
  name: string;
  desc?: string;
  children?: Partition[];
}

export type UploadState = "queued" | "uploading" | "submitting" | "done" | "failed" | "canceled";

export interface UploadTask {
  id: string;
  files: string[];
  names: string[];
  title: string;
  desc: string;
  tags: string;
  tid: number;
  copyright: 1 | 2;
  source: string;
  /** 仅自己可见 */
  only_self?: boolean;
  room_id?: string;
  state: UploadState;
  error?: string;
  total: number;
  sent: number;
  speed_bps: number;
  bvid?: string;
  created_at: string;
  finished_at?: string;
}

/** The submit form's fields (POST /api/uploads, GET /api/uploads/draft). */
export interface UploadForm {
  title: string;
  desc: string;
  tags: string;
  tid: number;
  copyright: 1 | 2;
  source: string;
  /** 仅自己可见 */
  only_self: boolean;
}
