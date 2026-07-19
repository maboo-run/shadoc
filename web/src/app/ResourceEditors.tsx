import { useEffect, useRef, useState } from "react";
import { useModalFocus } from "./useModalFocus";
import { StatusIndicator } from "./StatusIndicator";
import { ModalPortal } from "./ModalPortal";
import { Toast } from "./Toast";
import { OperationFeedback, useOperation } from "./OperationFeedback";
import {
  ProtectionPolicyEditor,
  defaultRetentionPolicy,
  emptyResourcePolicy,
  normalizeResourcePolicy,
  normalizeRetentionPolicy,
  type ResourcePolicy,
  type RetentionPolicy,
} from "./ProtectionPolicyEditor";
import { translate, type Locale } from "../i18n";

type ResourceAPI = {
  listResource(resource: string): Promise<Array<Record<string, unknown>>>;
  createResource(resource: string, payload: Record<string, unknown>): Promise<unknown>;
  updateResource(resource: string, id: string, payload: Record<string, unknown>): Promise<void>;
  action(path: string, payload?: Record<string, unknown>): Promise<unknown>;
  saveMaintenance(id: string, payload: Record<string, unknown>): Promise<void>;
};

type EditorProps = {
  api: ResourceAPI;
  initial: Record<string, unknown> | null;
  onClose(): void;
  onSubmit(payload: Record<string, unknown>): Promise<void>;
  locale?: Locale;
};

type TaskEditorProps = Omit<EditorProps, "onSubmit"> & {
  onDraftSaved(): Promise<void>;
  onSaved(): Promise<void>;
};

type TaskScopeImpact = {
  rule: string;
  reason?: string;
  matchedFiles: number;
  estimatedBytes: number;
};

type TaskScopePreview = {
  previewId: string;
  fingerprint: string;
  requiresDeleteConfirmation: boolean;
  expiresAt?: string;
  summary: {
    scannedItems?: number;
    includedFiles?: number;
    includedBytes?: number;
    excludedFiles?: number;
    excludedBytes?: number;
    unreadableItems?: number;
    truncated?: boolean;
    activeRules?: TaskScopeImpact[];
    suggestions?: TaskScopeImpact[];
    deleteFiles?: number;
    deleteDirectories?: number;
    targetIdentity?: string;
  };
};

type RepositoryOption = {
  id: string;
  name: string;
	engine: "restic" | "rsync";
  kind: "local" | "sftp" | "s3";
  path: string;
  status: string;
};

type TaskOption = { id: string; repositoryId: string };

type TaskPlanRecord = {
  id?: string;
  name?: string;
  schedule?: { kind?: "daily" | "weekly" | "interval"; timeOfDay?: string; dayOfWeek?: number; intervalHours?: number };
  timezone?: string;
  maxParallel?: number;
  catchUpWindowMinutes?: number;
  taskIds?: string[];
  enabled?: boolean;
};

type ConnectionOption = {
  id: string;
  name: string;
  engine: string;
  purpose: string;
  host: string;
};

type DirectoryEntry = { name: string; path: string; directory: boolean };

type DirectoryPathStyle = "posix" | "windows";
type DirectoryCreation = { parent: string; name: string };

function splitDirectoryPath(value: string, style: DirectoryPathStyle): DirectoryCreation {
  if (style === "windows") {
    const normalized = value.replaceAll("/", "\\").replace(/\\+$/, "");
    const separator = normalized.lastIndexOf("\\");
    const parent = separator === 2 && normalized[1] === ":" ? normalized.slice(0, 3) : normalized.slice(0, separator);
    return { parent, name: normalized.slice(separator + 1) };
  }
  const normalized = value === "/" ? value : value.replace(/\/+$/, "");
  const separator = normalized.lastIndexOf("/");
  return { parent: separator <= 0 ? "/" : normalized.slice(0, separator), name: normalized.slice(separator + 1) };
}

function joinDirectoryPath(parent: string, name: string, style: DirectoryPathStyle): string {
  if (style === "windows") return `${parent.replace(/[\\/]+$/, "")}\\${name}`;
  return parent === "/" ? `/${name}` : `${parent.replace(/\/+$/, "")}/${name}`;
}

function validDirectoryName(value: string): boolean {
  const name = value.trim();
  return Boolean(name && name !== "." && name !== ".." && !/[\\/\0\r\n]/.test(name));
}

function pathNotFound(message: string): boolean {
  return /no such file|cannot find the (?:file|path)|not found|不存在|系统找不到/i.test(message);
}

function permissionDenied(message: string): boolean {
  return /permission denied|access is denied|拒绝访问|没有权限|无权限/i.test(message);
}

function agentPathStyle(agent?: Record<string, unknown>): DirectoryPathStyle {
  const capabilities = Array.isArray(agent?.capabilities) ? agent.capabilities : [];
  return capabilities.includes("path-style:windows") ? "windows" : "posix";
}

function agentHasEngine(agent: Record<string, unknown> | undefined, engine: "restic" | "rsync"): boolean {
  const capabilities = Array.isArray(agent?.capabilities) ? agent.capabilities.map(String) : [];
  return capabilities.includes(engine);
}

function agentEligibleForEngine(agent: Record<string, unknown> | undefined, engine: "restic" | "rsync"): boolean {
  return Boolean(agent && agent.taskEligible !== false && agentHasEngine(agent, engine));
}

function agentTaskEligibility(agent: Record<string, unknown> | undefined, locale: Locale, engine: "restic" | "rsync"): string {
  if (!agent) return "";
  const t = (source: string) => translate(locale, source);
  if (agent.taskEligible !== false) return agentHasEngine(agent, engine) ? t("可用于任务") : t(engine === "restic" ? "缺少 Restic 能力" : "缺少 rsync 能力");
  const labels: Record<string, string> = {
    offline: "心跳已超时",
    incompatible: "协议不兼容",
    certificate_expired: "证书已过期",
    draining: "正在排空任务",
    revoked: "身份已撤销",
  };
  return t(labels[String(agent.compatibilityStatus ?? "")] ?? "尚不能执行任务");
}

function CreateDirectoryDialog({ request, pathStyle, locale, onClose, onCreate }: {
  request: DirectoryCreation;
  pathStyle: DirectoryPathStyle;
  locale: Locale;
  onClose(): void;
  onCreate(path: string): Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLDivElement>(null);
  const [directoryName, setDirectoryName] = useState(request.name);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const target = validDirectoryName(directoryName) ? joinDirectoryPath(request.parent, directoryName.trim(), pathStyle) : "";
  useModalFocus(dialogRef, () => { if (!creating) onClose(); });

  const submit = async () => {
    if (!target || creating) return;
    setCreating(true);
    setError("");
    try {
      await onCreate(target);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("创建目录失败"));
      setCreating(false);
    }
  };

  return <ModalPortal>
    <div ref={dialogRef} className="dialog directory-create-dialog" role="dialog" aria-modal="true" aria-labelledby="create-directory-title">
      <header><div><h2 id="create-directory-title">{t("新建目录")}</h2><p>{t("新目录将在所选父目录中创建。请只输入目录名称，系统会生成完整绝对路径。")}</p></div></header>
      <div className="form-grid">
        <label className="full-field">{t("父目录")}<input disabled readOnly value={request.parent} /></label>
        {pathStyle === "posix" && request.parent === "/home" && <p className="directory-dialog-warning full-field">{t("该父目录通常不可由普通 Agent 账户写入。建议取消并进入该账户自己的用户主目录。")}</p>}
        <label className="full-field">{t("新目录名称")}<input required maxLength={255} value={directoryName} aria-invalid={directoryName.length > 0 && !validDirectoryName(directoryName)} onChange={(event) => setDirectoryName(event.target.value)} onKeyDown={(event) => {
          if (event.key === "Enter") { event.preventDefault(); void submit(); }
        }} placeholder={t("例如 restic")} /></label>
        <div className="directory-path-preview full-field"><span>{t("将创建")}</span><code>{target || t("输入目录名称后显示完整路径")}</code></div>
        {directoryName.length > 0 && !validDirectoryName(directoryName) && <p className="field-hint warning-text full-field">{t("目录名称不能是点号、双点号，也不能包含斜杠或换行。")}</p>}
        {error && <div className="error-message full-field" role="alert"><span>{error}</span>{permissionDenied(error) && <p>{t("Agent 运行账户无权写入该父目录。请选择该账户拥有写权限的目录，例如它自己的用户主目录。")}</p>}</div>}
      </div>
      <footer>
        <button className="secondary-button" type="button" disabled={creating} onClick={onClose}>{t("取消")}</button>
        <button className="primary-button" type="button" disabled={!target || creating} onClick={() => void submit()}>{t(creating ? "正在创建目录…" : "创建目录")}</button>
      </footer>
    </div>
  </ModalPortal>;
}

