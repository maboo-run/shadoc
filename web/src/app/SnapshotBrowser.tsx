import { useEffect, useRef, useState } from "react";
import { translate, type Locale } from "../i18n";
import { StatusIndicator } from "./StatusIndicator";

type SnapshotSummary = {
  id: string;
  time?: string;
  paths?: string[];
};

type SnapshotNode = {
  name: string;
  type: "dir" | "file";
  path: string;
  size?: number;
};

export type SnapshotContentsPage = {
  items: SnapshotNode[];
  path?: string;
  search?: string;
  recursive: boolean;
  truncated: boolean;
  nextCursor?: string;
};

type SnapshotChange = {
  path: string;
  change: "added" | "modified" | "removed";
  type: string;
  size?: number;
  previousSize?: number;
};

type SnapshotDiff = {
  fromSnapshotId: string;
  toSnapshotId: string;
  added: number;
  modified: number;
  removed: number;
  items: SnapshotChange[];
  examplesTruncated: boolean;
  incomplete: boolean;
};

type SnapshotBrowserProps = {
  api: { action(path: string, payload?: Record<string, unknown>): Promise<unknown> };
  repositoryID: string;
  snapshotID: string;
  sourcePath: string;
  snapshots: SnapshotSummary[];
  cachedPage?: SnapshotContentsPage;
  onPageChange?(page: SnapshotContentsPage): void;
  selectedIncludes: string[];
  onSelectedIncludesChange(value: string[]): void;
  locale: Locale;
};

export function SnapshotBrowser({ api, repositoryID, snapshotID, sourcePath, snapshots, cachedPage, onPageChange, selectedIncludes, onSelectedIncludesChange, locale }: SnapshotBrowserProps) {
  const t = (source: string) => translate(locale, source);
  const [page, setPage] = useState<SnapshotContentsPage>(cachedPage ?? { items: [], recursive: false, truncated: false });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const requestVersion = useRef(0);
  const onPageChangeRef = useRef(onPageChange);

  useEffect(() => { onPageChangeRef.current = onPageChange; }, [onPageChange]);

  useEffect(() => {
    if (!repositoryID || !snapshotID || !sourcePath) return;
    if (cachedPage) {
      setPage(cachedPage);
      setLoading(false);
      setError("");
      return;
    }
    const version = ++requestVersion.current;
    setPage({ items: [], recursive: false, truncated: false });
    setLoading(true);
    setError("");
    void loadSnapshotPage(api, repositoryID, snapshotID, sourcePath, "").then((value) => {
      if (requestVersion.current === version) {
        setPage(value);
        onPageChangeRef.current?.(value);
      }
    }).catch((reason) => {
      if (requestVersion.current === version) setError(reason instanceof Error ? reason.message : t("快照内容读取失败；仍可恢复整个快照目录"));
    }).finally(() => {
      if (requestVersion.current === version) setLoading(false);
    });
  }, [api, repositoryID, snapshotID, sourcePath, cachedPage]);

  const selectNode = (node: SnapshotNode, checked: boolean) => {
    const include = snapshotRelativePath(sourcePath, node.path);
    if (!include) return;
    onSelectedIncludesChange(checked
      ? [...new Set([...selectedIncludes, include])]
      : selectedIncludes.filter((item) => item !== include));
  };
  const loadMore = async () => {
    if (!page.nextCursor || loading) return;
    setLoading(true);
    setError("");
    try {
      const next = await loadSnapshotPage(api, repositoryID, snapshotID, sourcePath, page.nextCursor);
      const merged = { ...next, items: [...page.items, ...next.items] };
      setPage(merged);
      onPageChangeRef.current?.(merged);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("快照内容读取失败；仍可恢复整个快照目录"));
    } finally {
      setLoading(false);
    }
  };
  const reload = async () => {
    if (loading) return;
    const version = ++requestVersion.current;
    setLoading(true);
    setError("");
    try {
      const value = await loadSnapshotPage(api, repositoryID, snapshotID, sourcePath, "");
      if (requestVersion.current === version) {
        setPage(value);
        onPageChangeRef.current?.(value);
      }
    } catch (reason) {
      if (requestVersion.current === version) setError(reason instanceof Error ? reason.message : t("快照内容读取失败；仍可恢复整个快照目录"));
    } finally {
      if (requestVersion.current === version) setLoading(false);
    }
  };
  return <div className="snapshot-browser-content">
    <div className="snapshot-browser-toolbar">
      <p className="field-help">{t("不选择项目时恢复整个目录；选择后只恢复所选目录或文件。")}</p>
      <button type="button" className="secondary-button" disabled={loading} onClick={() => void reload()}>{t(loading ? "正在加载…" : "重新加载")}</button>
    </div>
    {loading && page.items.length === 0 && <p>{t("正在读取快照内容…")}</p>}
    {error && <p className="error-message" role="alert">{error}</p>}
    {!loading && !error && page.items.length === 0 && <p>{t("当前目录没有可浏览的项目")}</p>}
    <div className="snapshot-node-list" role="list">
      {page.items.map((node) => {
        const include = snapshotRelativePath(sourcePath, node.path);
        return <div key={`${node.type}:${node.path}`} className="snapshot-node-row" role="listitem">
          <label className="snapshot-node-select">
            <input type="checkbox" aria-label={`${t("选择")} ${node.path}`} checked={selectedIncludes.includes(include)} disabled={!include} onChange={(event) => selectNode(node, event.target.checked)} />
            <span>{node.type === "dir" ? t("目录") : t("文件")}</span>
          </label>
          <span className="snapshot-node-name">{node.name}</span>
          <span className="snapshot-node-path">{node.path}</span>
          <span className="snapshot-node-size">{node.size ? formatBytes(node.size, locale) : "—"}</span>
        </div>;
      })}
    </div>
    {page.truncated && <div className="snapshot-pagination" role="status">
      <span>{t("还有更多项目，当前列表不是完整结果。")}</span>
      <button type="button" className="secondary-button" disabled={loading || !page.nextCursor} onClick={() => void loadMore()}>{t(loading ? "正在加载…" : "加载更多")}</button>
    </div>}
  </div>;
}

