export type Theme = "dark" | "light";

const storageKey = "shadoc.theme";
const legacyStorageKey = "restic-control.theme";
const supportedThemes: Theme[] = ["dark", "light"];

function isTheme(value: string | null): value is Theme {
  return value !== null && (supportedThemes as string[]).includes(value);
}

export function loadTheme(): Theme {
  const legacy = localStorage.getItem(legacyStorageKey);
  if (isTheme(legacy)) {
    localStorage.setItem(storageKey, legacy);
    localStorage.removeItem(legacyStorageKey);
    return legacy;
  }
  const stored = localStorage.getItem(storageKey);
  return isTheme(stored) ? stored : "dark";
}

export function saveTheme(theme: Theme): void {
  localStorage.setItem(storageKey, theme);
}

export function applyTheme(theme: Theme): void {
  document.documentElement.dataset.theme = theme;
}
