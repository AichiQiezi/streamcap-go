import { ChevronDown, Eye, EyeOff } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { Button, cx, Field, Input, Segmented, Select, Switch } from "@/components/ui";
import { api } from "@/lib/api";
import { COOKIE_HINTS, COOKIE_PLATFORMS, platformColor, platformName } from "@/lib/platforms";
import { useStore } from "@/lib/store";
import type { SettingsValues } from "@/lib/types";

type Kind = "switch" | "text" | "secret" | "number" | "select" | "segmented";

interface Item {
  key: string;
  label: string;
  hint?: string;
  kind: Kind;
  options?: { value: string; label: string }[];
  suffix?: string;
  placeholder?: string;
  mono?: boolean;
  width?: string;
  when?: (v: SettingsValues) => boolean;
}

interface Section {
  id: string;
  title: string;
  desc?: string;
  items: Item[];
}

interface Channel {
  id: string;
  name: string;
  enable: string;
  items: Item[];
}

const CHANNELS: Channel[] = [
  { id: "dingtalk", name: "钉钉", enable: "dingtalk_enabled", items: [
    { key: "dingtalk_webhook_url", label: "Webhook 地址", kind: "secret", mono: true },
    { key: "dingtalk_at_objects", label: "@ 的手机号", kind: "text", placeholder: "多个用英文逗号分隔" },
    { key: "dingtalk_at_all", label: "@ 所有人", kind: "switch" },
  ] },
  { id: "wechat", name: "微信推送", enable: "wechat_enabled", items: [{ key: "wechat_webhook_url", label: "Webhook 地址", kind: "secret", mono: true }] },
  { id: "feishu", name: "飞书", enable: "feishu_enabled", items: [{ key: "feishu_webhook_url", label: "Webhook 地址", kind: "secret", mono: true }] },
  { id: "bark", name: "Bark", enable: "bark_enabled", items: [
    { key: "bark_webhook_url", label: "推送地址", kind: "secret", mono: true },
    { key: "bark_interrupt_level", label: "中断级别", kind: "select", options: [
      { value: "active", label: "active（默认）" }, { value: "timeSensitive", label: "timeSensitive（时效性）" }, { value: "passive", label: "passive（静默）" },
    ] },
    { key: "bark_sound", label: "提示音", kind: "text", placeholder: "留空为默认" },
  ] },
  { id: "ntfy", name: "ntfy", enable: "ntfy_enabled", items: [
    { key: "ntfy_server_url", label: "服务地址", kind: "text", mono: true },
    { key: "ntfy_tags", label: "标签", kind: "text" },
    { key: "ntfy_email", label: "邮箱", kind: "text" },
    { key: "ntfy_action_url", label: "点击跳转地址", kind: "text", mono: true },
  ] },
  { id: "serverchan", name: "Server 酱", enable: "serverchan_enabled", items: [
    { key: "serverchan_sendkey", label: "SendKey", kind: "secret", mono: true },
    { key: "serverchan_channel", label: "通道", kind: "text", width: "w-24" },
    { key: "serverchan_tags", label: "标签", kind: "text" },
  ] },
  { id: "telegram", name: "Telegram", enable: "telegram_enabled", items: [
    { key: "telegram_api_token", label: "Bot Token", kind: "secret", mono: true },
    { key: "telegram_chat_id", label: "Chat ID", kind: "text", mono: true },
  ] },
  { id: "email", name: "邮件", enable: "email_enabled", items: [
    { key: "smtp_server", label: "SMTP 服务器", kind: "text", mono: true },
    { key: "email_username", label: "用户名", kind: "text" },
    { key: "email_password", label: "密码 / 授权码", kind: "secret" },
    { key: "sender_email", label: "发件邮箱", kind: "text" },
    { key: "sender_name", label: "发件人名称", kind: "text" },
    { key: "recipient_email", label: "收件邮箱", kind: "text" },
  ] },
];

