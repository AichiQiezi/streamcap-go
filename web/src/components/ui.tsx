import * as RDialog from "@radix-ui/react-dialog";
import * as RMenu from "@radix-ui/react-dropdown-menu";
import * as RSwitch from "@radix-ui/react-switch";
import * as RTooltip from "@radix-ui/react-tooltip";
import clsx from "clsx";
import { Loader2, X } from "lucide-react";
import { forwardRef, useState, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";

export const cx = clsx;

type Variant = "primary" | "secondary" | "ghost" | "danger";

const variants: Record<Variant, string> = {
  primary: "bg-primary text-primary-fg hover:opacity-90 shadow-sm",
  secondary: "bg-surface-2 text-fg border border-line hover:bg-surface-3",
  ghost: "text-fg-2 hover:text-fg hover:bg-surface-2",
  danger: "bg-err/10 text-err border border-err/25 hover:bg-err/15",
};

interface BtnProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: "sm" | "md";
  loading?: boolean;
  icon?: ReactNode;
}

/** Button styling for elements that must not be a <button>, e.g. a download link. */
export function buttonClass(variant: Variant = "secondary", size: "sm" | "md" = "md", className?: string) {
  return cx(
    "inline-flex items-center justify-center gap-1.5 rounded-lg font-medium whitespace-nowrap transition",
    "disabled:opacity-50 disabled:pointer-events-none select-none",
    size === "sm" ? "h-7 px-2.5 text-[12.5px]" : "h-8.5 px-3.5 text-[13px]",
    variants[variant],
    className,
  );
}

export const Button = forwardRef<HTMLButtonElement, BtnProps>(function Button(
  { variant = "secondary", size = "md", loading, icon, className, children, disabled, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={buttonClass(variant, size, className)}
      {...rest}
    >
      {loading ? <Loader2 className="size-3.5 animate-spin" /> : icon}
      {children}
    </button>
  );
});

interface IconBtnProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  label: string;
  tone?: "default" | "danger";
}

export const IconButton = forwardRef<HTMLButtonElement, IconBtnProps>(function IconButton(
  { label, tone = "default", className, children, ...rest },
  ref,
) {
  return (
    <Tip label={label}>
      <button
        ref={ref}
        aria-label={label}
        className={cx(
          "inline-flex size-7.5 items-center justify-center rounded-md transition disabled:opacity-40",
          tone === "danger" ? "text-fg-3 hover:text-err hover:bg-err/10" : "text-fg-3 hover:text-fg hover:bg-surface-2",
          className,
        )}
        {...rest}
      >
        {children}
      </button>
    </Tip>
  );
});

export function Tip({ label, children, side = "top" }: { label: ReactNode; children: ReactNode; side?: "top" | "bottom" | "right" | "left" }) {
  return (
    <RTooltip.Root delayDuration={350}>
      <RTooltip.Trigger asChild>{children}</RTooltip.Trigger>
      <RTooltip.Portal>
        <RTooltip.Content
          side={side}
          sideOffset={6}
          className="z-50 rounded-md bg-fg px-2 py-1 text-[11.5px] font-medium text-bg shadow-lg animate-fade"
        >
          {label}
        </RTooltip.Content>
      </RTooltip.Portal>
    </RTooltip.Root>
  );
}

export const TipProvider = RTooltip.Provider;

