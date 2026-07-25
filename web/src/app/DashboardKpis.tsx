import { translate, type Locale } from "../i18n";
import type { Dashboard } from "./App";

type DashboardKpisProps = {
  dashboard: Dashboard;
  agentOnline: number;
  agentTotal: number;
  locale: Locale;
};

export function DashboardKpis({ dashboard, agentOnline, agentTotal, locale }: DashboardKpisProps) {
  const t = (source: string) => translate(locale, source);
  const successRate = dashboard.runOverview?.successRate;
  const alertCount = dashboard.alerts.length;
  const repoAbnormal = dashboard.repositoryStatus === "abnormal";
  const successRateTone = successRate !== undefined && successRate < 80 ? " warn" : successRate !== undefined && successRate >= 95 ? " ok" : "";

  return (
    <>
      <section className="dashboard-kpis" aria-label={t("备份概览")}>
        <div className={`kpi-card${successRateTone}`} aria-label={t("成功率")}>
          <div className="kpi-card-label">{t("▌ 成功率")}</div>
          <div className={`kpi-card-value${successRateTone}`}>
            {successRate !== undefined ? `${successRate.toFixed(1)}%` : "-"}
          </div>
        </div>
        <div className={`kpi-card${alertCount > 0 ? " warn" : ""}`} aria-label={t("当前告警")}>
          <div className="kpi-card-label">{t("▌ 当前告警")}</div>
          <div className={`kpi-card-value${alertCount > 0 ? " warn" : " ok"}`}>{String(alertCount)}</div>
        </div>
        <div className={`kpi-card${repoAbnormal ? " warn" : ""}`} aria-label={t("仓库状态")}>
          <div className="kpi-card-label">{t("▌ 仓库状态")}</div>
          <div className={`kpi-card-value${repoAbnormal ? " warn" : " ok"}`}>{t(repoAbnormal ? "异常" : "正常")}</div>
        </div>
        <div className={`kpi-card${agentOnline < agentTotal ? " warn" : ""}`} aria-label={t("AGENT 在线")}>
          <div className="kpi-card-label">{t("▌ AGENT 在线")}</div>
          <div className={`kpi-card-value${agentOnline < agentTotal ? " warn" : " ok"}`}>{`${agentOnline}/${agentTotal}`}</div>
        </div>
      </section>
      <p className="dashboard-kpi-explanation">
        {t("成功率分母只包含完整成功、部分成功和失败；等待、运行中、已取消和已跳过的记录被排除。")}
      </p>
    </>
  );
}
