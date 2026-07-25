import { translate, type Locale } from "../i18n";
import type { Dashboard } from "./App";

type DashboardTrendChartProps = {
  dashboard: Dashboard;
  locale: Locale;
};

export function DashboardTrendChart({ dashboard, locale }: DashboardTrendChartProps) {
  const t = (source: string) => translate(locale, source);
  const coverage = dashboard.scheduleCoverage ?? [];

  if (!coverage.length) {
    return (
      <section className="dashboard-panel trend-chart-panel">
        <div className="panel-head">
          <h3 className="panel-title">{t("运行趋势 · 14 天")}</h3>
        </div>
        <div className="trend-chart-empty" role="img" aria-label={t("运行趋势")}>
          <p className="dashboard-empty">{t("暂无趋势数据")}</p>
        </div>
      </section>
    );
  }

  const max = Math.max(...coverage.map((c) => c.coveragePercent), 100);
  const bars = coverage.slice(0, 14).map((c) => {
    const height = Math.max(4, (c.coveragePercent / max) * 100);
    const tone = c.coveragePercent < 60 ? "bad" : c.coveragePercent < 80 ? "warn" : "";
    return { height, tone, label: c.planName, value: c.coveragePercent };
  });
  while (bars.length < 14) bars.unshift({ height: 0, tone: "dim", label: "", value: 0 });

  return (
    <section className="dashboard-panel trend-chart-panel">
      <div className="panel-head">
        <h3 className="panel-title">{t("运行趋势 · 14 天")}</h3>
      </div>
      <div className="trend-chart" role="img" aria-label={t("运行趋势")}>
        {bars.map((bar, idx) => (
          <div
            key={idx}
            className={`trend-bar${bar.tone ? ` ${bar.tone}` : ""}`}
            style={{ height: `${bar.height}%` }}
            title={bar.label ? `${bar.label}: ${bar.value.toFixed(1)}%` : ""}
          />
        ))}
      </div>
      <div className="trend-chart-x">
        <span>{t("14 天")}</span>
        <span>{t("7 天")}</span>
        <span>{t("今天")}</span>
      </div>
    </section>
  );
}
