import { useEffect, useState, type FormEvent } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI, CompatibilityReport } from "./App";

type ToolPaths = {
  rsync: string;
  mysqlDump: string;
  mysqlRestore: string;
  postgresDump: string;
  postgresRestore: string;
};

const emptyPaths: ToolPaths = {
  rsync: "",
  mysqlDump: "",
  mysqlRestore: "",
  postgresDump: "",
  postgresRestore: "",
};

function reportFrom(value: unknown, invalidMessage: string): CompatibilityReport {
  if (!value || typeof value !== "object" || !Array.isArray((value as CompatibilityReport).findings)) {
    throw new Error(invalidMessage);
  }
  return value as CompatibilityReport;
}

export function LocalToolDetectionPanel({
  api,
  locale,
  report,
  onReport,
}: {
  api: AppAPI;
  locale: Locale;
  report: CompatibilityReport | null;
  onReport(report: CompatibilityReport): void;
}) {
  const t = (source: string) => translate(locale, source);
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState<"" | "reprobe" | "save">("");
  const [paths, setPaths] = useState<ToolPaths>(emptyPaths);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    setPaths({ ...emptyPaths, ...report?.configuredPaths });
  }, [report?.configuredPaths]);

  async function reprobe() {
    setBusy("reprobe");
    setMessage("");
    setError("");
    try {
      const refreshed = reportFrom(
        await api.action("/api/compatibility/reprobe", {}),
        t("本机工具检测返回了无效结果"),
      );
      onReport(refreshed);
      setMessage(t("本机工具检测已刷新"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法重新检测本机工具"));
    } finally {
      setBusy("");
    }
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy("save");
    setMessage("");
    setError("");
    try {
      const refreshed = reportFrom(
        await api.action("/api/compatibility/tool-paths", paths),
        t("本机工具检测返回了无效结果"),
      );
      onReport(refreshed);
      setMessage(t("工具路径已保存并重新检测"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法保存本机工具路径"));
    } finally {
      setBusy("");
    }
  }

  const fields: Array<{
    key: keyof ToolPaths;
    label: string;
    placeholder: string;
  }> = [
    { key: "rsync", label: t("rsync 路径"), placeholder: "/usr/bin/rsync" },
    { key: "mysqlDump", label: t("MySQL 备份客户端路径"), placeholder: "/usr/bin/mysqldump" },
    { key: "mysqlRestore", label: t("MySQL 恢复客户端路径"), placeholder: "/usr/bin/mysql" },
    { key: "postgresDump", label: t("PostgreSQL 备份客户端路径"), placeholder: "/usr/bin/pg_dump" },
    { key: "postgresRestore", label: t("PostgreSQL 恢复客户端路径"), placeholder: "/usr/bin/psql" },
  ];

  return (
    <section className="content-section local-tool-detection" aria-labelledby="local-tool-detection-title">
      <div className="local-tool-detection-toolbar">
        <div>
          <h2 id="local-tool-detection-title">{t("本机工具检测")}</h2>
          <p>{t("安装客户端后可立即重新检测，无需重启控制服务。")}</p>
        </div>
        <div className="local-tool-detection-actions">
          <button
            className="primary-button"
            type="button"
            disabled={busy !== ""}
            onClick={() => void reprobe()}
          >
            {t(busy === "reprobe" ? "正在检测…" : "重新检测本机工具")}
          </button>
          <button
            className="secondary-button"
            type="button"
            aria-expanded={expanded}
            aria-controls="local-tool-path-form"
            disabled={busy !== ""}
            onClick={() => setExpanded((value) => !value)}
          >
            {t(expanded ? "收起手动路径" : "手动指定工具路径")}
          </button>
        </div>
      </div>
      {message && <p className="success-message" role="status">{message}</p>}
      {error && <p className="error-message" role="alert">{error}</p>}
      {expanded && (
        <form id="local-tool-path-form" className="local-tool-path-form" onSubmit={(event) => void save(event)}>
          <div className="local-tool-path-heading">
            <strong>{t("手动指定工具路径")}</strong>
            <p>{t("自动检测失败时，可指定受支持客户端的绝对路径。")}</p>
          </div>
          <div className="local-tool-path-grid">
            {fields.map((field) => (
              <label key={field.key}>
                {field.label}
                <input
                  type="text"
                  value={paths[field.key]}
                  placeholder={field.placeholder}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(event) => setPaths((current) => ({ ...current, [field.key]: event.target.value }))}
                />
              </label>
            ))}
          </div>
          <div className="local-tool-path-footer">
            <p className="field-hint">{t("路径留空时恢复使用系统 PATH 自动检测。")}</p>
            <button className="primary-button" type="submit" disabled={busy !== ""}>
              {t(busy === "save" ? "正在保存…" : "保存路径并重新检测")}
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
