import { useCallback, useEffect, useState } from "react";
import { translate, type Locale } from "../i18n";
import { StatusIndicator } from "./StatusIndicator";

type HealthAPI = {
  action(path: string, payload?: Record<string, unknown>): Promise<unknown>;
};

export type AlertState = {
  stateKey: string;
  kind: string;
  severity: "info" | "warning" | "critical";
  status: "active" | "resolved";
  objectType: string;
  objectId: string;
  objectName: string;
  reason: string;
  message: string;
  targetPage: string;
  recoveryCondition: string;
  firstAt: string;
  lastAt: string;
  resolvedAt?: string;
  occurrenceCount: number;
};

type AlertEvent = Omit<AlertState, "firstAt" | "lastAt" | "resolvedAt"> & {
  id: number;
  occurredAt: string;
  transition: "raised" | "repeated" | "resolved";
};

type NotificationDelivery = {
  id: number;
  notificationId: string;
  occurredAt: string;
  channel: string;
  stateKey: string;
  transition: string;
  attempt: number;
  maxAttempts: number;
  status: "skipped_disabled" | "rate_limited" | "retrying" | "failed_final" | "delivered";
  errorSummary?: string;
  deliveredAt?: string;
};

type HealthResponse = {
  active?: AlertState[];
  events?: AlertEvent[];
  deliveries?: NotificationDelivery[];
};

export function HealthEvents({
  api,
  locale,
  timeZone,
  view,
  onNavigate,
}: {
  api: HealthAPI;
  locale: Locale;
  timeZone: string;
  view: "alerts" | "deliveries";
  onNavigate?(page: string, objectId: string, kind: string): void | Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const [data, setData] = useState<Required<HealthResponse>>({ active: [], events: [], deliveries: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    setLoading(true);
    setError("");
    void api.action("/api/alerts?limit=100").then((value) => {
      const response = (value ?? {}) as HealthResponse;
      setData({
        active: Array.isArray(response.active) ? response.active : [],
        events: Array.isArray(response.events) ? response.events : [],
        deliveries: Array.isArray(response.deliveries) ? response.deliveries : [],
      });
    }).catch((reason) => setError(reason instanceof Error ? reason.message : t("无法读取保护告警与通知投递记录"))).finally(() => setLoading(false));
  }, [api, locale]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(load, [load]);
  const date = (value?: string) => {
    if (!value) return "—";
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium", timeZone }).format(parsed);
  };
  const severity = (value: AlertState["severity"]) => t(value === "critical" ? "严重" : value === "warning" ? "警告" : "信息");
  const transition = (value: AlertEvent["transition"]) => t(value === "raised" ? "发生" : value === "resolved" ? "已恢复" : "再次发生");
  const deliveryStatus = (value: NotificationDelivery["status"]) => t({
    skipped_disabled: "通道已停用",
    rate_limited: "已限频",
    retrying: "等待重试",
    failed_final: "最终失败",
    delivered: "已送达",
  }[value]);

  return <>
    {view === "alerts" && <>
    <section className="content-section health-events activity-record-section" aria-labelledby="active-health-alerts-title">
      <div className="section-heading">
        <div><h2 id="active-health-alerts-title">{t("当前保护告警")}</h2><p>{t("这里只显示尚未满足恢复条件的问题；恢复后会移入历史。")}</p></div>
        <button className="secondary-button" type="button" disabled={loading} onClick={load}>{t(loading ? "正在刷新…" : "刷新")}</button>
      </div>
      {error && <p className="error-message" role="alert">{error}</p>}
      {!loading && data.active.length === 0 && <p className="empty-state">{t("当前没有保护告警")}</p>}
      <ul className="health-alert-list">
        {data.active.map((item) => <li key={item.stateKey} className={`health-alert ${item.severity}`}>
          <div className="health-alert-heading"><strong>{item.objectName}</strong><StatusIndicator value={item.severity} locale={locale} label={severity(item.severity)} variant="pill" /></div>
          <p>{item.message}</p>
          <dl>
            <div><dt>{t("原因")}</dt><dd>{item.reason}</dd></div>
            <div><dt>{t("首次发生")}</dt><dd>{date(item.firstAt)}</dd></div>
            <div><dt>{t("最近发生")}</dt><dd>{date(item.lastAt)}</dd></div>
            <div><dt>{t("发生次数")}</dt><dd>{new Intl.NumberFormat(locale).format(item.occurrenceCount)}</dd></div>
            <div className="wide"><dt>{t("恢复条件")}</dt><dd>{item.recoveryCondition}</dd></div>
            <div><dt>{t("处理入口")}</dt><dd>{t(item.targetPage)}</dd></div>
          </dl>
          {onNavigate && item.targetPage && <button className="text-button" type="button" onClick={() => void onNavigate(item.targetPage, item.objectId, item.kind)}>{t("处理")}</button>}
        </li>)}
      </ul>
    </section>

    <section className="content-section" aria-labelledby="alert-history-title">
      <h2 id="alert-history-title">{t("告警历史")}</h2>
      <div className="table-frame"><table>
        <thead><tr><th>{t("时间")}</th><th>{t("变化")}</th><th>{t("级别")}</th><th>{t("对象")}</th><th>{t("原因")}</th><th>{t("恢复条件")}</th></tr></thead>
        <tbody>
          {data.events.map((event) => <tr key={event.id}><td>{date(event.occurredAt)}</td><td>{transition(event.transition)}</td><td>{severity(event.severity)}</td><td>{event.objectName}</td><td>{event.reason}</td><td>{event.recoveryCondition}</td></tr>)}
          {!loading && data.events.length === 0 && <tr><td className="empty-row" colSpan={6}>{t("尚无告警历史")}</td></tr>}
        </tbody>
      </table></div>
    </section>
    </>}

    {view === "deliveries" && <section className="content-section activity-record-section" aria-labelledby="delivery-history-title">
      <div className="section-heading">
        <div><h2 id="delivery-history-title">{t("通知投递记录")}</h2><p>{t("查看每次告警投递的通道、状态、重试次数与最近错误。")}</p></div>
        <button className="secondary-button" type="button" disabled={loading} onClick={load}>{t(loading ? "正在刷新…" : "刷新")}</button>
      </div>
      {error && <p className="error-message" role="alert">{error}</p>}
      <div className="table-frame"><table>
        <thead><tr><th>{t("时间")}</th><th>{t("通道")}</th><th>{t("状态")}</th><th>{t("告警标识")}</th><th>{t("尝试")}</th><th>{t("最近错误")}</th></tr></thead>
        <tbody>
          {data.deliveries.map((delivery) => <tr key={delivery.id}><td>{date(delivery.occurredAt)}</td><td>{delivery.channel}</td><td>{deliveryStatus(delivery.status)}</td><td><code>{delivery.stateKey}</code></td><td>{delivery.attempt === 0 ? "—" : `${delivery.attempt}/${delivery.maxAttempts}`}</td><td>{delivery.errorSummary || "—"}</td></tr>)}
          {!loading && data.deliveries.length === 0 && <tr><td className="empty-row" colSpan={6}>{t("尚无通知投递记录")}</td></tr>}
        </tbody>
      </table></div>
    </section>}
  </>;
}
