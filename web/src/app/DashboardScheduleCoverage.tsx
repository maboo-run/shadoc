import { translate, type Locale } from "../i18n";
import type { Dashboard } from "./App";

type DashboardScheduleCoverageProps = {
  dashboard: Dashboard;
  locale: Locale;
};

export function DashboardScheduleCoverage({ dashboard, locale }: DashboardScheduleCoverageProps) {
  const t = (source: string) => translate(locale, source);
  const items = dashboard.scheduleCoverage ?? [];

  if (!items.length) {
    return (
      <section className="dashboard-panel schedule-coverage-panel" aria-label={t("调度覆盖")}>
        <div className="panel-head">
          <h3 className="panel-title">{t("调度覆盖")}</h3>
        </div>
        <p className="dashboard-empty">{t("尚无调度覆盖数据")}</p>
      </section>
    );
  }

  return (
    <section className="dashboard-panel schedule-coverage-panel" aria-label={t("调度覆盖")}>
      <div className="panel-head">
        <h3 className="panel-title">{t("调度覆盖")}</h3>
      </div>
      <div className="gauge-list">
        {items.map((item) => {
          const warn = item.coveragePercent < 80;
          return (
            <div className={`gauge-row${warn ? " warn" : ""}`} key={item.planId} aria-label={item.planName}>
              <span className="gauge-label">{item.planName}</span>
              <div className="gauge-bar">
                <div className={`gauge-fill${warn ? " warn" : ""}`} style={{ width: `${item.coveragePercent}%` }} />
              </div>
              <span className="gauge-value">{`${item.coveragePercent.toFixed(0)}%`}</span>
            </div>
          );
        })}
      </div>
    </section>
  );
}
