import { useEffect, useRef, useState } from "react";
import type { AppAPI } from "./App";
import { generateRepositoryPassword } from "./ResourceEditors";
import { OperationFeedback, useOperation } from "./OperationFeedback";
import { translate, type Locale } from "../i18n";
import { StatusIndicator, type StatusTone } from "./StatusIndicator";

type ProtectionTemplate = {
  id: string;
  name: string;
  retention: Record<string, number>;
  resources: Record<string, string | number>;
  health?: Record<string, number>;
  schedule: Record<string, string | number>;
  timezone: string;
  maxParallel: number;
  catchUpWindowMinutes: number;
};

type DraftItem = {
  id: string;
  taskId: string;
  repositoryId: string;
  taskName: string;
  directory?: { path: string; exclusions?: string[]; skipIfUnchanged?: boolean };
  database?: { connectionId: string; database: string };
  repositoryName: string;
  repositoryKind: "local" | "sftp";
  remoteHostId?: string;
  repositoryPath: string;
  status: string;
  error?: string;
  hasPassword?: boolean;
};

type ProtectionDraft = {
  id: string;
  name: string;
  templateId?: string;
  planId: string;
  status: string;
  items: DraftItem[];
};

type ChecklistItem = {
  taskId: string;
  taskName: string;
  resourceStatus: string;
  activationStatus: string;
  firstCompleteSuccessStatus: string;
  firstCompleteSuccessAt?: string;
  maintenanceStatus: string;
  restoreVerificationStatus: string;
};

type ProtectionChecklist = {
  draftId: string;
  draftStatus: string;
  planId: string;
  planStatus: string;
  nextRun?: string;
  notificationStatus: string;
  items: ChecklistItem[];
  complete: boolean;
};

type Mapping = {
  key: string;
  label: string;
  taskName: string;
  directory?: { path: string; exclusions: string[]; skipIfUnchanged: boolean };
  database?: { connectionId: string; database: string };
  repositoryName: string;
  repositoryPath: string;
  password: string;
};

type DirectoryEntry = { name: string; path: string; directory: boolean };

const terminalOperationStatuses = new Set(["success", "failed", "cancelled", "cleanup_required"]);

function lines(value: string) {
  return [...new Set(value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean))];
}

function safeSegment(value: string, index: number) {
  const normalized = value.normalize("NFKC").replace(/[^\p{L}\p{N}._-]+/gu, "-").replace(/^-+|-+$/g, "");
  return normalized || `item-${index + 1}`;
}

function joinPath(base: string, segment: string) {
  const separator = /^[A-Za-z]:[\\/]/.test(base) ? "\\" : "/";
  return `${base.replace(/[\\/]+$/, "")}${separator}${segment}`;
}

function mappingFor(label: string, index: number, basePath: string, source: Pick<Mapping, "directory" | "database">): Mapping {
  const segment = safeSegment(label, index);
  return {
    key: source.database ? `database:${source.database.connectionId}:${source.database.database}` : `directory:${source.directory?.path}`,
    label,
    taskName: `${label} ${index + 1}`,
    ...source,
    repositoryName: `${label} repository ${index + 1}`,
    repositoryPath: joinPath(basePath, segment),
    password: generateRepositoryPassword(),
  };
}

function mappedRepositoryPaths(items: Mapping[], basePath: string) {
  const segments = items.map((item, index) => safeSegment(item.label, index));
  const counts = new Map<string, number>();
  segments.forEach((segment) => counts.set(segment.toLocaleLowerCase(), (counts.get(segment.toLocaleLowerCase()) ?? 0) + 1));
  return items.map((item, index) => {
    const segment = segments[index];
    const uniqueSegment = (counts.get(segment.toLocaleLowerCase()) ?? 0) > 1 ? `${segment}-${index + 1}` : segment;
    return { ...item, repositoryPath: joinPath(basePath, uniqueSegment) };
  });
}