function sections(qualities: { value: string; label: string }[], formats: string[]): Section[] {
  return [
    {
      id: "record",
      title: "录制",
      items: [
        { key: "live_save_path", label: "保存目录", kind: "text", mono: true, width: "w-[360px]", hint: "所有录像的根目录，留空使用程序目录下的 downloads" },
        { key: "record_quality", label: "默认画质", kind: "segmented", options: qualities, hint: "新添加直播间时的默认值" },
        { key: "video_format", label: "默认格式", kind: "select", options: formats.map((f) => ({ value: f, label: f })) },
        { key: "loop_time_seconds", label: "开播检测间隔", kind: "number", suffix: "秒", hint: "太短可能被平台限流，建议不低于 60 秒" },
        { key: "platform_max_concurrent", label: "同平台并发检测数", kind: "number", hint: "同一平台同时检测的直播间数量上限" },
        { key: "segmented_recording_enabled", label: "分段录制", kind: "switch", hint: "按时长切分文件" },
        { key: "video_segment_time", label: "分段时长", kind: "number", suffix: "秒", when: (v) => Boolean(v.segmented_recording_enabled) },
        { key: "default_live_source", label: "优先直播源", kind: "segmented", options: [{ value: "FLV", label: "FLV" }, { value: "HLS", label: "HLS" }], hint: "仅对抖音、TikTok 生效；h265 流会自动改用 HLS" },
        { key: "recording_space_threshold", label: "磁盘空间下限", kind: "number", suffix: "GB", hint: "可用空间低于该值时暂停新的录制" },
      ],
    },
    {
      id: "post",
      title: "录制完成后",
      items: [
        { key: "merge_sessions", label: "合并同一场直播", kind: "switch", hint: "断流重连产生的多段，在下播后无损拼成一个文件（开启分段录制时不合并）" },
        { key: "convert_to_mp4", label: "自动转为 MP4", kind: "switch", hint: "无损换封装，不重新编码；原文件移到 original 子目录" },
        { key: "delete_original", label: "合并/转换后删除原文件", kind: "switch", when: (v) => Boolean(v.convert_to_mp4) || Boolean(v.merge_sessions) },
      ],
    },
    {
      id: "naming",
      title: "文件整理",
      desc: "录像按以下规则分文件夹存放",
      items: [
        { key: "folder_name_platform", label: "按平台建文件夹", kind: "switch" },
        { key: "folder_name_author", label: "按主播建文件夹", kind: "switch" },
        { key: "folder_name_time", label: "按日期建文件夹", kind: "switch" },
        { key: "folder_name_title", label: "按直播标题建文件夹", kind: "switch" },
        { key: "filename_includes_title", label: "文件名包含直播标题", kind: "switch" },
        { key: "ascii_save_path", label: "英文路径命名", kind: "switch", hint: "平台用英文标识，主播名和标题转拼音，适合 NAS 和命令行环境" },
      ],
    },
    {
      id: "network",
      title: "网络代理",
      items: [
        { key: "enable_proxy", label: "启用代理", kind: "switch" },
        { key: "proxy_address", label: "代理地址", kind: "text", mono: true, placeholder: "127.0.0.1:7890", width: "w-[260px]", when: (v) => Boolean(v.enable_proxy) },
        { key: "default_platform_with_proxy", label: "走代理的平台", kind: "text", mono: true, width: "w-[360px]", hint: "平台标识，英文逗号分隔", when: (v) => Boolean(v.enable_proxy) },
      ],
    },
    {
      id: "notify",
      title: "开播通知",
      desc: "在下方配置推送渠道；单个直播间还需在编辑里打开「开播消息推送」",
      items: [
        { key: "stream_start_notification_enabled", label: "开播时推送", kind: "switch" },
        { key: "stream_end_notification_enabled", label: "下播时推送", kind: "switch" },
        { key: "only_notify_no_record", label: "新直播间默认仅通知不录制", kind: "switch" },
        { key: "custom_notification_title", label: "自定义标题", kind: "text", placeholder: "留空使用默认", width: "w-[300px]" },
        { key: "custom_stream_start_content", label: "开播内容", kind: "text", placeholder: "留空使用默认", width: "w-[300px]" },
        { key: "custom_stream_end_content", label: "下播内容", kind: "text", placeholder: "留空使用默认", width: "w-[300px]" },
      ],
    },
  ];
}

