import { useEffect, useRef, useState } from "react";
import { translate, type Locale } from "../i18n";
import { StatusIndicator } from "./StatusIndicator";

export type AgentServiceStatus = {
  enabled: boolean;
  running: boolean;
  port: number;
  advertisedHost: string;
  listenAddress: string;
  serviceUrl: string;
  error?: string;
};

export type AgentServiceAPI = {
  agentServiceStatus(): Promise<AgentServiceStatus>;
  saveAgentServiceSettings(settings: { enabled: boolean; port: number; advertisedHost: string }): Promise<AgentServiceStatus>;
  listResource(resource: string): Promise<Array<Record<string, unknown>>>;
};

export function AgentServiceSettings({
  api,
  locale,
  onStatus,
  onMessage,
}: {
  api: AgentServiceAPI;
  locale: Locale;
  onStatus(status: AgentServiceStatus): void;
  onMessage(message: string): void;
}) {
  const t = (source: string) => translate(locale, source);
  const [status, setStatus] = useState<AgentServiceStatus | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [port, setPort] = useState("9443");
  const [advertisedHost, setAdvertisedHost] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [agents, setAgents] = useState<Array<Record<string, unknown>>>([]);
  const [migrationAcknowledged, setMigrationAcknowledged] = useState(false);
  const dirty = useRef(false);

  const proposedServiceURL = serviceURL(advertisedHost, Number(port));
  const endpointChanging = Boolean(status?.serviceUrl) && (!enabled || proposedServiceURL !== status?.serviceUrl);
  const affectedAgents = endpointChanging ? agents.filter((agent) => agent.status !== "revoked" && !agent.uninstalledAt && !agent.revokedAt) : [];

  const apply = (next: AgentServiceStatus, syncForm = false) => {
    setStatus(next);
    if (syncForm || !dirty.current) {
      setEnabled(next.enabled);
      setPort(String(next.port || 9443));
      setAdvertisedHost(next.advertisedHost ?? "");
    }
    onStatus(next);
  };

  useEffect(() => {
    let active = true;
    const load = (syncForm = false) => {
      void Promise.all([api.agentServiceStatus(), api.listResource("agents")]).then(([next, agentItems]) => {
        if (active) {
          apply(next, syncForm);
          setAgents(agentItems);
        }
      }).catch((cause) => {
        if (active) setError(cause instanceof Error ? cause.message : translate(locale, "无法读取 Agent 服务配置"));
      });
    };
    const refresh = () => load(false);
    load(true);
    const interval = window.setInterval(refresh, 15_000);
    window.addEventListener("focus", refresh);
    return () => {
      active = false;
      window.clearInterval(interval);
      window.removeEventListener("focus", refresh);
    };
  }, [api, locale]);

  return <section className="content-section editor-section" aria-labelledby="agent-service-settings-title">
    <div className="editor-section-heading section-heading">
      <div>
        <h2 id="agent-service-settings-title">{t("Agent HTTPS 服务")}</h2>
        <p>{t("为远程 Agent 提供独立的 TLS 1.3 注册、心跳和任务通道。")}</p>
      </div>
      <StatusIndicator
        value={status === null ? "pending" : status.running ? "running" : "disabled"}
        locale={locale}
        label={t(status === null ? "正在检测…" : status.running ? "运行中" : "已停用")}
        tone={status === null ? "pending" : status.running ? "active" : "stopped"}
      />
    </div>
    <form className="form-grid" onSubmit={(event) => {
      event.preventDefault();
      setSaving(true);
      setError("");
      void api.saveAgentServiceSettings({ enabled, port: port === "" ? 0 : Number(port), advertisedHost }).then((next) => {
        dirty.current = false;
        setMigrationAcknowledged(false);
        apply(next, true);
        onMessage(t(next.running ? "Agent HTTPS 服务已启动" : "Agent HTTPS 服务已停止"));
      }).catch((cause) => {
        onMessage(cause instanceof Error ? cause.message : translate(locale, "无法保存 Agent 服务配置"));
      }).finally(() => setSaving(false));
    }}>
      <label className="full-field">
        <input type="checkbox" checked={enabled} onChange={(event) => { dirty.current = true; setMigrationAcknowledged(false); setEnabled(event.target.checked); }} />
        {t("启用 Agent HTTPS 服务")}
      </label>
      <label>{t("HTTPS 监听端口")}
        <input type="number" min="1024" max="65535" required value={port} onChange={(event) => { dirty.current = true; setMigrationAcknowledged(false); setPort(event.target.value); }} />
      </label>
      <label>{t("控制服务访问地址（IP 或域名）")}
        <input required={enabled} value={advertisedHost} onChange={(event) => { dirty.current = true; setMigrationAcknowledged(false); setAdvertisedHost(event.target.value); }} />
      </label>
      <p className="field-hint full-field">
        {t("填写运行控制服务这台设备、可供远程 Agent 访问的 IP 或域名；不是 Agent 的 IP。局域网部署时填写这台设备的局域网 IP 或局域网域名。启用后将监听这台设备的所有局域网接口。不要填写 0.0.0.0。")}
      </p>
      <p className="warning-text field-hint full-field">
        {t("修改 IP、域名或端口后，已部署 Agent 仍使用原地址，需要使用新地址重新部署或更新其服务配置。")}
      </p>
      {!!affectedAgents.length && <section className="agent-service-impact full-field" role="note">
        <div><h3>{locale === "en-US" ? `This change affects ${affectedAgents.length} Agents` : `此变更会影响 ${affectedAgents.length} 个 Agent`}</h3><p>{t("保存只会切换 Agent HTTPS 服务，不会静默改写远程节点；请按下列路径逐个迁移。")}</p></div>
        <ul>{affectedAgents.slice(0, 20).map((agent) => <li key={String(agent.id)}>{locale === "en-US"
          ? `${String(agent.id)} · ${agent.remoteHostId ? "Managed migration: stop and uninstall, then redeploy with the same Agent ID." : "Manual migration: update --service while preserving the existing identity files."}`
          : `${String(agent.id)} · ${agent.remoteHostId ? "托管迁移：停止并卸载后，使用同一 Agent ID 重新部署。" : "手工迁移：保留现有身份文件，只更新 --service 并重启。"}`}</li>)}</ul>
        {affectedAgents.length > 20 && <p>{locale === "en-US" ? `${affectedAgents.length - 20} more affected Agents are shown on the Agents page.` : `另有 ${affectedAgents.length - 20} 个受影响 Agent，请在 Agent 节点页逐项处理。`}</p>}
        <label><input type="checkbox" checked={migrationAcknowledged} onChange={(event) => setMigrationAcknowledged(event.target.checked)} />{t("我已记录受影响 Agent，并会逐个完成地址迁移")}</label>
      </section>}
      {status?.serviceUrl && <p className="field-hint full-field">{t("Agent 连接地址：")}{status.serviceUrl}</p>}
      {status?.error && <p className="form-error full-field" role="alert">{t(status.error)}</p>}
      {error && <p className="form-error full-field" role="alert">{error}</p>}
      <button className="primary-button form-action" type="submit" disabled={saving || affectedAgents.length > 0 && !migrationAcknowledged}>
        {t(saving ? "正在应用…" : "保存并应用")}
      </button>
    </form>
  </section>;
}

function serviceURL(host: string, port: number): string {
  const value = host.trim().replace(/^\[|\]$/g, "");
  if (!value || !Number.isInteger(port) || port < 1 || port > 65535) return "";
  return `https://${value.includes(":") ? `[${value}]` : value}:${port}`;
}
