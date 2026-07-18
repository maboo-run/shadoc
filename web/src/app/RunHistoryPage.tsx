import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";
import { ModalPortal } from "./ModalPortal";
import { Toast } from "./Toast";
import { useModalFocus } from "./useModalFocus";
import { StatusIndicator, statusLabel } from "./StatusIndicator";

type ActivityMetrics = {
  durationMilliseconds?: number;
  filesProcessed?: number;
  filesChanged?: number;
  bytesProcessed?: number;
  bytesChanged?: number;
};

type ActivityItem = {
  recordType: "run" | "operation";
  id: string;
  kind: string;
  engine?: string;
  status: string;
  trigger?: string;
  objectType?: string;
  objectId?: string;
  objectName?: string;
  occurredAt: string;
  startedAt?: string;
  finishedAt?: string;
  attemptCount: number;
  errorSummary?: string;
  metrics?: ActivityMetrics;
};

type ActivityPage = {
  items: ActivityItem[];
  nextCursor?: string;
  truncated: boolean;
  page?: number;
  pageSize?: number;
  total?: number;
  generatedAt: string;
};

type ActivityFilters = {
  recordType: string;
  objectId: string;
  engine: string;
  kind: string;
  status: string;
  trigger: string;
  from: string;
  to: string;
  limit: string;
};

const emptyFilters: ActivityFilters = {
  recordType: "",
  objectId: "",
  engine: "",
  kind: "",
  status: "",
  trigger: "",
  from: "",
  to: "",
  limit: "50",
};

const statuses = ["queued", "running", "success", "partial", "failed", "cancelled", "cleanup_required", "skipped", "blocker", "interrupted"];

