import { useState } from "react";
import { translate, type Locale } from "../i18n";
import { timestampAtSecond } from "./dateTime";
import { OperationFeedback, type OperationController } from "./OperationFeedback";

type AgentRecord = Record<string, unknown>;
type Tone = "healthy" | "warning" | "danger" | "neutral";
type AgentResticOperation = {
  agentId: string;
  active: boolean;
  status?: string;
  stage?: string;
  errorSummary?: string;
  error?: string;
};
type AgentOperation = {
  agentId: string;
  operation: OperationController;
  starting?: boolean;
};

type AgentFleetProps = {
  agents: AgentRecord[];
  remoteHosts: AgentRecord[];
  locale: Locale;
  timeZone: string;
  currentServiceURL: string;
  latestResticVersion?: string;
  busy: boolean;
  upgradeOperation?: AgentOperation;
  toolProbeOperation?: AgentOperation;
  heartbeatOperation?: AgentOperation;
  resticOperation?: AgentResticOperation;
  onCancelRestic?(): void;
  onUpgrade(agent: AgentRecord): void;
  onInstallRestic?(agent: AgentRecord): void;
  onReprobeTools?(agent: AgentRecord): void;
  onProbeHeartbeat?(agent: AgentRecord): void;
  onRedeploy(agent: AgentRecord): void;
  onRemove(agent: AgentRecord): void;
};

