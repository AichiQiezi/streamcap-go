/** The sites the Go backend supports. Detection mirrors each resolver's Match(). */

const MATCHERS: [RegExp, string][] = [
  [/^https?:\/\/([a-z0-9-]+\.)*douyin\.com\//i, "douyin"],
  [/^https?:\/\/([a-z0-9-]+\.)*bilibili\.com\//i, "bilibili"],
];

export const PLATFORM_NAMES: Record<string, string> = { douyin: "抖音", bilibili: "B站" };

const COLORS: Record<string, string> = { douyin: "#fe2c55", bilibili: "#fb7299" };

/** Platforms that accept a login cookie (cookies.json keys). */
export const COOKIE_PLATFORMS = ["douyin", "bilibili"];

export const COOKIE_HINTS: Record<string, string> = {
  douyin: "一般不需要；遇到频繁检测失败或需要登录才能看的直播间时再填",
  bilibili: "未登录时多数直播间最高只有 720p；填入含 SESSDATA 的登录 Cookie 可录原画",
};

export function platformName(key: string | null | undefined, fallback?: string | null): string {
  return (key && PLATFORM_NAMES[key]) || fallback?.replace(/直播$/, "") || "未知平台";
}

export function platformColor(key: string | null | undefined): string {
  return (key && COLORS[key]) || "#8b8d94";
}

export function detectPlatform(url: string): string | null {
  const u = url.trim();
  for (const [re, key] of MATCHERS) if (re.test(u)) return key;
  return null;
}
