import { translate, type Locale } from "../i18n";
import type { DashboardTask } from "./App";
import { StatusIndicator } from "./StatusIndicator";
import { adminTime } from "./App";

type DashboardRecentRunsProps = {
  tasks: DashboardTask[];
  locale: Locale;
  timeZone: string;
  onViewAll(): void;
};

export function DashboardRecentRuns({ tasks, locale, timeZone, onViewAll }: DashboardRecentRunsProps) {
  const t = (source: string) => translate(locale, source);
  const recent = [...tasks].sort((left, right) => {
    const leftTime = runTimestamp(left.lastRun);
    const rightTime = runTimestamp(right.lastRun);
    if (leftTime === rightTime) return left.id.localeCompare(right.id);
    return rightTime - leftTime;
  }).slice(0, 5);

  return (
    <section className="dashboard-panel recent-runs-panel" aria-label={t("最近运行")}>
      <div className="panel-head">
        <h3 className="panel-title">{t("最近运行")}</h3>
        <button type="button" className="panel-link" onClick={onViewAll}>{t("查看全部 ->")}</button>
      </div>
      {recent.length === 0 ? (
        <p className="dashboard-empty">{t("尚未创建备份任务")}</p>
      ) : (
        <div className="recent-runs-table" role="table">
          <div className="recent-runs-row recent-runs-head" role="row">
            <span role="columnheader">{t("任务")}</span>
            <span role="columnheader">{t("状态")}</span>
            <span role="columnheader">{t("时间")}</span>
          </div>
          {recent.map((task) => (
            <div className="recent-runs-row" role="row" key={task.id}>
              <span role="cell" className="recent-runs-name">
                <span className="status-dot" aria-hidden="true" />
                {task.name}
              </span>
              <span role="cell"><StatusIndicator value={task.status} locale={locale} variant="pill" /></span>
              <span role="cell" className="mono">{adminTime(task.lastRun, locale, timeZone)}</span>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function runTimestamp(value: string): number {
  const timestamp = Date.parse(value);
  return Number.isNaN(timestamp) ? Number.NEGATIVE_INFINITY : timestamp;
}