export function Switch({
  checked,
  onChange,
  disabled,
  label,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  disabled?: boolean;
  label?: string;
}) {
  return (
    <RSwitch.Root
      checked={checked}
      onCheckedChange={onChange}
      disabled={disabled}
      aria-label={label}
      className={cx(
        "relative inline-flex h-5 w-8.5 shrink-0 items-center rounded-full transition-colors disabled:opacity-50",
        checked ? "bg-ok" : "bg-surface-3 border border-line-strong",
      )}
    >
      <RSwitch.Thumb
        className={cx(
          "block size-4 rounded-full bg-white shadow-sm transition-transform",
          checked ? "translate-x-[15px]" : "translate-x-[2px]",
        )}
      />
    </RSwitch.Root>
  );
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input(
  { className, ...rest },
  ref,
) {
  return (
    <input
      ref={ref}
      className={cx(
        // default to full width unless the caller sizes it (no tailwind-merge, so avoid two w-* classes)
        !/(^|\s)w-/.test(className ?? "") && "w-full",
        "h-8.5 max-w-full rounded-lg border border-line bg-surface px-3 text-[13px] text-fg placeholder:text-fg-3",
        "transition focus:border-line-strong focus:outline-none focus:ring-3 focus:ring-[var(--ring)]",
        className,
      )}
      {...rest}
    />
  );
});

export function Select({
  value,
  onChange,
  options,
  className,
}: {
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  className?: string;
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className={cx(
        "h-8.5 rounded-lg border border-line bg-surface px-2.5 pr-7 text-[13px] text-fg focus:outline-none",
        "focus:ring-3 focus:ring-[var(--ring)] appearance-none bg-no-repeat",
        "bg-[length:14px] bg-[right_8px_center]",
        className,
      )}
      style={{
        backgroundImage:
          "url(\"data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%238b8d94' stroke-width='2'%3E%3Cpath d='m6 9 6 6 6-6'/%3E%3C/svg%3E\")",
      }}
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  );
}