function AgentDirectoryInput({ api, agentId, name, label, initialValue, placeholder, pathStyle = "posix", locale = "zh-CN", onPathChange }: { api: ResourceAPI; agentId: string; name: string; label: string; initialValue: string; placeholder: string; pathStyle?: DirectoryPathStyle; locale?: Locale; onPathChange?(): void }) {
  const t = (source: string) => translate(locale, source);
  const [path, setPath] = useState(initialValue);
  const [entries, setEntries] = useState<DirectoryEntry[]>([]);
  const [browseState, setBrowseState] = useState<"idle" | "loading" | "ready" | "error">("idle");
  const [browsedPath, setBrowsedPath] = useState("");
  const [browseError, setBrowseError] = useState("");
  const [message, setMessage] = useState("");
  const [creation, setCreation] = useState<DirectoryCreation | null>(null);
  const browseRequest = useRef(0);
  const selectPath = (value: string) => {
    browseRequest.current += 1;
    setPath(value);
    setEntries([]);
    setBrowseState("idle");
    setBrowsedPath("");
    setBrowseError("");
    onPathChange?.();
  };
  const browse = (value: string) => {
    if (!agentId || !value.trim()) return;
    const request = ++browseRequest.current;
    setBrowseState("loading");
    setBrowseError("");
    void api.action(`/api/agents/${encodeURIComponent(agentId)}/filesystem/browse`, { path: value.trim() })
      .then((result) => {
        if (request !== browseRequest.current) return;
        const summary = result as Record<string, unknown>;
        setEntries(Array.isArray(summary.entries) ? summary.entries as DirectoryEntry[] : []);
        setBrowsedPath(String(summary.path ?? value.trim()));
        setBrowseState("ready");
      })
      .catch((cause) => {
        if (request !== browseRequest.current) return;
        setEntries([]);
        setBrowsedPath("");
        setBrowseError(cause instanceof Error ? cause.message : t("无法读取目录"));
        setBrowseState("error");
      });
  };
  useEffect(() => {
    if (!agentId || !path.trim()) {
      browseRequest.current += 1;
      setEntries([]);
      setBrowsedPath("");
      setBrowseState("idle");
      return;
    }
    const timer = window.setTimeout(() => browse(path), 350);
    return () => window.clearTimeout(timer);
  }, [agentId, path]); // eslint-disable-line react-hooks/exhaustive-deps
  const missingPath = browseState === "error" && pathNotFound(browseError) ? splitDirectoryPath(path.trim(), pathStyle) : null;
  return <div className="full-field">
    <label>{label}<input name={name} value={path} onChange={(event) => selectPath(event.target.value)} placeholder={placeholder} required /></label>
    {browseState === "loading" && <span className="field-hint" role="status">{t("正在读取目录…")}</span>}
    {browseState === "ready" && browsedPath === path.trim() && <div className="directory-browser" aria-label={t("子目录")}>
      <div className="directory-browser-toolbar">
        <div><strong>{locale === "en-US" ? `Current directory: ${browsedPath}` : `当前目录：${browsedPath}`}</strong><span>{locale === "en-US" ? `${entries.length} subdirectories` : `${entries.length} 个子目录`}</span></div>
        <button className="secondary-button" type="button" onClick={() => setCreation({ parent: browsedPath, name: "" })}>{t("在此新建目录")}</button>
      </div>
      {pathStyle === "posix" && browsedPath === "/home" && <p className="directory-location-hint">{t("“/home”通常由系统账户管理。请先进入 Agent 运行账户自己的用户目录，再新建目录。")}</p>}
      <div className="directory-suggestions">
        {entries.map((entry) => <button className="text-button" type="button" key={entry.path} onClick={() => selectPath(entry.path)}>📁 {entry.name}</button>)}
        {!entries.length && <p className="directory-empty">{t("此目录中没有子目录")}</p>}
      </div>
    </div>}
    {browseState === "error" && <div className="directory-read-error" role="status">
      <strong>{permissionDenied(browseError) ? t("Agent 运行账户没有该目录的访问权限") : missingPath ? t("该路径尚不存在") : t("无法读取目录")}</strong>
      <p>{permissionDenied(browseError) ? t("请选择 Agent 运行账户可读写的目录，例如它自己的用户主目录。") : missingPath ? t("可以创建这个绝对路径，然后将它用作当前选择。") : t("请检查路径后重试。")}</p>
      {missingPath && validDirectoryName(missingPath.name) && <button className="secondary-button" type="button" onClick={() => setCreation(missingPath)}>{t("创建此路径")}</button>}
      <details><summary>{t("查看错误详情")}</summary><code>{browseError}</code></details>
    </div>}
    {creation && <CreateDirectoryDialog request={creation} pathStyle={pathStyle} locale={locale} onClose={() => setCreation(null)} onCreate={async (target) => {
      await api.action(`/api/agents/${encodeURIComponent(agentId)}/filesystem/directories`, { path: target });
      setCreation(null);
      const refreshCurrentPath = path.trim() === target;
      selectPath(target);
      setMessage(t("目录已创建"));
      if (refreshCurrentPath) browse(target);
    }} />}
    <Toast message={message} locale={locale} onClose={() => setMessage("")} />
  </div>;
}

