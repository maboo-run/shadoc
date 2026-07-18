import { useEffect, useRef, useState, type FormEvent } from "react";
import type { AppAPI } from "./App";
import { ModalPortal } from "./ModalPortal";
import { OperationFeedback, useOperation } from "./OperationFeedback";
import { Toast } from "./Toast";
import type { ControlPlaneImportPreview } from "./controlPlaneTypes";
import { useModalFocus } from "./useModalFocus";
import { StatusIndicator } from "./StatusIndicator";
import { translate, type Locale } from "../i18n";

const maximumBundleBytes = 32 * 1024 * 1024;
const terminalOperationStatuses = new Set(["success", "partial", "failed", "cancelled", "cleanup_required"]);

export function ControlPlaneRecovery({ api, locale, timeZone }: { api: AppAPI; locale: Locale; timeZone?: string }) {
  const t = (source: string) => translate(locale, source);
  const [exportPassphrase, setExportPassphrase] = useState("");
  const [exportConfirmation, setExportConfirmation] = useState("");
  const [exportAdministratorPassword, setExportAdministratorPassword] = useState("");
  const [exporting, setExporting] = useState(false);
  const [bundle, setBundle] = useState<File | null>(null);
  const [preflightPassphrase, setPreflightPassphrase] = useState("");
  const [preflighting, setPreflighting] = useState(false);
  const [preview, setPreview] = useState<ControlPlaneImportPreview | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [importPassphrase, setImportPassphrase] = useState("");
  const [administratorPassword, setAdministratorPassword] = useState("");
  const [impactConfirmed, setImpactConfirmed] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const fileInput = useRef<HTMLInputElement>(null);
  const confirmationDialog = useRef<HTMLFormElement>(null);
  const handledOperation = useRef("");
  const operation = useOperation(api);

  const closeConfirmation = () => {
    if (uploading) return;
    setConfirming(false);
    setImportPassphrase("");
    setAdministratorPassword("");
    setImpactConfirmed(false);
  };
  useModalFocus(confirmationDialog, closeConfirmation, confirming);

  useEffect(() => {
    const record = operation.operation;
    if (!record || !terminalOperationStatuses.has(record.status)) return;
    const key = `${record.id}:${record.status}`;
    if (handledOperation.current === key) return;
    handledOperation.current = key;
    if (record.status === "success") {
      setMessage(t("控制面恢复导入完成；请按复验清单逐项重新接入。"));
    } else {
      setMessage(record.errorSummary || t(record.status === "cancelled" ? "操作已取消" : "控制面恢复导入失败"));
    }
  }, [locale, operation.operation]);

  async function exportBundle(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setMessage("");
    if (exportPassphrase !== exportConfirmation) {
      setError(t("两次输入的恢复包口令不一致"));
      return;
    }
    setExporting(true);
    try {
      const download = await api.exportControlPlane({
        administratorPassword: exportAdministratorPassword,
        recoveryPassphrase: exportPassphrase,
        recoveryPassphraseConfirmation: exportConfirmation,
      });
      const url = URL.createObjectURL(download.blob);
      try {
        const link = document.createElement("a");
        link.href = url;
        link.download = download.filename;
        link.rel = "noopener";
        document.body.append(link);
        link.click();
        link.remove();
      } finally {
        window.setTimeout(() => URL.revokeObjectURL(url), 0);
      }
      setMessage(t("加密恢复包已生成，请立即保存到控制服务之外。"));
    } catch (reason) {
      setError(errorMessage(reason, t("无法生成控制面恢复包")));
    } finally {
      setExportPassphrase("");
      setExportConfirmation("");
      setExportAdministratorPassword("");
      setExporting(false);
    }
  }

  function selectBundle(selected: File | undefined) {
    setError("");
    setPreview(null);
    setPreflightPassphrase("");
    if (!selected) {
      setBundle(null);
      return;
    }
    if (selected.size > maximumBundleBytes) {
      setBundle(null);
      if (fileInput.current) fileInput.current.value = "";
      setError(t("恢复包不能超过 32 MiB"));
      return;
    }
    setBundle(selected);
  }

  async function preflightBundle(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!bundle) {
      setError(t("请先选择控制面恢复包"));
      return;
    }
    setError("");
    setMessage("");
    setPreflighting(true);
    try {
      setPreview(await api.preflightControlPlaneImport(bundle, preflightPassphrase));
    } catch (reason) {
      setPreview(null);
      setError(errorMessage(reason, t("恢复包预检失败")));
    } finally {
      setPreflightPassphrase("");
      setPreflighting(false);
    }
  }

  async function importBundle(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!bundle || !preview?.canImport || !preview.previewId || !impactConfirmed) return;
    setError("");
    setUploading(true);
    try {
      const accepted = await api.importControlPlane(bundle, {
        recoveryPassphrase: importPassphrase,
        previewId: preview.previewId,
        administratorPassword,
        impactConfirmed,
      });
      handledOperation.current = "";
      operation.adopt(accepted);
      setMessage(t("控制面恢复导入已开始"));
      setConfirming(false);
      setBundle(null);
      setPreview(null);
      if (fileInput.current) fileInput.current.value = "";
    } catch (reason) {
      setError(errorMessage(reason, t("无法启动控制面恢复导入")));
    } finally {
      setImportPassphrase("");
      setAdministratorPassword("");
      setImpactConfirmed(false);
      setUploading(false);
    }
  }

  return (
    <>
      <header className="system-page-intro">
        <p>{t("导出经过认证的加密控制面恢复包，或在全新控制服务中预检并恢复持久配置。")}</p>
      </header>

      {error && <p className="error-message" role="alert">{error}</p>}

      <section className="content-section recovery-section">
        <div className="section-heading">
          <div>
            <h2>{t("导出控制面恢复包")}</h2>
            <p>{t("恢复包包含非秘密配置清单，以及使用独立恢复口令加密的托管秘密和 Agent CA。它不包含管理员凭据、会话、一次性令牌、运行记录或活动操作。")}</p>
          </div>
        </div>
        <form className="form-grid" onSubmit={(event) => void exportBundle(event)}>
          <label>
            {t("恢复包独立口令")}
            <input type="password" autoComplete="new-password" minLength={12} maxLength={1024} required value={exportPassphrase} onChange={(event) => setExportPassphrase(event.target.value)} />
          </label>
          <label>
            {t("再次输入恢复包口令")}
            <input type="password" autoComplete="new-password" minLength={12} maxLength={1024} required value={exportConfirmation} onChange={(event) => setExportConfirmation(event.target.value)} />
          </label>
          <label className="full-field">
            {t("导出时的管理员密码")}
            <input type="password" autoComplete="current-password" required value={exportAdministratorPassword} onChange={(event) => setExportAdministratorPassword(event.target.value)} />
          </label>
          <p className="field-hint full-field">{t("恢复口令不会写入恢复包、操作记录或浏览器存储。遗失该口令后无法解密恢复包。")}</p>
          <button className="primary-button" type="submit" disabled={exporting}>
            {t(exporting ? "正在生成加密恢复包…" : "生成并下载恢复包")}
          </button>
        </form>
      </section>

      <section className="content-section recovery-section">
        <div className="section-heading">
          <div>
            <h2>{t("导入控制面恢复包")}</h2>
            <p>{t("先执行不落库的预检。系统会检查包完整性、目标冲突、所需工具和导入后的逐项复验动作；预检不会覆盖现有资源。")}</p>
          </div>
        </div>
        <form className="form-grid" onSubmit={(event) => void preflightBundle(event)}>
          <label className="full-field">
            {t("控制面恢复包")}
            <input ref={fileInput} type="file" accept=".rcbundle,application/octet-stream,application/json" onChange={(event) => selectBundle(event.target.files?.[0])} />
          </label>
          {bundle && <p className="recovery-file-summary full-field">{formatSelectedFile(locale, bundle)}</p>}
          <label className="full-field">
            {t("预检恢复口令")}
            <input type="password" autoComplete="off" minLength={12} maxLength={1024} required value={preflightPassphrase} onChange={(event) => setPreflightPassphrase(event.target.value)} />
          </label>
          <button className="secondary-button" type="submit" disabled={preflighting || !bundle}>
            {t(preflighting ? "正在验证恢复包…" : "执行只读导入预检")}
          </button>
        </form>

        {preview && <ImportPreview preview={preview} locale={locale} timeZone={timeZone} onConfirm={() => setConfirming(true)} />}
        <OperationFeedback operation={operation} locale={locale} />
      </section>

      {confirming && preview?.canImport && preview.previewId && bundle && (
        <ModalPortal>
          <form ref={confirmationDialog} className="dialog" role="dialog" aria-modal="true" aria-labelledby="control-plane-import-title" onSubmit={(event) => void importBundle(event)}>
            <header>
              <div>
                <h2 id="control-plane-import-title">{t("确认导入控制面")}</h2>
                <p>{t("导入只创建预检中列出的资源，不覆盖目标现有配置。")}</p>
              </div>
            </header>
            <div className="dialog-body">
              <div className="confirmation-panel">
                <h3>{t("导入后的安全状态")}</h3>
                <p>{t("仓库保持未连接，数据库连接保持草稿，任务、计划、维护与通知保持停用；必须完成对应复验后再手动启用。")}</p>
              </div>
              <label>
                {t("导入恢复口令")}
                <input type="password" autoComplete="off" minLength={12} maxLength={1024} required value={importPassphrase} onChange={(event) => setImportPassphrase(event.target.value)} />
              </label>
              <label>
                {t("当前管理员密码")}
                <input type="password" autoComplete="current-password" required value={administratorPassword} onChange={(event) => setAdministratorPassword(event.target.value)} />
              </label>
              <label className="recovery-impact-confirmation">
                <input type="checkbox" checked={impactConfirmed} onChange={(event) => setImpactConfirmed(event.target.checked)} />
                {t("我确认目标中没有同名资源，并理解导入资源会保持停用直到逐项复验")}
              </label>
              {error && <p className="form-error" role="alert">{error}</p>}
            </div>
            <footer>
              <button className="secondary-button" type="button" disabled={uploading} onClick={closeConfirmation}>{t("取消")}</button>
              <button className="danger-button" type="submit" disabled={uploading || !impactConfirmed || !importPassphrase || !administratorPassword}>
                {t(uploading ? "正在上传并导入…" : "开始导入")}
              </button>
            </footer>
          </form>
        </ModalPortal>
      )}

      <Toast message={message} locale={locale} onClose={() => setMessage("")} />
    </>
  );
}