export function SnapshotDiffPanel({ api, repositoryID, snapshotID, sourcePath, snapshots, locale }: Omit<SnapshotBrowserProps, "selectedIncludes" | "onSelectedIncludesChange">) {
  const t = (source: string) => translate(locale, source);
  const [baseline, setBaseline] = useState("");
  const [diff, setDiff] = useState<SnapshotDiff | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { setBaseline(""); setDiff(null); setError(""); }, [repositoryID, snapshotID]);
  const compare = async () => {
    if (!baseline) return;
    setLoading(true);
    setError("");
    try {
      const query = new URLSearchParams({ from: baseline, to: snapshotID, path: sourcePath, limit: "100" });
      setDiff(await api.action(`/api/repositories/${encodeURIComponent(repositoryID)}/snapshot-diff?${query.toString()}`) as SnapshotDiff);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("快照比较失败"));
    } finally {
      setLoading(false);
    }
  };
  return <section className="snapshot-diff" aria-label={t("快照差异")}>
    <h3>{t("快照差异")}</h3>
    <div className="snapshot-diff-controls">
      <label>{t("对比基线快照")}<select value={baseline} onChange={(event) => { setBaseline(event.target.value); setDiff(null); }}>
        <option value="">{t("请选择较早快照")}</option>
        {snapshots.filter((snapshot) => snapshot.id !== snapshotID).map((snapshot) => <option key={snapshot.id} value={snapshot.id}>{snapshot.id}{snapshot.time ? ` · ${new Intl.DateTimeFormat(locale, { dateStyle: "short", timeStyle: "short" }).format(new Date(snapshot.time))}` : ""}</option>)}
      </select></label>
      <button type="button" className="secondary-button" disabled={!baseline || loading} onClick={() => void compare()}>{t(loading ? "正在比较…" : "比较快照")}</button>
    </div>
    {error && <p className="error-message" role="alert">{error}</p>}
    {diff && <div className="snapshot-diff-result">
      <p className="strong-cell">{diffSummary(diff, locale)}</p>
      {diff.incomplete && <p className="error-message" role="alert">{t("比较达到安全上限，计数和样例都不完整。")}</p>}
      {!diff.incomplete && diff.examplesTruncated && <p>{t("变更样例已截断，但上方计数来自完整比较。")}</p>}
      <ul>{diff.items.map((item) => <li key={`${item.change}:${item.path}`}><StatusIndicator value="info" locale={locale} label={t(item.change === "added" ? "新增" : item.change === "modified" ? "修改" : "删除")} tone={item.change === "added" ? "success" : item.change === "removed" ? "warning" : "info"} variant="pill" /> {item.path}</li>)}</ul>
    </div>}
  </section>;
}

async function loadSnapshotPage(api: SnapshotBrowserProps["api"], repositoryID: string, snapshotID: string, path: string, cursor: string): Promise<SnapshotContentsPage> {
  const query = new URLSearchParams({ path, limit: "200" });
  if (cursor) query.set("cursor", cursor);
  return api.action(`/api/repositories/${encodeURIComponent(repositoryID)}/snapshots/${encodeURIComponent(snapshotID)}/contents?${query.toString()}`) as Promise<SnapshotContentsPage>;
}

export function snapshotRelativePath(source: string, path: string): string {
  const root = source.replace(/\/+$/, "");
  if (!root || path === root || !path.startsWith(`${root}/`)) return "";
  const relative = path.slice(root.length + 1);
  return relative.split("/").some((segment) => segment === ".." || segment === "") ? "" : relative;
}

function formatBytes(value: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, { style: "unit", unit: "byte", unitDisplay: "narrow", maximumFractionDigits: 0 }).format(value);
}

function diffSummary(diff: SnapshotDiff, locale: Locale): string {
  return locale === "en-US"
    ? `${diff.added} added · ${diff.modified} modified · ${diff.removed} deleted`
    : `新增 ${diff.added} · 修改 ${diff.modified} · 删除 ${diff.removed}`;
}
