import { useEffect, useSyncExternalStore } from "react";

export type ThemeMode = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const KEY = "sc-theme";
const media = window.matchMedia("(prefers-color-scheme: dark)");
const listeners = new Set<() => void>();

function readMode(): ThemeMode {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    /* storage unavailable */
  }
  return "system";
}

let mode = readMode();

const resolve = (m: ThemeMode): ResolvedTheme => (m === "system" ? (media.matches ? "dark" : "light") : m);

/** Mirrors the inline script in index.html, which runs before first paint. */
function apply() {
  const root = document.documentElement;
  const dark = resolve(mode) === "dark";
  if (root.classList.contains("dark") === dark) return;
  // Without this, every element with a `transition` class fades at its own pace and the
  // page flickers through half-themed states.
  root.classList.add("theme-switching");
  root.classList.toggle("dark", dark);
  root.style.colorScheme = dark ? "dark" : "light";
  // Force the new colors to resolve, then re-enable transitions. Not rAF: it never fires while
  // the window is hidden in the tray, which is exactly when an OS theme change tends to land.
  void getComputedStyle(root).color;
  setTimeout(() => root.classList.remove("theme-switching"), 1);
}

media.addEventListener("change", () => {
  if (mode !== "system") return;
  apply();
  listeners.forEach((l) => l());
});

export function setThemeMode(m: ThemeMode) {
  mode = m;
  try {
    localStorage.setItem(KEY, m);
  } catch {
    /* storage unavailable */
  }
  apply();
  listeners.forEach((l) => l());
}

const subscribe = (cb: () => void) => {
  listeners.add(cb);
  return () => listeners.delete(cb);
};

export function useTheme() {
  const m = useSyncExternalStore(subscribe, () => mode);
  const resolved = useSyncExternalStore(subscribe, () => resolve(mode));
  useEffect(apply, []);
  return { mode: m, resolved, setMode: setThemeMode };
}