export function RunHistoryPage({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const initial = useRef(filtersFromLocation());
  const [draft, setDraft] = useState<ActivityFilters>(initial.current);
  const [applied, setApplied] = useState<ActivityFilters>(initial.current);
  const [pageNumber, setPageNumber] = useState(pageFromLocation());
  const [jumpTarget, setJumpTarget] = useState(String(pageFromLocation()));
  const [page, setPage] = useState<ActivityPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [detail, setDetail] = useState<Record<string, unknown> | null>(null);
  const [detailRecordType, setDetailRecordType] = useState<"run" | "operation">("run");
  const [log, setLog] = useState("");
  const [detailError, setDetailError] = useState("");
  const [openingID, setOpeningID] = useState("");
  const [message, setMessage] = useState("");
  const requestSequence = useRef(0);

  useEffect(() => {
    const requestID = ++requestSequence.current;
    setLoading(true);
    setLoadError("");
    setPage(null);
    const query = activityQuery(applied, pageNumber, true);
    void (api.action(`/api/activity?${query}`) as Promise<ActivityPage>)
      .then((value) => {
        if (requestSequence.current !== requestID) return;
        setPage({ ...value, items: Array.isArray(value.items) ? value.items : [] });
      })
      .catch((cause) => {
        if (requestSequence.current !== requestID) return;
        setPage(null);
        setLoadError(cause instanceof Error ? cause.message : t("无法读取运行记录"));
      })
      .finally(() => {
        if (requestSequence.current === requestID) setLoading(false);
      });
  }, [api, applied, pageNumber, locale]);

  useEffect(() => {
    const restoreFilters = () => {
      const next = filtersFromLocation();
      const nextPage = pageFromLocation();
      setDraft(next);
      setApplied(next);
      setPageNumber(nextPage);
      setJumpTarget(String(nextPage));
    };
    window.addEventListener("popstate", restoreFilters);
    return () => window.removeEventListener("popstate", restoreFilters);
  }, []);

  useEffect(() => {
    if (loading || page?.total == null) return;
    const availablePages = Math.max(1, Math.ceil(page.total / Number(applied.limit)));
    if (pageNumber <= availablePages) return;
    setPageNumber(availablePages);
    setJumpTarget(String(availablePages));
    const query = activityQuery(applied, availablePages, true);
    window.history.replaceState({}, "", `/admin/runs${query ? `?${query}` : ""}`);
  }, [applied, loading, page, pageNumber]);

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    const next = normalizeFilters(draft);
    setDraft(next);
    setApplied(next);
    setPageNumber(1);
    setJumpTarget("1");
    const query = activityQuery(next, 1, true);
    window.history.pushState({}, "", `/admin/runs${query ? `?${query}` : ""}`);
  }

  function resetFilters() {
    const next = { ...emptyFilters };
    setDraft(next);
    setApplied(next);
    setPageNumber(1);
    setJumpTarget("1");
    window.history.pushState({}, "", "/admin/runs");
  }

  function openPage(nextPage: number) {
    const bounded = Math.min(pageCount, Math.max(1, nextPage));
    if (bounded === pageNumber || loading) return;
    setPageNumber(bounded);
    setJumpTarget(String(bounded));
    const query = activityQuery(applied, bounded, true);
    window.history.pushState({}, "", `/admin/runs${query ? `?${query}` : ""}`);
  }

  function jumpToPage(event: FormEvent) {
    event.preventDefault();
    const requested = Number.parseInt(jumpTarget, 10);
    if (!Number.isFinite(requested)) return;
    openPage(requested);
  }

  async function openDetail(item: ActivityItem) {
    setOpeningID(item.id);
    setDetailError("");
    setLog("");
    try {
      const value = item.recordType === "run"
        ? await api.runDetail(item.id)
        : await api.action(`/api/operations/${encodeURIComponent(item.id)}`) as Record<string, unknown>;
      setDetailRecordType(item.recordType);
      setDetail(value);
      if (item.recordType === "run" && !value.rawLogExpired) {
        try {
          setLog(await api.runLog(item.id));
        } catch (cause) {
          setDetailError(cause instanceof Error ? cause.message : t("无法读取日志"));
        }
      }
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : t("无法读取运行详情"));
    } finally {
      setOpeningID("");
    }
  }

  const exportQuery = activityQuery(applied, 0, false);
  const items = page?.items ?? [];
  const pageSize = Number(applied.limit);
  const total = Math.max(0, Number(page?.total ?? items.length));
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  return (
    <>
      <header className="run-history-toolbar">
        <p>{t("统一查看备份任务运行与持久化操作；筛选由服务器执行，原始日志只在打开单条详情时读取。")}</p>
        <a className="secondary-button" href={`/api/activity/export${exportQuery ? `?${exportQuery}` : ""}`}>{t("导出当前筛选")}</a>
      </header>
      <section className="content-section run-history-page">
        <form className="run-history-filters" onSubmit={applyFilters}>
          <label>{t("记录类型")}
            <select value={draft.recordType} onChange={(event) => setDraft({ ...draft, recordType: event.target.value })}>
              <option value="">{t("全部记录")}</option>
              <option value="run">{t("任务运行")}</option>
              <option value="operation">{t("持久化操作")}</option>
            </select>
          </label>
          <label>{t("对象 ID")}<input value={draft.objectId} maxLength={256} onChange={(event) => setDraft({ ...draft, objectId: event.target.value })} /></label>
          <label>{t("引擎筛选")}
            <select value={draft.engine} onChange={(event) => setDraft({ ...draft, engine: event.target.value })}>
              <option value="">{t("全部引擎")}</option>
              <option value="restic">Restic</option>
              <option value="rsync">rsync</option>
            </select>
          </label>
          <label>{t("操作类型筛选")}<input value={draft.kind} maxLength={256} placeholder={t("例如 backup 或 directory_restore")} onChange={(event) => setDraft({ ...draft, kind: event.target.value })} /></label>
          <label>{t("状态筛选")}
            <select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}>
              <option value="">{t("全部状态")}</option>
              {statuses.map((value) => <option key={value} value={value}>{statusLabel(value, locale)}</option>)}
            </select>
          </label>
          <label>{t("触发方式")}
            <select value={draft.trigger} onChange={(event) => setDraft({ ...draft, trigger: event.target.value })}>
              <option value="">{t("全部触发方式")}</option>
              <option value="manual">{t("手动触发")}</option>
              <option value="schedule">{t("计划调度")}</option>
            </select>
          </label>
          <label>{t("开始时间")}<input type="datetime-local" value={dateTimeInputValue(draft.from)} onChange={(event) => setDraft({ ...draft, from: dateTimeQueryValue(event.target.value) })} /></label>
          <label>{t("结束时间")}<input type="datetime-local" value={dateTimeInputValue(draft.to)} onChange={(event) => setDraft({ ...draft, to: dateTimeQueryValue(event.target.value) })} /></label>
          <label>{t("每页数量")}
            <select value={draft.limit} onChange={(event) => setDraft({ ...draft, limit: event.target.value })}>
              {[25, 50, 100, 200].map((value) => <option key={value} value={value}>{value}</option>)}
            </select>
          </label>
          <div className="run-history-filter-actions">
            <button className="primary-button" type="submit" disabled={loading}>{t("应用筛选")}</button>
            <button className="secondary-button" type="button" disabled={loading} onClick={resetFilters}>{t("重置筛选")}</button>
          </div>
        </form>

        <div className="run-history-page-state" role="status" aria-live="polite">
          {loading
            ? t("正在读取统一活动记录…")
            : loadError || activityFreshness(page, locale)}
        </div>
        {loadError && <p className="error-message" role="alert">{loadError}</p>}
        <div className="table-frame"><table className="run-history-table">
          <thead><tr>{["记录 ID", "记录类型", "类型", "引擎", "状态", "触发", "关联对象", "开始", "结束", "尝试", "指标与错误", "操作"].map((label) => <th key={label}>{t(label)}</th>)}</tr></thead>
          <tbody>
            {items.map((item) => <tr key={`${item.recordType}:${item.id}`}>
              <td><span className="technical-identifier">{item.id}</span></td>
              <td>{item.recordType === "run" ? t("任务运行") : t("持久化操作")}</td>
              <td>{kindLabel(item.kind, locale)}</td>
              <td>{engineLabel(item.engine, locale)}</td>
              <td><ActivityStatus value={item.status} locale={locale} /></td>
              <td>{triggerLabel(item.trigger, locale)}</td>
              <td><span className="run-history-object">{item.objectName || item.objectId || "—"}{item.objectId && item.objectName !== item.objectId ? <small>{item.objectId}</small> : null}</span></td>
              <td>{formatDateTime(item.startedAt || item.occurredAt, locale)}</td>
              <td>{formatDateTime(item.finishedAt, locale)}</td>
              <td>{new Intl.NumberFormat(locale).format(item.attemptCount ?? 0)}</td>
              <td><ActivityEvidence item={item} locale={locale} /></td>
              <td><button className="text-button" type="button" disabled={openingID === item.id} onClick={() => void openDetail(item)}>{openingID === item.id ? t("正在读取…") : t("查看详情")}</button></td>
            </tr>)}
            {!loading && !loadError && items.length === 0 && <tr><td className="empty-row" colSpan={12}>{t("没有符合筛选条件的运行记录")}</td></tr>}
          </tbody>
        </table></div>
        <nav className="run-history-pagination" aria-label={t("运行记录分页")}>
          <span className="run-history-page-summary">{locale === "en-US" ? `${total} total · Page ${pageNumber}/${pageCount}` : `共 ${total} 条 · 第 ${pageNumber}/${pageCount} 页`}</span>
          <div className="run-history-page-buttons">
            <button className="run-history-page-step" type="button" disabled={pageNumber <= 1 || loading} onClick={() => openPage(pageNumber - 1)}><span aria-hidden="true">‹</span>{t("上一页")}</button>
            {paginationWindow(pageNumber, pageCount).map((item) => typeof item === "number"
              ? <button key={item} className="run-history-page-number" type="button" aria-current={item === pageNumber ? "page" : undefined} disabled={loading} onClick={() => openPage(item)}>{item}</button>
              : <span key={item} className="run-history-page-ellipsis" aria-hidden="true">…</span>)}
            <button className="run-history-page-step" type="button" disabled={pageNumber >= pageCount || loading} onClick={() => openPage(pageNumber + 1)}>{t("下一页")}<span aria-hidden="true">›</span></button>
          </div>
          <form className="run-history-page-jump" onSubmit={jumpToPage}>
            <label>{t("跳转至")}<input aria-label={t("跳转页码")} type="number" min="1" max={pageCount} value={jumpTarget} onChange={(event) => setJumpTarget(event.target.value)} /></label>
            <span>{t("页")}</span>
            <button className="secondary-button" type="submit" disabled={loading}>{t("跳转")}</button>
          </form>
        </nav>
      </section>
      <Toast message={message} locale={locale} onClose={() => setMessage("")} />
      {detail && <RunDetailDialog recordType={detailRecordType} detail={detail} log={log} error={detailError} locale={locale} onClose={() => { setDetail(null); setLog(""); setDetailError(""); }} />}
    </>
  );
}

