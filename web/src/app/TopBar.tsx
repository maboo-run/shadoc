import { useState } from "react";
import { translate, type Locale } from "../i18n";
import { applyTheme, loadTheme, saveTheme, type Theme } from "../styles/theme";

type TopBarProps = {
  locale: Locale;
  activePage: string;
  onOpenCommandPalette(): void;
  onRefresh?(): void;
};

export function TopBar({ locale, activePage, onOpenCommandPalette, onRefresh }: TopBarProps) {
  const t = (source: string) => translate(locale, source);
  const [theme, setTheme] = useState<Theme>(() => {
    const initial = loadTheme();
    applyTheme(initial);
    return initial;
  });

  function toggleTheme() {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    saveTheme(next);
    applyTheme(next);
  }

  return (
    <div className="topbar" role="banner">
      <div className="topbar-breadcrumb" aria-label={t("当前位置")}>
        <strong>{t(activePage)}</strong>
      </div>
      <button
        type="button"
        className="cmd-palette-trigger"
        onClick={onOpenCommandPalette}
        aria-label={t("搜索页面、任务、仓库")}
      >
        <span className="cmd-prompt" aria-hidden="true">$</span>
        <span className="cmd-placeholder">{t("搜索页面、任务、仓库…")}</span>
        <kbd className="cmd-kbd" aria-hidden="true">⌘K</kbd>
      </button>
      <div className="topbar-actions">
        {onRefresh && (
          <button type="button" className="topbar-icon-button" onClick={onRefresh} aria-label={t("刷新")}>
            <span aria-hidden="true">↻</span>
          </button>
        )}
        <button
          type="button"
          className="topbar-icon-button theme-toggle"
          onClick={toggleTheme}
          aria-label={t("切换主题")}
          aria-pressed={theme === "light"}
        >
          <span aria-hidden="true">◐</span>
        </button>
      </div>
    </div>
  );
}