export function ProtectionWizard({ api, locale, timeZone, onNavigate }: { api: AppAPI; locale: Locale; timeZone: string; onNavigate(page: string, search?: string): void }) {
  const t = (source: string) => translate(locale, source);
  const [step, setStep] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [templates, setTemplates] = useState<ProtectionTemplate[]>([]);
  const [drafts, setDrafts] = useState<ProtectionDraft[]>([]);
  const [connections, setConnections] = useState<Array<Record<string, unknown>>>([]);
  const [agents, setAgents] = useState<Array<Record<string, unknown>>>([]);
  const [hosts, setHosts] = useState<Array<Record<string, unknown>>>([]);
  const [notificationReady, setNotificationReady] = useState(false);
  const [allowedRoots, setAllowedRoots] = useState("/");
  const [showRoots, setShowRoots] = useState(false);
  const [sourceKind, setSourceKind] = useState<"directory" | "database">("directory");
  const [targetKind, setTargetKind] = useState<"local" | "agent">("local");
  const [agentId, setAgentId] = useState("");
  const [directoryPaths, setDirectoryPaths] = useState("");
  const [directoryBrowsePath, setDirectoryBrowsePath] = useState("/");
  const [directoryEntries, setDirectoryEntries] = useState<DirectoryEntry[]>([]);
  const [newDirectoryName, setNewDirectoryName] = useState("");
  const [browsing, setBrowsing] = useState(false);
  const [connectionId, setConnectionId] = useState("");
  const [databaseNames, setDatabaseNames] = useState<string[]>([]);
  const [selectedDatabases, setSelectedDatabases] = useState<string[]>([]);
  const [enumerating, setEnumerating] = useState(false);
  const [repositoryKind, setRepositoryKind] = useState<"local" | "sftp">("local");
  const [remoteHostId, setRemoteHostId] = useState("");
  const [basePath, setBasePath] = useState("/srv/shadoc");
  const [mappings, setMappings] = useState<Mapping[]>([]);
  const [templateId, setTemplateId] = useState("");
  const [keepDaily, setKeepDaily] = useState(7);
  const [keepMonthly, setKeepMonthly] = useState(6);
  const [scheduleTime, setScheduleTime] = useState("02:00");
  const [maxParallel, setMaxParallel] = useState(2);
  const [catchUpMinutes, setCatchUpMinutes] = useState(120);
  const [compression, setCompression] = useState("auto");
  const [uploadLimit, setUploadLimit] = useState(0);
  const [healthHours, setHealthHours] = useState(30);
  const [notificationMode, setNotificationMode] = useState<"configured" | "none">("configured");
  const [saveTemplate, setSaveTemplate] = useState(false);
  const [newTemplateName, setNewTemplateName] = useState("");
  const [passwordsConfirmed, setPasswordsConfirmed] = useState(false);
  const [draft, setDraft] = useState<ProtectionDraft | null>(null);
  const [checklist, setChecklist] = useState<ProtectionChecklist | null>(null);
  const [tasks, setTasks] = useState<Array<Record<string, unknown>>>([]);
  const [plans, setPlans] = useState<Array<Record<string, unknown>>>([]);
  const [scopePreviews, setScopePreviews] = useState<Record<string, Record<string, unknown>>>({});
  const application = useOperation(api);
  const firstRun = useOperation(api);
  const handledOperation = useRef("");
  const handledFirstRun = useRef("");

  useEffect(() => {
    let active = true;
    void Promise.all([
      api.listResource("protection-templates"), api.listResource("protection-drafts"), api.listResource("database-connections"),
      api.listResource("agents"), api.listResource("remote-hosts"), api.action("/api/local-filesystem/settings"),
      api.action("/api/ntfy"), api.action("/api/webhook"),
    ]).then(([templateItems, draftItems, connectionItems, agentItems, hostItems, localSettings, ntfy, webhook]) => {
      if (!active) return;
      setTemplates(templateItems as unknown as ProtectionTemplate[]);
      setDrafts(draftItems as unknown as ProtectionDraft[]);
      setConnections(connectionItems.filter((item) => item.purpose === "backup" && item.status === "ready"));
      setAgents(agentItems.filter((item) => item.taskEligible !== false && item.status === "online"));
      setHosts(hostItems);
      const roots = (localSettings as { roots?: unknown }).roots;
      if (Array.isArray(roots)) {
        const values = roots.map(String);
        setAllowedRoots(values.join("\n"));
        setDirectoryBrowsePath(values[0] ?? "/");
      }
      const notifications = [ntfy, webhook] as Array<Record<string, unknown>>;
      setNotificationReady(notifications.some((notification) => notification.configured === true && notification.enabled !== false));
      setLoading(false);
    }).catch((cause) => {
      if (active) {
        setError(cause instanceof Error ? cause.message : t("无法读取创建保护所需资源"));
        setLoading(false);
      }
    });
    return () => { active = false; };
  }, [api, locale]);

  async function refreshResult(id: string) {
    const [current, currentChecklist, taskItems, planItems] = await Promise.all([
      api.action(`/api/protection-drafts/${encodeURIComponent(id)}`),
      api.action(`/api/protection-drafts/${encodeURIComponent(id)}/checklist`),
      api.listResource("tasks"), api.listResource("plans"),
    ]);
    setDraft(current as ProtectionDraft);
    setChecklist(currentChecklist as ProtectionChecklist);
    setTasks(taskItems);
    setPlans(planItems);
  }

  useEffect(() => {
    const operation = application.operation;
    if (!draft?.id || !operation || !terminalOperationStatuses.has(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledOperation.current === key) return;
    handledOperation.current = key;
    void refreshResult(draft.id).catch((cause) => setError(cause instanceof Error ? cause.message : t("无法刷新保护检查表")));
  }, [application.operation, draft?.id]);

  useEffect(() => {
    const operation = firstRun.operation;
    if (!draft?.id || !operation || !terminalOperationStatuses.has(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledFirstRun.current === key) return;
    handledFirstRun.current = key;
    void refreshResult(draft.id).catch((cause) => setError(cause instanceof Error ? cause.message : t("无法刷新保护检查表")));
  }, [firstRun.operation, draft?.id]);

  function selectedSources() {
    if (sourceKind === "database") {
      return selectedDatabases.map((name) => ({ label: name, database: { connectionId, database: name } }));
    }
    return lines(directoryPaths).map((path) => ({ label: path.split(/[\\/]/).filter(Boolean).at(-1) ?? path, directory: { path, exclusions: [], skipIfUnchanged: true } }));
  }

  function enterMappings() {
    const sources = selectedSources();
    if (!sources.length) {
      setError(t(sourceKind === "database" ? "请至少选择一个逻辑数据库" : "请至少输入一个待保护目录"));
      return;
    }
    if (targetKind === "agent" && !agentId) {
      setError(t("请选择源端 Agent"));
      return;
    }
    const kind = targetKind === "agent" ? "sftp" : repositoryKind;
    setRepositoryKind(kind);
    setMappings(mappedRepositoryPaths(sources.map((source, index) => mappingFor(source.label, index, basePath, source)), basePath));
    setError("");
    setStep(1);
  }

  function updateMappingPaths() {
    if (!basePath.trim()) {
      setError(t("请输入仓库基础路径"));
      return;
    }
    setMappings((items) => mappedRepositoryPaths(items, basePath.trim()));
    setError("");
  }

  async function browseDirectory() {
    if (!directoryBrowsePath.trim()) return;
    setBrowsing(true);
    setError("");
    try {
      const endpoint = targetKind === "agent" ? `/api/agents/${encodeURIComponent(agentId)}/filesystem/browse` : "/api/local-filesystem/browse";
      const result = await api.action(endpoint, { path: directoryBrowsePath.trim() }) as { path?: string; entries?: DirectoryEntry[] };
      setDirectoryBrowsePath(result.path ?? directoryBrowsePath.trim());
      setDirectoryEntries(Array.isArray(result.entries) ? result.entries : []);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法读取目录"));
      setDirectoryEntries([]);
    } finally {
      setBrowsing(false);
    }
  }

  async function saveAllowedRoots() {
    try {
      await api.updateResource("local-filesystem", "settings", { roots: lines(allowedRoots) });
      setMessage(t("允许根目录已保存"));
      setShowRoots(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法保存允许根目录"));
    }
  }

  async function createDirectory() {
    const name = newDirectoryName.trim();
    if (!name || name === "." || name === ".." || /[\\/\0\r\n]/.test(name)) {
      setError(t("请输入不含斜杠的目录名称"));
      return;
    }
    const target = joinPath(directoryBrowsePath.trim(), name);
    try {
      const endpoint = targetKind === "agent" ? `/api/agents/${encodeURIComponent(agentId)}/filesystem/directories` : "/api/local-filesystem/directories";
      await api.action(endpoint, { path: target });
      setDirectoryBrowsePath(target);
      setDirectoryPaths((value) => lines(`${value}\n${target}`).join("\n"));
      setDirectoryEntries([]);
      setNewDirectoryName("");
      setMessage(t("目录已创建并加入保护对象"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("创建目录失败"));
    }
  }

  async function enumerateDatabases() {
    if (!connectionId) return;
    setEnumerating(true);
    setError("");
    try {
      const result = await api.action(`/api/database-connections/${encodeURIComponent(connectionId)}/databases`, {}) as { items?: string[] };
      setDatabaseNames(Array.isArray(result.items) ? result.items : []);
      setSelectedDatabases([]);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法读取逻辑数据库"));
    } finally {
      setEnumerating(false);
    }
  }

  async function saveDraftAndApply() {
    if (!passwordsConfirmed) return;
    setError("");
    try {
      let selectedTemplateID = templateId;
      if (!selectedTemplateID && saveTemplate) {
        const created = await api.createResource("protection-templates", {
          name: newTemplateName, retention: { keepDaily, keepMonthly }, resources: { compression, uploadKiBPerSecond: uploadLimit },
          health: { maxSuccessAgeHours: healthHours }, schedule: { kind: "daily", timeOfDay: scheduleTime }, timezone: timeZone,
          maxParallel, catchUpWindowMinutes: catchUpMinutes,
        }) as ProtectionTemplate;
        selectedTemplateID = created.id;
        setTemplateId(created.id);
      }
      const created = await api.createResource("protection-drafts", {
        name: sourceKind === "database" ? t("数据库保护集合") : t("目录保护集合"),
        templateId: selectedTemplateID,
        executionTarget: targetKind === "agent" ? { kind: "agent", agentId } : { kind: "local" },
        ...(!selectedTemplateID ? {
          retention: { keepDaily, keepMonthly }, resources: { compression, uploadKiBPerSecond: uploadLimit }, health: { maxSuccessAgeHours: healthHours },
          schedule: { kind: "daily", timeOfDay: scheduleTime }, timezone: timeZone, maxParallel, catchUpWindowMinutes: catchUpMinutes,
        } : {}),
        notificationMode,
        items: mappings.map((item) => ({
          taskName: item.taskName, ...(item.directory ? { directory: item.directory } : { database: item.database }),
          repositoryName: item.repositoryName, repositoryKind, remoteHostId: repositoryKind === "sftp" ? remoteHostId : "",
          repositoryPath: item.repositoryPath, password: item.password, passwordConfirmed: true,
        })),
      }) as ProtectionDraft;
      setDraft(created);
      setMappings((items) => items.map((item) => ({ ...item, password: "" })));
      setChecklist(null);
      setStep(4);
      handledOperation.current = "";
      await application.start(`/api/protection-drafts/${encodeURIComponent(created.id)}/apply`, {});
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法保存保护草稿"));
    }
  }

  async function openDraft(item: ProtectionDraft) {
    setError("");
    try {
      await refreshResult(item.id);
      setStep(4);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法打开保护草稿"));
    }
  }

  async function retryDraft() {
    if (!draft) return;
    handledOperation.current = "";
    await application.start(`/api/protection-drafts/${encodeURIComponent(draft.id)}/apply`, {});
  }

  async function cancelDraft() {
    if (!draft) return;
    try {
      const value = await api.action(`/api/protection-drafts/${encodeURIComponent(draft.id)}/cancel`, {}) as ProtectionDraft;
      setDraft(value);
      setMessage(t("未创建资源的凭据已清理；已经创建的安全资源被保留"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法清理保护草稿"));
    }
  }

  async function previewScope(item: DraftItem, task: Record<string, unknown>) {
    try {
      const preview = await api.action(`/api/tasks/${encodeURIComponent(item.taskId)}/preview`, {}) as Record<string, unknown>;
      setScopePreviews((values) => ({ ...values, [item.taskId]: preview }));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("任务范围预览失败"));
    }
  }

  async function activateTask(item: DraftItem, task: Record<string, unknown>) {
    const preview = scopePreviews[item.taskId];
    try {
      await api.updateResource("tasks", item.taskId, { ...task, enabled: true, ...(preview ? { previewId: preview.previewId } : {}) });
      await refreshResult(draft!.id);
      setMessage(t("任务已启用"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法启用任务"));
    }
  }

  async function activatePlan() {
    const plan = plans.find((item) => item.id === draft?.planId);
    if (!plan || !draft) return;
    try {
      await api.updateResource("plans", draft.planId, { ...plan, enabled: true });
      await refreshResult(draft.id);
      setMessage(t("备份计划已启用"));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("无法启用备份计划"));
    }
  }

  function resetWizard() {
    handledFirstRun.current = "";
    setStep(0); setDraft(null); setChecklist(null); setMappings([]); setDirectoryPaths(""); setSelectedDatabases([]); setPasswordsConfirmed(false); setError("");
  }

  if (loading) return <p role="status">{t("正在读取创建保护所需资源…")}</p>;

  const steps = [t("选择来源"), t("独立仓库映射"), t("保护策略"), t("确认与密码"), t("保护检查表")];
  const allTasksEnabled = Boolean(draft?.items.length) && draft!.items.every((item) => tasks.some((task) => task.id === item.taskId && task.enabled === true));
  const plan = plans.find((item) => item.id === draft?.planId);

  return <>
    <header className="page-header protection-wizard-header">
      <div><h1>{t("创建保护")}</h1><p>{t("一次选择多个来源，预览一库一任务映射，再逐项完成首次备份与恢复验证。")}</p></div>
      {step > 0 && step < 4 && <button className="secondary-button" type="button" onClick={() => setStep((value) => Math.max(0, value - 1))}>{t("上一步")}</button>}
    </header>
    <ol className="wizard-steps" aria-label={t("创建保护步骤")}>
      {steps.map((label, index) => <li className={index === step ? "active" : index < step ? "complete" : ""} aria-current={index === step ? "step" : undefined} key={label}><span>{index + 1}</span>{label}</li>)}
    </ol>
    {error && <p className="error-message" role="alert">{error}</p>}
    {message && <p className="operation-feedback operation-success" role="status">{message}</p>}

    {step === 0 && <>
      {drafts.some((item) => item.status !== "cancelled") && <section className="content-section resume-drafts" aria-labelledby="resume-protection-title">
        <div className="section-heading"><div><h2 id="resume-protection-title">{t("继续未完成的保护")}</h2><p>{t("草稿和逐项结果保存在控制服务中，关闭浏览器不会丢失。")}</p></div></div>
        <div className="draft-resume-list">{drafts.filter((item) => item.status !== "cancelled").map((item) => <button className="secondary-button" type="button" key={item.id} onClick={() => void openDraft(item)}>{item.name} · {statusText(item.status, locale)}</button>)}</div>
      </section>}
      <section className="content-section wizard-panel">
        <div className="editor-section-heading"><h2>{t("选择执行节点与保护对象")}</h2><p>{t("数据库名称从已验证连接读取；目录浏览受允许根目录和服务账号权限约束。")}</p></div>
        <div className="form-grid">
          <label>{t("保护对象类型")}<select aria-label={t("保护对象类型")} value={sourceKind} onChange={(event) => { setSourceKind(event.target.value as "directory" | "database"); if (event.target.value === "database") setTargetKind("local"); }}><option value="directory">{t("目录")}</option><option value="database">{t("数据库")}</option></select></label>
          {sourceKind === "directory" && <label>{t("执行节点")}<select value={targetKind} onChange={(event) => { const value = event.target.value as "local" | "agent"; setTargetKind(value); if (value === "agent") setRepositoryKind("sftp"); }}><option value="local">{t("Service 本机")}</option><option value="agent">{t("远程 Agent")}</option></select></label>}
          {sourceKind === "directory" && targetKind === "agent" && <label className="full-field">{t("源端 Agent")}<select aria-label={t("源端 Agent")} value={agentId} onChange={(event) => setAgentId(event.target.value)}><option value="">{t("请选择源端 Agent")}</option>{agents.map((agent) => <option value={String(agent.id)} key={String(agent.id)}>{String(agent.id)}</option>)}</select></label>}
          {sourceKind === "directory" && <>
            <label className="full-field">{t("待保护目录（每行一个）")}<textarea aria-label={t("待保护目录（每行一个）")} value={directoryPaths} onChange={(event) => setDirectoryPaths(event.target.value)} placeholder="/srv/photos&#10;/srv/documents" /></label>
            <div className="directory-picker full-field">
              <label>{t("目录路径")}<input aria-label={t("目录路径")} value={directoryBrowsePath} onChange={(event) => setDirectoryBrowsePath(event.target.value)} /></label>
              <button className="secondary-button" type="button" disabled={browsing || (targetKind === "agent" && !agentId)} onClick={() => void browseDirectory()}>{t(targetKind === "agent" ? "浏览 Agent 目录" : "浏览本机目录")}</button>
              {targetKind === "local" && <button className="text-button" type="button" onClick={() => setShowRoots((value) => !value)}>{t("配置允许根目录")}</button>}
              {showRoots && <div className="allowed-roots-editor"><label>{t("允许根目录（每行一个）")}<textarea aria-label={t("允许根目录（每行一个）")} value={allowedRoots} onChange={(event) => setAllowedRoots(event.target.value)} /></label><button className="secondary-button" type="button" onClick={() => void saveAllowedRoots()}>{t("保存允许根目录")}</button></div>}
              {!!directoryEntries.length && <div className="directory-suggestions" aria-label={t("子目录")}>{directoryEntries.map((entry) => <button className="text-button" type="button" aria-label={locale === "en-US" ? `Select ${entry.name}` : `选择 ${entry.name}`} key={entry.path} onClick={() => { setDirectoryBrowsePath(entry.path); setDirectoryPaths((value) => lines(`${value}\n${entry.path}`).join("\n")); }}>{t("选择")} {entry.name}</button>)}</div>}
              <div className="directory-create-inline"><label>{t("在当前目录中新建")}<input value={newDirectoryName} onChange={(event) => setNewDirectoryName(event.target.value)} placeholder={t("目录名称")} /></label><button className="secondary-button" type="button" disabled={!newDirectoryName.trim() || (targetKind === "agent" && !agentId)} onClick={() => void createDirectory()}>{t("创建并选择")}</button></div>
            </div>
          </>}
          {sourceKind === "database" && <>
            <label className="full-field">{t("数据库连接")}<select aria-label={t("数据库连接")} value={connectionId} onChange={(event) => { setConnectionId(event.target.value); setDatabaseNames([]); setSelectedDatabases([]); }}><option value="">{t("请选择备份用途的数据库连接")}</option>{connections.map((connection) => <option value={String(connection.id)} key={String(connection.id)}>{String(connection.name)} · {String(connection.engine)}</option>)}</select></label>
            <button className="secondary-button" type="button" disabled={!connectionId || enumerating} onClick={() => void enumerateDatabases()}>{t(enumerating ? "正在读取逻辑数据库…" : "读取逻辑数据库")}</button>
            {!!databaseNames.length && <fieldset className="database-selection full-field"><legend>{t("选择要保护的逻辑数据库")}</legend>{databaseNames.map((name) => <label key={name}><input type="checkbox" aria-label={name} checked={selectedDatabases.includes(name)} onChange={(event) => setSelectedDatabases((values) => event.target.checked ? [...values, name] : values.filter((item) => item !== name))} />{name}</label>)}</fieldset>}
          </>}
        </div>
        <div className="wizard-actions"><button className="primary-button" type="button" onClick={enterMappings}>{t("下一步：仓库映射")}</button></div>
      </section>
    </>}

    {step === 1 && <section className="content-section wizard-panel">
      <div className="editor-section-heading"><h2>{t("预览一库一任务映射")}</h2><p>{t("每个保护对象都使用新的独立仓库；不会复用已有仓库或把多个来源混在一起。")}</p></div>
      <div className="form-grid mapping-controls">
        <label>{t("仓库类型")}<select value={repositoryKind} disabled={targetKind === "agent"} onChange={(event) => setRepositoryKind(event.target.value as "local" | "sftp")}><option value="local">{t("本地仓库")}</option><option value="sftp">{t("远程 SFTP 仓库")}</option></select></label>
        {repositoryKind === "sftp" && <label>{t("远程主机")}<select value={remoteHostId} onChange={(event) => setRemoteHostId(event.target.value)}><option value="">{t("请选择远程主机")}</option>{hosts.map((host) => <option value={String(host.id)} key={String(host.id)}>{String(host.name)}</option>)}</select></label>}
        <label className="full-field">{t("仓库基础路径")}<input aria-label={t("仓库基础路径")} value={basePath} onChange={(event) => setBasePath(event.target.value)} /></label>
        <button className="secondary-button" type="button" onClick={updateMappingPaths}>{t("更新映射路径")}</button>
      </div>
      <div className="table-frame"><table aria-label={t("保护映射预览")}><thead><tr><th>{t("保护对象")}</th><th>{t("任务名称")}</th><th>{t("仓库")}</th><th>{t("仓库名称")}</th><th>{t("仓库路径")}</th></tr></thead><tbody>{mappings.map((item, index) => <tr key={item.key}><td>{item.label}</td><td><input aria-label={locale === "en-US" ? `Task name ${index + 1}` : `任务名称 ${index + 1}`} value={item.taskName} onChange={(event) => setMappings((values) => values.map((value, itemIndex) => itemIndex === index ? { ...value, taskName: event.target.value } : value))} /></td><td>{t("独立仓库")} {index + 1}</td><td><input aria-label={locale === "en-US" ? `Repository name ${index + 1}` : `仓库名称 ${index + 1}`} value={item.repositoryName} onChange={(event) => setMappings((values) => values.map((value, itemIndex) => itemIndex === index ? { ...value, repositoryName: event.target.value } : value))} /></td><td><input aria-label={locale === "en-US" ? `Repository path ${index + 1}` : `仓库路径 ${index + 1}`} value={item.repositoryPath} onChange={(event) => setMappings((values) => values.map((value, itemIndex) => itemIndex === index ? { ...value, repositoryPath: event.target.value } : value))} /></td></tr>)}</tbody></table></div>
      <div className="wizard-actions"><button className="secondary-button" type="button" onClick={() => setStep(0)}>{t("上一步")}</button><button className="primary-button" type="button" disabled={repositoryKind === "sftp" && !remoteHostId} onClick={() => setStep(2)}>{t("下一步：保护策略")}</button></div>
    </section>}

    {step === 2 && <section className="content-section wizard-panel">
      <div className="editor-section-heading"><h2>{t("选择计划、保留与资源策略")}</h2><p>{t("模板只复用策略，不包含来源、仓库绑定、密码或 SSH 私钥。")}</p></div>
      <div className="form-grid">
        <label className="full-field">{t("保护模板")}<select aria-label={t("保护模板")} value={templateId} onChange={(event) => setTemplateId(event.target.value)}><option value="">{t("自定义本次策略")}</option>{templates.map((template) => <option value={template.id} key={template.id}>{template.name}</option>)}</select></label>
        {!templateId && <>
          <label>{t("保留每日快照数")}<input type="number" min={0} value={keepDaily} onChange={(event) => setKeepDaily(Number(event.target.value))} /></label>
          <label>{t("保留每月快照数")}<input type="number" min={0} value={keepMonthly} onChange={(event) => setKeepMonthly(Number(event.target.value))} /></label>
          <label>{t("每日执行时间")}<input type="time" value={scheduleTime} onChange={(event) => setScheduleTime(event.target.value)} /></label>
          <label>{t("同时执行数")}<input type="number" min={1} value={maxParallel} onChange={(event) => setMaxParallel(Number(event.target.value))} /></label>
          <label>{t("补跑窗口（分钟）")}<input type="number" min={0} value={catchUpMinutes} onChange={(event) => setCatchUpMinutes(Number(event.target.value))} /></label>
          <label>{t("压缩模式")}<select value={compression} onChange={(event) => setCompression(event.target.value)}><option value="auto">auto</option><option value="off">off</option><option value="max">max</option></select></label>
          <label>{t("上传限速（KiB/s，0 不限制）")}<input type="number" min={0} value={uploadLimit} onChange={(event) => setUploadLimit(Number(event.target.value))} /></label>
          <label>{t("最长无完整成功（小时）")}<input type="number" min={0} value={healthHours} onChange={(event) => setHealthHours(Number(event.target.value))} /></label>
          <label className="full-field checkbox-label"><input type="checkbox" checked={saveTemplate} onChange={(event) => setSaveTemplate(event.target.checked)} />{t("将本次策略保存为模板")}</label>
          {saveTemplate && <label className="full-field">{t("新模板名称")}<input value={newTemplateName} onChange={(event) => setNewTemplateName(event.target.value)} required /></label>}
        </>}
        <label className="full-field">{t("通知选择")}<select value={notificationMode} onChange={(event) => setNotificationMode(event.target.value as "configured" | "none")}><option value="configured">{t("使用已配置通知通道")}</option><option value="none">{t("本次暂不要求通知")}</option></select><span className={notificationReady ? "field-hint" : "field-hint warning-text"}>{t(notificationReady ? "通知通道已就绪" : "通知通道尚未配置，检查表会保持待处理")}</span></label>
      </div>
      <div className="wizard-actions"><button className="secondary-button" type="button" onClick={() => setStep(1)}>{t("上一步")}</button><button className="primary-button" type="button" disabled={saveTemplate && !newTemplateName.trim()} onClick={() => setStep(3)}>{t("下一步：确认创建")}</button></div>
    </section>}

    {step === 3 && <section className="content-section wizard-panel">
      <div className="editor-section-heading"><h2>{t("确认映射并保存仓库密码")}</h2><p>{t("密码只在本步骤显示。草稿仅保存秘密库引用，恢复页面不会再次显示明文。")}</p></div>
      <div className="password-grid">{mappings.map((item, index) => <article className="password-card" key={item.key}><span>{item.label}</span><strong>{item.repositoryName}</strong><code>{item.password}</code><button className="text-button" type="button" onClick={() => { void navigator.clipboard?.writeText(item.password); setMessage(t("仓库密码已复制")); }}>{t("复制仓库密码")}</button><small>{item.repositoryPath} · {t("独立仓库")} {index + 1}</small></article>)}</div>
      <label className="confirmation-check"><input type="checkbox" aria-label={t("我已安全保存全部独立仓库密码")} checked={passwordsConfirmed} onChange={(event) => setPasswordsConfirmed(event.target.checked)} />{t("我已安全保存全部独立仓库密码")}</label>
      <div className="wizard-actions"><button className="secondary-button" type="button" onClick={() => setStep(2)}>{t("上一步")}</button><button className="primary-button" type="button" disabled={!passwordsConfirmed || application.active} onClick={() => void saveDraftAndApply()}>{t("保存草稿并创建保护")}</button></div>
    </section>}

    {step === 4 && draft && <section className="wizard-result">
      <section className="content-section checklist-heading"><div><h2>{t("保护检查表")}</h2><p>{t("资源创建只是开始；完成范围确认、首次完整成功、计划、通知、维护 dry-run 与恢复验证才算闭环。")}</p></div><StatusIndicator value={checklist?.complete ? "success" : "pending"} locale={locale} label={t(checklist?.complete ? "保护闭环已完成" : "仍有待处理项")} variant="pill" /></section>
      <OperationFeedback operation={application} locale={locale} />
      <OperationFeedback operation={firstRun} locale={locale} />
      <section className="content-section itemized-results" aria-label={t("逐项创建结果")}>
        {draft.items.map((item) => {
          const task = tasks.find((value) => value.id === item.taskId);
          const enabled = task?.enabled === true;
          const preview = scopePreviews[item.taskId];
          const check = checklist?.items.find((value) => value.taskId === item.taskId);
          return <article className={`protection-result protection-result-${item.status}`} key={item.id}>
            <div><span>{item.database ? `${item.database.database}` : item.directory?.path}</span><h3>{item.taskName}</h3><p>{item.repositoryName} · {item.repositoryPath}</p></div>
            <StatusIndicator value={item.status === "applying" ? "running" : item.status} locale={locale} label={statusText(item.status, locale)} tone={protectionItemTone(item.status)} variant="pill" />
            {item.error && <p className="error-message">{item.error}</p>}
            {item.status === "ready" && task && !enabled && item.directory && !preview && <button className="secondary-button" type="button" onClick={() => void previewScope(item, task)}>{t("预览来源范围")}</button>}
            {preview && !enabled && <div className="scope-confirmation"><p>{t("范围预览已生成；确认后才会启用任务。")}</p><button className="primary-button" type="button" onClick={() => void activateTask(item, task!)}>{t("确认范围并启用")}</button></div>}
            {item.status === "ready" && task && !enabled && item.database && <button className="primary-button" type="button" onClick={() => void activateTask(item, task)}>{t("启用数据库任务")}</button>}
            {enabled && check?.firstCompleteSuccessStatus !== "complete" && <button className="secondary-button" type="button" disabled={firstRun.active} onClick={() => void firstRun.start(`/api/tasks/${encodeURIComponent(item.taskId)}/run`, {})}>{t("执行首次备份")}</button>}
            {check && <ul className="mini-checklist"><li>{t("任务启用")}：{checkStatusText(check.activationStatus, locale)}</li><li>{t("首次完整成功")}：{checkStatusText(check.firstCompleteSuccessStatus, locale)}</li><li>{t("维护计划")}：{checkStatusText(check.maintenanceStatus, locale)}</li><li>{t("恢复验证")}：{checkStatusText(check.restoreVerificationStatus, locale)}</li></ul>}
          </article>;
        })}
      </section>
      {checklist && <section className="content-section collection-checklist"><h2>{t("集合级检查")}</h2><dl><div><dt>{t("备份计划")}</dt><dd>{checkStatusText(checklist.planStatus, locale)}{checklist.nextRun ? ` · ${new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short", timeZone }).format(new Date(checklist.nextRun))}` : ""}</dd></div><div><dt>{t("通知通道")}</dt><dd>{checkStatusText(checklist.notificationStatus, locale)}</dd></div></dl>{allTasksEnabled && plan?.enabled !== true && <button className="primary-button" type="button" onClick={() => void activatePlan()}>{t("启用备份计划")}</button>}</section>}
      <div className="wizard-actions result-actions">
        {(draft.status === "partial" || draft.status === "pending") && <button className="primary-button" type="button" disabled={application.active} onClick={() => void retryDraft()}>{t("继续创建未完成项")}</button>}
        {(draft.status === "partial" || draft.status === "pending") && <button className="danger-button" type="button" disabled={application.active} onClick={() => void cancelDraft()}>{t("清理未完成草稿")}</button>}
        <button className="secondary-button" type="button" onClick={() => void refreshResult(draft.id)}>{t("刷新检查表")}</button>
        <button className="secondary-button" type="button" onClick={() => onNavigate("通知配置")}>{t("配置通知")}</button>
        <button className="secondary-button" type="button" onClick={() => onNavigate("备份仓库")}>{t("复核维护 dry-run")}</button>
        <button className="secondary-button" type="button" onClick={() => onNavigate("快照与恢复")}>{t("配置恢复验证")}</button>
        <button className="text-button" type="button" onClick={resetWizard}>{t("创建另一组保护")}</button>
      </div>
    </section>}
  </>;
}

function statusText(status: string, locale: Locale) {
  const t = (source: string) => translate(locale, source);
  switch (status) {
    case "pending": return t("等待创建");
    case "applying": return t("正在创建");
    case "partial": return t("部分完成");
    case "ready": return t("资源已创建");
    case "failed": return t("创建失败");
    case "cancelled": return t("已清理");
    case "retained": return t("已创建并保留");
    default: return t("状态未知");
  }
}

function checkStatusText(status: string, locale: Locale) {
  const t = (source: string) => translate(locale, source);
  switch (status) {
    case "ready": case "scheduled": case "complete": case "verified": return t("已完成");
    case "disabled": return t("待启用");
    case "pending": case "pending_review": return t("待处理");
    case "not_configured": return t("未配置");
    case "not_supported": return t("当前不支持");
    case "skipped": return t("已跳过");
    case "missing": return t("尚未创建");
    default: return t("状态未知");
  }
}

function protectionItemTone(status: string): StatusTone {
  if (status === "applying") return "active";
  if (["ready", "retained"].includes(status)) return "success";
  if (status === "failed") return "danger";
  if (status === "partial") return "warning";
  if (status === "cancelled") return "stopped";
  return "pending";
}