function ImportPreview({ preview, locale, timeZone, onConfirm }: { preview: ControlPlaneImportPreview; locale: Locale; timeZone?: string; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const counts = Object.entries(preview.resourceCounts).filter(([, count]) => count > 0);
  return (
    <div className="recovery-preview" aria-label={t("导入预检结果")}>
      <div className="recovery-preview-heading">
        <div>
          <h3>{t("导入预检结果")}</h3>
          <p>{formatSourceVersion(locale, preview.sourceApplicationVersion)}</p>
          {preview.expiresAt && <p>{formatPreviewExpiry(locale, preview.expiresAt, timeZone)}</p>}
        </div>
        <StatusIndicator value={preview.canImport ? "success" : "failed"} locale={locale} label={t(preview.canImport ? "可以导入" : "存在冲突")} variant="pill" />
      </div>

      <div className="recovery-preview-grid">
        <section>
          <h4>{t("将创建的资源")}</h4>
          <ul>{counts.map(([kind, count]) => <li key={kind}>{formatResourceCount(locale, kind, count)}</li>)}</ul>
        </section>
        <section>
          <h4>{t("明确排除的临时数据")}</h4>
          <ul>{preview.excludedTransientClasses.map((item) => <li key={item}>{t(transientClassLabel(item))}</li>)}</ul>
        </section>
      </div>

      {preview.conflicts.length > 0 && (
        <section className="recovery-preview-block">
          <h4>{t("目标冲突")}</h4>
          <p className="warning-text">{t("目标中存在冲突，不能导入。请先处理冲突并重新预检。")}</p>
          <div className="table-frame"><table><thead><tr><th>{t("资源")}</th><th>{t("字段")}</th><th>{t("导入值")}</th><th>{t("现有资源")}</th></tr></thead><tbody>
            {preview.conflicts.map((item, index) => <tr key={`${item.resourceType}:${item.resourceId}:${item.field}:${index}`}><td>{resourceReference(item.resourceType, item.resourceId, locale)}</td><td>{t(resourceFieldLabel(item.field))}</td><td>{item.value || "—"}</td><td><span className="technical-identifier">{item.existingId || "—"}</span></td></tr>)}
          </tbody></table></div>
        </section>
      )}

      {preview.missingTools.length > 0 && (
        <section className="recovery-preview-block">
          <h4>{t("目标缺少的工具")}</h4>
          <p className="warning-text">{t("可以先导入，但相关资源在安装工具并重新验证前保持停用。")}</p>
          <div className="table-frame"><table><thead><tr><th>{t("工具")}</th><th>{t("路径")}</th><th>{t("影响资源")}</th></tr></thead><tbody>
            {preview.missingTools.map((item) => <tr key={`${item.tool}:${item.path}`}><td>{item.tool}</td><td><span className="technical-identifier">{item.path || "—"}</span></td><td>{item.requiredBy.map((value) => resourceRequirement(value, locale)).join(locale === "en-US" ? ", " : "、")}</td></tr>)}
          </tbody></table></div>
        </section>
      )}

      <section className="recovery-preview-block">
        <h4>{t("导入后复验清单")}</h4>
        <div className="table-frame"><table><thead><tr><th>{t("资源")}</th><th>{t("复验动作")}</th></tr></thead><tbody>
          {preview.revalidation.map((item) => <tr key={`${item.resourceType}:${item.resourceId}:${item.action}`}><td>{resourceReference(item.resourceType, item.resourceId, locale)}</td><td>{t(revalidationLabel(item.action))}</td></tr>)}
          {!preview.revalidation.length && <tr><td className="empty-row" colSpan={2}>{t("没有需要逐项复验的资源")}</td></tr>}
        </tbody></table></div>
      </section>

      {preview.restartRequired && <p className="warning-text recovery-restart-warning">{t("导入 Agent CA 后必须重启控制服务，才能重新启用 Agent 身份。")}</p>}
      {preview.canImport && preview.previewId && <button className="danger-button" type="button" onClick={onConfirm}>{t("确认导入控制面")}</button>}
    </div>
  );
}

function resourceLabel(kind: string): string {
  const labels: Record<string, string> = {
    remoteHosts: "远程主机", repositories: "备份仓库", databaseConnections: "数据库实例", tasks: "备份任务", plans: "备份计划",
    maintenancePolicies: "维护策略", scheduleWatermarks: "计划发生水位", agents: "Agent 身份", audits: "审计记录",
  };
  return labels[kind] ?? kind;
}

function revalidationLabel(action: string): string {
  const labels: Record<string, string> = {
    verify_existing_repository_read_only: "只读验证已有仓库并确认快照可读",
    validate_rsync_target: "重新验证 rsync 目标路径",
    run_connection_preflight: "重新执行数据库连接预检",
    restart_service_and_wait_for_heartbeat: "重启控制服务并等待 Agent 心跳",
    preview_scope_then_enable: "重新预览任务范围后再启用",
    enable_after_tasks: "任务复验并启用后再启用计划",
    run_dry_run_then_enable: "执行维护 dry-run 后再启用",
    send_test_then_enable: "发送测试通知后再启用",
  };
  return labels[action] ?? "需要人工复验";
}

function transientClassLabel(value: string): string {
  return ({
    sessions: "登录会话",
    active_operations: "进行中的操作",
    agent_enrollment_tokens: "Agent 一次性注册令牌",
    raw_logs: "原始运行日志",
  } as Record<string, string>)[value] ?? "其他临时运行数据";
}

function resourceTypeLabel(value: string): string {
  return ({
    repository: "备份仓库",
    database_connection: "数据库实例",
    task: "备份任务",
    plan: "备份计划",
    remote_host: "远程主机",
    agent: "Agent 节点",
  } as Record<string, string>)[value] ?? "其他资源";
}

function resourceReference(type: string, id: string | undefined, locale: Locale) {
  return <span>{translate(locale, resourceTypeLabel(type))}{id ? <> · <span className="technical-identifier">{id}</span></> : null}</span>;
}

function resourceRequirement(value: string, locale: Locale): string {
  const [type, id] = value.split(":", 2);
  const label = translate(locale, resourceTypeLabel(type));
  return id ? `${label} · ${id}` : label;
}

function resourceFieldLabel(value: string | undefined): string {
  if (!value) return "其他字段";
  return ({ name: "名称", path: "路径", host: "主机", port: "端口", username: "用户" } as Record<string, string>)[value] ?? "其他字段";
}

function formatResourceCount(locale: Locale, kind: string, count: number): string {
  const label = translate(locale, resourceLabel(kind));
  return locale === "en-US" ? `${label}: ${new Intl.NumberFormat(locale).format(count)}` : `${label}：${new Intl.NumberFormat(locale).format(count)}`;
}

function formatSourceVersion(locale: Locale, version: string): string {
  return locale === "en-US" ? `Source version: ${version}` : `源版本：${version}`;
}

function formatPreviewExpiry(locale: Locale, value: string, timeZone?: string): string {
  const parsed = new Date(value);
  const formatted = Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium", ...(timeZone ? { timeZone } : {}) }).format(parsed);
  return locale === "en-US" ? `Preview expires: ${formatted}` : `预检有效期至：${formatted}`;
}

function formatSelectedFile(locale: Locale, file: File): string {
  const size = new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(file.size / (1024 * 1024));
  return locale === "en-US" ? `Selected: ${file.name} (${size} MiB)` : `已选择：${file.name}（${size} MiB）`;
}

function errorMessage(reason: unknown, fallback: string): string {
  return reason instanceof Error && reason.message ? reason.message : fallback;
}
