import { useEffect, useState } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI, Dashboard as DashboardData } from "./App";
import { DashboardKpis } from "./DashboardKpis";
import { DashboardTrendChart } from "./DashboardTrendChart";
import { DashboardScheduleCoverage } from "./DashboardScheduleCoverage";
import { DashboardRecentRuns } from "./DashboardRecentRuns";
import { DashboardAlerts } from "./DashboardAlerts";

type DashboardProps = {
  api: AppAPI;
  locale: Locale;
  timeZone: string;
  dashboard: DashboardData;
  onNavigate(page: string, search?: string): void;
};

export function Dashboard({ api, locale, timeZone, dashboard, onNavigate }: DashboardProps) {
  const t = (source: string) => translate(locale, source);
  const [agentOnline, setAgentOnline] = useState(0);
  const [agentTotal, setAgentTotal] = useState(0);

  useEffect(() => {
    let cancelled = false;
    api.listResource("agents").then((agents) => {
      if (cancelled) return;
      const list = agents as Array<{ status?: string; runtimeStatus?: string }>;
      const online = list.filter((a) => a.status === "online" && a.runtimeStatus === "running").length;
      setAgentOnline(online);
      setAgentTotal(list.length);
    }).catch(() => undefined);
    return () => { cancelled = true; };
  }, [api, dashboard]);

  return (
    <>
      <header className="page-header">
        <div>
          <h1>{t("仪表盘")}</h1>
          <p>{t("查看计划、仓库与最近运行状态。")}</p>
        </div>
        <div className="page-header-actions">
          <button className="primary-button" type="button" onClick={() => onNavigate("备份任务", "?view=create")}>{t("新建备份任务")}</button>
        </div>
      </header>

      <DashboardKpis dashboard={dashboard} agentOnline={agentOnline} agentTotal={agentTotal} locale={locale} />

      <div className="dashboard-grid">
        <div className="dashboard-grid-left">
          <DashboardTrendChart dashboard={dashboard} locale={locale} />
          <DashboardRecentRuns tasks={dashboard.tasks} locale={locale} timeZone={timeZone} onViewAll={() => onNavigate("运行记录")} />
        </div>
        <div className="dashboard-grid-right">
          <DashboardScheduleCoverage dashboard={dashboard} locale={locale} />
          <DashboardAlerts
            dashboard={dashboard}
            locale={locale}
            timeZone={timeZone}
            onViewAll={() => onNavigate("告警历史")}
            onHandle={(page) => onNavigate(page)}
          />
        </div>
      </div>
    </>
  );
}