export function RepositoryEditor({ api, initial, onClose, onSubmit, locale = "zh-CN" }: EditorProps) {
	const t = (source: string) => translate(locale, source);
	const [engine, setEngine] = useState<"restic" | "rsync">(initial?.engine === "rsync" ? "rsync" : "restic");
  const [connectionMode, setConnectionMode] = useState<"create" | "existing">("create");
  const initialKind = initial?.kind === "s3" ? "s3" : initial?.kind === "sftp" || initial?.remoteHostId ? "sftp" : "local";
  const [kind, setKind] = useState<"local" | "sftp" | "s3">(initialKind);
  const [selectedRemoteHostId, setSelectedRemoteHostId] = useState(String(initial?.remoteHostId ?? ""));
  const [hosts, setHosts] = useState<Array<Record<string, unknown>>>([]);
  const [agents, setAgents] = useState<Array<Record<string, unknown>>>([]);
  const [browseAgentId, setBrowseAgentId] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [passwordMode, setPasswordMode] = useState<"generated" | "custom">("generated");
  const [password, setPassword] = useState("");
  const [passwordConfirmation, setPasswordConfirmation] = useState("");
  const [showPassword, setShowPassword] = useState(false);
  const [passwordConfirmed, setPasswordConfirmed] = useState(false);
  const initialS3 = (initial?.s3 ?? {}) as Record<string, unknown>;
  const [s3AccessKey, setS3AccessKey] = useState("");
  const [s3SecretKey, setS3SecretKey] = useState("");
  const [s3CredentialsConfirmed, setS3CredentialsConfirmed] = useState(false);
  const [maintenanceEnabled, setMaintenanceEnabled] = useState(false);
  const [policy, setPolicy] = useState<Record<string, any>>({});
  const [maintenanceRetention, setMaintenanceRetention] = useState<RetentionPolicy>({ ...defaultRetentionPolicy });
  const [preview, setPreview] = useState<Record<string, any> | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [maintenanceDirty, setMaintenanceDirty] = useState(false);
	const readyRepository = Boolean(engine === "restic" && initial?.id && initial?.status === "ready");
	const existingConnection = Boolean(!initial && engine === "restic" && connectionMode === "existing");
	const showMaintenance = engine === "restic" && (initial ? initial.status !== "disconnected" : connectionMode === "create");
	const repositoryPasswordInvalid = engine === "restic" && !initial && (
		!passwordConfirmed
		|| (existingConnection ? password.length === 0 : password.length < 12)
		|| (passwordMode === "custom" && password !== passwordConfirmation)
	);
	const s3CredentialsProvided = Boolean(s3AccessKey || s3SecretKey);
	const s3Invalid = kind === "s3" && (
		(!initial && (!s3AccessKey || !s3SecretKey || !s3CredentialsConfirmed))
		|| (Boolean(initial) && s3CredentialsProvided && (!s3AccessKey || !s3SecretKey || !s3CredentialsConfirmed))
	);
	const canSelectRepositoryBrowseAgent = engine === "rsync" && kind === "local";
	const repositoryBrowseAgent = kind === "sftp"
		? agents.find((agent) => String(agent.remoteHostId ?? "") === selectedRemoteHostId)
		: canSelectRepositoryBrowseAgent ? agents.find((agent) => String(agent.id ?? "") === browseAgentId) : undefined;
	const repositoryBrowseAgentId = String(repositoryBrowseAgent?.id ?? "");

  useEffect(() => {
    let active = true;
    void Promise.all([api.listResource("remote-hosts"), api.listResource("agents")])
      .then(([hostItems, agentItems]) => { if (active) { setHosts(hostItems); setAgents(agentItems.filter((item) => item.status === "online" && item.taskEligible !== false && (item.capabilities as unknown[] | undefined)?.includes("filesystem-browse"))); } })
      .catch(() => active && setError("无法读取远程主机"));
    return () => {
      active = false;
    };
  }, [api]);

  useEffect(() => {
	if (!initial?.id || engine !== "restic") return;
    let active = true;
    void api.action(`/api/repositories/${String(initial.id)}/maintenance-policy`).then((value) => {
      if (!active) return;
      const current = (value ?? {}) as Record<string, any>;
      setPolicy(current);
      setMaintenanceRetention(normalizeRetentionPolicy(current.retention, defaultRetentionPolicy));
      setMaintenanceEnabled(Boolean(current.enabled));
    }).catch(() => active && setError("无法读取仓库维护设置"));
    return () => { active = false; };
	}, [api, initial?.id, engine]);

  const maintenanceValues = (form: HTMLFormElement) => {
    const value = (name: string) => (form.elements.namedItem(name) as HTMLInputElement | HTMLSelectElement | null)?.value ?? "";
    return {
      schedule: { kind: "weekly", dayOfWeek: Number(value("dayOfWeek")), timeOfDay: value("timeOfDay") },
      timezone: value("timezone"),
      retention: maintenanceRetention,
      catchUpWindowMinutes: Number(value("catchUpWindowMinutes")),
      enabled: maintenanceEnabled,
    };
  };

  return (
    <section className="resource-editor-page" aria-label={t(initial ? "编辑备份仓库" : "新建备份仓库")}>
      <header className="editor-page-header">
        <div>
          <button className="text-button back-button" type="button" aria-label={t("返回仓库列表")} onClick={onClose}>← {t("返回仓库列表")}</button>
          <h1>{t(initial ? "编辑备份仓库" : "新建备份仓库")}</h1>
          <p>{t("配置仓库连接、保留策略和版本维护；任务只负责产生备份。")}</p>
        </div>
      </header>
      <form
        className="resource-editor-form"
        onSubmit={(event) => {
          event.preventDefault();
          const form = new FormData(event.currentTarget);
          const value = (key: string) => String(form.get(key) ?? "");
		  if (engine === "restic" && !initial && (existingConnection ? password.length === 0 : password.length < 12)) {
            setError(t(existingConnection ? "请输入已有仓库密码" : "请先生成或输入至少 12 位的仓库密码"));
            return;
          }
		  if (engine === "restic" && !initial && passwordMode === "custom" && password !== passwordConfirmation) {
            setError("两次输入的仓库密码不一致");
            return;
          }
		  if (engine === "restic" && !initial && !passwordConfirmed) {
            setError("请先确认仓库密码已安全保存到应用之外");
            return;
          }
		  if (s3Invalid) {
			setError(t("请完整填写并确认 S3 凭据"));
			return;
		  }
          setError("");
          const maintenance = maintenanceValues(event.currentTarget);
          if (readyRepository && maintenanceDirty && !preview?.previewId) {
            setError("保存维护设置前，请先生成与当前设置一致的 dry-run 预览");
            return;
          }
          const repositoryPayload = {
            name: value("name"),
			engine,
            kind,
            remoteHostId: kind === "sftp" ? value("remoteHostId") : "",
            path: value("path"),
            password: initial ? value("password") : password,
			passwordConfirmed: Boolean(initial) || passwordConfirmed,
			...(kind === "s3" ? { s3: {
				endpoint: value("s3Endpoint"), bucket: value("s3Bucket"), region: value("s3Region"), prefix: value("s3Prefix"),
				pathStyle: form.has("s3PathStyle"), accessKey: s3AccessKey, secretKey: s3SecretKey, credentialsConfirmed: s3CredentialsConfirmed,
			} } : {}),
			...(!initial && engine === "restic" ? { connectionMode } : {}),
			...(!initial && engine === "restic" && connectionMode === "create" ? { maintenance } : {}),
          };
          const save = initial?.id && maintenanceDirty
            ? api.saveMaintenance(String(initial.id), { ...maintenance, previewId: preview?.previewId }).then(() => onSubmit(repositoryPayload))
            : onSubmit(repositoryPayload);
          void save.catch((cause) =>
            setError(cause instanceof Error ? cause.message : t(existingConnection ? "连接已有仓库失败" : "保存失败")),
          );
        }}
      >
        {error && <p className="form-error form-banner">{error}</p>}
        <section className="editor-section">
          <div className="editor-section-heading"><h2>{t("仓库信息")}</h2><p>{t("本地仓库由当前服务账号读写；远程仓库通过已验证的 SSH 主机访问。")}</p></div>
          <div className="form-grid">
          <label>
            {t("名称")}
            <input name="name" defaultValue={String(initial?.name ?? "")} required />
          </label>
		  <label>
			{t("仓库引擎")}
			<select value={engine} disabled={Boolean(initial?.id)} onChange={(event) => {
			  const selected = event.target.value as "restic" | "rsync";
			  setEngine(selected);
			  if (selected === "rsync" && kind === "s3") setKind("local");
			}}>
			  <option value="restic">{t("Restic 备份仓库")}</option>
			  <option value="rsync">{t("rsync 同步仓库")}</option>
			</select>
		  </label>
          {!initial && engine === "restic" && <label className="full-field">
            {t("仓库接入方式")}
            <select aria-label={t("仓库接入方式")} value={connectionMode} onChange={(event) => {
              const mode = event.target.value as "create" | "existing";
              setConnectionMode(mode);
              setPasswordMode(mode === "existing" ? "custom" : "generated");
              setPassword("");
              setPasswordConfirmation("");
              setPasswordConfirmed(false);
            }}>
              <option value="create">{t("创建新仓库")}</option>
              <option value="existing">{t("连接已有仓库")}</option>
            </select>
          </label>}
          {existingConnection && <div className="operation-feedback full-field" role="note">
            <strong>{t("不会初始化或修改远端仓库")}</strong>
            <p>{t("系统只会执行固定的只读快照验证，核对路径、固定主机、密码、仓库格式及快照可读性；验证通过后才能浏览和恢复。")}</p>
          </div>}
          <label>
            {t("仓库类型")}
            <select value={kind} onChange={(event) => setKind(event.target.value as "local" | "sftp" | "s3")}>
              <option value="local">{t("本地仓库")}</option>
              <option value="sftp">{t("远程 SFTP 仓库")}</option>
              {engine === "restic" && <option value="s3">{t("S3 兼容对象存储")}</option>}
            </select>
          </label>
          {kind === "sftp" && (
            <label className="full-field">
              {t("远程主机")}
              <select name="remoteHostId" value={selectedRemoteHostId} onChange={(event) => setSelectedRemoteHostId(event.target.value)} required>
                <option value="" disabled>{t("请选择远程主机")}</option>
                {hosts.map((host) => (
                  <option key={String(host.id)} value={String(host.id)}>
                    {String(host.name)} · {String(host.username)}@{String(host.host)}
                  </option>
                ))}
              </select>
              {!hosts.length && <span className="field-hint warning-text">{t("请先创建并验证远程主机")}</span>}
            </label>
          )}
          {canSelectRepositoryBrowseAgent && <label className="full-field">{t("目录浏览 Agent（可选）")}
            <select aria-label={t("目录浏览 Agent（可选）")} value={browseAgentId} onChange={(event) => setBrowseAgentId(event.target.value)}>
              <option value="">{t("不使用 Agent，手工输入路径")}</option>
              {agents.map((agent) => <option key={String(agent.id)} value={String(agent.id)}>{String(agent.id)}</option>)}
            </select>
            <span className="field-hint">{t("选择 Agent 后可直接查看并创建目标目录；该选择不改变仓库与任务的绑定关系。")}</span>
            {!agents.length && <span className="field-hint warning-text">{t("没有在线且支持目录浏览的 Agent；仍可手工输入路径。")}</span>}
          </label>}
          {kind === "s3" ? <fieldset className="full-field">
            <legend>{t("S3 后端设置")}</legend>
            <div className="operation-feedback" role="note">
              <strong>{t("仅支持结构化 S3 配置")}</strong>
              <p>{t("服务只会生成固定的 Restic S3 参数；远端必须使用可信 HTTPS，只有本机回环测试环境可使用 HTTP。")}</p>
            </div>
            <div className="form-grid">
              <label>{t("S3 端点")}<input name="s3Endpoint" type="url" defaultValue={String(initialS3.endpoint ?? "")} placeholder="https://objects.example.com" required /></label>
              <label>{t("存储桶")}<input name="s3Bucket" defaultValue={String(initialS3.bucket ?? "")} placeholder="backup-prod" required /></label>
              <label>{t("区域")}<input name="s3Region" defaultValue={String(initialS3.region ?? "")} placeholder="us-east-1" required /></label>
              <label>{t("对象前缀（可选）")}<input name="s3Prefix" defaultValue={String(initial?.path ?? "")} placeholder="photos/main" /></label>
              <label className="checkbox-field"><input name="s3PathStyle" type="checkbox" defaultChecked={Boolean(initialS3.pathStyle)} />{t("使用 Path-style 存储桶寻址")}</label>
              <label>{t("S3 Access Key")}<input aria-label={t("S3 Access Key")} type="password" autoComplete="off" value={s3AccessKey} onChange={(event) => { setS3AccessKey(event.target.value); setS3CredentialsConfirmed(false); }} required={!initial} /></label>
              <label>{t("S3 Secret Key")}<input aria-label={t("S3 Secret Key")} type="password" autoComplete="off" value={s3SecretKey} onChange={(event) => { setS3SecretKey(event.target.value); setS3CredentialsConfirmed(false); }} required={!initial} /></label>
              {initial && <p className="field-hint full-field">{t("凭据已加密保存；两项都留空可保留现有凭据，填写则会整体轮换。")}</p>}
              {(!initial || s3CredentialsProvided) && <label className="checkbox-field full-field"><input aria-label={t("确认 S3 凭据用途")} type="checkbox" checked={s3CredentialsConfirmed} disabled={!s3AccessKey || !s3SecretKey} onChange={(event) => setS3CredentialsConfirmed(event.target.checked)} />{t("我确认这些凭据只用于访问上述存储桶")}</label>}
            </div>
          </fieldset> : kind === "sftp" ? <>
            <AgentDirectoryInput api={api} agentId={repositoryBrowseAgentId} name="path" label={t("远端绝对路径")} initialValue={String(initial?.path ?? "")} placeholder="/volume1/backups/photos" pathStyle={agentPathStyle(repositoryBrowseAgent)} locale={locale} />
            {selectedRemoteHostId && !repositoryBrowseAgentId && <p className="field-hint warning-text full-field">{t("所选远程主机没有在线且支持目录浏览的已绑定 Agent；仍可手工输入路径。")}</p>}
          </> : browseAgentId && canSelectRepositoryBrowseAgent ? <AgentDirectoryInput api={api} agentId={browseAgentId} name="path" label={t("Agent 本地绝对路径")} initialValue={String(initial?.path ?? "")} placeholder="/srv/archive or D:\\Backup" pathStyle={agentPathStyle(repositoryBrowseAgent)} locale={locale} /> : <label className="full-field">
            {t(kind === "local" ? "本机绝对路径" : "远端绝对路径")}
            <input name="path" defaultValue={String(initial?.path ?? "")} placeholder={kind === "local" ? "/Volumes/Backup/photos" : "/volume1/backups/photos"} required />
          </label>}
		  {engine === "restic" && (initial ? (
            <label className="full-field">
              {t("仓库密码")}
              <input name="password" type="password" value="" disabled />
              <span className="field-hint">{t("密码只能通过仓库操作菜单中的“轮换密码”修改。")}</span>
            </label>
          ) : (
            <fieldset className="full-field">
              <legend>{t("仓库密码")}</legend>
              <label>
                {t("密码来源")}
                <select
                  aria-label={t("密码来源")}
                  value={passwordMode}
                  disabled={existingConnection}
                  onChange={(event) => {
                    setPasswordMode(event.target.value as "generated" | "custom");
                    setPassword("");
                    setPasswordConfirmation("");
                    setPasswordConfirmed(false);
                  }}
                >
                  {!existingConnection && <option value="generated">{t("由应用生成（推荐）")}</option>}
                  <option value="custom">{t(existingConnection ? "输入已有仓库密码" : "自行输入")}</option>
                </select>
              </label>
              <label>
                {t("仓库密码")}
                <input
                  name="password"
                  type={showPassword ? "text" : "password"}
                  minLength={existingConnection ? undefined : 12}
                  value={password}
                  readOnly={passwordMode === "generated"}
                  onChange={(event) => {
                    setPassword(event.target.value);
                    setPasswordConfirmed(false);
                  }}
                  required
                />
              </label>
              {passwordMode === "custom" && (
                <label>
                  {t("再次输入仓库密码")}
                  <input
                    aria-label={t("再次输入仓库密码")}
                    type={showPassword ? "text" : "password"}
                    minLength={existingConnection ? undefined : 12}
                    value={passwordConfirmation}
                    onChange={(event) => {
                      setPasswordConfirmation(event.target.value);
                      setPasswordConfirmed(false);
                    }}
                    required
                  />
                </label>
              )}
              <div className="compact-actions" aria-label={t("仓库密码操作")}>
                {passwordMode === "generated" && <button
                  className="compact-button"
                  type="button"
                  onClick={() => {
                    setPassword(generateRepositoryPassword());
                    setPasswordConfirmed(false);
                  }}
                >
                  {t(password ? "重新生成" : "生成密码")}
                </button>}
                <button className="compact-button" type="button" onClick={() => setShowPassword((value) => !value)}>
                  {t(showPassword ? "隐藏密码" : "显示密码")}
                </button>
                <button
                  className="compact-button"
                  type="button"
                  disabled={!password}
                  onClick={() => {
                    void navigator.clipboard
                      .writeText(password)
                      .then(() => setMessage(t("密码已复制；请保存到密码管理器。")))
                      .catch(() => setMessage(t("无法复制仓库密码，请显示后手动保存")));
                  }}
                >
                  {t("复制仓库密码")}
                </button>
              </div>
              <label>
                <input
                  type="checkbox"
                  checked={passwordConfirmed}
                  disabled={!password || (passwordMode === "custom" && password !== passwordConfirmation)}
                  onChange={(event) => setPasswordConfirmed(event.target.checked)}
                />
                {t("我已将仓库密码安全保存到应用之外")}
              </label>
	              <span className="field-hint">
	                {t(existingConnection ? "请输入这个已有仓库当前使用的密码；密码只会加密保存到秘密库。" : "仓库密码不同于管理员密码和秘密库口令，创建后不会再次显示明文。")}
	              </span>
            </fieldset>
		  ))}
          </div>
        </section>
		{showMaintenance && <section className="editor-section">
          <div className="editor-section-heading">
            <div className="title-with-help"><h2>{t("定时维护")}</h2><button type="button" className="help-tip" aria-label={t("仓库维护影响说明")} title={t("维护会执行 forget、prune 和完整性检查；同一仓库的备份与恢复会等待。")}>?</button></div>
            <p>{t("保留策略属于仓库，并由仓库维护统一执行快照清理、空间回收和完整性检查。")}</p>
          </div>
          <div className="form-grid" key={String(policy.updatedAt ?? initial?.id ?? "new")} onChange={() => { setPreview(null); setMaintenanceDirty(true); }}>
            <label className="full-field checkbox-field">
              <input aria-label={t("启用定时维护")} type="checkbox" checked={maintenanceEnabled} onChange={(event) => setMaintenanceEnabled(event.target.checked)} />
              {t("启用定时维护")}
            </label>
            <label>{t("时区")}<select name="timezone" defaultValue={String(policy.timezone ?? "Asia/Shanghai")} required>{Array.from(new Set([String(policy.timezone ?? "Asia/Shanghai"), "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Tokyo", "Asia/Singapore", "UTC", "Europe/London", "America/New_York", "America/Los_Angeles"])).map((timezone) => <option key={timezone} value={timezone}>{timezone}</option>)}</select></label>
            <label>{t("星期")}<select name="dayOfWeek" defaultValue={String(policy.schedule?.dayOfWeek ?? 0)}>{["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"].map((day, index) => <option value={index} key={day}>{t(day)}</option>)}</select></label>
            <label>{t("执行时间")}<input name="timeOfDay" type="time" defaultValue={String(policy.schedule?.timeOfDay ?? "03:00")} required /></label>
            <label>
              {t("离线补跑宽限（分钟）")}
              <input
                aria-label={t("离线补跑宽限（分钟）")}
                name="catchUpWindowMinutes"
                type="number"
                min="0"
                max="10080"
                defaultValue={Number(policy.catchUpWindowMinutes ?? 60)}
                required
              />
              <span className="field-hint">{t("只补跑宽限期内最近一次；更早发生会记录为错过。")}</span>
            </label>
            <ProtectionPolicyEditor
              retention={maintenanceRetention}
              onRetentionChange={setMaintenanceRetention}
              locale={locale}
            />
            {policy.policyFingerprint && <p className="full-field field-hint">{t("当前策略版本：")}<code>{String(policy.policyFingerprint)}</code></p>}
            {initial && <p className="full-field field-hint">{t("下次执行：")}{policy.enabled && policy.nextRun ? String(policy.nextRun) : t("维护计划已停用")}</p>}
            {readyRepository ? <button className="secondary-button" type="button" disabled={previewing} onClick={(event) => {
              const form = event.currentTarget.form;
              if (!form || !initial?.id) return;
              setPreviewing(true); setMaintenanceDirty(true); setError("");
              void api.action(`/api/repositories/${String(initial.id)}/maintenance`, { retention: maintenanceValues(form).retention, dryRun: true })
                .then((value) => setPreview(value as Record<string, any>)).catch((cause) => setError(cause instanceof Error ? cause.message : "维护预览失败")).finally(() => setPreviewing(false));
            }}>{t(previewing ? "正在预览…" : "生成 dry-run 预览")}</button> : <p className="full-field field-hint">{t("仓库初始化后可生成 dry-run 预览；新建时保存的维护计划会在初始化后生效。")}</p>}
            {preview && <div className="full-field operation-feedback" role="status"><strong>{locale === "en-US" ? `Preview: keep ${String(preview.keepCount ?? 0)} snapshots and remove ${String(preview.removeCount ?? 0)} snapshots` : `预览结果：保留 ${String(preview.keepCount ?? 0)} 个快照，移除 ${String(preview.removeCount ?? 0)} 个快照`}</strong><p>{t("修改维护设置后需要重新预览。")}</p></div>}
          </div>
		</section>}
        <footer className="editor-actions">
          <button className="secondary-button" type="button" onClick={onClose}>{t("取消")}</button>
		  <button className="primary-button" type="submit" disabled={(kind === "sftp" && !hosts.length) || repositoryPasswordInvalid || s3Invalid}>{t(existingConnection ? "验证并连接" : "保存仓库")}</button>
        </footer>
      </form>
      <Toast message={message} locale={locale} onClose={() => setMessage("")} />
    </section>
  );
}