export function AgentFleet({ agents, remoteHosts, locale, timeZone, currentServiceURL, latestResticVersion, busy, upgradeOperation, toolProbeOperation, heartbeatOperation, resticOperation, onCancelRestic, onUpgrade, onInstallRestic, onReprobeTools, onProbeHeartbeat, onRedeploy, onRemove }: AgentFleetProps) {
  const t = (source: string) => translate(locale, source);
  const [expandedAgents, setExpandedAgents] = useState<Set<string>>(() => new Set());
  if (!agents.length) {
    return <section className="content-section agent-fleet-empty"><strong>{t("尚无 Agent")}</strong><p>{t("远程部署或生成一次性注册令牌后，节点会显示在这里。")}</p></section>;
  }
  return <ul className="agent-fleet" aria-label={t("Agent 节点列表")}>
    {agents.map((agent) => {
      const id = String(agent.id ?? "");
      const managed = Boolean(agent.remoteHostId);
      const uninstalled = Boolean(agent.uninstalledAt);
      const revoked = agent.status === "revoked" || Boolean(agent.revokedAt);
      const readiness = readinessState(String(agent.compatibilityStatus ?? (revoked || uninstalled ? "revoked" : "unknown")), Boolean(agent.taskEligible), locale);
      const targetVersion = String(agent.targetVersion ?? "");
      const upgradeAvailable = Boolean(agent.upgradeAvailable) && !uninstalled && !revoked;
      const linux = String(agent.platform ?? "").startsWith("linux/") || agent.os === "linux";
      const online = agent.status === "online" && agent.runtimeStatus === "running";
      const currentResticVersion = String(agent.resticVersion ?? "");
      const capabilities = Array.isArray(agent.capabilities) ? agent.capabilities.map(String) : [];
      const supportsManagedRestic = capabilities.includes("managed-restic-install-v1");
      const managedResticRepair = managed && linux && !supportsManagedRestic && String(agent.buildVersion ?? "") === targetVersion;
      const managesRestic = Boolean(onInstallRestic && managed && linux && !uninstalled && !revoked);
      const resticCurrent = Boolean(latestResticVersion && currentResticVersion === latestResticVersion);
      const showResticAction = managesRestic && !resticCurrent;
      const canInstallRestic = Boolean(showResticAction && online && supportsManagedRestic && latestResticVersion);
      const canReprobeTools = Boolean(onReprobeTools && managed && !uninstalled && !revoked && online);
      const canProbeHeartbeat = Boolean(onProbeHeartbeat && managed && !uninstalled && !revoked);
      const resticAvailability = !managesRestic || resticCurrent ? ""
        : !supportsManagedRestic ? "该 Agent 版本不支持一键安装 Restic，请先升级或重新部署 Agent。"
        : !online ? "Agent 在线后才可安装或升级 Restic。"
        : latestResticVersion === "" ? "暂时无法读取官方 Restic 版本，页面会自动重试。"
        : "";
      const expanded = expandedAgents.has(id);
      const detailID = `agent-${id}-details`;
      const remoteHost = remoteHosts.find((host) => String(host.id ?? "") === String(agent.remoteHostId ?? ""));
      const address = remoteAddress(remoteHost, agent, locale);
      const agentUpgradeOperation = scopedOperation(upgradeOperation, id);
      const agentToolProbeOperation = scopedOperation(toolProbeOperation, id);
      const agentHeartbeatOperation = scopedOperation(heartbeatOperation, id);
      const heartbeatProbing = Boolean(agentHeartbeatOperation?.active || (heartbeatOperation?.agentId === id && heartbeatOperation.starting));
      const agentResticOperation = resticOperation?.agentId === id ? resticOperation : undefined;
      return <li key={id}>
        <article className={`agent-node agent-node-${heartbeatTone(agent)}`} aria-labelledby={`agent-${id}`}>
          <button
            className="agent-node-summary"
            type="button"
            aria-expanded={expanded}
            aria-controls={detailID}
            aria-label={`${id} ${t(expanded ? "收起详情" : "查看详情")}`}
            onClick={() => setExpandedAgents((current) => {
              const next = new Set(current);
              if (next.has(id)) next.delete(id);
              else next.add(id);
              return next;
            })}
          >
            <span className="agent-summary-identity">
              <strong id={`agent-${id}`}>{id}</strong>
              <span>{String(agent.platform || t("平台未报告"))}</span>
            </span>
            <span className="agent-summary-fact">
              <span>{t("心跳")}</span>
              <strong className={`agent-summary-status agent-check-${heartbeatTone(agent)}`}><span className="status-indicator-dot" aria-hidden="true" />{heartbeatLabel(agent, locale, timeZone)}</strong>
            </span>
            <span className="agent-summary-fact">
              <span>{t("最后心跳")}</span>
              <strong>{formatTime(agent.lastHeartbeatAt, locale, timeZone)}</strong>
            </span>
            <span className="agent-summary-fact">
              <span>{t("远程地址")}</span>
              <strong className="technical-identifier">{address}</strong>
            </span>
            <span className="agent-summary-disclosure">{t(expanded ? "收起详情" : "查看详情")}<span aria-hidden="true">{expanded ? "↑" : "↓"}</span></span>
          </button>

          {agentUpgradeOperation && <OperationFeedback operation={agentUpgradeOperation} locale={locale} persistTerminal compact autoDismissSuccess dismissibleTerminal />}
          {agentToolProbeOperation && <OperationFeedback operation={agentToolProbeOperation} locale={locale} hideTerminal compact />}
          {agentResticOperation && <AgentResticOperationStatus operation={agentResticOperation} locale={locale} onCancel={onCancelRestic} />}

          {expanded && <div className="agent-node-details" id={detailID}>
            <dl className="agent-runtime-facts">
              <div><dt>{t("任务状态")}</dt><dd><span className={`agent-readiness agent-readiness-${readiness.tone}`}><span className="status-dot" />{readiness.label}</span></dd></div>
              <div><dt>{t("远程主机")}</dt><dd>{remoteHostDetail(remoteHost, agent, locale)}</dd></div>
              <div><dt>{t("版本与平台")}</dt><dd><code>{String(agent.buildVersion || t("版本未报告"))}</code><span aria-hidden="true"> · </span>{String(agent.platform || t("平台未报告"))}<span aria-hidden="true"> · </span>{managed ? t("托管安装") : t("手动安装")}{revoked && <><span aria-hidden="true"> · </span><span className="agent-credential-revoked">{t("凭据已撤销")}</span></>}</dd></div>
              <div><dt>{t("通信兼容性")}</dt><dd><span className={agent.protocolCompatible === false ? "agent-detail-danger" : undefined}>{compatibilityLabel(agent, locale)}</span></dd></div>
              <div><dt>{t("证书")}</dt><dd><span>{certificateLabel(agent, locale)}</span><span aria-hidden="true"> · </span>{formatTime(agent.certificateNotAfter, locale, timeZone)}</dd></div>
              <div><dt>{t("证书续期")}</dt><dd>{t(agent.renewalStatus === "failed" ? "续期失败，旧证书仍有效" : agent.renewalStatus === "healthy" ? "续期通道正常" : "尚无续期状态")}</dd></div>
              <div><dt>{t("备份引擎")}</dt><dd>{toolLabel(agent, locale)}</dd></div>
              {agent.endpointStatus !== "migration_required" && <div><dt>{t("Service 地址")}</dt><dd><code>{String(agent.serviceUrl || t("未报告地址"))}</code></dd></div>}
            </dl>
            <div className="agent-node-actions">
              {canProbeHeartbeat && <button className={`secondary-button agent-heartbeat-button${heartbeatProbing ? " agent-heartbeat-button-probing" : ""}`} type="button" disabled={busy || heartbeatProbing} aria-busy={heartbeatProbing} onClick={() => onProbeHeartbeat?.(agent)}>
                {heartbeatProbing && <span className="agent-heartbeat-spinner" aria-hidden="true" />}
                {t(heartbeatProbing ? "探测中…" : "主动探测心跳")}
              </button>}
              {managed && !uninstalled && !revoked && <button className="secondary-button" type="button" disabled={busy || !canReprobeTools} onClick={() => onReprobeTools?.(agent)}>{t("重新探测工具")}</button>}
              {showResticAction && <button className="primary-button" type="button" disabled={busy || !canInstallRestic} onClick={() => onInstallRestic?.(agent)}>{t(currentResticVersion ? "升级 Agent Restic" : "安装 Agent Restic")}</button>}
              {managed && upgradeAvailable && <button className="primary-button" type="button" disabled={busy} onClick={() => onUpgrade(agent)}>{managedResticRepair ? t("更新 Agent 以启用 Restic 安装") : locale === "en-US" ? `Upgrade Agent to ${targetVersion}` : `升级 Agent 至 ${targetVersion}`}</button>}
              {uninstalled
                ? <button className="secondary-button" type="button" disabled={busy} onClick={() => onRedeploy(agent)}>{t("重新部署")}</button>
                : <button className="danger-text text-button" type="button" disabled={busy || (revoked && !managed)} onClick={() => onRemove(agent)}>{t(managed ? "停止并卸载" : revoked ? "凭据已撤销" : "撤销凭据")}</button>}
            </div>
            {resticAvailability && <p className="field-hint">{t(resticAvailability)}</p>}
            {agent.endpointStatus === "migration_required" && <EndpointMigration agent={agent} managed={managed} locale={locale} currentServiceURL={currentServiceURL} />}
            {!managed && upgradeAvailable && <ManualUpgrade agent={agent} locale={locale} />}
            {!currentResticVersion && (!managed || !linux) && <ManualResticInstall locale={locale} />}
          </div>}
        </article>
      </li>;
    })}
  </ul>;
}