function ActivityEvidence({ item, locale }: { item: ActivityItem; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const metrics = item.metrics;
  if (!metrics && !item.errorSummary) return <>—</>;
  return <span className="run-history-evidence">
    {metrics?.durationMilliseconds != null && <small>{t("耗时")} {formatDuration(metrics.durationMilliseconds, locale)}</small>}
    {metrics?.bytesChanged != null && <small>{t("变化数据")} <strong>{formatBytes(metrics.bytesChanged)}</strong></small>}
    {metrics?.bytesProcessed != null && <small>{t("处理数据")} <strong>{formatBytes(metrics.bytesProcessed)}</strong></small>}
    {metrics?.filesChanged != null && <small>{t("变化文件")} {new Intl.NumberFormat(locale).format(metrics.filesChanged)}</small>}
    {metrics?.filesProcessed != null && <small>{t("处理文件")} {new Intl.NumberFormat(locale).format(metrics.filesProcessed)}</small>}
    {item.errorSummary && <span className="run-history-error">{item.errorSummary}</span>}
  </span>;
}

function ActivityStatus({ value, locale }: { value: string; locale: Locale }) {
  return <StatusIndicator value={value} locale={locale} />;
}

export function RunDetailDialog({ recordType, detail, log, error, locale, onClose }: { recordType: "run" | "operation"; detail: Record<string, unknown>; log: string; error: string; locale: Locale; onClose(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleID = useId();
  useModalFocus(dialogRef, onClose);
  return <ModalPortal>
    <div ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby={titleID}>
      <header><div><h2 id={titleID}>{t("运行详情")}</h2><p>{t("查看结构化执行结果与脱敏日志。")}</p></div></header>
      <div className="dialog-body">
        <dl className="run-detail-overview">
          <div><dt>{t("记录 ID")}</dt><dd><span className="technical-identifier">{String(detail.id ?? "—")}</span></dd></div>
          <div><dt>{t("状态")}</dt><dd><StatusIndicator value={String(detail.status ?? "unknown")} locale={locale} /></dd></div>
          <div><dt>{t("阶段")}</dt><dd>{stageLabel(detail.stage, locale)}</dd></div>
          <div><dt>{t("尝试次数")}</dt><dd>{detailValue(detail.attemptCount, locale)}</dd></div>
        </dl>
        <section className="run-detail-summary" aria-labelledby={`${titleID}-summary`}>
          <h3 id={`${titleID}-summary`}>{t("摘要")}</h3>
          <StructuredDetail value={detail.summary ?? detail.detail ?? detail.errorSummary} locale={locale} />
        </section>
        {recordType === "run" && detail.rawLogExpired
          ? <p className="warning-text">{t("原始日志已按生命周期策略过期。")}</p>
          : recordType === "run" && log
            ? <><pre className="log-view">{log}</pre><a className="secondary-button" href={`/api/runs/${encodeURIComponent(String(detail.id))}/log?download=1`}>{t("下载日志")}</a></>
            : <p>{recordType === "run" ? t("此运行没有日志正文。") : t("持久化操作只展示结构化详情，不提供原始日志。")}</p>}
        {error && <p className="error-message" role="alert">{error}</p>}
      </div>
      <footer><button className="secondary-button" type="button" onClick={onClose}>{t("关闭详情")}</button></footer>
    </div>
  </ModalPortal>;
}

function filtersFromLocation(): ActivityFilters {
  const query = new URLSearchParams(window.location.search);
  const limit = ["25", "50", "100", "200"].includes(query.get("limit") ?? "") ? String(query.get("limit")) : emptyFilters.limit;
  return normalizeFilters({
    recordType: query.get("recordType") ?? "",
    objectId: query.get("objectId") ?? "",
    engine: query.get("engine") ?? "",
    kind: query.get("kind") ?? "",
    status: query.get("status") ?? "",
    trigger: query.get("trigger") ?? "",
    from: validDateQuery(query.get("from")),
    to: validDateQuery(query.get("to")),
    limit,
  });
}

function pageFromLocation(): number {
  const value = Number.parseInt(new URLSearchParams(window.location.search).get("page") ?? "1", 10);
  return Number.isFinite(value) && value > 0 ? value : 1;
}

function normalizeFilters(value: ActivityFilters): ActivityFilters {
  return {
    recordType: value.recordType === "run" || value.recordType === "operation" ? value.recordType : "",
    objectId: value.objectId.trim(),
    engine: value.engine.trim(),
    kind: value.kind.trim(),
    status: value.status.trim(),
    trigger: value.trigger.trim(),
    from: validDateQuery(value.from),
    to: validDateQuery(value.to),
    limit: ["25", "50", "100", "200"].includes(value.limit) ? value.limit : emptyFilters.limit,
  };
}

function activityQuery(filters: ActivityFilters, page: number, includeLimit: boolean): string {
  const query = new URLSearchParams();
  if (filters.recordType) query.set("recordType", filters.recordType);
  if (filters.objectId) query.set("objectId", filters.objectId);
  if (filters.engine) query.set("engine", filters.engine);
  if (filters.kind) query.set("kind", filters.kind);
  if (filters.status) query.set("status", filters.status);
  if (filters.trigger) query.set("trigger", filters.trigger);
  if (filters.from) query.set("from", filters.from);
  if (filters.to) query.set("to", filters.to);
  if (includeLimit) {
    query.set("limit", filters.limit);
    if (page > 0) query.set("page", String(page));
  }
  return query.toString();
}

function paginationWindow(current: number, total: number): Array<number | string> {
  if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
  const pages = new Set([1, total, current - 2, current - 1, current, current + 1, current + 2]);
  const visible = [...pages].filter((value) => value >= 1 && value <= total).sort((left, right) => left - right);
  const result: Array<number | string> = [];
  visible.forEach((value, index) => {
    const previous = visible[index - 1];
    if (previous != null && value - previous > 1) result.push(`ellipsis-${previous}`);
    result.push(value);
  });
  return result;
}

function validDateQuery(value: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "" : date.toISOString();
}

function dateTimeInputValue(value: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function dateTimeQueryValue(value: string): string {
  return value ? validDateQuery(new Date(value).toISOString()) : "";
}

function activityFreshness(page: ActivityPage | null, locale: Locale): string {
  if (!page) return "";
  const generated = formatDateTime(page.generatedAt, locale);
  if (page.truncated) return locale === "en-US" ? `Generated ${generated}. More matching records are available.` : `数据生成于 ${generated}；仍有更早的匹配记录。`;
  return locale === "en-US" ? `Generated ${generated}. This is the end of the matching history.` : `数据生成于 ${generated}；已到匹配历史末尾。`;
}

function formatDateTime(value: string | undefined, locale: Locale): string {
  if (!value) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium" }).format(date);
}

function formatBytes(value: number): string {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatDuration(milliseconds: number, locale: Locale): string {
  if (milliseconds < 1_000) return `${new Intl.NumberFormat(locale).format(milliseconds)} ms`;
  if (milliseconds < 60_000) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(milliseconds / 1_000)} s`;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(milliseconds / 60_000)} min`;
}

function triggerLabel(value: string | undefined, locale: Locale): string {
  if (!value) return "—";
  return translate(locale, ({ manual: "手动触发", schedule: "计划调度", scheduled: "计划调度" } as Record<string, string>)[value] ?? "未知触发方式");
}

function kindLabel(value: string, locale: Locale): string {
  const source = ({ backup: "备份", repository_initialize: "仓库初始化", repository_connect: "连接已有仓库", repository_verify_existing: "验证已有仓库", repository_capacity_probe: "仓库容量检测", repository_maintenance: "仓库维护", repository_password_rotation: "仓库密码轮换", restic_install: "Restic 安装", directory_restore: "目录恢复", database_restore: "数据库恢复", restore_verification: "恢复验证", restore_verification_cleanup: "恢复验证清理", control_plane_import: "控制面恢复导入", protection_setup: "创建保护", agent_deploy: "部署 Agent", agent_uninstall: "卸载 Agent", agent_upgrade: "升级 Agent", agent_restic_install: "Agent Restic 安装", application_update: "应用升级", task_run: "备份任务运行" } as Record<string, string>)[value] ?? "其他操作";
  return translate(locale, source);
}

function engineLabel(value: string | undefined, locale: Locale): string {
  if (!value) return "—";
  return translate(locale, ({ restic: "Restic", rsync: "rsync", mysql: "MySQL", postgresql: "PostgreSQL" } as Record<string, string>)[value] ?? "未知类型");
}

function stageLabel(value: unknown, locale: Locale): string {
  if (!value) return "—";
  const source = ({
    queued: "等待执行",
    starting: "正在启动",
    initializing: "正在初始化仓库",
    restoring: "正在恢复",
    cleanup: "正在检查残留",
    probing: "正在探测目标服务器",
    probing_capacity: "正在读取仓库存储容量",
    waiting_for_heartbeat: "正在等待 Agent 注册和心跳",
    downloading_agent_restic: "正在下载并校验 Agent Restic",
    staging_agent_restic: "正在暂存 Agent Restic",
    activating_agent_restic: "正在切换 Restic 并重启 Agent",
    waiting_for_agent_restic: "正在验证 Agent Restic 能力心跳",
    rolling_back_agent_restic: "正在恢复旧版 Agent Restic",
    agent_restic_verified: "Agent Restic 能力已验证",
    verifying_read_only: "正在只读验证已有仓库",
    completed: "已完成",
  } as Record<string, string>)[String(value)] ?? "其他阶段";
  return translate(locale, source);
}

function detailValue(value: unknown, locale: Locale): string {
  if (value == null || value === "") return "—";
  if (Array.isArray(value)) return value.length ? value.map(String).join(locale === "en-US" ? ", " : "、") : "—";
  if (typeof value === "object") return translate(locale, "结构化详情");
  return String(value);
}

function StructuredDetail({ value, locale }: { value: unknown; locale: Locale }): ReactNode {
  if (value == null || value === "") return <p>—</p>;
  if (Array.isArray(value)) {
    if (!value.length) return <p>—</p>;
    return <ul className="structured-detail-list">{value.map((item, index) => <li key={index}>{detailValue(item, locale)}</li>)}</ul>;
  }
  if (typeof value !== "object") return <p>{String(value)}</p>;
  const entries = Object.entries(value as Record<string, unknown>);
  if (!entries.length) return <p>—</p>;
  return <dl className="structured-detail-grid">{entries.map(([key, item]) => <div key={key}>
    <dt>{detailFieldLabel(key, locale)}</dt>
    <dd>{detailValue(item, locale)}</dd>
  </div>)}</dl>;
}

function detailFieldLabel(key: string, locale: Locale): string {
  const known: Record<string, string> = {
    error: "错误",
    message: "说明",
    snapshotId: "快照 ID",
    snapshot_id: "快照 ID",
    filesChanged: "变化文件",
    files_changed: "变化文件",
    filesProcessed: "处理文件",
    files_processed: "处理文件",
    bytesChanged: "变化数据",
    bytes_changed: "变化数据",
    durationMilliseconds: "耗时",
    duration_milliseconds: "耗时",
    exitCode: "退出码",
    exit_code: "退出码",
  };
  if (known[key]) return translate(locale, known[key]);
  const readable = key.replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/[_-]+/g, " ").trim();
  return readable ? readable.charAt(0).toUpperCase() + readable.slice(1) : translate(locale, "其他信息");
}
