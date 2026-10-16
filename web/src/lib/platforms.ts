/** The sites the Go backend supports. Detection mirrors each resolver's Match(). */

const MATCHERS: [RegExp, string][] = [
  [/^https?:\/\/([a-z0-9-]+\.)*douyin\.com\//i, "douyin"],
  [/^https?:\/\/([a-z0-9-]+\.)*bilibili\.com\//i, "bilibili"],
  [/^https?:\/\/(b23\.tv|bili2233\.cn|bili22\.cn|bili33\.cn)\//i, "bilibili"], // app share short links
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

const URL_RE = /https?:\/\/[^\s"'<>，。！？、；【】（）()「」]+/gi;
const BARE_RE = /^([a-z0-9-]+\.)+(com|cn|tv)\/\S+/i; // "live.bilibili.com/123" without the scheme

/**
 * The links in pasted text. App share messages wrap the link in prose — 抖音: "9- 长按复制此条消息，
 * 打开抖音搜索… https://v.douyin.com/xxx/ 3@1.com :7pm", B站: "【xx的直播间】 https://b23.tv/xxx" —
 * so only URL-shaped pieces are kept, deduplicated, in order.
 */
export function extractLinks(text: string): string[] {
  const out: string[] = [];
  for (const token of text.split(/[\s,，]+/)) {
    const urls = token.match(URL_RE);
    if (urls) out.push(...urls.map((u) => u.replace(/[.,;:!?)]+$/, "")));
    else if (BARE_RE.test(token)) out.push(`https://${token}`);
  }
  return [...new Set(out)];
}