export function Segmented<T extends string>({
  value,
  onChange,
  options,
  size = "md",
}: {
  value: T;
  onChange: (v: T) => void;
  options: { value: T; label: ReactNode; title?: string }[];
  size?: "sm" | "md";
}) {
  return (
    <div className="inline-flex rounded-lg border border-line bg-surface-2 p-0.5">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          title={o.title}
          onClick={() => onChange(o.value)}
          className={cx(
            "inline-flex items-center gap-1.5 rounded-md font-medium transition",
            size === "sm" ? "h-6 px-2 text-[12px]" : "h-7 px-3 text-[12.5px]",
            value === o.value ? "bg-fg text-bg shadow-sm" : "text-fg-2 hover:text-fg",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Field({ label, hint, children, inline }: { label: ReactNode; hint?: ReactNode; children: ReactNode; inline?: boolean }) {
  if (inline)
    // Label and control share a row when there's room; otherwise the control wraps below
    // instead of squeezing the label into a one-character column.
    return (
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 py-2.5">
        <div className="min-w-[200px] flex-1">
          <div className="text-[13px] font-medium text-fg">{label}</div>
          {hint && <div className="mt-0.5 text-[12px] leading-relaxed [overflow-wrap:anywhere] text-fg-3">{hint}</div>}
        </div>
        <div className="max-w-full shrink-0">{children}</div>
      </div>
    );
  return (
    <label className="block space-y-1.5">
      <span className="block text-[12.5px] font-medium text-fg-2">{label}</span>
      {children}
      {hint && <span className="block text-[12px] text-fg-3">{hint}</span>}
    </label>
  );
}

export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  width = 560,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: ReactNode;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  width?: number;
}) {
  const [scrolled, setScrolled] = useState(false);
  return (
    <RDialog.Root open={open} onOpenChange={onOpenChange}>
      <RDialog.Portal>
        <RDialog.Overlay className="fixed inset-0 z-40 bg-[var(--overlay)] backdrop-blur-[2px] animate-fade" />
        <RDialog.Content
          style={{ maxWidth: width }}
          {...(description ? {} : { "aria-describedby": undefined })}
          onOpenAutoFocus={(e) => {
            setScrolled(false);
            // Radix focuses the first tabbable element, which is the close button in the header.
            // Start on the first field instead (if the body has one), leaving the ✕ un-ringed.
            const field = (e.currentTarget as HTMLElement).querySelector<HTMLElement>(
              "[data-dialog-body] :is(input:not([type=hidden]),textarea,select):not(:disabled)",
            );
            if (field) {
              e.preventDefault();
              field.focus({ preventScroll: true });
            }
          }}
          className="fixed top-1/2 left-1/2 z-50 flex max-h-[min(88vh,820px)] w-[calc(100vw-32px)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-2xl border border-line bg-surface shadow-2xl animate-pop focus:outline-none"
        >
          <div className={cx("flex items-start justify-between gap-4 border-b px-6 pt-5 pb-4 transition-colors", scrolled ? "border-line" : "border-transparent")}>
            <div>
              <RDialog.Title className="text-[16px] font-semibold tracking-[-0.01em]">{title}</RDialog.Title>
              {description && <RDialog.Description className="mt-1 text-[12.5px] text-fg-3">{description}</RDialog.Description>}
            </div>
            <RDialog.Close className="-mr-1.5 rounded-md p-1 text-fg-3 hover:bg-surface-2 hover:text-fg" aria-label="关闭">
              <X className="size-4" />
            </RDialog.Close>
          </div>
          <div data-dialog-body className="min-h-0 flex-1 overflow-y-auto px-6 pb-5" onScroll={(e) => setScrolled(e.currentTarget.scrollTop > 0)}>
            {children}
          </div>
          {footer && <div className="flex items-center justify-end gap-2 border-t border-line px-6 py-3.5">{footer}</div>}
        </RDialog.Content>
      </RDialog.Portal>
    </RDialog.Root>
  );
}

export function Confirm({
  open,
  onOpenChange,
  title,
  body,
  confirmText = "确认",
  danger,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  title: string;
  body: ReactNode;
  confirmText?: string;
  danger?: boolean;
  onConfirm: () => Promise<void> | void;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={title}
      width={420}
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            取消
          </Button>
          <Button
            variant={danger ? "danger" : "primary"}
            onClick={async () => {
              await onConfirm();
              onOpenChange(false);
            }}
          >
            {confirmText}
          </Button>
        </>
      }
    >
      <div className="text-[13px] leading-relaxed text-fg-2">{body}</div>
    </Dialog>
  );
}

export const Menu = RMenu.Root;
export const MenuTrigger = RMenu.Trigger;

export function MenuContent({
  children,
  align = "end",
  side = "bottom",
}: {
  children: ReactNode;
  align?: "start" | "end";
  side?: "top" | "bottom" | "left" | "right";
}) {
  return (
    <RMenu.Portal>
      <RMenu.Content
        align={align}
        side={side}
        sideOffset={6}
        className="z-50 min-w-[180px] rounded-xl border border-line bg-surface p-1 shadow-xl animate-fade"
      >
        {children}
      </RMenu.Content>
    </RMenu.Portal>
  );
}

export function MenuItem({
  children,
  icon,
  onSelect,
  danger,
  disabled,
}: {
  children: ReactNode;
  icon?: ReactNode;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
}) {
  return (
    <RMenu.Item
      disabled={disabled}
      onSelect={onSelect}
      className={cx(
        "flex h-8 cursor-default items-center gap-2.5 rounded-lg px-2.5 text-[13px] outline-none select-none",
        "data-[disabled]:opacity-40",
        danger ? "text-err data-[highlighted]:bg-err/10" : "text-fg-2 data-[highlighted]:bg-surface-2 data-[highlighted]:text-fg",
      )}
    >
      <span className="[&>svg]:size-3.5 opacity-80">{icon}</span>
      {children}
    </RMenu.Item>
  );
}

export const MenuSep = () => <RMenu.Separator className="my-1 h-px bg-line" />;

/** `inverted` sits on a primary button, so it inherits the button's text color instead of the surface tokens. */
export function Kbd({ children, inverted }: { children: ReactNode; inverted?: boolean }) {
  return (
    <kbd
      className={cx(
        "rounded border px-1.5 py-px font-mono text-[10.5px]",
        inverted ? "border-current/25 text-current/70" : "border-line bg-surface-2 text-fg-3",
      )}
    >
      {children}
    </kbd>
  );
}