export function generateRepositoryPassword() {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
  const bytes = new Uint8Array(32);
  globalThis.crypto.getRandomValues(bytes);
  return Array.from(bytes, (value) => alphabet[value & 63]).join("");
}


function formatTaskPreviewBytes(bytes: number, locale: Locale): string {
  if (bytes < 1024) return `${new Intl.NumberFormat(locale).format(bytes)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: value < 10 ? 1 : 0 }).format(value)} ${units[unit]}`;
}

function taskPreviewImpact(impact: TaskScopeImpact, locale: Locale): string {
  const files = new Intl.NumberFormat(locale).format(Number(impact.matchedFiles ?? 0));
  const bytes = formatTaskPreviewBytes(Number(impact.estimatedBytes ?? 0), locale);
  return locale === "en-US" ? `${files} files, ${bytes}` : `${files} 个文件，${bytes}`;
}

function confirmedTaskPreview(initial: Record<string, unknown> | null): TaskScopePreview | null {
  const confirmation = initial?.scopeConfirmation as Record<string, unknown> | undefined;
  if (!confirmation?.previewId || !confirmation?.fingerprint) return null;
  const rsync = initial?.rsync as Record<string, unknown> | undefined;
  return {
    previewId: String(confirmation.previewId),
    fingerprint: String(confirmation.fingerprint),
    requiresDeleteConfirmation: Boolean(rsync?.delete),
    summary: (confirmation.summary as TaskScopePreview["summary"] | undefined) ?? {},
  };
}

