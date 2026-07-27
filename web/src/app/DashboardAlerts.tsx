import { translate, type Locale } from "../i18n";
import type { Dashboard } from "./App";
import { adminTime } from "./App";
import { formatEmbeddedDateTimes } from "./dateTime";

type DashboardAlertsProps = {
  dashboard: Dashboard;
  locale: Locale;
  timeZone: string;
  onViewAll(): void;
  onHandle?(targetPage: string): void;
};

export function DashboardAlerts({ dashboard, locale, timeZone, onViewAll, onHandle }: DashboardAlertsProps) {
  const t = (source: string) => translate(locale, source);
  const alerts = dashboard.alerts.slice(0, 3);

  return (
    <section className="dashboard-panel alerts-panel" aria-label={t("当前告警")}>
      <div className="panel-head">
        <h3 className="panel-title">{t("当前告警")}</h3>
        {dashboard.alerts.length > 0 && (
          <button type="button" className="panel-link" onClick={onViewAll}>{t("查看全部 ->")}</button>
        )}
      </div>
      {alerts.length === 0 ? (
        <p className="dashboard-empty">{t("当前无告警")}</p>
      ) : (
        <ul className="alert-card-list">
          {alerts.map((alert) => {
            const severity = alert.severity ?? "warning";
            const tone = severity === "critical" ? "crit" : severity === "info" ? "info" : "";
            const title = alert.objectName ?? alert.object ?? t("系统");
            return (
              <li className={`alert-card${tone ? ` ${tone}` : ""}`} key={alert.stateKey ?? alert.id ?? alert.message}>
                <div className="alert-card-heading">
                  <strong>{title}</strong>
                  <span className="alert-card-message">{formatEmbeddedDateTimes(alert.message, locale, timeZone)}</span>
                </div>
                {alert.lastAt && (
                  <div className="alert-card-time">▌ {adminTime(alert.lastAt, locale, timeZone)}</div>
                )}
                {alert.targetPage && onHandle && (
                  <button type="button" className="text-button alert-handle" onClick={() => onHandle(alert.targetPage!)}>{t("处理")}</button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