function scopedOperation(value: AgentOperation | undefined, agentID: string): OperationController | undefined {
  return value?.agentId === agentID ? value.operation : undefined;
}

function AgentResticOperationStatus({ operation, locale, onCancel }: { operation: AgentResticOperation; locale: Locale; onCancel?(): void }) {
  const t = (source: string) => translate(locale, source);
  const label = agentResticOperationLabel(operation.stage, operation.status, locale);
  const tone = operation.status === "failed" ? "danger" : operation.status === "cancelled" ? "neutral" : "active";
  return <div className={`agent-inline-operation agent-inline-operation-${tone}`} role="status" aria-live="polite">
    <span className={operation.active ? "agent-inline-operation-spinner" : "status-indicator-dot"} aria-hidden="true" />
    <div><strong>{label}</strong>{operation.status !== "failed" && operation.errorSummary && <small>{operation.errorSummary}</small>}{operation.error && <small>{operation.error}</small>}</div>
    {operation.active && onCancel && <button className="text-button" type="button" onClick={onCancel}>{t("取消操作")}</button>}
    {operation.status === "failed" && operation.errorSummary && <details><summary>{t("查看失败详情")}</summary><code>{operation.errorSummary}</code></details>}
  </div>;
}

function agentResticOperationLabel(stage = "queued", status = "queued", locale: Locale): string {
  const labels: Record<string, string> = {
    queued: "等待执行",
    downloading_agent_restic: "正在下载并校验 Agent Restic",
    staging_agent_restic: "正在暂存 Agent Restic",
    switching_agent_restic: "正在切换 Restic 并重启 Agent",
    waiting_for_agent_restic: "正在验证 Agent Restic 能力心跳",
    rolling_back_agent_restic: "正在恢复旧版 Agent Restic",
    agent_restic_verified: "Agent Restic 能力已验证",
  };
  if (status === "failed") return translate(locale, "Agent Restic 安装失败，旧版本已恢复");
  if (status === "cancelled") return translate(locale, "Agent Restic 安装已取消，旧版本已恢复");
  return translate(locale, labels[stage] ?? (status === "success" ? "Agent Restic 能力已验证" : "等待执行"));
}