function TaskScopePreviewPanel({ preview, valid, exclusions, deleteConfirmed, locale, onExclusionsChange, onDeleteConfirmed }: {
  preview: TaskScopePreview;
  valid: boolean;
  exclusions: string[];
  deleteConfirmed: boolean;
  locale: Locale;
  onExclusionsChange(rules: string[]): void;
  onDeleteConfirmed(confirmed: boolean): void;
}) {
  const t = (source: string) => translate(locale, source);
  const summary = preview.summary;
  const number = (value: number | undefined) => new Intl.NumberFormat(locale).format(Number(value ?? 0));
  return <section className={`task-scope-preview full-field${valid ? "" : " stale"}`} aria-live="polite">
    <div className="task-scope-preview-heading">
      <div>
        <strong>{t("范围预览已生成")}</strong>
        <span>{locale === "en-US" ? `Scanned ${number(summary.scannedItems)} items` : `已扫描 ${number(summary.scannedItems)} 项`}</span>
      </div>
      <StatusIndicator value={valid ? "success" : "warning"} locale={locale} label={t(valid ? "与当前配置一致" : "已失效")} variant="pill" />
    </div>
    {!valid && <p className="task-scope-stale warning-text">{t("任务范围已改变，需要重新生成预览。")}</p>}
    <p>{locale === "en-US"
      ? `${number(summary.includedFiles)} files will be protected, ${number(summary.excludedFiles)} files are excluded, and ${number(summary.unreadableItems)} items could not be read.`
      : `${number(summary.includedFiles)} 个文件将纳入保护，${number(summary.excludedFiles)} 个文件被排除，${number(summary.unreadableItems)} 项无法读取。`}</p>
    <div className="task-scope-totals">
      <span><small>{t("纳入大小")}</small><strong>{formatTaskPreviewBytes(Number(summary.includedBytes ?? 0), locale)}</strong></span>
      <span><small>{t("排除大小")}</small><strong>{formatTaskPreviewBytes(Number(summary.excludedBytes ?? 0), locale)}</strong></span>
    </div>
    {summary.truncated && <p className="task-scope-truncated" role="alert">{t("预览达到扫描上限，结果已截断；不能把这些数量视为完整范围。")}</p>}
    {!!summary.activeRules?.length && <div className="task-scope-rules">
      <strong>{t("当前生效规则及影响")}</strong>
      <ul>{summary.activeRules.map((impact) => <li key={impact.rule}><code>{impact.rule}</code><span>{taskPreviewImpact(impact, locale)}</span></li>)}</ul>
    </div>}
    {!!summary.suggestions?.length && <div className="task-scope-suggestions">
      <div><strong>{t("可选排除建议")}</strong><p>{t("建议不会自动应用。勾选后会修改规则，并要求重新预览。")}</p></div>
      {summary.suggestions.map((suggestion) => {
        const selected = exclusions.includes(suggestion.rule);
        return <label key={suggestion.rule}>
          <input type="checkbox" aria-label={`${t("采用建议")} ${suggestion.rule}`} checked={selected} onChange={(event) => {
            onExclusionsChange(event.target.checked
              ? [...exclusions, suggestion.rule]
              : exclusions.filter((rule) => rule !== suggestion.rule));
          }} />
          <span><code>{suggestion.rule}</code><small>{t(String(suggestion.reason ?? ""))}</small></span>
          <em>{taskPreviewImpact(suggestion, locale)}</em>
        </label>;
      })}
    </div>}
    {preview.requiresDeleteConfirmation && <div className="task-scope-delete">
      <strong>{locale === "en-US"
        ? `Target ${String(summary.targetIdentity ?? "—")} will delete ${number(summary.deleteFiles)} files and ${number(summary.deleteDirectories)} directories.`
        : `目标 ${String(summary.targetIdentity ?? "—")} 将删除 ${number(summary.deleteFiles)} 个文件和 ${number(summary.deleteDirectories)} 个目录。`}</strong>
      <label><input type="checkbox" checked={deleteConfirmed} disabled={!valid} onChange={(event) => onDeleteConfirmed(event.target.checked)} />{t("我确认按此预览删除目标中的额外内容")}</label>
    </div>}
  </section>;
}