function Control({ item, value, onChange }: { item: Item; value: unknown; onChange: (v: string | boolean) => void }) {
  const [reveal, setReveal] = useState(false);
  const str = value == null ? "" : String(value);
  switch (item.kind) {
    case "switch":
      return <Switch checked={Boolean(value)} onChange={onChange} label={item.label} />;
    case "segmented":
      return <Segmented size="sm" value={str} onChange={onChange} options={item.options ?? []} />;
    case "select":
      return <Select value={str} onChange={onChange} options={item.options ?? []} className="w-56" />;
    case "number":
      return (
        <div className="flex items-center gap-2 text-[12.5px] text-fg-3">
          <Input className="h-8 w-24 text-right font-mono" inputMode="decimal" value={str} onChange={(e) => onChange(e.target.value.replace(/[^\d.]/g, ""))} />
          {item.suffix}
        </div>
      );
    case "secret":
      return (
        <div className="relative">
          <Input
            type={reveal ? "text" : "password"}
            className={cx("h-8 pr-8", item.width ?? "w-[320px]", item.mono && "font-mono text-[12px]")}
            value={str}
            placeholder={item.placeholder}
            onChange={(e) => onChange(e.target.value)}
            autoComplete="off"
          />
          <button type="button" aria-label={reveal ? "隐藏" : "显示"} onClick={() => setReveal((r) => !r)} className="absolute top-1/2 right-2 -translate-y-1/2 text-fg-3 hover:text-fg">
            {reveal ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
          </button>
        </div>
      );
    default:
      return (
        <Input
          className={cx("h-8", item.width ?? "w-[260px]", item.mono && "font-mono text-[12px]")}
          value={str}
          placeholder={item.placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

function Card({ id, title, desc, children }: { id: string; title: string; desc?: string; children: React.ReactNode }) {
  return (
    <section id={id} className="scroll-mt-6">
      <h2 className="text-[15px] font-semibold tracking-[-0.01em]">{title}</h2>
      {desc && <p className="mt-1 text-[12.5px] text-fg-3">{desc}</p>}
      <div className="mt-3 divide-y divide-line rounded-xl border border-line bg-surface px-4 md:px-5">{children}</div>
    </section>
  );
}

export function SettingsPage() {
  const options = useStore((s) => s.options);
  const [saved, setSaved] = useState<SettingsValues | null>(null);
  const [draft, setDraft] = useState<SettingsValues>({});
  const [cookiesSaved, setCookiesSaved] = useState<Record<string, string>>({});
  const [cookies, setCookies] = useState<Record<string, string>>({});
  const [defaultPath, setDefaultPath] = useState("");
  const [saving, setSaving] = useState(false);
  const [openChannel, setOpenChannel] = useState<string | null>(null);
  const [active, setActive] = useState("record");
  const scroller = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api.settings().then((s) => {
      setSaved(s.values);
      setDraft(s.values);
      setCookiesSaved(s.cookies);
      setCookies(s.cookies);
      setDefaultPath(s.default_save_path);
    });
  }, []);

  const secs = useMemo(() => sections(options.qualities, [...options.video_formats, ...options.audio_formats]), [options]);
  const nav = [...secs.map((s) => ({ id: s.id, title: s.title })), { id: "channels", title: "推送渠道" }, { id: "cookies", title: "平台 Cookie" }];

  const changed = saved ? Object.keys(draft).filter((k) => draft[k] !== saved[k]) : [];
  const cookieChanged = [...new Set([...Object.keys(cookies), ...Object.keys(cookiesSaved)])].filter((k) => (cookies[k] ?? "") !== (cookiesSaved[k] ?? ""));
  const dirty = changed.length + cookieChanged.length > 0;

  const set = (k: string, v: string | boolean) => setDraft((d) => ({ ...d, [k]: v }));

  async function save() {
    setSaving(true);
    try {
      if (changed.length) await api.saveSettings(Object.fromEntries(changed.map((k) => [k, draft[k]])));
      if (cookieChanged.length) await api.saveCookies(Object.fromEntries(cookieChanged.map((k) => [k, cookies[k] ?? ""])));
      setSaved(draft);
      setCookiesSaved(cookies);
      toast.success("设置已保存");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  useEffect(() => {
    const root = scroller.current?.closest("main");
    if (!root) return;
    const obs = new IntersectionObserver(
      (entries) => entries.forEach((e) => e.isIntersecting && setActive(e.target.id)),
      { root, rootMargin: "-10% 0px -75% 0px" },
    );
    nav.forEach((n) => {
      const el = document.getElementById(n.id);
      if (el) obs.observe(el);
    });
    return () => obs.disconnect();
  }, [saved]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!saved)
    return (
      <div className="mx-auto max-w-[1080px] space-y-4 px-8 pt-7">
        <div className="h-8 w-40 animate-pulse rounded-lg bg-surface-2" />
        <div className="h-96 animate-pulse rounded-xl bg-surface" />
      </div>
    );

  const cookieKeys = COOKIE_PLATFORMS;

  return (
    <div ref={scroller} className="mx-auto grid w-full max-w-[1080px] grid-cols-1 gap-10 px-5 pt-7 pb-32 md:px-8 lg:grid-cols-[160px_minmax(0,1fr)]">
      <aside className="self-start lg:sticky lg:top-7">
        <h1 className="text-[22px] font-semibold tracking-[-0.02em] lg:mb-5">设置</h1>
        <nav className="hidden space-y-0.5 lg:block">
          {nav.map((n) => (
            <a
              key={n.id}
              href={`#${n.id}`}
              onClick={(e) => {
                e.preventDefault();
                document.getElementById(n.id)?.scrollIntoView({ behavior: "smooth" });
              }}
              className={cx(
                "block rounded-lg px-2.5 py-1.5 text-[13px] transition",
                active === n.id ? "bg-surface-2 font-medium text-fg" : "text-fg-3 hover:text-fg-2",
              )}
            >
              {n.title}
            </a>
          ))}
        </nav>
      </aside>

      <div className="space-y-9 pt-1">
        {secs.map((s) => (
          <Card key={s.id} id={s.id} title={s.title} desc={s.desc}>
            {s.items
              .filter((it) => !it.when || it.when(draft))
              .map((it) => (
                <Field key={it.key} inline label={it.label} hint={it.key === "live_save_path" ? `${it.hint}（当前：${defaultPath}）` : it.hint}>
                  <Control item={it} value={draft[it.key]} onChange={(v) => set(it.key, v)} />
                </Field>
              ))}
          </Card>
        ))}

        <section id="channels" className="scroll-mt-6">
          <h2 className="text-[15px] font-semibold tracking-[-0.01em]">推送渠道</h2>
          <p className="mt-1 text-[12.5px] text-fg-3">开播 / 下播通知会发到所有已启用的渠道</p>
          <div className="mt-3 grid gap-2">
            {CHANNELS.map((c) => {
              const on = Boolean(draft[c.enable]);
              const open = openChannel === c.id;
              return (
                <div key={c.id} className="rounded-xl border border-line bg-surface">
                  <div className="flex items-center gap-3 px-5 py-3">
                    <button type="button" onClick={() => setOpenChannel(open ? null : c.id)} className="flex flex-1 items-center gap-2.5 text-left">
                      <ChevronDown className={cx("size-3.5 text-fg-3 transition", !open && "-rotate-90")} />
                      <span className="text-[13px] font-medium">{c.name}</span>
                      {on && <span className="rounded bg-ok/12 px-1.5 py-px text-[11px] font-medium text-ok">已启用</span>}
                    </button>
                    <Switch checked={on} onChange={(v) => { set(c.enable, v); if (v) setOpenChannel(c.id); }} label={`启用${c.name}`} />
                  </div>
                  {open && (
                    <div className="divide-y divide-line border-t border-line px-5">
                      {c.items.map((it) => (
                        <Field key={it.key} inline label={it.label} hint={it.hint}>
                          <Control item={it} value={draft[it.key]} onChange={(v) => set(it.key, v)} />
                        </Field>
                      ))}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </section>

        <section id="cookies" className="scroll-mt-6">
          <div className="flex flex-wrap items-end justify-between gap-4">
            <div className="min-w-[240px] flex-1">
              <h2 className="text-[15px] font-semibold tracking-[-0.01em]">平台 Cookie</h2>
              <p className="mt-1 text-[12.5px] text-fg-3">只保存在本机的 data/cookies.json，用于请求对应平台的接口</p>
            </div>
          </div>
          <div className="mt-3 divide-y divide-line rounded-xl border border-line bg-surface px-5">
            {cookieKeys.map((k) => (
              <div key={k} className="flex flex-wrap items-center gap-x-4 gap-y-1.5 py-3">
                <span className="flex w-28 shrink-0 items-center gap-2 text-[13px] font-medium">
                  <span className="size-1.5 rounded-full" style={{ background: platformColor(k) }} />
                  {platformName(k)}
                </span>
                {COOKIE_HINTS[k] && <span className="order-last w-full text-[12px] text-fg-3 sm:pl-32">{COOKIE_HINTS[k]}</span>}
                <Input
                  className="h-8 min-w-[240px] flex-1 font-mono text-[11.5px]"
                  placeholder="粘贴 Cookie 字符串"
                  value={cookies[k] ?? ""}
                  onChange={(e) => setCookies((c) => ({ ...c, [k]: e.target.value }))}
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
            ))}
          </div>
        </section>
      </div>

      {dirty && (
        <div className="fixed bottom-6 left-1/2 z-30 flex lg:ml-[110px] -translate-x-1/2 items-center gap-3 rounded-2xl border border-line bg-surface py-2 pr-2 pl-5 shadow-2xl animate-fade">
          <span className="text-[13px] text-fg-2">
            有 <span className="font-mono tabular text-fg">{changed.length + cookieChanged.length}</span> 项更改未保存
          </span>
          <Button variant="ghost" size="sm" onClick={() => { setDraft(saved); setCookies(cookiesSaved); }}>
            放弃
          </Button>
          <Button variant="primary" size="sm" loading={saving} onClick={save}>
            保存
          </Button>
        </div>
      )}
    </div>
  );
}