function ManualResticInstall({ locale }: { locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  return <section className="agent-remediation" role="note">
    <div><h3>{t("此 Agent 需要手工安装 Restic")}</h3><p>{t("一键安装目前仅支持通过页面远程部署的 Linux Agent。")}</p></div>
    <ol>
      <li>{t("在 Agent 主机安装官方稳定版 Restic，并确保 Agent 服务账户可以执行。")}</li>
      <li>{t("重启 Agent 服务，使其重新探测备份引擎。")}</li>
      <li>{t("确认备份引擎显示 Restic 版本后再启用任务或远程恢复。")}</li>
    </ol>
  </section>;
}

function EndpointMigration({ agent, managed, locale, currentServiceURL }: { agent: AgentRecord; managed: boolean; locale: Locale; currentServiceURL: string }) {
  const t = (source: string) => translate(locale, source);
  return <section className="agent-remediation agent-endpoint-migration" role="note">
    <div><h3>{t("需要迁移 Service 地址")}</h3><p>{t("Agent 仍在使用旧地址；地址失效后将无法续期证书、领取任务或上报结果。")}</p></div>
    <div className="agent-endpoint-route"><code>{String(agent.serviceUrl || "—")}</code><span aria-hidden="true">→</span><code>{currentServiceURL || t("当前地址不可用")}</code></div>
    <ol>{managed ? <>
      <li>{t("等待当前任务结束，然后执行“停止并卸载”。")}</li>
      <li>{t("使用原 Agent ID 和绑定主机重新部署；页面会自动写入当前 Service 地址。")}</li>
      <li>{t("确认地址状态变为“一致”，再重新启用相关任务。")}</li>
    </> : <>
      <li>{t("在源端服务定义中把 --service 改为当前地址，保留现有数据目录与证书文件。")}</li>
      <li>{t("重启 Agent 服务，不要重新生成身份或复制私钥。")}</li>
      <li>{t("确认地址状态变为“一致”，再重新启用相关任务。")}</li>
    </>}</ol>
  </section>;
}

function ManualUpgrade({ agent, locale }: { agent: AgentRecord; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const target = String(agent.targetVersion ?? "");
  return <section className="agent-remediation" role="note">
    <div><h3>{t("手动安装的 Agent 需要手工更新")}</h3><p>{locale === "en-US" ? `The Service expects ${target}; this Agent reports ${String(agent.buildVersion || "unknown")}.` : `控制服务版本为 ${target}，该 Agent 报告 ${String(agent.buildVersion || "未知版本")}。`}</p></div>
    <ol>
      <li>{t("从当前控制服务的发布制品中取得与平台匹配的 Agent 文件。")}</li>
      <li>{t("停止源端 Agent，替换程序文件；保留数据目录、CA、私钥和证书。")}</li>
      <li>{t("重新启动并等待页面显示目标版本与协议兼容。")}</li>
    </ol>
  </section>;
}

function readinessState(status: string, eligible: boolean, locale: Locale): { label: string; tone: Tone } {
  const t = (source: string) => translate(locale, source);
  const states: Record<string, { label: string; tone: Tone }> = {
    compatible: { label: "可执行任务", tone: "healthy" },
    draining: { label: "正在排空任务", tone: "warning" },
    offline: { label: "心跳已超时", tone: "danger" },
    incompatible: { label: "协议不兼容", tone: "danger" },
    certificate_expired: { label: "证书已过期", tone: "danger" },
    revoked: { label: "身份已撤销", tone: "neutral" },
  };
  const state = status === "compatible" && !eligible
    ? { label: "尚不能执行任务", tone: "warning" as const }
    : states[status] ?? (eligible ? states.compatible : { label: "尚不能执行任务", tone: "warning" as const });
  return { ...state, label: t(state.label) };
}

function heartbeatLabel(agent: AgentRecord, locale: Locale, timeZone: string): string {
  if (agent.runtimeStatus === "running") return translate(locale, "在线");
  if (agent.runtimeStatus === "stopped") return translate(locale, "已停止");
  if (!agent.lastHeartbeatAt) return translate(locale, "从未上报");
  const date = new Date(String(agent.lastHeartbeatAt));
  if (Number.isNaN(date.getTime())) return translate(locale, "状态未知");
  return new Intl.DateTimeFormat(locale, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", timeZone }).format(date);
}

function heartbeatTone(agent: AgentRecord): Tone {
  if (agent.runtimeStatus === "running") return "healthy";
  if (agent.runtimeStatus === "stopped") return "neutral";
  return "danger";
}

function compatibilityLabel(agent: AgentRecord, locale: Locale): string {
  if (agent.protocolCompatible === true) return translate(locale, "兼容");
  if (agent.protocolCompatible === false) return translate(locale, "不兼容");
  return translate(locale, "状态未知");
}

function certificateLabel(agent: AgentRecord, locale: Locale): string {
  const labels: Record<string, string> = {
    valid: "有效", expiring_30: "30 天内到期", expiring_14: "14 天内到期", expiring_7: "7 天内到期", expired: "已过期", unknown: "到期时间未知",
  };
  const label = labels[String(agent.certificateStatus ?? "unknown")] ?? "到期时间未知";
  return translate(locale, label);
}

function toolLabel(agent: AgentRecord, locale: Locale): string {
  const tools: string[] = [];
  if (agent.resticVersion) tools.push(`Restic ${String(agent.resticVersion)}`);
  if (agent.rsyncVersion) tools.push(`rsync ${String(agent.rsyncVersion)}`);
  const capabilities = Array.isArray(agent.capabilities) ? agent.capabilities.map(String) : [];
  if (!agent.resticVersion && capabilities.includes("restic")) tools.push(`Restic · ${translate(locale, "版本未报告")}`);
  if (!agent.rsyncVersion && capabilities.includes("rsync")) tools.push(`rsync · ${translate(locale, "版本未报告")}`);
  return tools.length ? tools.join(" · ") : translate(locale, "未检测到 Restic 或 rsync");
}

function remoteAddress(remoteHost: AgentRecord | undefined, agent: AgentRecord, locale: Locale) {
  const t = (source: string) => translate(locale, source);
  if (!remoteHost) return agent.remoteHostId ? t("远程主机信息不可用") : t("未绑定远程主机");
  const host = String(remoteHost.host ?? "").trim();
  if (!host) return t("远程主机信息不可用");
  const formattedHost = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  const port = Number(remoteHost.port ?? 0);
  return Number.isInteger(port) && port > 0 ? `${formattedHost}:${port}` : formattedHost;
}

function remoteHostDetail(remoteHost: AgentRecord | undefined, agent: AgentRecord, locale: Locale) {
  const address = remoteAddress(remoteHost, agent, locale);
  if (!remoteHost) return address;
  const name = String(remoteHost.name || remoteHost.id || translate(locale, "远程主机"));
  const username = String(remoteHost.username ?? "").trim();
  return `${name} · ${username ? `${username}@` : ""}${address}`;
}

function formatTime(value: unknown, locale: Locale, timeZone: string) {
  if (!value) return "—";
  const date = new Date(String(value));
  if (Number.isNaN(date.getTime())) return String(value);
  const exactTime = timestampAtSecond(date);
  return <time dateTime={exactTime} title={exactTime}>{new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium", timeZone }).format(date)}</time>;
}
