import { useEffect, useState } from "react";
import type { AppAPI } from "./App";
import { translate, type Locale } from "../i18n";

export type ApplicationReleaseState = {
  currentVersion: string;
  latest: {
    version: string;
    publishedAt: string;
    summary: string;
    compatible: boolean;
    platform: string;
  };
  updateAvailable: boolean;
  managed: boolean;
};

export function ApplicationVersionStatus({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [version, setVersion] = useState("");
  const [updateAvailable, setUpdateAvailable] = useState(false);

  useEffect(() => {
    let active = true;

    void api.applicationVersion().then((value) => {
      if (active) setVersion(value.version);
    }).catch(() => undefined);

    void api.applicationReleases().then((value) => {
      if (!active) return;
      setVersion(value.currentVersion);
      setUpdateAvailable(value.updateAvailable);
    }).catch(() => undefined);

    return () => { active = false; };
  }, [api]);

  return (
    <div className="sidebar-version" aria-live="polite">
      <span>{t("当前应用版本")}</span>
      <strong>{version || t("正在读取…")}</strong>
      {updateAvailable && (
        <span className="sidebar-version-notice">
          <span className="status-dot" aria-hidden="true" />
          {t("当前有新版本")}
        </span>
      )}
    </div>
  );
}
