import { useEffect, useMemo, useRef, useState } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";
import { StatusIndicator } from "./StatusIndicator";
import { isTrendReport, type TrendReport, type TrendWindow } from "./TaskHealthTrends";
import { RunDetailDialog } from "./RunHistoryPage";

type RunMetrics = {
  durationMilliseconds?: number;
  filesProcessed?: number;
  filesChanged?: number;
  bytesProcessed?: number;
  bytesChanged?: number;
};

type RunActivity = {
  recordType: "run";
  id: string;
  status: string;
  trigger?: string;
  occurredAt: string;
  startedAt?: string;
  finishedAt?: string;
  attemptCount: number;
  errorSummary?: string;
  metrics?: RunMetrics;
};

type ActivityPage = {
  items: RunActivity[];
  nextCursor?: string;
  truncated: boolean;
  generatedAt: string;
};

export function TaskHealthDetailPage({ taskId, api, locale, onBack }: { taskId: string; api: AppAPI; locale: Locale; onBack(): void }) {
  const t = (source: string) => translate(locale, source);
  const [report, setReport] = useState<TrendReport | null>(null);
  const [windowDays, setWindowDays] = useState(30);
  const [runs, setRuns] = useState<RunActivity[]>([]);
  const [nextCursor, setNextCursor] = useState("");
  const [loadingReport, setLoadingReport] = useState(true);
  const [loadingRuns, setLoadingRuns] = useState(false);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<Record<string, unknown> | null>(null);
  const [detailLog, setDetailLog] = useState("");
  const [detailError, setDetailError] = useState("");
  const [openingID, setOpeningID] = useState("");
  const requestSequence = useRef(0);
  const task = useMemo(() => report?.tasks.find((candidate) => candidate.taskId === taskId) ?? null, [report, taskId]);
  const window = task?.windows.find((candidate) => candidate.windowDays === windowDays) ?? null;

  useEffect(() => {
    let active = true;
    setLoadingReport(true);
    setError("");
    void api.action("/api/task-trends").then((value) => {
      if (!active) return;
      if (!isTrendReport(value)) throw new Error(t("任务趋势数据格式无效"));
      setReport(value);
    }).catch((cause) => {
      if (active) setError(cause instanceof Error ? cause.message : t("无法读取任务健康趋势"));
    }).finally(() => {
      if (active) setLoadingReport(false);
    });
    return () => { active = false; };
  }, [api, locale, taskId]);

  useEffect(() => {
    if (!window) return;
    setRuns([]);
    setNextCursor("");
    void loadRuns(window, "", false);
  }, [api, taskId, windowDays, window?.windowStart]);

  async function loadRuns(activeWindow: TrendWindow, cursor: string, append: boolean) {
    const requestID = ++requestSequence.current;
    setLoadingRuns(true);
    setError("");
    const query = new URLSearchParams();
    query.set("recordType", "run");
    query.set("objectId", taskId);
    query.set("from", activeWindow.windowStart);
    query.set("to", activeWindow.windowEnd);
    query.set("limit", "50");
    if (cursor) query.set("cursor", cursor);
    try {
      const value = await api.action(`/api/activity?${query.toString()}`) as ActivityPage;
      if (requestSequence.current !== requestID) return;
      const items = Array.isArray(value.items) ? value.items.filter((item) => item.recordType === "run") : [];
      setRuns((current) => append ? [...current, ...items] : items);
      setNextCursor(typeof value.nextCursor === "string" ? value.nextCursor : "");
    } catch (cause) {
      if (requestSequence.current === requestID) setError(cause instanceof Error ? cause.message : t("无法读取任务运行指标"));
    } finally {
      if (requestSequence.current === requestID) setLoadingRuns(false);
    }
  }

  async function openFailure(run: RunActivity) {
    setOpeningID(run.id);
    setDetailError("");
    setDetailLog("");
    try {
      const value = await api.runDetail(run.id);
      setDetail(value);
      if (!value.rawLogExpired) {
        try {
          setDetailLog(await api.runLog(run.id));
        } catch (cause) {
          setDetailError(cause instanceof Error ? cause.message : t("无法读取日志"));
        }
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法读取运行详情"));
    } finally {
      setOpeningID("");
    }
  }

  if (loadingReport) return <p role="status">{t("正在读取任务健康趋势…")}</p>;
  if (!task) return <><button className="back-button" type="button" aria-label={t("返回任务列表")} onClick={onBack}>← {t("返回任务列表")}</button><p className="error-message" role="alert">{error || t("未找到备份任务")}</p></>;

  return <div className="task-health-detail">
    <header className="page-header task-health-detail-heading">
      <div>
        <button className="back-button" type="button" aria-label={t("返回任务列表")} onClick={onBack}>← {t("返回任务列表")}</button>
        <h1>{task.taskName}</h1>
        <p>{t("查看任务在所选周期内的成功情况，以及每次运行实际处理和写入的数据。")}</p>
      </div>
      <div className="segmented-control trend-window-control" aria-label={t("统计窗口")}>
        {[7, 30, 90].map((days) => <button
          className={windowDays === days ? "selected" : ""}
          type="button"
          aria-pressed={windowDays === days}
          key={days}
          onClick={() => setWindowDays(days)}
        >{days === 7 ? t("过去 7 天") : days === 30 ? t("过去 30 天") : t("过去 90 天")}</button>)}
      </div>
    </header>

    {window && <section className="content-section task-health-stat-section" aria-label={t("健康趋势详情")}>
      <div className="section-heading"><div><h2>{t("健康趋势详情")}</h2><p>{t("成功率只计算已经结束且结果明确的运行。")}</p></div></div>
      <dl className="task-health-stat-grid">
        <Stat label={t("完整成功率")} value={successRateText(window, locale)} prominent />
        <Stat label={t("最近完整成功")} value={task.latestCompleteSuccessAt ? formatDateTime(task.latestCompleteSuccessAt, locale) : "—"} />
        <Stat label={t("平均耗时")} value={formatDurationMetric(window.averageDurationMilliseconds, locale)} />
        <Stat label="P95" value={formatDurationMetric(window.p95DurationMilliseconds, locale)} />
        <Stat label={t("重试")} value={new Intl.NumberFormat(locale).format(window.retryCount)} />
      </dl>
      <p className="task-health-window-note">{excludedText(window.excludedCount, locale)}</p>
    </section>}

    <section className="content-section task-health-run-section">
      <div className="section-heading">
        <div><h2>{t("逐次运行指标")}</h2><p>{t("横线表示该次运行没有采集到对应指标，不代表数值为零。")}</p></div>
      </div>
      {error && <p className="error-message" role="alert">{error}</p>}
      <div className="task-health-metric-key" aria-label={t("指标说明")}>
        <span><strong>{t("源数据量")}</strong>{t("本次运行扫描或处理的数据总量")}</span>
        <span><strong>{t("写入数据量")}</strong>{t("本次运行实际新增或传输的数据量")}</span>
        <span><strong>{t("处理文件数")}</strong>{t("本次运行检查过的文件数量")}</span>
        <span><strong>{t("变化文件数")}</strong>{t("本次运行新增或修改的文件数量")}</span>
      </div>
      <div className="table-frame"><table className="task-health-run-table" aria-label={`${task.taskName}${t("的逐次运行指标")}`}>
        <thead><tr>{["运行 ID", "开始时间", "状态", "触发", "失败原因", "源数据量", "写入数据量", "处理文件数", "变化文件数", "耗时", "重试"].map((label) => <th key={label}>{t(label)}</th>)}</tr></thead>
        <tbody>
          {runs.map((run) => <tr key={run.id}>
            <td><span className="technical-identifier">{run.id}</span></td>
            <td>{formatDateTime(run.startedAt || run.occurredAt, locale)}</td>
            <td><StatusIndicator value={run.status} locale={locale} /></td>
            <td>{triggerLabel(run.trigger, locale)}</td>
            <td className="task-health-run-error">{run.errorSummary
              ? <><span><strong>{t("失败原因")}</strong>{run.errorSummary}</span><button className="text-button" type="button" disabled={openingID === run.id} onClick={() => void openFailure(run)}>{t(openingID === run.id ? "正在读取…" : "查看失败详情")}</button></>
              : "—"}</td>
            <td>{formatBytesMetric(run.metrics?.bytesProcessed)}</td>
            <td>{formatBytesMetric(run.metrics?.bytesChanged)}</td>
            <td>{formatIntegerMetric(run.metrics?.filesProcessed, locale)}</td>
            <td>{formatIntegerMetric(run.metrics?.filesChanged, locale)}</td>
            <td>{formatDurationMetric(run.metrics?.durationMilliseconds, locale)}</td>
            <td>{new Intl.NumberFormat(locale).format(Math.max((run.attemptCount || 1) - 1, 0))}</td>
          </tr>)}
          {!runs.length && !loadingRuns && <tr><td className="empty-row" colSpan={11}>{t("所选周期内没有任务运行")}</td></tr>}
        </tbody>
      </table></div>
      <div className="task-health-pagination" role="status" aria-live="polite">
        {loadingRuns && <span>{t("正在读取任务运行指标…")}</span>}
        {nextCursor && window && <button className="secondary-button" type="button" disabled={loadingRuns} onClick={() => void loadRuns(window, nextCursor, true)}>{t("加载更早记录")}</button>}
      </div>
    </section>
    {detail && <RunDetailDialog recordType="run" detail={detail} log={detailLog} error={detailError} locale={locale} onClose={() => { setDetail(null); setDetailLog(""); setDetailError(""); }} />}
  </div>;
}

function Stat({ label, value, prominent = false }: { label: string; value: string; prominent?: boolean }) {
  return <div className={prominent ? "prominent" : ""}><dt>{label}</dt><dd>{value}</dd></div>;
}

function successRateText(window: TrendWindow, locale: Locale) {
  if (window.successRate == null || window.eligibleCount === 0) return "—";
  const value = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(window.successRate);
  return locale === "en-US" ? `${value}% (${window.completeSuccessCount}/${window.eligibleCount})` : `${value}%（${window.completeSuccessCount}/${window.eligibleCount}）`;
}

function excludedText(count: number, locale: Locale) {
  return locale === "en-US" ? `${count} queued, running, cancelled, or skipped runs excluded.` : `另排除 ${count} 次等待、运行中、已取消或已跳过的记录。`;
}

function triggerLabel(value: string | undefined, locale: Locale) {
  if (!value) return "—";
  return translate(locale, ({ manual: "手动触发", schedule: "计划调度", scheduled: "计划调度" } as Record<string, string>)[value] ?? "未知触发方式");
}

function formatDateTime(value: string, locale: Locale) {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium" }).format(parsed);
}

function formatIntegerMetric(value: number | undefined, locale: Locale) {
  return Number.isFinite(value) && Number(value) >= 0 ? new Intl.NumberFormat(locale).format(Number(value)) : "—";
}

function formatBytesMetric(value: number | undefined) {
  if (!Number.isFinite(value) || Number(value) < 0) return "—";
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let amount = Number(value);
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit += 1; }
  return `${amount.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatDurationMetric(value: number | undefined, locale: Locale) {
  if (!Number.isFinite(value) || Number(value) < 0) return "—";
  const milliseconds = Number(value);
  const units = locale === "en-US"
    ? { milliseconds: "ms", seconds: "s", minutes: "min" }
    : { milliseconds: "毫秒", seconds: "秒", minutes: "分钟" };
  if (milliseconds < 1_000) return `${new Intl.NumberFormat(locale).format(milliseconds)} ${units.milliseconds}`;
  if (milliseconds < 60_000) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(milliseconds / 1_000)} ${units.seconds}`;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(milliseconds / 60_000)} ${units.minutes}`;
}
