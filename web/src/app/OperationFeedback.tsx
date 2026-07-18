import { useEffect, useRef, useState } from "react";
import type { AppAPI } from "./App";
import { translate, type Locale } from "../i18n";
import { ModalPortal } from "./ModalPortal";
import { useModalFocus } from "./useModalFocus";
import { StatusIndicator } from "./StatusIndicator";

export type OperationRecord = {
  id: string;
  kind?: string;
  status: string;
  stage: string;
  errorSummary?: string;
  detail?: Record<string, unknown>;
};

export type AcceptedOperation = { operationId: string; status: string; stage?: string; kind?: string; expectedDisconnect?: boolean };

export type OperationController = {
  operation: OperationRecord | null;
  active: boolean;
  error: string;
  start(path: string, payload: Record<string, unknown>): Promise<AcceptedOperation | null>;
  adopt(accepted: AcceptedOperation): void;
  cancel(): Promise<void>;
  preflightCleanup(): Promise<Record<string, unknown> | null>;
  cleanup(password: string): Promise<void>;
};

const terminal = new Set(["success", "partial", "failed", "cancelled", "cleanup_required"]);

export function useOperation(api: AppAPI): OperationController {
  const [operation, setOperation] = useState<OperationRecord | null>(null);
  const [error, setError] = useState("");
	const operationRef = useRef<OperationRecord | null>(null);
	operationRef.current = operation;
  const active = operation !== null && !terminal.has(operation.status);

  useEffect(() => {
    if (!operation?.id || terminal.has(operation.status)) return;
    let stopped = false;
    let timer = 0;
    const retryUntil = Date.now() + 2 * 60 * 1000;
    const poll = async () => {
      try {
        const value = (await api.action(`/api/operations/${operation.id}`)) as OperationRecord;
        if (stopped) return;
        setOperation(value);
        if (!terminal.has(value.status)) timer = window.setTimeout(poll, 300);
      } catch (reason) {
		if (stopped) return;
		if (operationRef.current?.kind === "application_update" && Date.now() < retryUntil) {
			setError("");
			timer = window.setTimeout(poll, 1000);
			return;
		}
		setError(errorMessage(reason));
      }
    };
    void poll();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [api, operation?.id]);

  return {
    operation,
    active,
    error,
    async start(path, payload) {
      setError("");
      try {
        const accepted = (await api.action(path, payload)) as AcceptedOperation;
		setOperation({
			id: accepted.operationId,
			status: accepted.status,
			stage: accepted.stage ?? "queued",
			kind: accepted.kind,
			detail: accepted.expectedDisconnect ? { expectedDisconnect: true } : undefined,
		});
        return accepted;
      } catch (reason) {
        setError(errorMessage(reason));
        return null;
      }
    },
    adopt(accepted) {
      setError("");
		setOperation({ id: accepted.operationId, status: accepted.status, stage: accepted.stage ?? "queued", kind: accepted.kind, detail: accepted.expectedDisconnect ? { expectedDisconnect: true } : undefined });
    },
    async cancel() {
      if (!operation?.id || !active) return;
      try {
        await api.action(`/api/operations/${operation.id}/cancel`, {});
      } catch (reason) {
        setError(errorMessage(reason));
      }
    },
    async preflightCleanup() {
      if (!operation?.id || operation.status !== "cleanup_required") return null;
      setError("");
      try {
        return (await api.action(`/api/operations/${operation.id}/cleanup/preflight`, {})) as Record<string, unknown>;
      } catch (reason) {
        setError(errorMessage(reason));
        return null;
      }
    },
    async cleanup(password) {
      if (!operation?.id || operation.status !== "cleanup_required") return;
      setError("");
      try {
        const value = (await api.action(`/api/operations/${operation.id}/cleanup`, { password })) as OperationRecord;
        setOperation(value);
      } catch (reason) {
        setError(errorMessage(reason));
      }
    },
  };
}

export function OperationFeedback({ operation, locale = "zh-CN", hideTerminal = false, cancellable = true }: { operation: OperationController; locale?: Locale; hideTerminal?: boolean; cancellable?: boolean }) {
  const t = (source: string) => translate(locale, source);
  const [cleanupReady, setCleanupReady] = useState(false);
  const [cleanupKind, setCleanupKind] = useState("");
  const [password, setPassword] = useState("");
  const record = operation.operation;
  if (hideTerminal && !operation.error && record && terminal.has(record.status) && record.status !== "cleanup_required") return null;
  if (hideTerminal && operation.error && !record) return null;
  if (!record && !operation.error) return null;
  const residual = String(record?.detail?.residualPath ?? "");
  return (
    <div className={`operation-feedback operation-${record?.status ?? "error"}`} role="status">
      {record && <div className="operation-feedback-heading">
        <strong>{operationLabel(record, locale)}</strong>
        <StatusIndicator value={record.status} locale={locale} variant="pill" />
      </div>}
      {record?.errorSummary && terminal.has(record.status) ? <details className="operation-error-detail">
        <summary>{t("查看失败详情")}</summary>
        <dl className="structured-detail-grid">
          <div><dt>{t("操作 ID")}</dt><dd><code>{record.id}</code></dd></div>
          <div><dt>{t("失败原因")}</dt><dd><code>{record.errorSummary}</code></dd></div>
        </dl>
      </details> : record?.errorSummary ? <p>{record.errorSummary}</p> : null}
      {record?.kind === "application_update" && operation.active && <p>{t("升级期间控制服务会短暂断开；页面将自动重连并继续读取结果。")}</p>}
      {residual && <p>{t("残留位置：")}{residual}</p>}
      {operation.error && <p>{operation.error}</p>}
      {record?.stage === "cleanup_resolved" && <p>{t("残留已安全清理，恢复目标可重新预检")}</p>}
      {record?.status === "cleanup_required" && !cleanupReady && (
        <button className="secondary-button" type="button" onClick={async () => {
          const result = await operation.preflightCleanup();
          setCleanupKind(String(result?.kind ?? record.kind ?? ""));
          setCleanupReady(Boolean(result));
        }}>
          {t(record.kind === "database_restore" ? "重新预检数据库目标" : "检查清理条件")}
        </button>
      )}
      {record?.status === "cleanup_required" && cleanupReady && (
        <CleanupConfirmationDialog
          kind={cleanupKind}
          password={password}
          error={operation.error}
          locale={locale}
          onPassword={setPassword}
          onClose={() => { setCleanupReady(false); setPassword(""); }}
          onConfirm={() => {
            void operation.cleanup(password).then(() => {
              setCleanupReady(false);
              setPassword("");
            });
          }}
        />
      )}
	  {operation.active && cancellable && (
        <button className="secondary-button" type="button" onClick={() => void operation.cancel()}>
          {t("取消操作")}
        </button>
      )}
    </div>
  );
}

function CleanupConfirmationDialog({ kind, password, error, locale, onPassword, onClose, onConfirm }: { kind: string; password: string; error: string; locale: Locale; onPassword(value: string): void; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const database = kind === "database_restore";
  const dialogRef = useRef<HTMLDivElement>(null);
  useModalFocus(dialogRef, onClose);
  return <ModalPortal>
    <div ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="cleanup-confirmation-title">
      <header><div><h2 id="cleanup-confirmation-title">{t(database ? "确认数据库已清理" : "删除恢复残留")}</h2><p>{t("清理前请重新验证管理员身份。")}</p></div></header>
      <div className="dialog-body">
        {database
          ? <p>{t("数据库目标已重新通过恢复预检。系统不会删除数据库，只会确认管理员已在数据库侧完成清理。")}</p>
          : <p className="warning-text">{t("已确认该目录属于本次恢复操作。删除后不可撤销。")}</p>}
        <label>
          {t("当前管理员密码")}
          <input type="password" autoComplete="current-password" value={password} onChange={(event) => onPassword(event.target.value)} />
        </label>
        {error && <p className="form-error">{error}</p>}
      </div>
      <footer>
        <button className="secondary-button" type="button" onClick={onClose}>{t("取消")}</button>
        <button className={database ? "primary-button" : "danger-button"} type="button" disabled={!password} onClick={onConfirm}>
          {t(database ? "确认数据库已清理" : "删除恢复残留")}
        </button>
      </footer>
    </div>
  </ModalPortal>;
}

function operationLabel(record: OperationRecord, locale: Locale): string {
  const t = (source: string) => translate(locale, source);
  if (record.stage === "cleanup_resolved") return t("清理已完成");
  if (record.kind === "agent_restic_install" && record.status === "success") return t("Agent Restic 安装完成，备份与恢复能力已验证");
  if (record.kind === "agent_restic_install" && record.status === "failed") return t("Agent Restic 安装失败，旧版本已恢复");
  if (record.kind === "agent_restic_install" && record.status === "cancelled") return t("Agent Restic 安装已取消，旧版本已恢复");
  if (record.kind === "agent_tool_probe" && record.status === "success") return t("Agent 工具重新探测完成，新能力心跳已验证");
  if (record.kind === "agent_tool_probe" && record.status === "failed") return t("Agent 工具重新探测失败");
  if (record.kind === "agent_tool_probe" && record.status === "cancelled") return t("Agent 工具重新探测已取消");
  if (record.status === "success") return t(record.kind === "application_update" ? "应用升级完成并通过健康检查" : record.kind === "agent_uninstall" ? "Agent 已停止并卸载" : record.kind === "agent_upgrade" ? "Agent 升级完成，新版本心跳已验证" : record.kind === "protection_setup" ? "保护资源创建完成" : "操作完成");
  if (record.status === "failed") return t(record.kind === "application_update" ? "应用升级失败，旧版本已保留或已自动回滚" : record.kind === "agent_uninstall" ? "Agent 停止或卸载失败" : record.kind === "agent_upgrade" ? "Agent 升级失败，系统已尝试恢复旧版本" : record.kind === "protection_setup" ? "保护资源未全部创建完成" : "操作失败");
  if (record.status === "cancelled") return t(record.kind === "agent_upgrade" ? "Agent 升级已取消，系统已尝试恢复旧版本" : record.kind === "protection_setup" ? "保护资源创建已取消" : "操作已取消");
  if (record.status === "cleanup_required") return t("需要人工清理");
  const stages: Record<string, string> = {
    queued: "等待执行",
    starting: "正在启动",
    initializing: "正在初始化仓库",
    restoring: "正在恢复",
    cleanup: "正在检查残留",
    rotating_password: "正在新增并验证仓库 key",
    downloading: "正在下载并校验 Restic",
    activating: "正在启用 Restic",
    probing: "正在探测目标服务器",
    stopping_agent: "正在停止 Agent 服务",
    removing_agent: "正在删除 Agent 程序和配置",
    revoking_agent: "正在撤销 Agent 身份",
    uploading: "正在上传 Agent",
    waiting_for_heartbeat: "正在等待 Agent 注册和心跳",
    draining_agent: "正在等待 Agent 当前任务结束",
    staging_agent_upgrade: "正在暂存新 Agent 程序",
    activating_agent_upgrade: "正在切换并重启 Agent",
    waiting_for_agent_upgrade: "正在验证目标版本心跳",
    rolling_back_agent_upgrade: "正在恢复旧 Agent 程序",
    agent_upgrade_verified: "Agent 新版本心跳已验证",
    downloading_agent_restic: "正在下载并校验 Agent Restic",
    staging_agent_restic: "正在暂存 Agent Restic",
    activating_agent_restic: "正在切换 Restic 并重启 Agent",
    waiting_for_agent_restic: "正在验证 Agent Restic 能力心跳",
    rolling_back_agent_restic: "正在恢复旧版 Agent Restic",
    agent_restic_verified: "Agent Restic 能力已验证",
    restarting_agent_for_tool_probe: "正在重启 Agent 以重新探测工具",
    waiting_for_agent_tool_probe: "正在等待 Agent 返回新的工具能力",
    agent_tool_probe_verified: "Agent 工具能力心跳已验证",
    probing_capacity: "正在读取仓库存储容量",
    waiting_for_agent_capacity: "正在等待 Agent 返回仓库容量",
    verifying_read_only: "正在只读验证已有仓库",
    connected: "已有仓库验证完成",
    validating_bundle: "正在验证恢复包",
    imported: "导入完成，正在保存结果",
    restoring_for_verification: "正在恢复验证样本",
    verification_evidence_persisted: "恢复验证证据已保存",
    cleaning_verification_content: "正在清理恢复验证临时内容",
    protection_item: "正在逐项创建独立保护资源",
	launching_updater: "正在启动独立升级助手",
	downloading_release: "正在下载官方稳定版",
	release_verified: "发布文件校验完成",
	saving_rollback: "正在保存可回滚版本",
	replacing_binary: "正在原子替换应用程序",
	restarting_service: "正在重启控制服务",
	verifying_health: "正在验证新版本健康状态",
	health_verified: "新版本健康检查通过",
	rolling_back: "新版本异常，正在自动回滚",
	verifying_rollback: "正在验证回滚后的服务",
	rollback_verified: "旧版本已恢复并通过健康检查",
  };
  return t(stages[record.stage] ?? "正在执行");
}

function errorMessage(reason: unknown): string {
  return reason instanceof Error ? reason.message : "无法读取操作状态";
}
