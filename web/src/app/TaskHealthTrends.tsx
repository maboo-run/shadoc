import { useEffect, useState } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";

export type MetricCoverage = {
  duration?: number;
  filesProcessed?: number;
  filesChanged?: number;
  bytesProcessed?: number;
  bytesChanged?: number;
};

export type TrendWindow = {
  windowDays: number;
  windowStart: string;
  windowEnd: string;
  eligibleCount: number;
  completeSuccessCount: number;
  partialCount: number;
  failedCount: number;
  excludedCount: number;
  successRate?: number;
  retryCount: number;
  averageDurationMilliseconds?: number;
  p95DurationMilliseconds?: number;
  filesProcessed?: number;
  filesChanged?: number;
  bytesProcessed?: number;
  bytesChanged?: number;
  metricCoverage: MetricCoverage;
};

export type TrendDay = {
  date: string;
  eligibleCount: number;
  completeSuccessCount: number;
  partialCount: number;
  failedCount: number;
  excludedCount: number;
  retryCount: number;
  averageDurationMilliseconds?: number;
  filesChanged?: number;
  bytesChanged?: number;
  metricCoverage: MetricCoverage;
};

export type TaskTrend = {
  taskId: string;
  taskName: string;
  engine: string;
  latestCompleteSuccessAt?: string;
  windows: TrendWindow[];
  daily: TrendDay[];
};

export type TrendReport = {
  generatedAt: string;
  eligibleStatuses: string[];
  excludedStatuses: string[];
  tasks: TaskTrend[];
};

export function TaskHealthTrends({ api, locale, onOpenTask }: { api: AppAPI; locale: Locale; onOpenTask(taskId: string): void }) {
  const t = (source: string) => translate(locale, source);
  const [report, setReport] = useState<TrendReport | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    setError("");
    void api.action("/api/task-trends").then((value) => {
      if (!active) return;
      if (isTrendReport(value)) setReport(value);
      else setReport({ generatedAt: new Date().toISOString(), eligibleStatuses: ["success", "partial", "failed"], excludedStatuses: ["queued", "running", "cancelled", "skipped"], tasks: [] });
    }).catch((cause) => {
      if (active) setError(cause instanceof Error ? cause.message : t("无法读取任务健康趋势"));
    });
    return () => { active = false; };
  }, [api, locale]);

  return (
    <section className="content-section task-health-trends" aria-label={t("任务健康趋势")}>
      <div className="section-heading trend-heading">
        <div>
          <h2>{t("任务健康趋势")}</h2>
          <p>{denominatorExplanation(locale)}</p>
          {report && <p>{freshnessText(report.generatedAt, locale)}</p>}
        </div>
      </div>
      {!report && !error && <p role="status">{t("正在读取任务健康趋势…")}</p>}
      {error && <p className="error-message" role="alert">{error}</p>}
      {report && report.tasks.length === 0 && <p className="empty-state">{t("暂无趋势数据")}</p>}
      {report && report.tasks.length > 0 && <div className="table-frame"><table>
        <thead><tr>{["任务", "完整成功率", "最近完整成功", "趋势详情"].map((label) => <th key={label}>{t(label)}</th>)}</tr></thead>
        <tbody>{report.tasks.map((task) => {
          const window = task.windows.find((candidate) => candidate.windowDays === 30);
          if (!window) return null;
          return <tr key={task.taskId}>
            <td className="strong-cell">{task.taskName}</td>
            <td><span className="trend-primary">{successRateText(window, locale)}</span><small>{excludedText(window.excludedCount, locale)}</small></td>
            <td>{task.latestCompleteSuccessAt ? formatDateTime(task.latestCompleteSuccessAt, locale) : t("尚无完整成功")}</td>
            <td><button className="text-button" type="button" onClick={() => onOpenTask(task.taskId)}>{t("详情")}</button></td>
          </tr>;
        })}</tbody>
      </table></div>}
    </section>
  );
}

export function isTrendReport(value: unknown): value is TrendReport {
  if (!value || typeof value !== "object") return false;
  const candidate = value as Partial<TrendReport>;
  return typeof candidate.generatedAt === "string" && Array.isArray(candidate.tasks) && Array.isArray(candidate.eligibleStatuses) && Array.isArray(candidate.excludedStatuses);
}

function denominatorExplanation(locale: Locale) {
  return locale === "en-US"
    ? "The denominator includes only complete successes, partial successes, and failures. Queued, running, cancelled, and skipped runs are excluded."
    : "成功率分母只包含完整成功、部分成功和失败；等待、运行中、已取消和已跳过的记录被排除。";
}

function freshnessText(value: string, locale: Locale) {
  return locale === "en-US" ? `Data generated at ${formatDateTime(value, locale)}.` : `数据更新于 ${formatDateTime(value, locale)}。`;
}

function successRateText(window: TrendWindow, locale: Locale) {
  if (window.successRate == null || window.eligibleCount === 0) return translate(locale, "暂无可计入运行");
  const value = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(window.successRate);
  return locale === "en-US" ? `${value}% (${window.completeSuccessCount}/${window.eligibleCount})` : `${value}%（${window.completeSuccessCount}/${window.eligibleCount}）`;
}

function excludedText(count: number, locale: Locale) {
  return locale === "en-US" ? `${count} additional runs excluded` : `另排除 ${count} 次`;
}

function formatDateTime(value: string, locale: Locale) {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(parsed);
}