export function TaskEditor({ api, initial, onClose, onDraftSaved, onSaved, locale = "zh-CN" }: TaskEditorProps) {
  const t = (source: string) => translate(locale, source);
  const formRef = useRef<HTMLFormElement>(null);
  const basicSectionRef = useRef<HTMLElement>(null);
  const scheduleSectionRef = useRef<HTMLElement>(null);
  const activationReviewDialogRef = useRef<HTMLDivElement>(null);
  const initialTarget = initial?.executionTarget as Record<string, unknown> | undefined;
  const initialRsync = initial?.rsync as Record<string, unknown> | undefined;
  const initialDirectory = initial?.directory as Record<string, unknown> | undefined;
  const initialDatabase = initial?.database as Record<string, unknown> | undefined;
  const initialConfirmation = initial?.scopeConfirmation as Record<string, unknown> | undefined;
  const initialHealth = initial?.health as Record<string, unknown> | undefined;
  const initialPreview = confirmedTaskPreview(initial);
  const initialSource = initial?.engine === "rsync" ? initialRsync : initialDirectory;
  const [taskID, setTaskID] = useState(String(initial?.id ?? ""));
  const [activeSection, setActiveSection] = useState<"basic" | "schedule">("basic");
  const [engine, setEngine] = useState<"restic" | "rsync">(initial?.engine === "rsync" ? "rsync" : "restic");
  const [kind, setKind] = useState<"directory" | "database">(initial?.kind === "database" ? "database" : "directory");
  const [targetKind, setTargetKind] = useState<"local" | "agent">(initialTarget?.kind === "agent" ? "agent" : "local");
  const [rsyncDestinationKind, setRsyncDestinationKind] = useState<"ssh" | "local">(initialRsync?.destinationKind === "local" ? "local" : "ssh");
  const [repositories, setRepositories] = useState<RepositoryOption[]>([]);
  const [selectedRepositoryID, setSelectedRepositoryID] = useState(String(initial?.repositoryId ?? ""));
  const [connections, setConnections] = useState<ConnectionOption[]>([]);
  const [hosts, setHosts] = useState<Array<Record<string, unknown>>>([]);
  const [agents, setAgents] = useState<Array<Record<string, unknown>>>([]);
  const [selectedAgentID, setSelectedAgentID] = useState(String(initialTarget?.agentId ?? ""));
  const [exclusions, setExclusions] = useState(Array.isArray(initialSource?.exclusions) ? (initialSource.exclusions as unknown[]).join("\n") : "");
  const [deleteMode, setDeleteMode] = useState(Boolean(initialRsync?.delete));
  const [enabled, setEnabled] = useState(Boolean(initial?.id ? initial.enabled : false));
  const [taskPlan, setTaskPlan] = useState<TaskPlanRecord | null>(null);
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [scheduleKind, setScheduleKind] = useState<"daily" | "weekly" | "interval">("daily");
  const [scheduleTime, setScheduleTime] = useState("02:30");
  const [scheduleDay, setScheduleDay] = useState(1);
  const [scheduleInterval, setScheduleInterval] = useState(24);
  const [scheduleTimezone, setScheduleTimezone] = useState("Asia/Shanghai");
  const [scheduleCatchUp, setScheduleCatchUp] = useState(60);
  const [resources, setResources] = useState<ResourcePolicy>(() => {
    const policy = normalizeResourcePolicy(
      initial?.resources,
      initial?.id ? emptyResourcePolicy : { ...emptyResourcePolicy, compression: "auto" },
    );
    return { ...policy, uploadKiBPerSecond: 0, downloadKiBPerSecond: 0 };
  });
  const [preview, setPreview] = useState<TaskScopePreview | null>(initialPreview);
  const [previewValid, setPreviewValid] = useState(Boolean(initialPreview));
  const [previewPending, setPreviewPending] = useState(false);
  const [scopeDirty, setScopeDirty] = useState(!initial?.id);
  const [deleteConfirmed, setDeleteConfirmed] = useState(Boolean(initialConfirmation?.deleteConfirmed));
  const [activationReviewOpen, setActivationReviewOpen] = useState(false);
  const [operation, setOperation] = useState<"idle" | "saving" | "previewing" | "preflighting">("idle");
  const databasePreflight = useOperation(api);
  const [pendingDatabaseActivation, setPendingDatabaseActivation] = useState<{
    taskID: string;
    operationID: string;
    payload: Record<string, unknown>;
    taskName: string;
  } | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const busy = operation !== "idle";
  useModalFocus(activationReviewDialogRef, () => { if (!busy) setActivationReviewOpen(false); }, activationReviewOpen);

  const invalidateScope = () => {
    setScopeDirty(true);
    setPreviewValid(false);
    setPreviewPending(false);
    setDeleteConfirmed(false);
  };

  useEffect(() => {
    let active = true;
    void Promise.all([
      api.listResource("repositories"),
      api.listResource("tasks"),
      api.listResource("database-connections"),
      api.listResource("remote-hosts"),
      api.listResource("agents"),
      api.listResource("plans"),
    ])
      .then(([repositoryItems, taskItems, connectionItems, hostItems, agentItems, planItems]) => {
        if (!active) return;
        const used = new Set(taskItems
          .filter((task) => String(task.id) !== String(initial?.id ?? ""))
          .map((task) => String(task.repositoryId ?? "")));
        setRepositories(repositoryItems
          .map((item): RepositoryOption => ({
            id: String(item.id),
            name: String(item.name),
            engine: item.engine === "rsync" ? "rsync" : "restic",
            kind: item.kind === "local" ? "local" : "sftp",
            path: String(item.path),
            status: String(item.status),
          }))
          .filter((item) => item.status === "ready" && !used.has(item.id)));
        setConnections(connectionItems
          .map((item) => ({
            id: String(item.id),
            name: String(item.name),
            engine: String(item.engine),
            purpose: String(item.purpose),
            host: String(item.host ?? item.socketPath ?? ""),
          }))
          .filter((item) => item.purpose === "backup"));
        setHosts(hostItems);
        setAgents(agentItems.filter((item) => item.status !== "revoked"));
        const currentPlan = planItems.find((item) => Array.isArray(item.taskIds) && item.taskIds.includes(String(initial?.id ?? ""))) as TaskPlanRecord | undefined;
        if (currentPlan) {
          setTaskPlan(currentPlan);
          setScheduleEnabled(currentPlan.enabled === true);
          const schedule = currentPlan.schedule ?? {};
          const currentKind = schedule.kind === "weekly" || schedule.kind === "interval" ? schedule.kind : "daily";
          setScheduleKind(currentKind);
          setScheduleTime(String(schedule.timeOfDay ?? "02:30"));
          setScheduleDay(Number(schedule.dayOfWeek ?? 1));
          setScheduleInterval(Number(schedule.intervalHours ?? 24));
          setScheduleTimezone(String(currentPlan.timezone ?? "Asia/Shanghai"));
          setScheduleCatchUp(Number(currentPlan.catchUpWindowMinutes ?? 60));
        }
        setLoading(false);
      })
      .catch(() => {
        if (active) {
          setError(t("无法读取任务依赖资源"));
          setLoading(false);
        }
      });
    return () => { active = false; };
  }, [api, initial?.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const legacyRsync = Boolean(initial?.id && initial?.engine === "rsync" && !initial?.repositoryId);
  const resticRepositories = repositories.filter((repository) => repository.engine === "restic");
  const rsyncRepositories = repositories.filter((repository) => repository.engine === "rsync" && (targetKind === "agent" || repository.kind === "sftp"));
  const selectedAgent = agents.find((agent) => String(agent.id ?? "") === selectedAgentID);
  const selectedAgentEligible = !selectedAgent || agentEligibleForEngine(selectedAgent, engine);
  const scopeTask = engine === "rsync" || (engine === "restic" && kind === "directory");
  const dependencyBlocked = loading
    || (engine === "restic" && !resticRepositories.length)
    || (engine === "rsync" && !legacyRsync && !rsyncRepositories.length)
    || (engine === "rsync" && legacyRsync && rsyncDestinationKind === "ssh" && !hosts.length)
    || (targetKind === "agent" && !agents.length)
    || (engine === "restic" && kind === "database" && !connections.length);
  const exclusionRules = exclusions.split("\n").map((item) => item.trim()).filter(Boolean);

  const buildPayload = (formElement: HTMLFormElement, forceEnabled?: boolean): Record<string, unknown> => {
    const form = new FormData(formElement);
    const value = (key: string) => String(form.get(key) ?? "");
    const common = {
      name: value("name"),
      engine,
      executionTarget: targetKind === "agent" ? { kind: "agent", agentId: value("agentId") } : { kind: "local" },
      health: { maxSuccessAgeHours: Number(value("maxSuccessAgeHours")) },
      enabled: forceEnabled ?? enabled,
    };
    if (engine === "rsync") {
      return {
        ...common,
        kind: "rsync",
        repositoryId: legacyRsync ? "" : value("repositoryId"),
        rsync: {
          path: value("path"),
          ...(legacyRsync ? {
            destinationKind: rsyncDestinationKind,
            destinationHostId: rsyncDestinationKind === "ssh" ? value("destinationHostId") : "",
            destinationPath: value("destinationPath"),
          } : {}),
          exclusions: exclusionRules,
          delete: deleteMode,
        },
        retention: {},
        resources: {},
      };
    }
    return {
      ...common,
      kind,
      repositoryId: value("repositoryId"),
      ...(kind === "directory"
        ? { directory: { path: value("path"), exclusions: exclusionRules, skipIfUnchanged: true } }
        : { database: { connectionId: value("connectionId"), database: value("database") } }),
      retention: {},
      resources,
    };
  };

  const persist = async (payload: Record<string, unknown>, currentID: string): Promise<string> => {
    if (currentID) {
      await api.updateResource("tasks", currentID, payload);
      return currentID;
    }
    const created = await api.createResource("tasks", payload) as Record<string, unknown> | undefined;
    const createdID = String(created?.id ?? "");
    if (createdID) setTaskID(createdID);
    return createdID;
  };

  const saveTaskSchedule = async (currentID: string, taskName: string) => {
    if (!currentID || (!taskPlan && !scheduleEnabled)) return;
    const schedule = scheduleKind === "weekly"
      ? { kind: scheduleKind, dayOfWeek: scheduleDay, timeOfDay: scheduleTime }
      : scheduleKind === "interval"
        ? { kind: scheduleKind, intervalHours: scheduleInterval }
        : { kind: scheduleKind, timeOfDay: scheduleTime };
    const existingTaskIDs = Array.isArray(taskPlan?.taskIds) ? taskPlan.taskIds : [];
    const sharedPlan = existingTaskIDs.length > 1;
    const payload = {
      name: sharedPlan ? `${taskName} · ${t("定时执行")}` : taskPlan?.name || `${taskName} · ${t("定时执行")}`,
      schedule,
      timezone: scheduleTimezone,
      maxParallel: 1,
      catchUpWindowMinutes: scheduleCatchUp,
      taskIds: [currentID],
      enabled: scheduleEnabled,
    };
    if (taskPlan?.id) {
      if (sharedPlan) {
        await api.updateResource("plans", taskPlan.id, {
          name: taskPlan.name ?? t("定时执行"),
          schedule: taskPlan.schedule ?? schedule,
          timezone: taskPlan.timezone ?? scheduleTimezone,
          maxParallel: Number(taskPlan.maxParallel ?? 1),
          catchUpWindowMinutes: Number(taskPlan.catchUpWindowMinutes ?? 60),
          taskIds: existingTaskIDs.filter((id) => id !== currentID),
          enabled: taskPlan.enabled === true,
        });
        const created = await api.createResource("plans", payload) as TaskPlanRecord | undefined;
        setTaskPlan({ ...payload, id: created?.id } as TaskPlanRecord);
      } else {
        await api.updateResource("plans", taskPlan.id, payload);
      }
    } else {
      const created = await api.createResource("plans", payload) as TaskPlanRecord | undefined;
      setTaskPlan({ ...payload, id: created?.id } as TaskPlanRecord);
    }
  };

  useEffect(() => {
    const pending = pendingDatabaseActivation;
    const record = databasePreflight.operation;
    if (!pending || !record || record.id !== pending.operationID || !["success", "failed", "cancelled", "cleanup_required"].includes(record.status)) return;
    if (record.status !== "success") {
      setPendingDatabaseActivation(null);
      setOperation("idle");
      setMessage(record.errorSummary || t(record.status === "cancelled" ? "操作已取消" : "数据库备份预检失败"));
      return;
    }
    setOperation("saving");
    void (async () => {
      try {
        await api.updateResource("tasks", pending.taskID, {
          ...pending.payload,
          enabled: true,
          databaseBackupPreflightOperationId: pending.operationID,
        });
        await saveTaskSchedule(pending.taskID, pending.taskName);
        setPendingDatabaseActivation(null);
        setActivationReviewOpen(false);
        await onSaved();
      } catch (cause) {
        setPendingDatabaseActivation(null);
        setOperation("idle");
        setMessage(cause instanceof Error ? cause.message : t("保存失败"));
      }
    })();
    // The callback intentionally runs once for the terminal operation. The task
    // payload is frozen before the real backup test starts.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [databasePreflight.operation, pendingDatabaseActivation]);

  const previewScope = async (formElement: HTMLFormElement, reviewBeforeEnable = false) => {
    if (!formElement.reportValidity() || busy || !scopeTask) return;
    setOperation("previewing");
    setError("");
    try {
      let currentID = taskID;
      if (!currentID || scopeDirty) {
        currentID = await persist(buildPayload(formElement, false), currentID);
        if (!currentID) throw new Error(t("任务已保存，但响应中缺少任务 ID"));
        setScopeDirty(false);
        await onDraftSaved();
      }
      const result = await api.action(`/api/tasks/${encodeURIComponent(currentID)}/preview`, {}) as TaskScopePreview;
      setPreview(result);
      setPreviewValid(true);
      setPreviewPending(true);
      setDeleteConfirmed(false);
      if (reviewBeforeEnable) setActivationReviewOpen(true);
    } catch (cause) {
      setPreviewValid(false);
      setPreviewPending(false);
      setError(cause instanceof Error ? cause.message : t("生成任务范围预览失败"));
    } finally {
      setOperation("idle");
    }
  };

  const submit = async (formElement: HTMLFormElement) => {
    if (busy) return;
    if (engine === "restic" && kind === "database" && !connections.length) {
      setError(t("请先创建用途为“备份”的数据库连接"));
      return;
    }
    if (scopeTask && enabled && !previewValid) {
      await previewScope(formElement, true);
      return;
    }
    if (scopeTask && enabled && preview?.requiresDeleteConfirmation && !deleteConfirmed) {
      setActivationReviewOpen(true);
      return;
    }
    setOperation("saving");
    setError("");
    setMessage("");
    try {
      const payload = buildPayload(formElement);
      const canConsumePreview = previewPending && previewValid && preview && (!preview.requiresDeleteConfirmation || deleteConfirmed);
      if (canConsumePreview) {
        payload.previewId = preview.previewId;
        if (preview.requiresDeleteConfirmation) payload.rsyncDeleteConfirmed = true;
      }
	  if (engine === "restic" && kind === "database" && enabled) {
		const taskName = String(new FormData(formElement).get("name") ?? "");
		const draftPayload = buildPayload(formElement, false);
		const currentID = await persist(draftPayload, taskID);
		if (!currentID) throw new Error(t("任务已保存，但响应中缺少任务 ID"));
		setTaskID(currentID);
		await onDraftSaved();
		setOperation("preflighting");
		const accepted = await databasePreflight.start(`/api/tasks/${encodeURIComponent(currentID)}/database-backup-preflight`, {});
		if (!accepted) throw new Error(t("无法启动数据库备份预检"));
		setPendingDatabaseActivation({ taskID: currentID, operationID: accepted.operationId, payload, taskName });
		return;
	  }
      const currentID = await persist(payload, taskID);
      await saveTaskSchedule(currentID, String(new FormData(formElement).get("name") ?? ""));
      setActivationReviewOpen(false);
      await onSaved();
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : t("保存失败"));
      setOperation("idle");
    }
  };

  const previewButtonLabel = preview && scopeDirty
    ? t("保存草稿并重新预览")
    : preview
      ? t("重新生成范围预览")
      : taskID && !scopeDirty
        ? t("生成范围预览")
        : t("保存草稿并预览范围");

  const scrollToSection = (section: "basic" | "schedule") => {
    setActiveSection(section);
    const target = section === "basic" ? basicSectionRef.current : scheduleSectionRef.current;
    target?.scrollIntoView?.({ behavior: "smooth", block: "start" });
  };

  return <section className="resource-editor-page task-editor-page" aria-label={t(initial?.id ? "编辑备份任务" : "新建备份任务")}>
    <header className="editor-page-header">
      <div>
        <button className="text-button back-button" type="button" disabled={busy} onClick={onClose}>← {t("返回备份任务")}</button>
        <h1>{t(initial?.id ? "编辑备份任务" : "新建备份任务")}</h1>
        <p>{t("先以停用草稿确认实际保护范围，再决定是否启用任务。")}</p>
      </div>
    </header>
    {error && <p className="form-error form-banner" role="alert">{error}</p>}
    <nav className="editor-subtabs" aria-label={t("任务配置") }>
      {([
        ["basic", "基本任务"],
        ["schedule", "定时执行"],
      ] as const).map(([value, label]) => <button
        key={value}
        type="button"
        aria-current={activeSection === value ? "location" : undefined}
        className={activeSection === value ? "selected" : ""}
        onClick={() => scrollToSection(value)}
      >{t(label)}</button>)}
    </nav>
    <form
      ref={formRef}
      className="resource-editor-form task-editor-form"
      onSubmit={(event) => {
        event.preventDefault();
        void submit(event.currentTarget);
      }}
    >
      <section ref={basicSectionRef} className="editor-section task-editor-section" aria-labelledby="task-editor-basic-title">
        <div className="editor-section-heading"><h2 id="task-editor-basic-title">{t("基本任务")}</h2></div>
        <div className="form-grid">
        <label>{t("任务名称")}<input name="name" defaultValue={String(initial?.name ?? "")} required /></label>
        <label>{t("执行引擎")}<select value={engine} onChange={(event) => {
          const nextEngine = event.target.value as "restic" | "rsync";
          setEngine(nextEngine);
          setSelectedRepositoryID("");
          if (targetKind === "agent" && selectedAgent && !agentEligibleForEngine(selectedAgent, nextEngine)) setEnabled(false);
          setKind("directory");
          invalidateScope();
        }}>
          <option value="restic">{t("Restic 备份")}</option>
          <option value="rsync">{t("rsync 增量同步")}</option>
        </select></label>
        <label>{t("执行位置")}<select value={targetKind} onChange={(event) => {
          const next = event.target.value as "local" | "agent";
          setTargetKind(next);
          if (next === "local") setRsyncDestinationKind("ssh");
          invalidateScope();
        }}>
          <option value="local">{t("Service 本机")}</option>
          <option value="agent">{t("远程 Agent")}</option>
        </select></label>
        {targetKind === "agent" && <label className="full-field">{t("源端 Agent")}
          <select name="agentId" value={selectedAgentID} onChange={(event) => {
            setSelectedAgentID(event.target.value);
            const selected = agents.find((agent) => String(agent.id) === event.target.value);
            if (!agentEligibleForEngine(selected, engine)) setEnabled(false);
            invalidateScope();
          }} required>
            <option value="" disabled>{t("请选择已注册 Agent")}</option>
            {agents.map((agent) => <option key={String(agent.id)} value={String(agent.id)}>{String(agent.id)} · {agentTaskEligibility(agent, locale, engine)}</option>)}
          </select>
          {!agents.length && <span className="field-hint warning-text">{t("请先注册 Agent")}</span>}
          {selectedAgent && !selectedAgentEligible && <span className="field-hint warning-text">{t("该 Agent 当前不能执行任务；仍可绑定并保存为停用草稿，恢复就绪后再启用。")}</span>}
        </label>}
        {engine === "restic" && <>
          <label>{t("任务类型")}<select value={kind} onChange={(event) => {
            setKind(event.target.value as "directory" | "database");
            invalidateScope();
          }}>
            <option value="directory">{t("目录备份")}</option>
            {targetKind === "local" && <option value="database">{t("数据库备份")}</option>}
          </select></label>
          <label className="full-field">{t("备份仓库")}
            <select name="repositoryId" value={selectedRepositoryID} disabled={loading} onChange={(event) => { setSelectedRepositoryID(event.target.value); invalidateScope(); }} required>
              <option value="" disabled>{t(loading ? "正在读取仓库…" : resticRepositories.length ? "请选择已初始化仓库" : "无可选仓库")}</option>
              {resticRepositories.map((repository) => <option key={repository.id} value={repository.id}>{repository.name} · {t(repository.kind === "local" ? "本地" : "远程")} · {repository.path}</option>)}
            </select>
            {!loading && !resticRepositories.length && <span className="field-hint warning-text">{t("无可选仓库")}</span>}
          </label>
        </>}
        {engine === "rsync" && !legacyRsync && <label className="full-field">{t("同步仓库")}
          <select name="repositoryId" value={selectedRepositoryID} onChange={(event) => { setSelectedRepositoryID(event.target.value); invalidateScope(); }} required>
            <option value="" disabled>{t(rsyncRepositories.length ? "请选择 rsync 同步仓库" : "无可选仓库")}</option>
            {rsyncRepositories.map((repository) => <option key={repository.id} value={repository.id}>{repository.name} · {t(repository.kind === "local" ? "Agent 本地" : "SSH 远程")} · {repository.path}</option>)}
          </select>
          {!rsyncRepositories.length && <span className="field-hint warning-text">{t("无可选仓库")}</span>}
        </label>}
        {scopeTask ? <>
          {targetKind === "agent"
            ? <AgentDirectoryInput key={`agent-source-${engine}`} api={api} agentId={selectedAgentID} name="path" label={t("源目录绝对路径")} initialValue={String(initialSource?.path ?? "")} placeholder="/srv/data or C:\\Data" pathStyle={agentPathStyle(selectedAgent)} locale={locale} onPathChange={invalidateScope} />
            : <label className="full-field">{t("源目录绝对路径")}<input key={`local-source-${engine}`} name="path" defaultValue={String(initialSource?.path ?? "")} onChange={invalidateScope} placeholder="/srv/data" required /></label>}
          {engine === "rsync" && legacyRsync && <>
            <label>{t("同步目标类型")}<select value={rsyncDestinationKind} onChange={(event) => {
              setRsyncDestinationKind(event.target.value as "ssh" | "local");
              invalidateScope();
            }}>
              <option value="ssh">{t("SSH 远程目录")}</option>
              {targetKind === "agent" && <option value="local">{t("Agent 本地目录")}</option>}
            </select></label>
            {rsyncDestinationKind === "ssh" && <label className="full-field">{t("目标远程主机")}
              <select name="destinationHostId" defaultValue={String(initialRsync?.destinationHostId ?? "")} onChange={invalidateScope} required>
                <option value="" disabled>{t("请选择 SSH 目标主机")}</option>
                {hosts.map((host) => <option key={String(host.id)} value={String(host.id)}>{String(host.name)} · {String(host.host)}</option>)}
              </select>
            </label>}
            <label className="full-field">{t("目标绝对路径")}<input name="destinationPath" defaultValue={String(initialRsync?.destinationPath ?? "")} onChange={invalidateScope} placeholder="/srv/archive" required /></label>
            {rsyncDestinationKind === "local" && <p className="field-hint full-field">{t("源目录和目标目录都位于所选 Agent，且不能相同或互相嵌套。")}</p>}
          </>}
          <label className="full-field">{t("排除规则（每行一条）")}
            <textarea name="exclusions" value={exclusions} onChange={(event) => {
              setExclusions(event.target.value);
              invalidateScope();
            }} placeholder={t("默认不排除任何内容；每条规则都需要通过预览确认影响。")} />
          </label>
          {engine === "rsync" && <label>{t("目标清理策略")}<select name="delete" value={String(deleteMode)} onChange={(event) => {
            setDeleteMode(event.target.value === "true");
            invalidateScope();
          }}>
            <option value="false">{t("保留目标额外文件")}</option>
            <option value="true">{t("删除目标额外文件")}</option>
          </select></label>}
        </> : <>
          <label className="full-field">{t("数据库连接")}
            <select name="connectionId" defaultValue={String(initialDatabase?.connectionId ?? "")} required>
              <option value="" disabled>{t("请选择备份用途的数据库连接")}</option>
              {connections.map((connection) => <option key={connection.id} value={connection.id}>{connection.name} · {connection.engine} · {connection.host || t("本机 Socket")}</option>)}
            </select>
            {!connections.length && !loading && <span className="field-hint warning-text">{t("请先创建用途为“备份”的数据库连接")}</span>}
          </label>
          <label className="full-field">{t("逻辑数据库名")}<input name="database" defaultValue={String(initialDatabase?.database ?? "")} placeholder={t("例如 gitea")} required /></label>
          <p className="field-hint full-field">{t("启用数据库任务前会自动执行一次轻量备份预检，不会创建数据库备份快照；预检失败时任务保持停用草稿。")}</p>
        </>}
        {engine === "restic" && <>
          <ProtectionPolicyEditor
            retention={defaultRetentionPolicy}
            onRetentionChange={() => undefined}
            resources={resources}
            onResourcesChange={setResources}
            showRetention={false}
            locale={locale}
          />
        </>}
        <label>{t("任务状态")}
          <select name="enabled" aria-label={t("任务状态")} value={String(enabled)} onChange={(event) => setEnabled(event.target.value === "true")}>
            <option value="true" disabled={targetKind === "agent" && !selectedAgentEligible}>{t("启用")}</option>
            <option value="false">{t("停用")}</option>
          </select>
          {scopeTask && <span className="field-hint">{t("启用只接受当前配置对应的有效预览；范围变化会自动撤销确认。")}</span>}
          {scopeTask && enabled && !previewValid && <span className="field-hint warning-text" role="status">{t("保存任务会先生成范围预览，确认后才启用任务。")}</span>}
        </label>
        <label>{t("最长无完整成功（小时）")}
          <input aria-label={t("最长无完整成功（小时）")} name="maxSuccessAgeHours" type="number" min="0" max="8760" defaultValue={Number(initialHealth?.maxSuccessAgeHours ?? (initial?.id ? 0 : 48))} required />
          <span className="field-hint">{t("超过此时长仍没有完整成功会产生严重告警；填 0 表示不设置此期望。")}</span>
        </label>
        {preview && <TaskScopePreviewPanel
          preview={preview}
          valid={previewValid}
          exclusions={exclusionRules}
          deleteConfirmed={deleteConfirmed}
          locale={locale}
          onExclusionsChange={(rules) => {
            setExclusions(rules.join("\n"));
            invalidateScope();
          }}
          onDeleteConfirmed={setDeleteConfirmed}
        />}
        </div>
      </section>
      <section ref={scheduleSectionRef} className="editor-section task-editor-section" aria-labelledby="task-editor-schedule-title">
        <div className="editor-section-heading"><h2 id="task-editor-schedule-title">{t("定时执行")}</h2></div>
        <div className="form-grid">
        <label className="full-field checkbox-field"><input type="checkbox" checked={scheduleEnabled} onChange={(event) => setScheduleEnabled(event.target.checked)} />{t("启用定时执行")}</label>
        <p className="full-field field-hint">{t("新任务默认不启用定时执行；未启用时仍可在任务列表中立即运行。")}</p>
        <label>{t("执行频率")}<select value={scheduleKind} disabled={!scheduleEnabled} onChange={(event) => setScheduleKind(event.target.value as "daily" | "weekly" | "interval")}>
          <option value="daily">{t("每日")}</option><option value="weekly">{t("每周")}</option><option value="interval">{t("固定间隔")}</option>
        </select></label>
        {(scheduleKind === "daily" || scheduleKind === "weekly") && <label>{t("执行时间")}<input type="time" value={scheduleTime} disabled={!scheduleEnabled} onChange={(event) => setScheduleTime(event.target.value)} /></label>}
        {scheduleKind === "weekly" && <label>{t("星期")}<select value={scheduleDay} disabled={!scheduleEnabled} onChange={(event) => setScheduleDay(Number(event.target.value))}>{["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"].map((day, index) => <option key={day} value={index}>{t(day)}</option>)}</select></label>}
        {scheduleKind === "interval" && <label>{t("间隔小时数")}<input type="number" min="1" max="8760" value={scheduleInterval} disabled={!scheduleEnabled} onChange={(event) => setScheduleInterval(Number(event.target.value))} /></label>}
        <label>{t("时区")}<select value={scheduleTimezone} disabled={!scheduleEnabled} onChange={(event) => setScheduleTimezone(event.target.value)}>{Array.from(new Set([scheduleTimezone, "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Tokyo", "Asia/Singapore", "UTC", "Europe/London", "America/New_York", "America/Los_Angeles"])).map((timezone) => <option key={timezone} value={timezone}>{timezone}</option>)}</select></label>
        <label>{t("离线补跑宽限（分钟）")}<input type="number" min="0" max="10080" value={scheduleCatchUp} disabled={!scheduleEnabled} onChange={(event) => setScheduleCatchUp(Number(event.target.value))} /></label>
        </div>
      </section>
      <footer className="editor-actions task-editor-actions">
        <button className="secondary-button" type="button" disabled={busy} onClick={onClose}>{t("取消")}</button>
        {scopeTask && <button className="secondary-button" type="button" disabled={dependencyBlocked || busy} onClick={(event) => {
          const form = event.currentTarget.form;
          if (form) void previewScope(form);
        }}>{operation === "previewing" ? t("正在生成范围预览…") : previewButtonLabel}</button>}
        <button className="primary-button" type="submit" disabled={dependencyBlocked || busy}>{t(operation === "saving" ? "正在保存…" : operation === "preflighting" ? "正在执行数据库备份预检…" : engine === "restic" && kind === "database" && enabled ? "预检并启用" : "保存任务")}</button>
      </footer>
    </form>
    {databasePreflight.operation && <OperationFeedback operation={databasePreflight} locale={locale} persistTerminal compact />}
    {activationReviewOpen && preview && <ModalPortal>
      <div ref={activationReviewDialogRef} className="dialog task-scope-review-dialog" role="dialog" aria-modal="true" aria-labelledby="task-scope-review-title">
        <header><div><h2 id="task-scope-review-title">{t("确认任务范围")}</h2><p>{t("范围预览不会启用任务；确认后才会启用。")}</p></div></header>
        <div className="dialog-body">
          <TaskScopePreviewPanel
            preview={preview}
            valid={previewValid}
            exclusions={exclusionRules}
            deleteConfirmed={deleteConfirmed}
            locale={locale}
            onExclusionsChange={(rules) => {
              setExclusions(rules.join("\n"));
              invalidateScope();
            }}
            onDeleteConfirmed={setDeleteConfirmed}
          />
          {error && <p className="form-error" role="alert">{error}</p>}
        </div>
        <footer>
          <button className="secondary-button" type="button" disabled={busy} onClick={() => setActivationReviewOpen(false)}>{t("取消")}</button>
          <button className="primary-button" type="button" disabled={busy || !previewValid || (preview.requiresDeleteConfirmation && !deleteConfirmed)} onClick={() => {
            const form = formRef.current;
            if (form) void submit(form);
          }}>{t("确认范围并启用任务")}</button>
        </footer>
      </div>
    </ModalPortal>}
    <Toast message={message} locale={locale} onClose={() => setMessage("")} />
  </section>;
}
