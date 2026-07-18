import { useCallback, useEffect, useId, useRef, useState, type ChangeEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { generateRepositoryPassword, RepositoryEditor, TaskEditor } from "./ResourceEditors";
import { OperationFeedback, useOperation, type AcceptedOperation } from "./OperationFeedback";
import { AgentServiceSettings, type AgentServiceStatus } from "./AgentServiceSettings";
import { useModalFocus } from "./useModalFocus";
import { ModalPortal } from "./ModalPortal";
import { Toast } from "./Toast";
import { HealthEvents, type AlertState } from "./HealthEvents";
import { ControlPlaneRecovery } from "./ControlPlaneRecovery";
import { SnapshotBrowser, SnapshotDiffPanel, type SnapshotContentsPage } from "./SnapshotBrowser";
import { TaskHealthTrends } from "./TaskHealthTrends";
import { TaskHealthDetailPage } from "./TaskHealthDetailPage";
import { RunHistoryPage } from "./RunHistoryPage";
import { AgentFleet } from "./AgentFleet";
import { NotificationChannels } from "./NotificationChannels";
import { ApplicationVersionStatus, type ApplicationReleaseState } from "./ApplicationVersionStatus";
import { StatusIndicator, statusLabel } from "./StatusIndicator";
import { isRFC3339Timestamp, timestampAtSecond } from "./dateTime";
import type {
  ControlPlaneExportRequest,
  ControlPlaneImportPreview,
  ControlPlaneImportRequest,
  ControlPlaneRecoveryDownload,
} from "./controlPlaneTypes";
import {
  loadLocale,
  loadTimeZone,
  localeLabel,
  saveLocale,
  saveTimeZone,
  supportedLocales,
  supportedTimeZones,
  translate,
  type Locale,
} from "../i18n";

export type DashboardTask = {
  id: string;
  name: string;
  kind: "directory" | "database";
  status: string;
  repository: string;
  lastScheduledAt?: string;
  lastRun: string;
  nextRun: string;
  enabled?: boolean;
  lastCompleteBackup?: { snapshotId: string; startedAt: string; finishedAt?: string };
};

export type Dashboard = {
  tasks: DashboardTask[];
  alerts: Array<Partial<AlertState> & { id?: string; object?: string; message: string }>;
  repositoryStatus?: string;
  nextRun?: string;
  scheduleCoverage?: Array<{ planId: string; planName: string; total: number; success: number; partial: number; missed: number; failed: number; cancelled: number; skipped: number; interrupted: number; coveragePercent: number }>;
  runOverview?: { total: number; succeeded: number; failed: number; partial: number; successRate: number };
};

export type AppAPI = {
  setupStatus(): Promise<{ initialized: boolean }>;
  setup(
    username: string,
    password: string,
    token?: string,
  ): Promise<{ username: string }>;
  login(username: string, password: string): Promise<{ username: string }>;
  session(): Promise<{ username: string }>;
  logout(): Promise<void>;
  vaultStatus(): Promise<{
    mode: "automatic" | "lock-on-restart";
    locked: boolean;
  }>;
  unlockVault(passphrase: string): Promise<void>;
  setVaultLockOnRestart(passphrase: string): Promise<void>;
  setVaultAutomatic(password: string, confirmed: boolean): Promise<void>;
  exportControlPlane(payload: ControlPlaneExportRequest): Promise<ControlPlaneRecoveryDownload>;
  preflightControlPlaneImport(bundle: File, recoveryPassphrase: string): Promise<ControlPlaneImportPreview>;
  importControlPlane(bundle: File, payload: ControlPlaneImportRequest): Promise<AcceptedOperation>;
  agentServiceStatus(): Promise<AgentServiceStatus>;
  saveAgentServiceSettings(settings: { enabled: boolean; port: number; advertisedHost: string }): Promise<AgentServiceStatus>;
  lifecyclePolicy(): Promise<LifecyclePolicy>;
  saveLifecyclePolicy(policy: LifecyclePolicy): Promise<void>;
  previewLifecycleCleanup(): Promise<LifecycleReport>;
  cleanupLifecycle(password: string): Promise<LifecycleReport>;
  applicationVersion(): Promise<{ version: string }>;
  applicationReleases(): Promise<ApplicationReleaseState>;
  exportDiagnostics(): Promise<{ blob: Blob; filename: string }>;
  dashboard(): Promise<Dashboard>;
  compatibility(): Promise<{
    blocked: boolean;
    findings: Array<{
      capability: string;
      tool: string;
      path?: string;
      severity: string;
      message: string;
      version?: string;
    }>;
  }>;
  runTask(taskId: string): Promise<void>;
  listResource(resource: string): Promise<Array<Record<string, unknown>>>;
  createResource(
    resource: string,
    payload: Record<string, unknown>,
  ): Promise<unknown>;
  updateResource(
    resource: string,
    id: string,
    payload: Record<string, unknown>,
  ): Promise<void>;
  deleteResource(resource: string, id: string): Promise<void>;
  runDetail(id: string): Promise<Record<string, unknown>>;
  runLog(id: string): Promise<string>;
  saveMaintenance(id: string, payload: Record<string, unknown>): Promise<void>;
  saveRepositoryCapacityPolicy(id: string, payload: Record<string, unknown>): Promise<unknown>;
  saveRestoreVerificationPolicy(taskId: string, payload: Record<string, unknown>): Promise<unknown>;
  deleteRestoreVerificationPolicy(taskId: string): Promise<void>;
  action(path: string, payload?: Record<string, unknown>): Promise<unknown>;
};

type AppProps = { api: AppAPI };

type LifecyclePolicy = {
  runDays: number;
  rawLogDays: number;
  auditDays: number;
  rawLogMaxBytes: number;
};

type LifecycleReport = {
  logsCleared: number;
  runsDeleted: number;
  auditsDeleted: number;
  rawLogBytesBefore: number;
  rawLogBytesAfter: number;
  completedAt: string;
};

const navigation = [
  "仪表盘",
  "兼容性中心",
  "远程主机",
  "Agent 节点",
  "备份仓库",
  "数据库实例",
  "备份任务",
  "快照与恢复",
  "运行记录",
  "告警历史",
  "投递记录",
  "审计日志",
  "通知配置",
  "Agent 服务",
  "安全设置",
  "配置备份与恢复",
  "数据生命周期",
  "界面语言",
];

const navigationGroups = {
  "连接管理": ["远程主机", "Agent 节点", "数据库实例"],
  "活动与记录": ["运行记录", "告警历史", "投递记录", "审计日志"],
  "系统": ["兼容性中心", "通知配置", "Agent 服务", "安全设置", "配置备份与恢复", "数据生命周期", "界面语言"],
} as const;

const sidebarNavigation = [
  "仪表盘", "连接管理", "备份仓库", "备份任务",
  "快照与恢复", "活动与记录", "系统",
];

function NavigationIcon({ item }: { item: string }) {
  const paths: Record<string, ReactNode> = {
    "仪表盘": <path d="M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z" />,
    "连接管理": <><circle cx="6" cy="12" r="3" /><circle cx="18" cy="6" r="3" /><circle cx="18" cy="18" r="3" /><path d="m9 11 6-4M9 13l6 4" /></>,
    "远程主机": <><rect x="3" y="5" width="18" height="12" rx="2" /><path d="M8 21h8M12 17v4M7 9h.01M10 9h.01" /></>,
    "Agent 节点": <><circle cx="12" cy="12" r="3" /><circle cx="5" cy="6" r="2" /><circle cx="19" cy="6" r="2" /><circle cx="5" cy="18" r="2" /><circle cx="19" cy="18" r="2" /><path d="m7 7.5 2.5 2M17 7.5l-2.5 2M7 16.5l2.5-2M17 16.5l-2.5-2" /></>,
    "备份仓库": <><ellipse cx="12" cy="5" rx="8" ry="3" /><path d="M4 5v7c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12v7c0 1.7 3.6 3 8 3s8-1.3 8-3v-7" /></>,
    "数据库实例": <><ellipse cx="12" cy="5" rx="7" ry="3" /><path d="M5 5v7c0 1.7 3.1 3 7 3s7-1.3 7-3V5M5 12v7c0 1.7 3.1 3 7 3s7-1.3 7-3v-7" /></>,
    "备份任务": <><path d="M6 3h9l4 4v14H6z" /><path d="M14 3v5h5M9 13h6M9 17h6" /></>,
    "备份计划": <><rect x="3" y="5" width="18" height="16" rx="2" /><path d="M16 3v4M8 3v4M3 10h18M8 14h.01M12 14h.01M16 14h.01M8 18h.01M12 18h.01" /></>,
    "快照与恢复": <><path d="M4 7v5h5M20 17v-5h-5" /><path d="M6.1 17A8 8 0 0 0 20 12M17.9 7A8 8 0 0 0 4 12" /></>,
    "活动与记录": <><path d="M4 19V9M10 19V5M16 19v-7M22 19H2" /></>,
    "系统": <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1a1.7 1.7 0 0 0 1.9.3A1.7 1.7 0 0 0 10 3v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z" /></>,
  };
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[item]}</svg>;
}

const pagePaths: Record<string, string> = {
  仪表盘: "dashboard", 创建保护: "protection", 兼容性中心: "compatibility", 远程主机: "remote-hosts",
  "Agent 节点": "agents",
  备份仓库: "repositories", 数据库实例: "database-connections", 备份任务: "tasks",
  备份计划: "plans", 快照与恢复: "restore",
  运行记录: "runs", 告警历史: "alerts", 投递记录: "deliveries", 通知配置: "notifications", 审计日志: "audits", 安全设置: "security",
  "Agent 服务": "agent-service", 配置备份与恢复: "disaster-recovery", 数据生命周期: "lifecycle", 界面语言: "language",
};

function pageFromLocation() {
  const slug = window.location.pathname.match(/^\/admin\/([^/]+)$/)?.[1];
  if (slug === "plans" || slug === "protection") return "备份任务";
  return navigation.find((page) => pagePaths[page] === slug) ?? navigation[0];
}

function taskHealthTargetFromLocation(page = pageFromLocation(), search = window.location.search) {
  if (page !== "备份任务") return "";
  const query = new URLSearchParams(search);
  return query.get("view") === "health" ? query.get("task") ?? "" : "";
}

function taskEditorTargetFromLocation(page = pageFromLocation(), search = window.location.search) {
  if (page !== "备份任务") return "";
  const query = new URLSearchParams(search);
  if (query.get("view") === "create") return "create";
  return query.get("view") === "edit" ? query.get("task") ?? "" : "";
}

export function App({ api }: AppProps) {
  const [locale, setLocale] = useState<Locale>(loadLocale);
  const [timeZone, setTimeZone] = useState(loadTimeZone);
  const t = (source: string) => translate(locale, source);
  const [view, setView] = useState<
    "loading" | "setup" | "login" | "unlock" | "dashboard"
  >("loading");
  const [username, setUsername] = useState("");
  const [dashboard, setDashboard] = useState<Dashboard>({
    tasks: [],
    alerts: [],
  });
  const [error, setError] = useState("");
  const [activePage, setActivePage] = useState(pageFromLocation);
  const [taskHealthTarget, setTaskHealthTarget] = useState(taskHealthTargetFromLocation);
  const [taskEditorTarget, setTaskEditorTarget] = useState(taskEditorTargetFromLocation);
  const [compatibility, setCompatibility] = useState<Awaited<
    ReturnType<AppAPI["compatibility"]>
  > | null>(null);
  const [pageData, setPageData] = useState<Array<Record<string, unknown>>>([]);
  const pageRequest = useRef(0);
  const routeLoaded = useRef(false);
  const [mobile, setMobile] = useState(() => window.matchMedia?.("(max-width: 820px)").matches ?? false);
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
  const menuButton = useRef<HTMLButtonElement>(null);
  const firstNavigationItem = useRef<HTMLButtonElement>(null);
  const [shellMessage, setShellMessage] = useState("");

  useEffect(() => {
    saveLocale(locale);
    document.documentElement.lang = locale;
    document.title = locale === "en-US" ? "Shadoc administration" : "影刻 · Shadoc 管理端";
  }, [locale]);

  useEffect(() => {
    saveTimeZone(timeZone);
  }, [timeZone]);

  async function enterDashboard(authenticatedUsername: string) {
	const vault = await api.vaultStatus();
	setUsername(authenticatedUsername);
	if (vault.locked) {
	  setError("");
	  setView("unlock");
	  return;
	}
    const data = await api.dashboard();
    setDashboard(data);
    setError("");
    setView("dashboard");
  }

  async function authenticate(
    mode: "setup" | "login",
    submittedUsername: string,
    password: string,
    token?: string,
  ) {
    const session =
      mode === "setup"
        ? await api.setup(submittedUsername, password, token)
        : await api.login(submittedUsername, password);
    await enterDashboard(session.username);
  }

  async function openPage(item: string, updateHistory = true, search = "") {
	if (item === "备份计划") item = "备份任务";
	const requestId = ++pageRequest.current;
    setActivePage(item);
    const normalizedSearch = search && !search.startsWith("?") ? `?${search}` : search;
    setTaskHealthTarget(taskHealthTargetFromLocation(item, normalizedSearch));
    setTaskEditorTarget(taskEditorTargetFromLocation(item, normalizedSearch));
    if (updateHistory) {
      const path = `/admin/${pagePaths[item] ?? pagePaths.仪表盘}${normalizedSearch}`;
      if (`${window.location.pathname}${window.location.search}` !== path) window.history.pushState({}, "", path);
    }
    setError("");
    setPageData([]);
    if (item === "仪表盘") {
	  try { const value = await api.dashboard(); if (pageRequest.current === requestId) setDashboard(value); } catch { if (pageRequest.current === requestId) setError("无法刷新仪表盘"); }
      return;
    }
    if (item === "兼容性中心") {
      try {
		const value = await api.compatibility();
		if (pageRequest.current === requestId) setCompatibility(value);
      } catch {
		if (pageRequest.current === requestId) setError("无法执行兼容性检测");
      }
      return;
    }
    if (item === "运行记录") {
      return;
    }
    const resource = pageResource(item);
    if (resource) {
      try {
		const value = await api.listResource(resource);
		if (pageRequest.current === requestId) setPageData(value);
      } catch {
		if (pageRequest.current === requestId) setError("无法读取页面数据");
      }
    }
  }

  async function refreshPage(item: string) {
    const resource = pageResource(item);
    if (!resource) return;
    const requestId = ++pageRequest.current;
    setError("");
    try {
      const value = await api.listResource(resource);
      if (pageRequest.current === requestId) setPageData(value);
    } catch {
      if (pageRequest.current === requestId) setError("无法读取页面数据");
    }
  }

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const setup = await api.setupStatus();
        if (!setup.initialized) {
          if (active) setView("setup");
          return;
        }
        const session = await api.session();
        const vault = await api.vaultStatus();
        const data = vault.locked ? null : await api.dashboard();
        if (active) {
          setUsername(session.username);
          if (vault.locked) setView("unlock");
          else {
            setDashboard(data!);
            setView("dashboard");
          }
        }
      } catch (cause) {
        if (!active) return;
        if (cause instanceof Error && cause.message === "unauthorized")
          setView("login");
        else {
          setError("无法连接 Shadoc 控制服务");
          setView("login");
        }
      }
    })();
    return () => {
      active = false;
    };
  }, [api]);

  useEffect(() => {
    const navigate = () => void openPage(pageFromLocation(), false, window.location.search);
    window.addEventListener("popstate", navigate);
    return () => window.removeEventListener("popstate", navigate);
  }, [api]);

  useEffect(() => {
    if (view !== "dashboard" || routeLoaded.current) return;
    routeLoaded.current = true;
    if (activePage !== "仪表盘") void openPage(activePage, false, window.location.search);
  }, [view]);

  useEffect(() => {
    const handleAccessState = (event: Event) => {
      const state = (event as CustomEvent<string>).detail;
      setError("");
      setPageData([]);
      setCompatibility(null);
      setTaskHealthTarget("");
      setTaskEditorTarget("");
      routeLoaded.current = false;
      if (state === "locked") setView("unlock");
      else {
        setUsername("");
        setDashboard({ tasks: [], alerts: [] });
        setView("login");
      }
    };
    window.addEventListener("shadoc:access-state", handleAccessState);
    return () => window.removeEventListener("shadoc:access-state", handleAccessState);
  }, []);

  useEffect(() => {
    const refresh = () => {
      if (view === "dashboard" && activePage === "仪表盘") void api.dashboard().then(setDashboard).catch(() => setError("无法刷新仪表盘"));
    };
    window.addEventListener("focus", refresh);
    return () => window.removeEventListener("focus", refresh);
  }, [activePage, api, view]);

  useEffect(() => {
    const query = window.matchMedia?.("(max-width: 820px)");
    if (!query) return;
    const update = () => { setMobile(query.matches); if (!query.matches) setMobileNavigationOpen(false); };
    query.addEventListener?.("change", update);
    return () => query.removeEventListener?.("change", update);
  }, []);

  useEffect(() => {
    if (!mobileNavigationOpen) return;
    firstNavigationItem.current?.focus();
    const close = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      setMobileNavigationOpen(false);
      menuButton.current?.focus();
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [mobileNavigationOpen]);

  if (view === "loading")
    return <div className="centered-state">{t("正在加载…")}</div>;
  if (view === "setup")
    return (
      <AccessScreen
        title={t("初始化影刻 · Shadoc")}
        action={t("创建管理员")}
        mode="setup"
        locale={locale}
        onSubmit={authenticate}
      />
    );
  if (view === "login")
    return (
      <AccessScreen
        title={t("登录影刻 · Shadoc")}
        action={t("登录")}
        mode="login"
        locale={locale}
        error={error}
        onSubmit={authenticate}
      />
    );
  if (view === "unlock")
    return (
      <UnlockScreen
        error={error}
        locale={locale}
        onUnlock={async (passphrase) => {
          try {
            await api.unlockVault(passphrase);
            await enterDashboard(username);
          } catch (cause) {
            setError(cause instanceof Error ? cause.message : "无法解锁秘密库");
          }
        }}
      />
    );

  return (
    <div className="app-shell">
      <div className="mobile-topbar">
        <button ref={menuButton} className="menu-button" type="button" aria-controls="administration-navigation" aria-expanded={mobileNavigationOpen} onClick={() => setMobileNavigationOpen(true)}>☰ <span>{t("打开导航")}</span></button>
        <strong>{t(activePage)}</strong>
        <span className="mobile-operation-state" aria-live="polite">{t(error ? "异常" : "在线")}</span>
      </div>
      {mobile && mobileNavigationOpen && <button className="navigation-scrim" type="button" aria-label={t("关闭导航")} onClick={() => { setMobileNavigationOpen(false); menuButton.current?.focus(); }} />}
      <aside id="administration-navigation" className={`sidebar${mobileNavigationOpen ? " mobile-open" : ""}`} aria-hidden={mobile && !mobileNavigationOpen} inert={mobile && !mobileNavigationOpen ? true : undefined}>
        <div className="brand">
          <span className="brand-mark"><img src="/shadoc-icon.png" alt="" /></span>
          <span>影刻 <small>Shadoc</small></span>
        </div>
        <nav aria-label={t("主导航")}>
          {sidebarNavigation.map((item, index) => {
            const children = navigationGroups[item as keyof typeof navigationGroups];
            const selected = item === activePage || !!children?.includes(activePage as never);
            const target = children?.[0] ?? item;
            return (
            <button
              ref={index === 0 ? firstNavigationItem : undefined}
              className={selected ? "nav-item selected" : "nav-item"}
              key={item}
              type="button"
              onClick={() => { setMobileNavigationOpen(false); void openPage(target); }}
            >
              <span className="nav-icon" aria-hidden="true"><NavigationIcon item={item} /></span>
              {t(item)}
            </button>
          )})}
        </nav>
        <div className="sidebar-account">
          <div className="sidebar-user">
            <span className="status-dot" />
            {username}
            <button className="text-button" type="button" onClick={() => void api.logout().then(() => {
              setUsername(""); setDashboard({ tasks: [], alerts: [] }); setPageData([]); setCompatibility(null); setError(""); setActivePage(navigation[0]); setTaskHealthTarget(""); setView("login");
            }).catch(() => setShellMessage(t("退出登录失败")))}>{t("退出登录")}</button>
          </div>
          <ApplicationVersionStatus api={api} locale={locale} />
        </div>
      </aside>

      <main className="main-content">
        {error && <p className="error-message" role="alert">{error}</p>}
        {Object.entries(navigationGroups).map(([label, pages]) => pages.includes(activePage as never) && (
          <div className="page-tabs" role="tablist" aria-label={t(label)} key={label}>
            {pages.map((page) => <button
              className={activePage === page ? "selected" : ""}
              type="button"
              role="tab"
              aria-selected={activePage === page}
              key={page}
              onClick={() => void openPage(page)}
            >{t(page)}</button>)}
          </div>
        ))}
        {activePage === "Agent 服务" ? (
          <AgentServicePage api={api} locale={locale} />
        ) : activePage === "界面语言" ? (
          <LanguageSettings locale={locale} onChange={setLocale} timeZone={timeZone} onTimeZoneChange={setTimeZone} />
        ) : activePage === "数据生命周期" ? (
          <LifecycleSettings api={api} locale={locale} />
        ) : activePage === "安全设置" ? (
          <VaultSettings api={api} locale={locale} />
        ) : activePage === "配置备份与恢复" ? (
          <ControlPlaneRecovery api={api} locale={locale} timeZone={timeZone} />
        ) : activePage === "兼容性中心" ? (
          <CompatibilityPage report={compatibility} api={api} locale={locale} />
        ) : activePage === "仪表盘" ? (
          <>
            <header className="page-header">
              <div>
                <h1>{t("仪表盘")}</h1>
                <p>{t("查看计划、仓库与最近运行状态。")}</p>
              </div>
              <div className="page-header-actions">
                <button className="primary-button" type="button" onClick={() => void openPage("备份任务", true, "?view=create")}>{t("新建备份任务")}</button>
              </div>
            </header>

            <section className="summary-strip" aria-label={t("备份概览")}>
              <Summary
                label={t("下次计划")}
                value={dashboard.nextRun && dashboard.nextRun !== "暂无计划" ? adminTime(dashboard.nextRun, locale, timeZone) : t("暂无计划")}
              />
              <Summary label={t("仓库状态")} value={t(dashboard.repositoryStatus === "abnormal" ? "异常" : "正常")} tone={dashboard.repositoryStatus === "abnormal" ? "warning" : "healthy"} />
              <Summary
                label={t("当前告警")}
                value={String(dashboard.alerts.length)}
                tone={dashboard.alerts.length ? "warning" : "healthy"}
              />
            </section>

            <section className="content-section">
              <div className="section-heading">
                <h2>{t("最近运行")}</h2>
                <button className="text-button" type="button" onClick={() => void openPage("运行记录")}>
                  {t("查看全部")}
                </button>
              </div>
              <div className="table-frame">
                <table>
                  <thead>
                    <tr>
                      <th>{t("任务")}</th>
                      <th>{t("类型")}</th>
                      <th>{t("状态")}</th>
                      <th>{t("目标仓库")}</th>
                      <th>{t("上次应运行")}</th>
                      <th>{t("实际运行")}</th>
                      <th>{t("最近完整备份")}</th>
                      <th>{t("下次计划")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {dashboard.tasks.map((task) => (
                      <tr key={task.id}>
                        <td className="strong-cell">{task.name}</td>
                        <td>{t(task.kind === "directory" ? "目录" : "数据库")}</td>
                        <td><StatusIndicator value={task.status} locale={locale} /></td>
                        <td>{task.repository}</td>
                        <td>{adminTime(task.lastScheduledAt ?? "尚无计划发生记录", locale, timeZone)}</td>
                        <td>{adminTime(task.lastRun, locale, timeZone)}</td>
                        <td>{task.lastCompleteBackup ? <><span className="strong-cell">{shortIdentifier(task.lastCompleteBackup.snapshotId)}</span><br />{adminTime(task.lastCompleteBackup.finishedAt ?? task.lastCompleteBackup.startedAt, locale, timeZone)}</> : "—"}</td>
                        <td>{adminTime(task.nextRun, locale, timeZone)}</td>
                      </tr>
                    ))}
                    {!dashboard.tasks.length && (
                      <tr>
                        <td className="empty-row" colSpan={8}>
                          {t("尚未创建备份任务")}
                        </td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
            </section>
            <TaskHealthTrends api={api} locale={locale} onOpenTask={(taskId) => void openPage("备份任务", true, `?task=${encodeURIComponent(taskId)}&view=health`)} />
            {dashboard.alerts.length > 0 && <section className="content-section dashboard-alerts" aria-label={t("当前告警")}>
              <h2>{t("当前告警")}</h2>
              <ul className="health-alert-list">{dashboard.alerts.map((alert) => <li className={`health-alert ${alert.severity ?? "warning"}`} key={alert.stateKey ?? alert.id}>
                <div className="health-alert-heading"><strong>{alert.objectName ?? alert.object ?? t("系统")}</strong>{alert.severity && <StatusIndicator value={alert.severity} locale={locale} label={t(alert.severity === "critical" ? "严重" : alert.severity === "warning" ? "警告" : "信息")} variant="pill" />}</div>
                <p>{alert.message}</p>
                <dl>
                  {alert.reason && <div><dt>{t("原因")}</dt><dd>{alert.reason}</dd></div>}
                  {alert.firstAt && <div><dt>{t("首次发生")}</dt><dd>{adminTime(alert.firstAt, locale, timeZone)}</dd></div>}
                  {alert.lastAt && <div><dt>{t("最近发生")}</dt><dd>{adminTime(alert.lastAt, locale, timeZone)}</dd></div>}
                  {alert.recoveryCondition && <div className="wide"><dt>{t("恢复条件")}</dt><dd>{alert.recoveryCondition}</dd></div>}
                </dl>
                {alert.targetPage && <button className="text-button" type="button" onClick={() => void openPage(
                  alert.targetPage!,
                  true,
                  "",
                )}>{t("处理")}</button>}
              </li>)}</ul>
            </section>}
          </>
        ) : (
          <ManagementPage
            name={activePage}
            data={pageData}
            api={api}
            reload={() => refreshPage(activePage)}
            onNavigate={(page, search) => openPage(page, true, search)}
            taskHealthTarget={taskHealthTarget}
            taskEditorTarget={taskEditorTarget}
            locale={locale}
            timeZone={timeZone}
          />
        )}
      </main>
      <Toast message={shellMessage} locale={locale} onClose={() => setShellMessage("")} />
    </div>
  );
}

function AgentServicePage({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [message, setMessage] = useState("");
  return <>
    <header className="system-page-intro">
      <p>{t("管理 Agent 注册、心跳与任务通道。")}</p>
    </header>
    <AgentServiceSettings api={api} locale={locale} onStatus={() => undefined} onMessage={setMessage} />
    <Toast message={message} locale={locale} onClose={() => setMessage("")} />
  </>;
}

function LanguageSettings({
  locale,
  onChange,
  timeZone,
  onTimeZoneChange,
}: {
  locale: Locale;
  onChange(locale: Locale): void;
  timeZone: string;
  onTimeZoneChange(timeZone: string): void;
}) {
  const t = (source: string) => translate(locale, source);
  return <>
    <header className="system-page-intro">
      <p>{t("选择管理界面使用的语言。修改会立即生效，并保存在当前浏览器中。")}</p>
    </header>
    <section className="content-section">
      <div className="form-grid">
        <label className="full-field">
          {t("界面显示语言")}
          <select aria-label={t("界面语言")} value={locale} onChange={(event) => onChange(event.target.value as Locale)}>
            {supportedLocales.map((value) => <option value={value} key={value}>{localeLabel(locale, value)}</option>)}
          </select>
        </label>
        <p className="field-hint full-field">{t("语言偏好只影响界面文案，不会修改任务、仓库、日志或远程 Agent 数据。")}</p>
        <label className="full-field">
          {t("界面显示时区")}
          <select aria-label={t("界面显示时区")} value={timeZone} onChange={(event) => onTimeZoneChange(event.target.value)}>
            {supportedTimeZones().map((value) => <option value={value} key={value}>{value}</option>)}
          </select>
        </label>
        <p className="field-hint full-field">{t("日期和时间按此时区显示；持久化数据仍使用 UTC。")}</p>
      </div>
    </section>
  </>;
}

function CompatibilityPage({
  report,
  api,
  locale,
}: {
  report: Awaited<ReturnType<AppAPI["compatibility"]>> | null;
  api: AppAPI;
  locale: Locale;
}) {
  const t = (source: string) => translate(locale, source);
  const [message, setMessage] = useState("");
  const [versions, setVersions] = useState<string[]>([]);
  const [exportingDiagnostics, setExportingDiagnostics] = useState(false);
  const installation = useOperation(api);
  const handledInstallation = useRef("");
  useEffect(() => {
    let active = true;
    void api
      .action("/api/restic/versions")
      .then((value) => {
        const items = (value as { versions?: unknown }).versions;
        if (active && Array.isArray(items))
          setVersions(
            items.filter((item): item is string => typeof item === "string"),
          );
      })
      .catch(() => {
        if (active) setMessage("暂时无法读取官方版本，可手动输入版本号");
      });
    return () => {
      active = false;
    };
  }, [api]);
  useEffect(() => {
    const operation = installation.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledInstallation.current === key) return;
    handledInstallation.current = key;
    setMessage(operation.status === "success"
      ? t("Restic 安装完成")
      : operation.errorSummary || t(operation.status === "cancelled" ? "操作已取消" : "Restic 安装失败"));
  }, [installation.operation, locale]);
  useEffect(() => {
    if (installation.error) setMessage(installation.error);
  }, [installation.error]);
  async function downloadDiagnostics() {
    setExportingDiagnostics(true);
    setMessage("");
    try {
      const download = await api.exportDiagnostics();
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
      setMessage(t("脱敏诊断包已下载"));
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : t("无法下载脱敏诊断包"));
    } finally {
      setExportingDiagnostics(false);
    }
  }
  return (
    <>
      <header className="system-page-intro">
        <p>{t("启动和任务运行所需的关键能力检测。")}</p>
      </header>
      <section className="content-section">
        <div className="table-frame">
          <table>
            <thead>
              <tr>
                <th>{t("能力")}</th>
                <th>{t("工具")}</th>
                <th>{t("状态")}</th>
                <th>{t("版本")}</th>
                <th>{t("路径")}</th>
                <th>{t("说明")}</th>
              </tr>
            </thead>
            <tbody>
              {report?.findings.map((finding) => (
                <tr key={finding.capability}>
                  <td className="strong-cell">{t(finding.capability)}</td>
                  <td>{finding.tool}</td>
                  <td>
                    <StatusIndicator
                      value={finding.severity === "info" ? "success" : "idle"}
                      locale={locale}
                    />
                  </td>
                  <td>{finding.version || "—"}</td>
                  <td>{finding.path || t("未发现")}</td>
                  <td>{t(finding.message)}</td>
                </tr>
              ))}
              {!report && (
                <tr>
                  <td colSpan={6} className="empty-row">
                    {t("正在检测…")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
      <section className="content-section diagnostic-export" aria-labelledby="diagnostic-export-title">
        <div>
          <h2 id="diagnostic-export-title">{t("脱敏诊断包")}</h2>
          <p>{t("包含应用版本、兼容性状态、资源计数、近期失败、活动告警、通知渠道状态和容量健康。")}</p>
          <p className="field-hint">{t("不包含秘密、原始日志、操作详情、路径、主机、用户名、URL、主题或命令参数；每个列表和整个文件都有固定上限。")}</p>
        </div>
        <button className="secondary-button" type="button" disabled={exportingDiagnostics} onClick={() => void downloadDiagnostics()}>{t(exportingDiagnostics ? "正在生成诊断包…" : "下载脱敏诊断包")}</button>
      </section>
      <section className="content-section">
        <h2>{t("安装受管 Restic")}</h2>
        <form
          className="form-grid"
          onSubmit={(e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            void installation.start("/api/restic/install", { version: String(f.get("version")) });
          }}
        >
          <label>
            {t("官方稳定版本（默认最新）")}
            {versions.length ? (
              <select name="version" defaultValue={versions[0]}>
                {versions.map((version) => (
                  <option key={version} value={version}>
                    {version}
                  </option>
                ))}
              </select>
            ) : (
              <input name="version" placeholder={t("例如 0.19.1")} required />
            )}
          </label>
          <button className="primary-button" type="submit" disabled={installation.active}>
            {t(installation.active ? "正在下载、校验并安装…" : "下载、校验并安装")}
          </button>
          <Toast message={message} locale={locale} onClose={() => setMessage("")} />
          <OperationFeedback operation={installation} locale={locale} hideTerminal />
        </form>
      </section>
    </>
  );
}

const resources: Record<string, string> = {
  远程主机: "remote-hosts",
  "Agent 节点": "agents",
  备份仓库: "repositories",
  数据库实例: "database-connections",
  备份任务: "tasks",
  备份计划: "plans",
  运行记录: "runs",
};
function pageResource(name: string) {
  return resources[name];
}

function AgentPage({
  data,
  api,
  reload,
  onNavigate,
  locale,
  timeZone,
}: {
  data: Array<Record<string, unknown>>;
  api: AppAPI;
  reload(): Promise<void>;
  onNavigate(page: string): Promise<void>;
  locale: Locale;
  timeZone: string;
}) {
  const t = (source: string) => translate(locale, source);
  const [enrollment, setEnrollment] = useState<Record<string, unknown> | null>(null);
  const [message, setMessage] = useState("");
  const [actionTarget, setActionTarget] = useState<Record<string, unknown> | null>(null);
  const [upgradeTarget, setUpgradeTarget] = useState<Record<string, unknown> | null>(null);
  const [resticTarget, setResticTarget] = useState<Record<string, unknown> | null>(null);
  const [toolProbeTarget, setToolProbeTarget] = useState<Record<string, unknown> | null>(null);
  const [latestResticVersion, setLatestResticVersion] = useState("");
  const [resticOperationAgentID, setResticOperationAgentID] = useState("");
  const [revoking, setRevoking] = useState(false);
  const [deployDialog, setDeployDialog] = useState(false);
  const [remoteHosts, setRemoteHosts] = useState<Array<Record<string, unknown>>>([]);
  const [deploymentHost, setDeploymentHost] = useState("");
  const [deploymentAgentID, setDeploymentAgentID] = useState("");
  const [redeploying, setRedeploying] = useState(false);
  const [agentService, setAgentService] = useState<AgentServiceStatus | null>(null);
  const [deploymentURL, setDeploymentURL] = useState("");
  const deployment = useOperation(api);
  const uninstall = useOperation(api);
  const upgrade = useOperation(api);
  const resticInstall = useOperation(api);
  const toolProbe = useOperation(api);
  const reloadRef = useRef(reload);
  const lastRemovalRefresh = useRef("");
  const handledDeployment = useRef("");
  const handledUpgrade = useRef("");
  const handledResticInstall = useRef("");
  const handledToolProbe = useRef("");
  const deployDialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(deployDialogRef, () => setDeployDialog(false), deployDialog);
  const deploymentWasRedeploy = useRef(false);
  reloadRef.current = reload;
  useEffect(() => {
    let active = true;
    void api.listResource("remote-hosts").then((hosts) => {
      if (active) {
        setRemoteHosts(hosts);
        setDeploymentHost((current) => current || String(hosts[0]?.id ?? ""));
      }
    }).catch((cause) => {
      if (active) setMessage(cause instanceof Error ? cause.message : "无法读取远程主机");
    });
    return () => { active = false; };
  }, [api]);
  useEffect(() => {
    let active = true;
    const load = () => {
      void api.action("/api/restic/versions").then((value) => {
        const versions = (value as { versions?: unknown }).versions;
        if (active && Array.isArray(versions)) setLatestResticVersion(String(versions[0] ?? ""));
      }).catch(() => {
        if (active) setLatestResticVersion("");
      });
    };
    load();
    const interval = window.setInterval(load, 60_000);
    window.addEventListener("focus", load);
    return () => {
      active = false;
      window.clearInterval(interval);
      window.removeEventListener("focus", load);
    };
  }, [api]);
  useEffect(() => {
    let active = true;
    const load = () => {
      void api.agentServiceStatus().then((status) => {
        if (active) setAgentService(status);
      }).catch((cause) => {
        if (active) setMessage(cause instanceof Error ? cause.message : t("无法读取 Agent 服务配置"));
      });
    };
    load();
    const interval = window.setInterval(load, 15_000);
    window.addEventListener("focus", load);
    return () => {
      active = false;
      window.clearInterval(interval);
      window.removeEventListener("focus", load);
    };
  }, [api, locale]);
  useEffect(() => {
    if (agentService?.serviceUrl) setDeploymentURL(agentService.serviceUrl);
  }, [agentService?.serviceUrl]);
  useEffect(() => {
    const operation = uninstall.operation;
    if (!operation) return;
    const refreshable = operation.stage === "removing_agent" || ["success", "failed", "cancelled", "cleanup_required"].includes(operation.status);
    if (!refreshable) return;
    const key = `${operation.id}:${operation.stage}:${operation.status}`;
    if (lastRemovalRefresh.current === key) return;
    lastRemovalRefresh.current = key;
    if (operation.status === "success") setMessage(t("Agent 已停止并卸载"));
    else if (operation.status === "failed") setMessage(operation.errorSummary || t("Agent 停止或卸载失败"));
    void reloadRef.current();
  }, [uninstall.operation?.id, uninstall.operation?.stage, uninstall.operation?.status]);
  useEffect(() => {
    const operation = deployment.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledDeployment.current === key) return;
    handledDeployment.current = key;
    if (operation.status === "success") setMessage(t(deploymentWasRedeploy.current ? "Agent 已重新部署" : "Agent 部署成功"));
    else if (operation.status === "failed") setMessage(operation.errorSummary || t("Agent 部署失败"));
    else setMessage(t("操作已取消"));
    void reloadRef.current();
  }, [deployment.operation?.id, deployment.operation?.status]);
  useEffect(() => {
    if (deployment.error) setMessage(deployment.error);
  }, [deployment.error]);
  useEffect(() => {
    if (uninstall.error) setMessage(uninstall.error);
  }, [uninstall.error]);
  useEffect(() => {
    const operation = upgrade.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledUpgrade.current === key) return;
    handledUpgrade.current = key;
    if (operation.status === "success") setMessage(t("Agent 升级完成，新版本心跳已验证"));
    else if (operation.status === "failed") setMessage(operation.errorSummary || t("Agent 升级失败，系统已尝试恢复旧版本"));
    else setMessage(t("Agent 升级已取消，系统已尝试恢复旧版本"));
    void reloadRef.current();
  }, [upgrade.operation?.id, upgrade.operation?.status]);
  useEffect(() => {
    if (upgrade.error) setMessage(upgrade.error);
  }, [upgrade.error]);
  useEffect(() => {
    const operation = resticInstall.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledResticInstall.current === key) return;
    handledResticInstall.current = key;
    if (operation.status === "success") { setMessage(t("Agent Restic 安装完成，备份与恢复能力已验证")); setResticOperationAgentID(""); }
    else if (operation.status === "failed") setMessage(operation.errorSummary || t("Agent Restic 安装失败，旧版本已恢复"));
    else { setMessage(t("Agent Restic 安装已取消，旧版本已恢复")); setResticOperationAgentID(""); }
    void reloadRef.current();
  }, [resticInstall.operation?.id, resticInstall.operation?.status]);
  useEffect(() => {
    if (resticInstall.error) setMessage(resticInstall.error);
  }, [resticInstall.error]);
  useEffect(() => {
    const operation = toolProbe.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledToolProbe.current === key) return;
    handledToolProbe.current = key;
    if (operation.status === "success") setMessage(t("Agent 工具重新探测完成，新能力心跳已验证"));
    else if (operation.status === "failed") setMessage(operation.errorSummary || t("Agent 工具重新探测失败"));
    else setMessage(t("Agent 工具重新探测已取消"));
    void reloadRef.current();
  }, [toolProbe.operation?.id, toolProbe.operation?.status]);
  useEffect(() => {
    if (toolProbe.error) setMessage(toolProbe.error);
  }, [toolProbe.error]);

  function openDeployment(agent?: Record<string, unknown>) {
    const isRedeploy = Boolean(agent?.uninstalledAt);
    setRedeploying(isRedeploy);
    setDeploymentAgentID(String(agent?.id ?? ""));
    setDeploymentHost(String(agent?.remoteHostId ?? remoteHosts[0]?.id ?? ""));
    setDeployDialog(true);
  }

  async function confirmAgentAction(agent: Record<string, unknown>) {
    setActionTarget(null);
    const id = encodeURIComponent(String(agent.id));
    if (agent.remoteHostId) {
      setMessage(t("正在停止并卸载 Agent…"));
      await uninstall.start(`/api/agents/${id}/uninstall`, {});
      return;
    }
    setRevoking(true);
    try {
      await api.action(`/api/agents/${id}/revoke`, {});
      setMessage(t("Agent 凭据已撤销"));
      await reload();
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : t("无法撤销 Agent"));
    } finally {
      setRevoking(false);
    }
  }
  async function confirmAgentUpgrade(agent: Record<string, unknown>) {
    setUpgradeTarget(null);
    setMessage(t("Agent 托管升级已开始"));
    await upgrade.start(`/api/agents/${encodeURIComponent(String(agent.id))}/upgrade`, {});
  }
  async function confirmAgentResticInstall(agent: Record<string, unknown>) {
    setResticTarget(null);
    setResticOperationAgentID(String(agent.id));
    setMessage(t("Agent Restic 安装已开始"));
    await resticInstall.start(`/api/agents/${encodeURIComponent(String(agent.id))}/restic/install`, { version: latestResticVersion });
  }
  async function confirmAgentToolProbe(agent: Record<string, unknown>) {
    setToolProbeTarget(null);
    setMessage(t("Agent 工具重新探测已开始"));
    await toolProbe.start(`/api/agents/${encodeURIComponent(String(agent.id))}/tools/reprobe`, {});
  }
  return <>
    <header className="page-header"><div><h1>{t("Agent 节点")}</h1><p>{t(pageDescription("Agent 节点"))}</p></div>
      <div className="page-header-actions">
        <button className="secondary-button" type="button" disabled={!agentService?.running} onClick={() => openDeployment()}>{t("远程部署 Agent")}</button>
        <button className="primary-button" type="button" disabled={!agentService?.running} onClick={() => void api.action("/api/agents/enrollment-token", {}).then((value) => setEnrollment(value as Record<string, unknown>)).catch((cause) => setMessage(cause instanceof Error ? cause.message : "无法创建注册令牌"))}>{t("生成注册令牌")}</button>
      </div>
    </header>
    <div className="agent-service-rail" role="status" aria-live="polite">
      <StatusIndicator
        value={agentService === null ? "pending" : agentService.running ? "running" : "stopped"}
        locale={locale}
        label={t(agentService === null ? "正在检测…" : agentService.running ? "Agent 服务运行中" : "Agent 服务未运行")}
        tone={agentService === null ? "pending" : agentService.running ? "active" : "stopped"}
      />
      <button className="text-button" type="button" onClick={() => void onNavigate("Agent 服务")}>{t("Agent 服务设置")}</button>
    </div>
    {!agentService?.running && <p className="field-hint">{t("请先在 Agent 服务设置中启用服务，再生成令牌或执行远程部署。")}</p>}
    <OperationFeedback operation={upgrade} locale={locale} hideTerminal />
    <OperationFeedback operation={toolProbe} locale={locale} hideTerminal />
    <Toast message={message} locale={locale} onClose={() => setMessage("")} />
    {deployDialog && <ModalPortal>
      <form ref={deployDialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="agent-deploy-title" onSubmit={(event) => {
        event.preventDefault();
        const form = new FormData(event.currentTarget);
        deploymentWasRedeploy.current = redeploying;
        setMessage(t(redeploying ? "Agent 重新部署已开始" : "Agent 部署已开始"));
        void deployment.start("/api/agents/deploy", {
          hostId: String(form.get("hostId") ?? ""),
          agentId: String(form.get("agentId") ?? ""),
          serviceUrl: String(form.get("serviceUrl") ?? ""),
        }).then(() => setDeployDialog(false));
      }}>
        <header><div><h2 id="agent-deploy-title">{t(redeploying ? "重新部署 Agent" : "远程部署 Agent")}</h2><p>{t("通过已验证并固定主机密钥的 SSH 连接安装用户级服务，成功注册后才算部署完成。")}</p></div></header>
        <div className="form-grid">
          <label className="full-field">{t("远程主机")}
            <select name="hostId" required value={deploymentHost} onChange={(event) => setDeploymentHost(event.target.value)}>
              <option value="" disabled>{t("请选择远程主机")}</option>
              {remoteHosts.map((host) => <option key={String(host.id)} value={String(host.id)}>{String(host.name ?? host.id)} · {String(host.username)}@{String(host.host)}</option>)}
            </select>
          </label>
          {!remoteHosts.length && <p className="field-hint full-field">{t("尚无可用远程主机，请先创建并完成 SSH 主机密钥验证。")}</p>}
          <label>Agent ID<input name="agentId" required readOnly={redeploying} maxLength={64} pattern="[A-Za-z0-9][A-Za-z0-9._-]{0,63}" value={deploymentAgentID} onChange={(event) => setDeploymentAgentID(event.target.value)} placeholder={t("例如 backup-node")} /></label>
          <label>{t("Service HTTPS 地址")}<input name="serviceUrl" type="url" required readOnly value={deploymentURL} placeholder="https://control.example:9443" /></label>
          <p className="field-hint full-field">{t("该地址必须能从目标服务器访问，并与 Agent Service TLS 证书名称一致。部署不会请求 root 权限。")}</p>
        </div>
        <footer>
          <button className="secondary-button" type="button" onClick={() => setDeployDialog(false)}>{t("取消")}</button>
          <button className="primary-button" type="submit" disabled={!remoteHosts.length || deployment.active}>{t(redeploying ? "开始重新部署" : "开始部署")}</button>
        </footer>
      </form>
    </ModalPortal>}
    {enrollment && <section className="content-section operation-feedback" aria-label={t("Agent 注册信息")}>
      <strong>{t("一次性注册信息（15 分钟内有效）")}</strong>
      <p>{t("令牌只显示本次，请将 CA 保存为文件后在源端执行 Agent。")}</p>
      <label>{t("注册令牌")}<textarea readOnly value={String(enrollment.token ?? "")} /></label>
      <label>Service CA<textarea readOnly value={String(enrollment.caPem ?? "")} /></label>
      <pre>{`shadoc-agent --service https://SERVICE:PORT --id SOURCE-NAME --enrollment-token '${String(enrollment.token ?? "")}' --ca-file ./agent-ca.crt`}</pre>
      <button className="secondary-button" type="button" onClick={() => setEnrollment(null)}>{t("关闭注册信息")}</button>
    </section>}
    <AgentFleet
      agents={data}
      remoteHosts={remoteHosts}
      locale={locale}
      timeZone={timeZone}
      currentServiceURL={String(agentService?.serviceUrl ?? "")}
      latestResticVersion={latestResticVersion}
      busy={revoking || deployment.active || uninstall.active || upgrade.active || resticInstall.active || toolProbe.active}
      resticOperation={resticOperationAgentID ? {
        agentId: resticOperationAgentID,
        active: resticInstall.active,
        status: resticInstall.operation?.status,
        stage: resticInstall.operation?.stage,
        errorSummary: resticInstall.operation?.errorSummary,
        error: resticInstall.error,
      } : undefined}
      onCancelRestic={() => void resticInstall.cancel()}
      onUpgrade={setUpgradeTarget}
      onInstallRestic={setResticTarget}
      onReprobeTools={setToolProbeTarget}
      onRedeploy={openDeployment}
      onRemove={setActionTarget}
    />
    {actionTarget && <AgentActionDialog agent={actionTarget} active={revoking || uninstall.active} locale={locale} onClose={() => setActionTarget(null)} onConfirm={() => void confirmAgentAction(actionTarget)} />}
    {upgradeTarget && <AgentUpgradeDialog agent={upgradeTarget} active={upgrade.active} locale={locale} onClose={() => setUpgradeTarget(null)} onConfirm={() => void confirmAgentUpgrade(upgradeTarget)} />}
    {resticTarget && <AgentResticInstallDialog agent={resticTarget} targetVersion={latestResticVersion} active={resticInstall.active} locale={locale} onClose={() => setResticTarget(null)} onConfirm={() => void confirmAgentResticInstall(resticTarget)} />}
    {toolProbeTarget && <AgentToolProbeDialog agent={toolProbeTarget} active={toolProbe.active} locale={locale} onClose={() => setToolProbeTarget(null)} onConfirm={() => void confirmAgentToolProbe(toolProbeTarget)} />}
  </>;
}

function AgentToolProbeDialog({ agent, active, locale, onClose, onConfirm }: { agent: Record<string, unknown>; active: boolean; locale: Locale; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!active) onClose(); });
  return <ModalPortal>
    <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="agent-tool-probe-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
      <header><div><h2 id="agent-tool-probe-title">{t("确认重新探测 Agent 工具")}</h2><p>{t("系统会等待当前任务结束后安全重启 Agent，并等待新的能力心跳。")}</p></div></header>
      <div className="dialog-body">
        <dl className="agent-upgrade-summary"><div><dt>Agent ID</dt><dd><code>{String(agent.id)}</code></dd></div></dl>
        <p>{t("重启后仅执行固定的 Restic 和 rsync 版本探测，不会执行自定义脚本或任意远程命令。")}</p>
      </div>
      <footer><button className="secondary-button" type="button" disabled={active} onClick={onClose}>{t("取消")}</button><button className="primary-button" type="submit" disabled={active}>{t("开始重新探测")}</button></footer>
    </form>
  </ModalPortal>;
}

function AgentResticInstallDialog({ agent, targetVersion, active, locale, onClose, onConfirm }: { agent: Record<string, unknown>; targetVersion: string; active: boolean; locale: Locale; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!active) onClose(); });
  const currentVersion = String(agent.resticVersion || t("未安装"));
  return <ModalPortal>
    <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="agent-restic-install-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
      <header><div><h2 id="agent-restic-install-title">{t(agent.resticVersion ? "确认升级 Agent Restic" : "确认安装 Agent Restic")}</h2><p>{t("控制服务会下载并校验官方制品，排空当前任务后原子安装并重启 Agent。")}</p></div></header>
      <div className="dialog-body">
        <dl className="agent-upgrade-summary">
          <div><dt>Agent ID</dt><dd><code>{String(agent.id)}</code></dd></div>
          <div><dt>{t("版本变化")}</dt><dd><code>{currentVersion}</code> → <code>{targetVersion}</code></dd></div>
        </dl>
        <p>{t("只有新心跳确认 Restic 备份与恢复能力后才算成功；失败或取消会恢复旧版本。")}</p>
        <p className="field-hint">{t("安装使用 Agent 专用用户目录，不调用系统包管理器，也不请求 root 权限。")}</p>
      </div>
      <footer>
        <button className="secondary-button" type="button" disabled={active} onClick={onClose}>{t("取消")}</button>
        <button className="primary-button" type="submit" disabled={active || !targetVersion}>{t(agent.resticVersion ? "开始升级 Restic" : "开始安装 Restic")}</button>
      </footer>
    </form>
  </ModalPortal>;
}

function AgentUpgradeDialog({ agent, active, locale, onClose, onConfirm }: { agent: Record<string, unknown>; active: boolean; locale: Locale; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!active) onClose(); });
  return <ModalPortal>
    <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="agent-upgrade-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
      <header><div><h2 id="agent-upgrade-title">{t("确认升级 Agent")}</h2><p>{t("升级会短暂停止该节点领取新任务，并验证新版本心跳后才提交。")}</p></div></header>
      <div className="dialog-body">
        <dl className="agent-upgrade-summary">
          <div><dt>Agent ID</dt><dd><code>{String(agent.id)}</code></dd></div>
          <div><dt>{t("版本变化")}</dt><dd><code>{String(agent.buildVersion || t("未知版本"))}</code> → <code>{String(agent.targetVersion)}</code></dd></div>
        </dl>
        <div className="confirmation-panel"><strong>{t("升级期间的保护措施")}</strong><ul>
          <li>{t("先阻止新任务并等待当前任务结束，不会中断正在写入的备份。")}</li>
          <li>{t("新程序只写入固定暂存路径，旧程序在验证完成前保留。")}</li>
          <li>{t("目标版本或协议心跳未通过时自动恢复旧程序；取消操作也会触发恢复。")}</li>
        </ul></div>
      </div>
      <footer><button className="secondary-button" type="button" disabled={active} onClick={onClose}>{t("取消")}</button><button className="primary-button" type="submit" disabled={active}>{t("开始托管升级")}</button></footer>
    </form>
  </ModalPortal>;
}

function AgentActionDialog({
  agent,
  active,
  locale,
  onClose,
  onConfirm,
}: {
  agent: Record<string, unknown>;
  active: boolean;
  locale: Locale;
  onClose(): void;
  onConfirm(): void;
}) {
  const t = (source: string) => translate(locale, source);
  const managed = Boolean(agent.remoteHostId);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!active) onClose(); });
  const title = managed ? "确认停止并卸载 Agent" : "确认撤销 Agent 凭据";
  return <ModalPortal>
    <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="agent-action-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
      <header><div><h2 id="agent-action-title">{t(title)}</h2><p>{managed ? t("系统将先停止远程 Agent 服务，确认停止后再删除服务定义、程序和凭据，最后撤销 Agent 身份。") : t("撤销后，该 Agent 证书将立即失效；远程进程和文件不会被删除。")}</p></div></header>
      <div className="form-grid"><p className="full-field"><strong>Agent ID：</strong><code>{String(agent.id)}</code></p></div>
      <footer>
        <button className="secondary-button" type="button" disabled={active} onClick={onClose}>{t("取消")}</button>
        <button className="danger-button" type="submit" disabled={active}>{t(managed ? "确认停止并卸载" : "确认撤销凭据")}</button>
      </footer>
    </form>
  </ModalPortal>;
}

function ManagementPage({
  name,
  data,
  api,
  reload,
  onNavigate,
  taskHealthTarget,
  taskEditorTarget,
  locale,
  timeZone,
}: {
  name: string;
  data: Array<Record<string, unknown>>;
  api: AppAPI;
  reload(): Promise<void>;
  onNavigate(page: string, search?: string): Promise<void>;
  taskHealthTarget?: string;
  taskEditorTarget?: string;
  locale: Locale;
  timeZone: string;
}) {
  const t = (source: string) => translate(locale, source);
  const [dialog, setDialog] = useState(false);
  const [message, setMessage] = useState("");
  const [editing, setEditing] = useState<Record<string, unknown> | null>(null);
  const [rotateRepository, setRotateRepository] = useState("");
  const [generatedSSHAccess, setGeneratedSSHAccess] = useState<{ publicKey: string; name: string; username: string; host: string } | null>(null);
  const [pendingDelete, setPendingDelete] = useState<Record<string, unknown> | null>(null);
  const [deleting, setDeleting] = useState(false);
  const generatedSSHAccessRef = useRef<HTMLElement>(null);
  const initialization = useOperation(api);
  const repositoryConnection = useOperation(api);
  const taskRun = useOperation(api);
  const [initializingRepositoryId, setInitializingRepositoryId] = useState("");
  const [initializationPhase, setInitializationPhase] = useState<"idle" | "running" | "success">("idle");
  const handledInitialization = useRef("");
  const handledRepositoryConnection = useRef("");
  const [repositoryConnectionMode, setRepositoryConnectionMode] = useState<"" | "connect" | "verify">("");
  const handledTaskRun = useRef("");
  const [runningTaskId, setRunningTaskId] = useState("");
  const runningTaskGuard = useRef("");
  const reloadRef = useRef(reload);
  reloadRef.current = reload;
  useModalFocus(generatedSSHAccessRef, () => setGeneratedSSHAccess(null), Boolean(generatedSSHAccess));
  useEffect(() => setMessage(""), [name]);
  useEffect(() => {
    const operation = initialization.operation;
    if (!operation || !initializingRepositoryId || !["success", "partial", "failed", "cancelled", "cleanup_required"].includes(operation.status)) return;
    const handledKey = `${operation.id}:${operation.status}`;
    if (handledInitialization.current === handledKey) return;
    handledInitialization.current = handledKey;
    if (operation.status === "success") {
      setInitializationPhase("success");
      setMessage(translate(locale, "仓库初始化完成"));
    } else {
      setInitializationPhase("idle");
      setInitializingRepositoryId("");
      setMessage(operation.errorSummary || translate(locale, operation.status === "cancelled" ? "操作已取消" : "仓库初始化失败"));
    }
    void reloadRef.current();
  }, [initialization.operation, initializingRepositoryId, locale]);
  useEffect(() => {
    if (!initialization.error || !initializingRepositoryId) return;
    setMessage(initialization.error);
    setInitializationPhase("idle");
    setInitializingRepositoryId("");
  }, [initialization.error, initializingRepositoryId]);
  useEffect(() => {
    if (initializationPhase !== "success") return;
    const timer = window.setTimeout(() => {
      setInitializationPhase("idle");
      setInitializingRepositoryId("");
    }, 1600);
    return () => window.clearTimeout(timer);
  }, [initializationPhase]);
  useEffect(() => {
    const operation = repositoryConnection.operation;
    if (!operation || !repositoryConnectionMode || !["success", "partial", "failed", "cancelled", "cleanup_required"].includes(operation.status)) return;
    const handledKey = `${operation.id}:${operation.status}`;
    if (handledRepositoryConnection.current === handledKey) return;
    handledRepositoryConnection.current = handledKey;
    if (operation.status === "success") {
      setMessage(t(repositoryConnectionMode === "connect" ? "已有仓库连接完成" : "已有仓库验证完成"));
    } else {
      setMessage(operation.errorSummary || t(operation.status === "cancelled" ? "操作已取消" : "已有仓库只读验证失败"));
    }
    setRepositoryConnectionMode("");
    void reloadRef.current();
  }, [repositoryConnection.operation, repositoryConnectionMode, locale]);
  useEffect(() => {
    if (!repositoryConnection.error || !repositoryConnectionMode) return;
    setMessage(repositoryConnection.error);
    setRepositoryConnectionMode("");
  }, [repositoryConnection.error, repositoryConnectionMode]);
  useEffect(() => {
    const operation = taskRun.operation;
    if (!operation || !runningTaskId || !["success", "partial", "failed", "cancelled", "cleanup_required"].includes(operation.status)) return;
    const handledKey = `${operation.id}:${operation.status}`;
    if (handledTaskRun.current === handledKey) return;
    handledTaskRun.current = handledKey;
    setMessage(operation.status === "success"
      ? translate(locale, "备份任务运行完成")
      : operation.errorSummary || translate(locale, operation.status === "cancelled" ? "操作已取消" : "备份任务运行失败"));
    setRunningTaskId("");
    runningTaskGuard.current = "";
    void reloadRef.current();
  }, [locale, runningTaskId, taskRun.operation]);
  useEffect(() => {
    if (!taskRun.error || !runningTaskId) return;
    setMessage(taskRun.error);
    setRunningTaskId("");
    runningTaskGuard.current = "";
  }, [runningTaskId, taskRun.error]);
  if (name === "快照与恢复") return <RestorePage api={api} locale={locale} />;
  if (name === "Agent 节点") return <AgentPage data={data} api={api} reload={reload} onNavigate={onNavigate} locale={locale} timeZone={timeZone} />;
  if (name === "运行记录") return <RunHistoryPage api={api} locale={locale} />;
  if (name === "备份任务" && taskHealthTarget) return <TaskHealthDetailPage taskId={taskHealthTarget} api={api} locale={locale} onBack={() => void onNavigate("备份任务")} />;
  if (name === "备份任务" && taskEditorTarget) {
    const task = taskEditorTarget === "create" ? null : data.find((item) => String(item.id ?? "") === taskEditorTarget) ?? null;
    if (taskEditorTarget !== "create" && !task) return <p className="field-hint" role="status">{t("正在读取…")}</p>;
    return <TaskEditor
      api={api}
      initial={task}
      onClose={() => { void onNavigate("备份任务"); }}
      onDraftSaved={reload}
      onSaved={async () => {
        await reload();
        await onNavigate("备份任务");
      }}
      locale={locale}
    />;
  }
  if (name === "通知配置") return <NotificationChannels api={api} locale={locale} />;
  if (name === "告警历史") return <HealthEvents api={api} locale={locale} timeZone={timeZone} view="alerts" onNavigate={(page) => onNavigate(page, "")} />;
  if (name === "投递记录") return <HealthEvents api={api} locale={locale} timeZone={timeZone} view="deliveries" />;
  if (name === "审计日志") return <AuditTable api={api} locale={locale} />;
  const creatable = [
    "远程主机",
    "备份仓库",
    "数据库实例",
    "备份任务",
  ].includes(name);
  async function submit(payload: Record<string, unknown>) {
    let created: unknown;
    const { connectionMode, ...repositoryPayload } = payload;
    if (name === "备份仓库" && !editing?.id && connectionMode === "existing") {
      handledRepositoryConnection.current = "";
      const accepted = await repositoryConnection.start("/api/repositories/connect", repositoryPayload);
      if (!accepted) throw new Error(t("无法启动已有仓库只读验证"));
      setRepositoryConnectionMode("connect");
      setDialog(false);
      setEditing(null);
      setMessage(t("正在只读验证已有仓库…"));
      return;
    }
    const persistedPayload = name === "备份仓库" ? repositoryPayload : payload;
    if (editing?.id)
      await api.updateResource(pageResource(name), String(editing.id), persistedPayload);
    else created = await api.createResource(pageResource(name), persistedPayload);
    setDialog(false);
    setEditing(null);
    const publicKey = name === "远程主机" ? (created as Record<string, unknown> | undefined)?.publicKey : undefined;
    if (typeof publicKey === "string" && publicKey) {
      setGeneratedSSHAccess({ publicKey, name: String(payload.name ?? "远程服务器"), username: String(payload.username ?? "SSH 用户"), host: String(payload.host ?? "") });
      setMessage("远程主机已保存。请将 SSH 公钥授权给服务器。 ");
    } else {
      setMessage("已保存");
    }
    await reload();
  }
  const generatedAuthorizationCommand = generatedSSHAccess ? sshAuthorizationCommand(generatedSSHAccess.publicKey) : "";
  async function beginCreate() {
    setMessage("");
    if (name === "备份任务") {
      await onNavigate("备份任务", "?view=create");
      return;
    }
    setEditing(null);
    setDialog(true);
  }
  async function beginDelete(item: Record<string, unknown>) {
    setMessage("");
    const resource = pageResource(name);
    try {
      const preview = await api.action(`/api/delete-previews/${resource}/${encodeURIComponent(String(item.id))}`) as Record<string, unknown>;
      setPendingDelete(preview);
    } catch (reason) {
      setMessage(reason instanceof Error ? reason.message : "无法读取删除影响，请刷新后重试");
    }
  }
  function initializeRepository(id: string) {
    handledInitialization.current = "";
    setInitializingRepositoryId(id);
    setInitializationPhase("running");
    setMessage(t("正在初始化仓库…"));
    void initialization.start(`/api/repositories/${encodeURIComponent(id)}/initialize`, {});
  }
  async function verifyExistingRepository(id: string) {
    handledRepositoryConnection.current = "";
    const accepted = await repositoryConnection.start(`/api/repositories/${encodeURIComponent(id)}/verify-existing`, {});
    if (!accepted) {
      setMessage(t("无法启动已有仓库只读验证"));
      return;
    }
    setRepositoryConnectionMode("verify");
    setMessage(t("正在只读验证已有仓库…"));
  }
  function runTask(id: string) {
	if (runningTaskGuard.current || taskRun.active) return;
	runningTaskGuard.current = id;
    handledTaskRun.current = "";
    setRunningTaskId(id);
    setMessage(t("备份任务已开始"));
    void taskRun.start(`/api/tasks/${encodeURIComponent(id)}/run`, {});
  }
  async function confirmDelete() {
    if (!pendingDelete) return;
    const preview = pendingDelete;
    const id = String(preview.id);
    setDeleting(true);
    try {
      await api.action(`/api/delete-previews/${pageResource(name)}/${encodeURIComponent(id)}/confirm`, { expectedUpdatedAt: String(preview.updatedAt ?? "") });
      setPendingDelete(null);
      const label = String(preview.name ?? id);
      setMessage(locale === "en-US" ? `${t(name)} deleted: ${label}` : `已删除${name}：${label}`);
      await reload();
    } catch (cause) {
      setMessage(cause instanceof Error ? cause.message : t("删除失败"));
    } finally {
      setDeleting(false);
    }
  }
  if (dialog && name === "备份仓库") {
    return <RepositoryEditor api={api} initial={editing} locale={locale} onClose={() => { setDialog(false); setEditing(null); }} onSubmit={submit} />;
  }
  return (
    <>
      <header className="page-header">
        <div>
          <h1>{t(name)}</h1>
          <p>{t(pageDescription(name))}</p>
        </div>
        {creatable && (
          <button
            className="primary-button"
            type="button"
            onClick={() => void beginCreate().catch(() => setMessage("无法检查前置资源"))}
          >
            {t(`新建${name}`)}
          </button>
        )}
      </header>
      <Toast message={message} locale={locale} onClose={() => setMessage("")} />
      {name === "备份仓库" && <OperationFeedback operation={repositoryConnection} locale={locale} hideTerminal />}
      <section className="content-section">
        <div className="table-frame">
          <table>
            <thead>
              <tr>
                {columns(name).map((column) => (
                  <th key={column.key}>{t(column.label)}</th>
                ))}
                <th>{t("操作")}</th>
              </tr>
            </thead>
            <tbody>
              {data.map((item, index) => (
                <tr key={String(item.id ?? index)}>
                  {columns(name).map((column) => (
                    <td
                      key={column.key}
                      className={column.key === "name" ? "strong-cell" : ""}
                    >
                      {column.key === "id" ? (
                        <span className="identifier-cell">
                          <code>{display(item.id)}</code>{" "}
                          <button
                            className="text-button"
                            type="button"
                            aria-label={`复制 ID ${String(item.id)}`}
                            onClick={() =>
                              void copyToClipboard(String(item.id))
                                .then(() => setMessage(`已复制 ID：${String(item.id)}`))
                                .catch(() => setMessage("复制失败，请手动选择 ID"))
                            }
                          >
                            {t("复制")}
                          </button>
                        </span>
                      ) : column.key === "preflight" ? (
                        databasePreflightDisplay(item.preflight, locale)
                      ) : column.key === "capacity" ? (
                        <RepositoryCapacityCell
                          value={item.capacity}
                          enabled={item.status === "ready"}
                          unsupported={item.kind === "s3"}
                          repositoryId={String(item.id ?? "")}
                          api={api}
                          locale={locale}
                          onUpdated={reload}
                        />
                      ) : column.key === "lastRun" ? (
                        name === "备份仓库" && String(item.id ?? "") === initializingRepositoryId && initializationPhase !== "idle"
                          ? <RepositoryOperationState phase={initializationPhase} locale={locale} />
                          : repositoryRunDisplay(item.lastRun, locale)
                      ) : (
                        resourceValue(name, column.key, item, locale)
                      )}
                    </td>
                  ))}
                  <td>
                    <RowActions
                      name={name}
                      item={item}
                      api={api}
                      reload={reload}
                      onRotate={() => setRotateRepository(String(item.id))}
                      onMessage={setMessage}
                      onEdit={() => {
                        if (name === "备份任务") {
                          void onNavigate("备份任务", `?view=edit&task=${encodeURIComponent(String(item.id ?? ""))}`);
                          return;
                        }
                        setEditing(item);
                        setDialog(true);
                      }}
                      onDelete={() => void beginDelete(item)}
                      initializationBusy={initialization.active || initializationPhase === "success"}
                      onInitialize={() => initializeRepository(String(item.id ?? ""))}
                      repositoryConnectionBusy={repositoryConnection.active}
                      onVerifyExisting={() => void verifyExistingRepository(String(item.id ?? ""))}
                      taskRunBusy={taskRun.active || Boolean(runningTaskId)}
                      taskRunActive={String(item.id ?? "") === runningTaskId}
                      onRun={() => runTask(String(item.id ?? ""))}
                      onOpenTaskHealth={() => void onNavigate("备份任务", `?task=${encodeURIComponent(String(item.id ?? ""))}&view=health`)}
                      locale={locale}
                    />
                  </td>
                </tr>
              ))}
              {!data.length && (
                <tr>
                  <td className="empty-row" colSpan={columns(name).length + 1}>
                    {t("尚无记录")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
      {dialog && name !== "备份仓库" && (
        <ResourceDialog
          name={name}
          initial={editing}
          api={api}
          locale={locale}
          onClose={() => {
            setDialog(false);
            setEditing(null);
          }}
          onSubmit={submit}
        />
      )}
      {rotateRepository && (
        <RotatePasswordDialog
          repositoryId={rotateRepository}
          api={api}
          locale={locale}
          onClose={() => setRotateRepository("")}
        />
      )}
      {generatedSSHAccess && (
        <ModalPortal>
          <section ref={generatedSSHAccessRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="generated-ssh-public-key-title">
            <header>
              <div>
                <h2 id="generated-ssh-public-key-title">{t("将 SSH 公钥授权到服务器")}</h2>
                <p>{locale === "en-US" ? `The private key is encrypted in the local vault and is never displayed or exported. Complete these steps before the application can connect as ${generatedSSHAccess.username}@${generatedSSHAccess.host}.` : `私钥已加密保存在本机秘密库中，不会显示或导出。完成以下步骤后，应用才能以 ${generatedSSHAccess.username}@${generatedSSHAccess.host} 连接。`}</p>
              </div>
            </header>
            <div className="form-grid">
              <label className="full-field">{t("SSH 公钥")}<textarea aria-label={t("SSH 公钥")} value={generatedSSHAccess.publicKey} readOnly /></label>
              <div className="full-field ssh-key-instructions">
                <strong>{t("操作步骤")}</strong>
                <ol>
                  <li>{locale === "en-US" ? `Run the following command as SSH user ${generatedSSHAccess.username} in the ${generatedSSHAccess.name} console or terminal.` : `在${generatedSSHAccess.name} 的控制台或终端中，以 SSH 用户 ${generatedSSHAccess.username} 执行以下命令。`}</li>
                  <li>{locale === "en-US" ? <>If signed in as another administrator, run <code>sudo -iu {generatedSSHAccess.username}</code> first.</> : <>如果当前登录的是其他管理员账户，先执行 <code>sudo -iu {generatedSSHAccess.username}</code> 切换用户。</>}</li>
                  <li>{locale === "en-US" ? <>The command writes the public key to <code>~/.ssh/authorized_keys</code>; the private key remains in this application.</> : <>执行后，公钥会写入 <code>~/.ssh/authorized_keys</code>；私钥始终留在本机应用中。</>}</li>
                  <li>{t("返回应用创建并初始化备份仓库，应用会验证连接和目录权限。")}</li>
                </ol>
                <label>{t("服务器授权命令")}<textarea aria-label={t("服务器授权命令")} value={generatedAuthorizationCommand} readOnly /></label>
              </div>
            </div>
            <footer>
              <button className="secondary-button" type="button" onClick={() => void copyToClipboard(generatedSSHAccess.publicKey).then(() => setMessage(t("SSH 公钥已复制"))).catch(() => setMessage(t("复制失败，请手动选择公钥")))}>{t("复制 SSH 公钥")}</button>
              <button className="secondary-button" type="button" onClick={() => void copyToClipboard(generatedAuthorizationCommand).then(() => setMessage(t("服务器授权命令已复制"))).catch(() => setMessage(t("复制失败，请手动选择命令")))}>{t("复制授权命令")}</button>
              <button className="primary-button" type="button" onClick={() => setGeneratedSSHAccess(null)}>{t("完成")}</button>
            </footer>
          </section>
        </ModalPortal>
      )}
      {pendingDelete && (
        <DeleteResourceDialog
          name={name}
          preview={pendingDelete}
          deleting={deleting}
          locale={locale}
          onClose={() => setPendingDelete(null)}
          onConfirm={() => void confirmDelete()}
        />
      )}
    </>
  );
}
function MaintenancePage({ api, onNavigate }: { api: AppAPI; onNavigate(page: string): Promise<void> }) {
  const [message, setMessage] = useState("");
  const [repositories, setRepositories] = useState<Array<Record<string, unknown>>>([]);
  const [repositoryId, setRepositoryId] = useState("");
  const [policy, setPolicy] = useState<Record<string, any>>({});
  const [preview, setPreview] = useState<Record<string, any> | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const maintenance = useOperation(api);
  const handledMaintenance = useRef("");
  useEffect(() => {
    let active = true;
    void api
      .listResource("repositories")
      .then((items) => {
        if (active) {
          const ready = items.filter((item) => item.status === "ready");
          setRepositories(ready);
          setRepositoryId((current) => current || String(ready[0]?.id ?? ""));
        }
      })
      .catch(() => active && setMessage("无法读取仓库列表"));
    return () => {
      active = false;
    };
  }, [api]);
  useEffect(() => {
    if (!repositoryId) return;
    let active = true;
    setPreview(null);
    void api.action(`/api/repositories/${repositoryId}/maintenance-policy`).then((value) => {
      if (active) setPolicy((value ?? {}) as Record<string, any>);
    }).catch(() => active && setMessage("无法读取现有维护计划"));
    return () => { active = false; };
  }, [api, repositoryId]);
  useEffect(() => {
    const operation = maintenance.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledMaintenance.current === key) return;
    handledMaintenance.current = key;
    setMessage(operation.status === "success" ? "仓库维护完成" : operation.errorSummary || (operation.status === "cancelled" ? "操作已取消" : "仓库维护失败"));
  }, [maintenance.operation]);
  useEffect(() => {
    if (maintenance.error) setMessage(maintenance.error);
  }, [maintenance.error]);

  const values = (form: HTMLFormElement) => {
    const data = new FormData(form);
    return {
      schedule: { kind: "weekly", dayOfWeek: Number(data.get("dayOfWeek")), timeOfDay: String(data.get("timeOfDay")) },
      timezone: String(data.get("timezone")),
      retention: { keepWithinDays: Number(data.get("keepWithinDays")), keepLast: Number(data.get("keepLast")) },
      enabled: data.get("enabled") === "on",
    };
  };
  return (
    <>
      <header className="page-header">
        <div>
          <div className="title-with-help">
            <h1>仓库维护</h1>
            <HelpTip
              label="仓库维护影响说明"
              text="维护会执行 forget、prune 和完整性检查。prune 会回收不再被保留快照引用的空间，期间同一仓库的备份与恢复会等待。"
            />
          </div>
          <p>维护与备份分离执行；按周期运行 forget、prune 与完整性检查。</p>
        </div>
      </header>
      <section className="content-section">
        <form
          className="form-grid"
          key={`${repositoryId}-${String(policy.updatedAt ?? "new")}`}
          onChange={() => setPreview(null)}
          onSubmit={(event) => {
            event.preventDefault();
            if (!repositoryId) {
              setMessage("请先创建并初始化仓库");
              return;
            }
            if (!preview?.previewId) {
              setMessage("请先生成与当前设置一致的 dry-run 预览");
              return;
            }
            void api.saveMaintenance(repositoryId, { ...values(event.currentTarget), previewId: preview.previewId })
              .then(() => { setMessage("维护计划已保存"); setPreview(null); })
              .catch((reason) => setMessage(reason instanceof Error ? reason.message : "维护计划保存失败"));
          }}
        >
          <label className="full-field">
            仓库
            <select name="repositoryId" required disabled={!repositories.length} value={repositoryId} onChange={(event) => setRepositoryId(event.target.value)}>
              {!repositories.length && <option value="">暂无已初始化仓库</option>}
              {repositories.map((repository) => (
                <option key={String(repository.id)} value={String(repository.id)}>
                  {String(repository.name)} · {repository.kind === "local" ? "本地" : "远程"} · {String(repository.path)}
                </option>
              ))}
            </select>
            {!repositories.length && <span className="field-hint warning-text">请先创建仓库并完成初始化</span>}
          </label>
          {policy.boundTask && <div className="full-field operation-feedback">
            <strong>保留策略由绑定任务“{String(policy.boundTask.name)}”统一管理</strong>
            <p>任务 ID：<code>{String(policy.boundTask.id)}</code></p>
            <button className="text-button" type="button" onClick={() => void onNavigate("备份任务")}>前往编辑绑定任务</button>
          </div>}
          <p className="full-field field-hint">下次执行：{policy.enabled && policy.nextRun ? display(policy.nextRun) : "维护计划已停用"}</p>
          <label>
            时区
            <input name="timezone" defaultValue={String(policy.timezone ?? "Asia/Shanghai")} required />
          </label>
          <label>
            星期
            <select name="dayOfWeek" defaultValue={String(policy.schedule?.dayOfWeek ?? 0)}>
              <option value="0">星期日</option>
              <option value="1">星期一</option>
              <option value="2">星期二</option>
              <option value="3">星期三</option>
              <option value="4">星期四</option>
              <option value="5">星期五</option>
              <option value="6">星期六</option>
            </select>
          </label>
          <label>
            执行时间
            <input name="timeOfDay" type="time" defaultValue={String(policy.schedule?.timeOfDay ?? "03:00")} required />
          </label>
          <label>
            保留窗口（天）
            <input
              name="keepWithinDays"
              type="number"
              min="0"
              defaultValue={Number(policy.retention?.keepWithinDays ?? 30)}
            />
          </label>
          <label>
            至少保留最近快照数
            <input name="keepLast" type="number" min="0" defaultValue={Number(policy.retention?.keepLast ?? 3)} />
          </label>
          <label className="full-field checkbox-field">
            <input name="enabled" type="checkbox" defaultChecked={Boolean(policy.enabled)} />
            启用定时维护
          </label>
          <button className="secondary-button" type="button" disabled={!repositories.length || previewing} onClick={(event) => {
            const form = event.currentTarget.form;
            if (!form || !repositoryId) return;
            setPreviewing(true);
            setMessage("");
            void api.action(`/api/repositories/${repositoryId}/maintenance`, { retention: values(form).retention, dryRun: true })
              .then((value) => setPreview(value as Record<string, any>))
              .catch((reason) => setMessage(reason instanceof Error ? reason.message : "维护预览失败"))
              .finally(() => setPreviewing(false));
          }}>
            {previewing ? "正在预览…" : "生成 dry-run 预览"}
          </button>
          <button className="primary-button form-action" type="submit" disabled={!repositories.length || !preview?.previewId}>
            保存维护周期
          </button>
          {preview && <div className="full-field operation-feedback" role="status">
            <strong>预览结果：保留 {String(preview.keepCount ?? 0)} 个快照，移除 {String(preview.removeCount ?? 0)} 个快照</strong>
            <p>预览有效至 {display(preview.expiresAt)}；修改任何设置后必须重新预览。</p>
            <button className="danger-button" type="button" disabled={maintenance.active} onClick={() => void maintenance.start(`/api/repositories/${repositoryId}/maintenance`, { previewId: preview.previewId, confirmed: true })}>确认并立即维护</button>
          </div>}
          <OperationFeedback operation={maintenance} hideTerminal />
          <Toast message={message} onClose={() => setMessage("")} />
        </form>
      </section>
    </>
  );
}
function AuditTable({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [action, setAction] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [items, setItems] = useState<Array<Record<string, unknown>>>([]);
  const [actions, setActions] = useState<string[]>([]);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState("");
  const query = new URLSearchParams();
  if (action) query.set("action", action);
  if (from) query.set("from", new Date(from).toISOString());
  if (to) query.set("to", new Date(to).toISOString());
  const filterQuery = query.toString();
  useEffect(() => {
    let active = true;
    const pageQuery = new URLSearchParams(filterQuery);
    pageQuery.set("page", String(page));
    pageQuery.set("pageSize", String(pageSize));
    setLoading(true); setLoadError("");
    void api.action(`/api/audits?${pageQuery}`).then((value) => {
      if (!active) return;
      const result = value as { items?: Array<Record<string, unknown>>; total?: number };
      const next = Array.isArray(result.items) ? result.items : [];
      setItems(next); setTotal(Number(result.total ?? 0));
      setActions((current) => [...new Set([...current, ...next.map((item) => String(item.action ?? ""))])].filter(Boolean).sort());
    }).catch((reason) => active && setLoadError(reason instanceof Error ? reason.message : "无法读取审计记录")).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [api, filterQuery, page, pageSize]);
  const exportURL = `/api/audits/export${query.size ? `?${query}` : ""}`;
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  return (
    <section className="content-section">
      <div className="section-heading">
        <h2>{t("审计记录")}</h2>
        <a className="secondary-button" href={exportURL}>{t("导出当前筛选 CSV")}</a>
      </div>
      <div className="filter-row">
        <label>{t("动作筛选")}<select value={action} onChange={(event) => { setAction(event.target.value); setPage(1); }}><option value="">{t("全部动作")}</option>{actions.map((value) => <option key={value} value={value}>{value}</option>)}</select></label>
        <label>{t("开始时间")}<input type="datetime-local" value={from} onChange={(event) => { setFrom(event.target.value); setPage(1); }} /></label>
        <label>{t("结束时间")}<input type="datetime-local" value={to} onChange={(event) => { setTo(event.target.value); setPage(1); }} /></label>
      </div>
      <div className="table-frame">
        <table>
          <thead>
            <tr>
              <th>{t("时间")}</th>
              <th>{t("操作者")}</th>
              <th>{t("动作")}</th>
              <th>{t("对象")}</th>
              <th>{t("标识")}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((item, index) => (
              <tr key={String(item.id ?? index)}>
                <td>{adminTime(item.occurredAt, locale)}</td>
                <td>{display(item.actor)}</td>
                <td>{display(item.action)}</td>
                <td>{display(item.targetType)}</td>
                <td>{display(item.targetId)}</td>
              </tr>
            ))}
            {!items.length && (
              <tr>
                <td className="empty-row" colSpan={5}>
                  {loading ? t("正在读取审计记录…") : loadError || t("尚无审计记录")}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="pagination" aria-label={t("审计记录分页")}>
        <span>{locale === "en-US" ? `${total} total · Page ${page}/${pageCount}` : `共 ${total} 条 · 第 ${page}/${pageCount} 页`}</span>
        <label>{t("每页")}<select aria-label={t("每页数量")} value={pageSize} onChange={(event) => { setPageSize(Number(event.target.value)); setPage(1); }}><option value="10">10</option><option value="25">25</option><option value="50">50</option></select></label>
        <button className="secondary-button" type="button" disabled={page <= 1 || loading} onClick={() => setPage((value) => value - 1)}>{t("上一页")}</button>
        <button className="secondary-button" type="button" disabled={page >= pageCount || loading} onClick={() => setPage((value) => value + 1)}>{t("下一页")}</button>
      </div>
    </section>
  );
}

function shortIdentifier(value: string): string {
  return value.length > 12 ? `${value.slice(0, 12)}…` : value;
}

function adminTime(value: unknown, locale: Locale = "zh-CN", timeZone?: string) {
  if (!value) return "—";
  const date = new Date(String(value));
  if (Number.isNaN(date.getTime())) return display(value, locale);
  const exactTime = timestampAtSecond(date);
  return <time dateTime={exactTime} title={exactTime}>{new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium", timeZone }).format(date)}</time>;
}

function restoreAgentCandidates(items: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
  return items.filter((item) => item.status === "online"
    && (item.capabilities as unknown[] | undefined)?.includes("restic-restore")
    && (item.capabilities as unknown[] | undefined)?.includes("filesystem-restore-target"));
}

function cacheSnapshotContentsPage(current: Record<string, SnapshotContentsPage>, key: string, page: SnapshotContentsPage): Record<string, SnapshotContentsPage> {
  const retained = Object.entries(current).filter(([entryKey]) => entryKey !== key).slice(-4);
  return Object.fromEntries([...retained, [key, page]]);
}

function RestorePage({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [repositories, setRepositories] = useState<Array<Record<string, unknown>>>([]);
  const [connections, setConnections] = useState<Array<Record<string, unknown>>>([]);
  const [agents, setAgents] = useState<Array<Record<string, unknown>>>([]);
  const [snapshotContentsCache, setSnapshotContentsCache] = useState<Record<string, SnapshotContentsPage>>({});
  const [snapshots, setSnapshots] = useState<Array<Record<string, unknown>>>([]);
  const [repo, setRepo] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [restoreKind, setRestoreKind] = useState<"directory" | "database">("directory");
  const [view, setView] = useState<"restore" | "browse" | "diff">("restore");
  const [preflightMessage, setPreflightMessage] = useState("");
  const [dirSnapshot, setDirSnapshot] = useState("");
  const [selectedIncludes, setSelectedIncludes] = useState<string[]>([]);
  const [dirTarget, setDirTarget] = useState("");
  const [targetKind, setTargetKind] = useState<"local" | "agent">("local");
  const [agentID, setAgentID] = useState("");
  const [dbSnapshot, setDbSnapshot] = useState("");
  const [connection, setConnection] = useState("");
  const [connectionMode, setConnectionMode] = useState<"saved" | "temporary">("saved");
  const [saveTemporaryConnection, setSaveTemporaryConnection] = useState(false);
  const [temporaryName, setTemporaryName] = useState("");
  const [temporaryEngine, setTemporaryEngine] = useState("mysql");
  const [temporaryHost, setTemporaryHost] = useState("");
  const [temporaryPort, setTemporaryPort] = useState(3306);
  const [temporaryUsername, setTemporaryUsername] = useState("");
  const [temporaryPassword, setTemporaryPassword] = useState("");
  const [temporaryRestoreProgram, setTemporaryRestoreProgram] = useState("/usr/bin/mysql");
  const [temporaryAdminProgram, setTemporaryAdminProgram] = useState("/usr/bin/mysql");
  const [database, setDatabase] = useState("");
  const [preparedDatabaseConnection, setPreparedDatabaseConnection] = useState("");
  const [confirmation, setConfirmation] = useState<Record<string, any> | null>(null);
  const [confirmationKind, setConfirmationKind] = useState<"directory" | "database" | "">("");
  const [password, setPassword] = useState("");
  const operation = useOperation(api);
  const handledRestore = useRef("");
  const agentRequestVersion = useRef(0);
  const invalidate = () => { setConfirmation(null); setConfirmationKind(""); setPassword(""); setPreparedDatabaseConnection(""); };
  useEffect(() => {
    let active = true;
    const agentVersion = ++agentRequestVersion.current;
    void Promise.all([api.listResource("repositories"), api.listResource("database-connections"), api.listResource("agents")]).then(([repoItems, connectionItems, agentItems]) => {
      if (!active) return;
      const ready = repoItems.filter((item) => item.status === "ready" && item.engine !== "rsync");
      const restoreConnections = connectionItems.filter((item) => item.purpose === "restore");
      const restoreAgents = restoreAgentCandidates(agentItems);
      setRepositories(ready);
      setConnections(restoreConnections);
      setRepo(String(ready[0]?.id ?? ""));
      setConnection(String(restoreConnections[0]?.id ?? ""));
      setConnectionMode(restoreConnections.length ? "saved" : "temporary");
      if (agentRequestVersion.current === agentVersion) {
        setAgents(restoreAgents);
        setAgentID(String(restoreAgents[0]?.id ?? ""));
      }
    }).catch(() => active && setError("无法读取恢复资源"));
    return () => { active = false; };
  }, [api]);
  useEffect(() => {
    if (view !== "restore") return;
    let active = true;
    const refreshAgents = () => {
      const version = ++agentRequestVersion.current;
      void api.listResource("agents").then((items) => {
        if (!active || agentRequestVersion.current !== version) return;
        const candidates = restoreAgentCandidates(items);
        setAgents(candidates);
        setAgentID((current) => candidates.some((item) => String(item.id) === current) ? current : String(candidates[0]?.id ?? ""));
      }).catch(() => undefined);
    };
    refreshAgents();
    const timer = window.setInterval(refreshAgents, 30_000);
    window.addEventListener("focus", refreshAgents);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", refreshAgents);
    };
  }, [api, view]);
  useEffect(() => {
    setSnapshots([]); setDirSnapshot(""); setDbSnapshot(""); setSelectedIncludes([]); invalidate();
    if (!repo) return;
    let active = true;
    setLoading(true); setError("");
    void api.action(`/api/repositories/${repo}/snapshots`).then((value) => {
      if (!active) return;
      const items = value as Array<Record<string, unknown>>;
      const detected = snapshotIsDatabase(items[0]) ? "database" : "directory";
      setSnapshots(items);
      setRestoreKind(detected);
      setTargetKind("local");
      setDirTarget("");
    }).catch(() => active && setError("快照读取失败")).finally(() => active && setLoading(false));
    return () => { active = false; };
  }, [api, repo]);
  useEffect(() => {
    const record = operation.operation;
    if (!record || !["success", "failed", "cancelled"].includes(record.status)) return;
    const key = `${record.id}:${record.status}`;
    if (handledRestore.current === key) return;
    handledRestore.current = key;
    setPreflightMessage(record.status === "success"
      ? t("恢复完成")
      : record.errorSummary || t(record.status === "cancelled" ? "操作已取消" : "恢复失败"));
  }, [locale, operation.operation]);
  useEffect(() => {
    if (operation.error) setPreflightMessage(operation.error);
  }, [operation.error]);
  const databaseSnapshots = snapshots.filter((item) => JSON.stringify(item.tags ?? []).includes("rc:source=database"));
  const directorySnapshots = snapshots.filter((item) => !databaseSnapshots.includes(item));
  const authorizeAndStart = async (kind: "directory" | "database", payload: Record<string, unknown>) => {
    if (!confirmation?.confirmationId || confirmationKind !== kind) return;
    try {
      await api.action(`/api/restores/${String(confirmation.confirmationId)}/authorize`, { password });
      await operation.start(`/api/repositories/${repo}/restore-${kind}`, { ...payload, confirmationId: confirmation.confirmationId });
      setPassword(""); setConfirmation(null); setConfirmationKind("");
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "恢复确认失败");
    }
  };
  const directorySnapshot = directorySnapshots.find((item) => String(item.id) === dirSnapshot);
  const selectedRepository = repositories.find((item) => String(item.id) === repo);
  const agentRepositoryAccessible = ["sftp", "s3"].includes(String(selectedRepository?.kind ?? ""));
  const agentRestoreUnavailableReason = !agentRepositoryAccessible
    ? t("当前备份仓库位于控制服务本机，远程 Agent 无法直接访问；请选择 SFTP 或 S3 仓库。")
    : !agents.length
      ? t("没有在线且具备目录恢复能力的 Agent。")
      : "";
  const directorySourcePath = String((directorySnapshot?.paths as unknown[] | undefined)?.[0] ?? "");
  const snapshotContentsCacheKey = JSON.stringify([repo, dirSnapshot, directorySourcePath]);
  const directoryPayload = { snapshotId: dirSnapshot, target: dirTarget, includes: selectedIncludes, targetKind, agentId: targetKind === "agent" ? agentID : "" };
  if (view === "browse" && dirSnapshot) return <>
    <header className="page-header"><div><button className="text-button restore-back" type="button" onClick={() => setView("restore")}>← {t("返回恢复设置")}</button><h1>{t("浏览快照内容")}</h1><p>{t("进入文件夹并选择要恢复的目录或文件。")}</p></div></header>
    <section className="content-section snapshot-secondary-page"><SnapshotBrowser api={api} repositoryID={repo} snapshotID={dirSnapshot} sourcePath={directorySourcePath} snapshots={directorySnapshots.map((item) => ({ id: String(item.id), time: String(item.time ?? ""), paths: (item.paths as string[] | undefined) ?? [] }))} cachedPage={snapshotContentsCache[snapshotContentsCacheKey]} onPageChange={(page) => setSnapshotContentsCache((current) => cacheSnapshotContentsPage(current, snapshotContentsCacheKey, page))} selectedIncludes={selectedIncludes} onSelectedIncludesChange={(value) => { setSelectedIncludes(value); invalidate(); }} locale={locale} /></section>
  </>;
  if (view === "diff" && dirSnapshot) return <>
    <header className="page-header"><div><button className="text-button restore-back" type="button" onClick={() => setView("restore")}>← {t("返回恢复设置")}</button><h1>{t("快照差异")}</h1><p>{t("将当前快照与较早快照比较；结果只用于核对，不会改变恢复选择。")}</p></div></header>
    <section className="content-section snapshot-secondary-page"><SnapshotDiffPanel api={api} repositoryID={repo} snapshotID={dirSnapshot} sourcePath={directorySourcePath} snapshots={directorySnapshots.map((item) => ({ id: String(item.id), time: String(item.time ?? ""), paths: (item.paths as string[] | undefined) ?? [] }))} locale={locale} /></section>
  </>;
  return (
    <>
      <header className="page-header restore-page-header">
        <div>
          <h1>{t("从快照恢复")}</h1>
          <p>{t("选择仓库和快照；只读预检和管理员复验通过后才会开始恢复。")}</p>
        </div>
      </header>
      <div className="restore-workbench">
      <section className="restore-step-card restore-source-step">
        <div className="restore-step-heading"><span aria-hidden="true">1</span><div><h2>{t("选择恢复点")}</h2><p>{t("先选择备份仓库，再选择要恢复的快照。")}</p></div></div>
        <div className="form-grid restore-workflow-form">
          <label className="full-field">
            {t("备份仓库")}
            <select value={repo} onChange={(event) => setRepo(event.target.value)} disabled={!repositories.length}>
              {!repositories.length && <option value="">{t("暂无可恢复的 Restic 仓库")}</option>}
              {repositories.map((item) => <option key={String(item.id)} value={String(item.id)}>{String(item.name)} · {t(item.kind === "local" ? "本地" : "远程")} · {String(item.path)}</option>)}
            </select>
          </label>
          {!loading && restoreKind === "directory" && <label className="full-field">
            {t("目录快照")}
            <select value={dirSnapshot} onChange={(event) => { setDirSnapshot(event.target.value); setSelectedIncludes([]); invalidate(); }} required>
              <option value="">{t("请选择目录快照")}</option>
              {directorySnapshots.map((item) => <option key={String(item.id)} value={String(item.id)}>{snapshotOptionLabel(item.id, item.time)}</option>)}
            </select>
          </label>}
          {!loading && restoreKind === "database" && <label className="full-field">
            {t("数据库快照")}
            <select value={dbSnapshot} onChange={(event) => { setDbSnapshot(event.target.value); invalidate(); }} required>
              <option value="">{t("请选择数据库快照")}</option>
              {databaseSnapshots.map((item) => <option key={String(item.id)} value={String(item.id)}>{snapshotOptionLabel(item.id, item.time)}</option>)}
            </select>
          </label>}
          {!loading && restoreKind === "directory" && dirSnapshot && <div className="full-field snapshot-tools">
            <button className="secondary-button" type="button" onClick={() => setView("browse")}>{t("浏览并选择快照内容")}</button>
            <button className="secondary-button" type="button" onClick={() => setView("diff")}>{t("查看快照差异")}</button>
            <span>{selectedIncludes.length ? locale === "en-US" ? `${selectedIncludes.length} items selected` : `已选择 ${selectedIncludes.length} 项` : t("未选择项目，将恢复整个目录")}</span>
          </div>}
        </div>
      </section>
      {loading && <section className="restore-step-card restore-snapshot-loading" role="status" aria-live="polite" aria-label={t("正在读取…")}>
        <span className="restore-loading-spinner" aria-hidden="true" />
        <span>{t("正在读取…")}</span>
      </section>}
      {!loading && restoreKind === "directory" && <section className="restore-step-card restore-target-step">
        <div className="restore-step-heading"><span aria-hidden="true">2</span><div><h2>{t("恢复目录到新位置")}</h2><p>{t("恢复只会写入你选择的新位置。")}</p></div></div>
        <form
          className="form-grid restore-workflow-form"
          onSubmit={(e) => {
            e.preventDefault();
            void api.action(`/api/repositories/${repo}/restore-directory/preflight`, directoryPayload)
              .then((value) => { setConfirmation(value as Record<string, any>); setConfirmationKind("directory"); setError(""); setPreflightMessage(t("目录恢复预检通过，请继续确认恢复信息。")); })
              .catch((reason) => { const message = reason instanceof Error ? reason.message : t("目录恢复预检失败"); setError(message); setPreflightMessage(message); });
          }}
        >
          <fieldset className="full-field">
            <legend>{t("恢复位置")}</legend>
            <div className="segmented-control" role="radiogroup" aria-label={t("恢复位置")}>
              <label className={targetKind === "local" ? "selected" : ""}><input type="radio" name="restore-target-kind" checked={targetKind === "local"} onChange={() => { setTargetKind("local"); setDirTarget(""); invalidate(); }} />{t("控制服务本机")}</label>
              <label className={targetKind === "agent" ? "selected" : ""}><input type="radio" name="restore-target-kind" checked={targetKind === "agent"} disabled={Boolean(agentRestoreUnavailableReason)} aria-describedby={agentRestoreUnavailableReason ? "agent-restore-unavailable-reason" : undefined} onChange={() => { setTargetKind("agent"); setDirTarget(""); invalidate(); }} />{t("远程 Agent")}</label>
            </div>
            {agentRestoreUnavailableReason && <p className="field-hint" id="agent-restore-unavailable-reason">{agentRestoreUnavailableReason}</p>}
          </fieldset>
          {targetKind === "local" ? <label className="full-field">{t("新目标绝对路径")}<input value={dirTarget} onChange={(event) => { setDirTarget(event.target.value); invalidate(); }} required /></label> : <>
            <label className="full-field">{t("目标 Agent")}<select value={agentID} onChange={(event) => { setAgentID(event.target.value); setDirTarget(""); invalidate(); }} required><option value="">{t("请选择 Agent")}</option>{agents.map((item) => <option key={String(item.id)} value={String(item.id)}>{String(item.id)} · {String(item.platform ?? item.os ?? item.platformOs ?? "")}</option>)}</select></label>
            <AgentRestoreTargetPicker api={api} agent={agents.find((item) => String(item.id) === agentID)} onChange={(value) => { setDirTarget(value); invalidate(); }} locale={locale} />
          </>}
          <div className="full-field restore-preflight-bar"><span className="restore-step-number" aria-hidden="true">3</span><div><strong>{t("只读预检")}</strong><small>{t("预检不会写入恢复目标。")}</small></div><button className="primary-button" type="submit" disabled={!repo || !dirSnapshot || !dirTarget || operation.active}>{t("执行只读预检")}</button></div>
          {confirmationKind === "directory" && <RestoreConfirmation confirmation={confirmation} password={password} setPassword={setPassword} label={t("确认并开始目录恢复")} locale={locale} onClose={() => { setConfirmation(null); setConfirmationKind(""); setPassword(""); }} onConfirm={() => void authorizeAndStart("directory", directoryPayload)} />}
        </form>
      </section>}
      {!loading && restoreKind === "database" && <section className="restore-step-card restore-target-step">
        <div className="restore-step-heading"><span aria-hidden="true">2</span><div><h2>{t("恢复数据库到新库或空库")}</h2><p>{t("目标必须是新建数据库或已确认的空数据库。")}</p></div></div>
        <form
          className="form-grid restore-workflow-form"
          onSubmit={async (e) => {
            e.preventDefault();
            try {
              let selectedConnection = connection;
              if (connectionMode === "temporary") {
                const payload = { name: temporaryName, engine: temporaryEngine, purpose: "restore", network: "tcp", host: temporaryHost, port: temporaryPort, socketPath: "", username: temporaryUsername, password: temporaryPassword, tls: { mode: "preferred" }, toolPaths: { restore: temporaryRestoreProgram, admin: temporaryAdminProgram, create: temporaryAdminProgram } };
                const created = await api.action(saveTemporaryConnection ? "/api/database-connections" : "/api/database-connections/temporary", payload) as Record<string, unknown>;
                selectedConnection = String(created.id ?? "");
                if (!selectedConnection) throw new Error("数据库连接创建后未返回 ID");
                setTemporaryPassword("");
                if (saveTemporaryConnection) {
                  setConnections((current) => [...current, created]);
                  setConnection(selectedConnection);
                  setConnectionMode("saved");
                }
              }
              const value = await api.action(`/api/repositories/${repo}/restore-database/preflight`, { snapshotId: dbSnapshot, connectionId: selectedConnection, database });
              setPreparedDatabaseConnection(selectedConnection);
              setConfirmation(value as Record<string, any>); setConfirmationKind("database"); setError(""); setPreflightMessage(t("数据库恢复预检通过，请继续确认恢复信息。"));
            } catch (reason) {
              const message = reason instanceof Error ? reason.message : t("数据库恢复预检失败");
              setError(message); setPreflightMessage(message);
            }
          }}
        >
          <fieldset className="full-field">
            <legend>{t("恢复凭据来源")}</legend>
            <label><input type="radio" name="database-credential-mode" checked={connectionMode === "saved"} disabled={!connections.length} onChange={() => { setConnectionMode("saved"); invalidate(); }} /> {t("使用已保存恢复连接")}</label>
            <label><input type="radio" name="database-credential-mode" checked={connectionMode === "temporary"} onChange={() => { setConnectionMode("temporary"); invalidate(); }} /> {t("本次使用临时凭据")}</label>
          </fieldset>
          {connectionMode === "saved" ? <label>
              {t("恢复连接")}
              <select value={connection} onChange={(event) => { setConnection(event.target.value); invalidate(); }} required>
                <option value="">{t("请选择恢复用途连接")}</option>
                {connections.map((item) => <option key={String(item.id)} value={String(item.id)}>{String(item.name)} · {String(item.engine ?? "")}</option>)}
              </select>
            </label> : <>
              <label>{t("临时连接名称")}<input value={temporaryName} onChange={(event) => { setTemporaryName(event.target.value); invalidate(); }} required /></label>
              <label>{t("数据库类型")}<select value={temporaryEngine} onChange={(event) => { const engine = event.target.value; setTemporaryEngine(engine); setTemporaryPort(engine === "mysql" ? 3306 : 5432); setTemporaryRestoreProgram(engine === "mysql" ? "/usr/bin/mysql" : "/usr/bin/psql"); setTemporaryAdminProgram(engine === "mysql" ? "/usr/bin/mysql" : "/usr/bin/psql"); invalidate(); }}><option value="mysql">MySQL</option><option value="postgresql">PostgreSQL</option></select></label>
              <label>{t("数据库地址")}<input value={temporaryHost} onChange={(event) => { setTemporaryHost(event.target.value); invalidate(); }} required /></label>
              <label>{t("端口")}<input type="number" min="1" max="65535" value={temporaryPort} onChange={(event) => { setTemporaryPort(Number(event.target.value)); invalidate(); }} required /></label>
              <label>{t("数据库用户名")}<input value={temporaryUsername} onChange={(event) => { setTemporaryUsername(event.target.value); invalidate(); }} required /></label>
              <label>{t("数据库密码")}<input type="password" autoComplete="new-password" value={temporaryPassword} onChange={(event) => { setTemporaryPassword(event.target.value); invalidate(); }} required /></label>
              <label>{t("恢复客户端路径")}<input value={temporaryRestoreProgram} onChange={(event) => setTemporaryRestoreProgram(event.target.value)} required /></label>
              <label>{t("管理客户端路径")}<input value={temporaryAdminProgram} onChange={(event) => setTemporaryAdminProgram(event.target.value)} required /></label>
              <label className="full-field"><input type="checkbox" checked={saveTemporaryConnection} onChange={(event) => setSaveTemporaryConnection(event.target.checked)} /> {t("另存为长期恢复连接")}</label>
              <p className="field-hint full-field">{t("未勾选时，凭据只在秘密库中短期保存，并在本次恢复结束或清理确认完成后删除。")}</p>
            </>}
          <label>
            {t("目标数据库名")}
            <input value={database} onChange={(event) => { setDatabase(event.target.value); invalidate(); }} required />
          </label>
          <div className="full-field restore-preflight-bar"><span className="restore-step-number" aria-hidden="true">3</span><div><strong>{t("只读预检")}</strong><small>{t("预检不会写入恢复目标。")}</small></div><button className="primary-button" type="submit" disabled={!repo || !dbSnapshot || !database || operation.active || (connectionMode === "saved" ? !connection : !temporaryName || !temporaryHost || !temporaryUsername || !temporaryPassword)}>{t("执行数据库只读预检")}</button></div>
          {confirmationKind === "database" && <RestoreConfirmation confirmation={confirmation} password={password} setPassword={setPassword} label={t("确认并开始数据库恢复")} locale={locale} onClose={() => { setConfirmation(null); setConfirmationKind(""); setPassword(""); }} onConfirm={() => void authorizeAndStart("database", { snapshotId: dbSnapshot, connectionId: preparedDatabaseConnection || connection, database })} />}
        </form>
      </section>}
      </div>
      {error && <p className="error-message" role="alert">{error}</p>}
      <OperationFeedback operation={operation} locale={locale} hideTerminal />
      <Toast message={preflightMessage} locale={locale} onClose={() => setPreflightMessage("")} />
    </>
  );
}

function snapshotIsDatabase(item: Record<string, unknown> | undefined): boolean {
  return Boolean(item && JSON.stringify(item.tags ?? []).includes("rc:source=database"));
}

function AgentRestoreTargetPicker({ api, agent, onChange, locale }: { api: AppAPI; agent?: Record<string, unknown>; onChange(value: string): void; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const agentID = String(agent?.id ?? "");
  const windows = (agent?.capabilities as unknown[] | undefined)?.includes("path-style:windows") ?? false;
  const root = windows ? "C:\\" : "/";
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const [parent, setParent] = useState(root);
  const [name, setName] = useState("");
  const [entries, setEntries] = useState<Array<{ name: string; path: string; directory: boolean }>>([]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const updateTarget = useCallback((nextParent: string, nextName: string) => {
    onChangeRef.current(nextName.trim() ? joinAgentPath(nextParent, nextName.trim(), windows) : "");
  }, [windows]);
  const browse = useCallback(async (path: string, nextName: string) => {
    if (!agentID || !path.trim()) return;
    setBusy(true); setMessage("");
    try {
      const value = await api.action(`/api/agents/${encodeURIComponent(agentID)}/filesystem/browse`, { path: path.trim() }) as Record<string, unknown>;
      const resolved = String(value.path ?? path.trim());
      setParent(resolved);
      setEntries((value.entries as Array<{ name: string; path: string; directory: boolean }> | undefined) ?? []);
      updateTarget(resolved, nextName);
    } catch (reason) {
      setMessage(reason instanceof Error ? reason.message : translate(locale, "无法浏览 Agent 目录"));
    } finally { setBusy(false); }
  }, [agentID, api, locale, updateTarget]);
  useEffect(() => {
    setParent(root);
    setName("");
    setEntries([]);
    setMessage("");
    updateTarget(root, "");
    if (agentID) void browse(root, "");
  }, [agentID, browse, root, updateTarget]);
  return <div className="full-field agent-restore-picker">
    <div className="agent-restore-controls">
      <label>{t("Agent 目标父目录")}<input value={parent} onChange={(event) => { setParent(event.target.value); updateTarget(event.target.value, name); }} required /></label>
      <label>{t("新恢复目录名称")}<input value={name} onChange={(event) => { setName(event.target.value); updateTarget(parent, event.target.value); }} required /></label>
      <button className="secondary-button" type="button" disabled={!agentID || busy} onClick={() => void browse(parent, name)}>{t(busy ? "正在读取…" : "刷新目录")}</button>
    </div>
    {message && <p className="error-message" role="alert">{message}</p>}
    {entries.length > 0 && <div className="agent-restore-directory-list" role="list" aria-label={t("Agent 子目录")}>{entries.map((entry) => <div key={entry.path} role="listitem"><button type="button" onClick={() => void browse(entry.path, name)}><span aria-hidden="true">⌄</span><span>{entry.name}</span><small>{entry.path}</small></button></div>)}</div>}
  </div>;
}

function joinAgentPath(parent: string, name: string, windows: boolean): string {
  const separator = windows ? "\\" : "/";
  return `${parent.replace(/[\\/]+$/, "")}${separator}${name.replace(/^[\\/]+/, "")}`;
}

function snapshotOptionLabel(id: unknown, time: unknown): string {
  const identifier = String(id);
  const shortID = identifier.length > 12 ? `${identifier.slice(0, 12)}…` : identifier;
  const match = String(time).match(/^(\d{4}-\d{2}-\d{2})T(\d{2}:\d{2})/);
  return `${shortID} · ${match ? `${match[1]} ${match[2]}` : display(time)}`;
}

function RestoreConfirmation({ confirmation, password, setPassword, label, locale, onClose, onConfirm }: { confirmation: Record<string, any> | null; password: string; setPassword(value: string): void; label: string; locale: Locale; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLDivElement>(null);
  const downloadLimit = Number(confirmation?.summary?.downloadKiBPerSecond ?? 0);
  const policySource = String(confirmation?.summary?.resourcePolicySource ?? "");
  useModalFocus(dialogRef, onClose);
  return <ModalPortal>
    <div ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="restore-confirmation-title">
      <header><div><h2 id="restore-confirmation-title">{label}</h2><p>{t("恢复前请核对预检结果并重新验证管理员身份。")}</p></div></header>
      <div className="dialog-body">
        <strong>{t("预检通过")}</strong>
        <p>{t("该确认只对当前仓库、快照和目标有效，并将在")} {display(confirmation?.expiresAt)} {t("失效。")}</p>
        <p>{downloadLimit > 0
          ? locale === "en-US"
            ? `Effective download limit: ${new Intl.NumberFormat(locale).format(downloadLimit)} KiB/s${policySource === "task" ? " (from the bound task)" : ""}`
            : `有效下载限速：${new Intl.NumberFormat(locale).format(downloadLimit)} KiB/s${policySource === "task" ? "（来自绑定任务）" : ""}`
          : t("有效下载限速：不额外限制")}</p>
        <label>{t("当前管理员密码")}<input type="password" value={password} onChange={(event) => setPassword(event.target.value)} autoComplete="current-password" /></label>
      </div>
      <footer>
        <button className="secondary-button" type="button" onClick={onClose}>{t("取消")}</button>
        <button className="danger-button" type="button" disabled={!password} onClick={onConfirm}>{label}</button>
      </footer>
    </div>
  </ModalPortal>;
}
function pageDescription(name: string) {
  return (
    {
      远程主机: "管理局域网或公网 SSH/SFTP 目标与固定主机密钥。",
      "Agent 节点": "管理远程源端执行节点及一次性注册凭据。",
      备份仓库: "每个任务使用独立加密 Restic 仓库。",
      数据库实例: "备份凭据与恢复凭据用途分离。",
      备份任务: "分别配置 Restic 备份或 rsync 增量同步，并选择本机或 Agent 执行。",
      备份计划: "同一时间点可触发多个独立任务。",
      运行记录: "查看结构化执行结果与脱敏日志。",
      告警历史: "查看当前告警、发生记录与恢复历史。",
      投递记录: "查看通知通道的发送结果、重试与失败原因。",
      通知配置: "配置 ntfy 与 Webhook 告警出口；所有通道默认关闭。",
      审计日志: "查看不可修改的管理操作记录。",
    }[name] ?? "管理备份资源。"
  );
}
function columns(name: string) {
  const map: Record<string, Array<{ key: string; label: string }>> = {
    远程主机: [
      { key: "id", label: "ID" },
      { key: "name", label: "名称" },
      { key: "host", label: "地址" },
      { key: "port", label: "端口" },
      { key: "username", label: "用户" },
    ],
    "Agent 节点": [
      { key: "id", label: "Agent ID" },
      { key: "runtimeStatus", label: "运行状态" },
      { key: "lastHeartbeatAt", label: "最后心跳" },
    ],
    备份仓库: [
      { key: "id", label: "ID" },
      { key: "name", label: "名称" },
      { key: "engine", label: "引擎" },
      { key: "kind", label: "类型" },
      { key: "path", label: "仓库路径" },
      { key: "status", label: "状态" },
      { key: "capacity", label: "存储容量" },
      { key: "lastRun", label: "最近运行" },
      { key: "nextRun", label: "下次执行" },
    ],
    数据库实例: [
      { key: "id", label: "ID" },
      { key: "name", label: "名称" },
      { key: "engine", label: "类型" },
      { key: "purpose", label: "用途" },
      { key: "host", label: "地址" },
      { key: "status", label: "预检状态" },
      { key: "preflight", label: "预检结果" },
    ],
    备份任务: [
      { key: "id", label: "ID" },
      { key: "name", label: "名称" },
      { key: "engine", label: "引擎" },
      { key: "kind", label: "类型" },
      { key: "executionTarget", label: "执行位置" },
      { key: "repositoryId", label: "仓库" },
      { key: "enabled", label: "启用" },
    ],
    备份计划: [
      { key: "name", label: "名称" },
      { key: "schedule", label: "执行周期" },
      { key: "timezone", label: "时区" },
      { key: "enabled", label: "启用" },
    ],
    运行记录: [
      { key: "taskId", label: "任务" },
      { key: "trigger", label: "触发" },
      { key: "status", label: "状态" },
      { key: "startedAt", label: "开始" },
      { key: "snapshotId", label: "快照" },
    ],
    审计日志: [
      { key: "occurredAt", label: "时间" },
      { key: "action", label: "动作" },
      { key: "targetType", label: "对象" },
      { key: "targetId", label: "标识" },
    ],
  };
  return map[name] ?? [];
}
function display(value: unknown, locale: Locale = "zh-CN") {
  const t = (source: string) => translate(locale, source);
  if (value === true) return t("是");
  if (value === false) return t("否");
  if (value == null || value === "") return "—";
  if (value === "local") return t("本地");
  if (value === "sftp") return t("远程 SFTP");
  if (value === "s3") return t("S3 对象存储");
  if (value === "draft") return t("草稿（不可启用）");
  if (value === "ready") return t("已验证");
  if (value === "backup") return t("备份");
  if (value === "restore") return t("恢复");
  if (value === "manual") return t("手动运行");
  if (value === "tcp") return t("TCP 网络");
  if (value === "unix") return "Unix Socket";
  if (isRFC3339Timestamp(value)) return timestampAtSecond(value);
  if (typeof value === "object") {
    const item = value as Record<string, unknown>;
    if (item.kind === "daily") return locale === "en-US" ? `Daily at ${String(item.timeOfDay)}` : `每日 ${String(item.timeOfDay)}`;
    if (item.kind === "weekly")
      return locale === "en-US" ? `Weekly on ${t(["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"][Number(item.dayOfWeek)] ?? "")} at ${String(item.timeOfDay)}` : `每周${["星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"][Number(item.dayOfWeek)] ?? ""} ${String(item.timeOfDay)}`;
    if (item.kind === "interval")
      return locale === "en-US" ? `Every ${String(item.intervalHours)} hours` : `每 ${String(item.intervalHours)} 小时`;
    return t("未知配置");
  }
  return String(value);
}

function resourceValue(name: string, key: string, item: Record<string, unknown>, locale: Locale): ReactNode {
  const t = (source: string) => translate(locale, source);
  const value = item[key];
  if (key === "engine") {
    return ({ restic: "Restic", rsync: "rsync", mysql: "MySQL", postgresql: "PostgreSQL" } as Record<string, string>)[String(value)] ?? t("未知类型");
  }
  if (key === "kind") {
    const labels: Record<string, string> = name === "备份仓库"
      ? { local: "本地目录", sftp: "远程 SFTP", s3: "S3 对象存储" }
      : { directory: "目录备份", database: "数据库备份", rsync: "rsync 增量同步" };
    return t(labels[String(value)] ?? "未知类型");
  }
  if (key === "purpose") {
    return t(({ backup: "备份用途", restore: "恢复用途" } as Record<string, string>)[String(value)] ?? "未知用途");
  }
  if (key === "executionTarget") {
    if (!value || typeof value !== "object") return t("Service 本机");
    const target = value as Record<string, unknown>;
    if (target.kind === "agent") {
      const agentID = String(target.agentId ?? "");
      return agentID ? <>{t("远程 Agent")} · <span className="technical-identifier">{agentID}</span></> : t("远程 Agent");
    }
    return target.kind === "local" || !target.kind ? t("Service 本机") : t("未知执行位置");
  }
  if (key === "enabled") {
    const enabled = value === true;
    return <StatusIndicator value={enabled ? "enabled" : "disabled"} locale={locale} variant="pill" />;
  }
  if (key === "status") {
    return <StatusIndicator value={String(value || "unknown")} locale={locale} variant="pill" />;
  }
  if (value && typeof value === "object") return t("未知配置");
  return display(value, locale);
}

function databasePreflightDisplay(value: unknown, locale: Locale = "zh-CN") {
		if (!value || typeof value !== "object") return translate(locale, "尚未预检");
		const result = value as Record<string, unknown>;
		const versions = [result.clientVersion ? `${locale === "en-US" ? "Client" : "客户端"} ${result.clientVersion}` : "", result.serverVersion ? `${locale === "en-US" ? "Server" : "服务端"} ${result.serverVersion}` : ""].filter(Boolean).join(locale === "en-US" ? ", " : "，");
		return [versions, result.error ? `${locale === "en-US" ? "Failed" : "失败"}：${result.error}` : translate(locale, "验证成功"), result.checkedAt ? `${locale === "en-US" ? "Checked at" : "检查于"} ${display(result.checkedAt, locale)}` : ""].filter(Boolean).join(locale === "en-US" ? "; " : "；");
}

function RepositoryCapacityCell({
  value,
  enabled,
  unsupported,
  repositoryId,
  api,
  locale,
  onUpdated,
}: {
  value: unknown;
  enabled: boolean;
  unsupported: boolean;
  repositoryId: string;
  api: AppAPI;
  locale: Locale;
  onUpdated(): Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const capacity = value && typeof value === "object" ? value as Record<string, unknown> : null;
  const probe = useOperation(api);
  const handled = useRef("");
  useEffect(() => {
    const operation = probe.operation;
    if (!operation || !["success", "partial", "failed", "cancelled", "cleanup_required"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handled.current === key) return;
    handled.current = key;
    void onUpdated();
  }, [onUpdated, probe.operation]);
  const refresh = <button
    className="icon-button capacity-refresh-button"
    type="button"
    aria-label={t("刷新存储容量")}
    title={t("刷新存储容量")}
    disabled={!enabled || unsupported || probe.active}
    onClick={() => {
      handled.current = "";
      void probe.start(`/api/repositories/${encodeURIComponent(repositoryId)}/capacity`, {});
    }}
  ><span aria-hidden="true">↻</span></button>;
  if (unsupported) {
    return <div className="capacity-cell capacity-cell-compact"><span>{t("对象存储容量不适用")}</span>{refresh}</div>;
  }
  if (!enabled) {
    return <div className="capacity-cell capacity-cell-compact"><span>—</span>{refresh}</div>;
  }
  if (!capacity) {
    return <div className="capacity-cell capacity-cell-compact" role="status" aria-live="polite"><span>—</span>{refresh}</div>;
  }
  const total = Number(capacity.totalBytes);
  const available = Number(capacity.availableBytes);
  if (!Number.isFinite(total) || !Number.isFinite(available) || total <= 0 || available < 0 || available > total) return t("容量数据无效");
  return (
    <div className="capacity-cell capacity-cell-compact" role="status" aria-live="polite">
      <strong>{locale === "en-US" ? `${formatBytes(available)} available / ${formatBytes(total)} total` : `${formatBytes(available)} 可用 / 共 ${formatBytes(total)}`}</strong>
      {refresh}
    </div>
  );
}

function repositoryRunDisplay(value: unknown, locale: Locale = "zh-CN") {
  if (!value || typeof value !== "object") return translate(locale, "尚未运行");
  const run = value as Record<string, unknown>;
  const summary = (run.summary ?? {}) as Record<string, unknown>;
  const metrics = [
    summary.changedItems != null ? (locale === "en-US" ? `${String(summary.changedItems)} files changed` : `${String(summary.changedItems)} 个文件变更`) : "",
    summary.dataAdded != null ? formatBytes(Number(summary.dataAdded)) : "",
  ].filter(Boolean).join(" · ");
  return <span className="capacity-cell"><strong>{statusLabel(String(run.status), locale)}</strong><small>{display(run.startedAt, locale)}{metrics ? ` · ${metrics}` : ""}</small></span>;
}

function RepositoryOperationState({ phase, locale }: { phase: "running" | "success"; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  return (
    <span className={`repository-operation-state ${phase}`} role="status" aria-live="polite" aria-atomic="true">
      <span className={phase === "running" ? "repository-operation-spinner" : "repository-operation-complete"} aria-hidden="true">
        {phase === "success" ? "✓" : ""}
      </span>
      <span className="capacity-cell">
        <strong>{t(phase === "running" ? "正在初始化" : "初始化完成")}</strong>
        <small>{t(phase === "running" ? "正在操作，请稍候" : "仓库状态已刷新")}</small>
      </span>
    </span>
  );
}

function formatBytes(value: number) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  const digits = Number.isInteger(amount) ? 0 : 1;
  return `${amount.toFixed(digits)} ${units[unit]}`;
}

async function copyToClipboard(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
    return;
  }
  const field = document.createElement("textarea");
  field.value = value;
  field.setAttribute("readonly", "");
  field.style.position = "fixed";
  field.style.opacity = "0";
  document.body.appendChild(field);
  field.select();
  const copied = document.execCommand("copy");
  field.remove();
  if (!copied) throw new Error("clipboard copy failed");
}

function sshAuthorizationCommand(publicKey: string): string {
  const quotedPublicKey = publicKey.replace(/'/g, "'\\''");
  return [
    "mkdir -p ~/.ssh",
    "chmod 700 ~/.ssh",
    `printf '%s\\n' '${quotedPublicKey}' >> ~/.ssh/authorized_keys`,
    "chmod 600 ~/.ssh/authorized_keys",
  ].join("\n");
}
function RowActions({
  name,
  item,
  api,
  reload,
  onEdit,
  onRotate,
  onMessage,
  onDelete,
  initializationBusy,
  onInitialize,
  repositoryConnectionBusy,
  onVerifyExisting,
  taskRunBusy,
  taskRunActive,
  onRun,
  onOpenTaskHealth,
  locale,
}: {
  name: string;
  item: Record<string, unknown>;
  api: AppAPI;
  reload(): Promise<void>;
  onEdit(): void;
  onRotate(): void;
  onMessage(message: string): void;
  onDelete(): void;
  initializationBusy: boolean;
  onInitialize(): void;
  repositoryConnectionBusy: boolean;
  onVerifyExisting(): void;
  taskRunBusy: boolean;
  taskRunActive: boolean;
  onRun(): void;
  onOpenTaskHealth(): void;
  locale: Locale;
}) {
  const t = (source: string) => translate(locale, source);
  const id = String(item.id ?? "");
  return (
    <>
      {name === "远程主机" && <button className="text-button" type="button" onClick={() => void api.action(`/api/remote-hosts/${encodeURIComponent(id)}/connection-test`, {}).then(() => onMessage(t("SSH 连接验证成功"))).catch((cause) => onMessage(cause instanceof Error ? cause.message : t("SSH 连接验证失败")))}>{t("测试连接")}</button>}
      {name === "远程主机" && <button className="text-button" type="button" onClick={() => void api.action(`/api/remote-hosts/${encodeURIComponent(id)}/ssh-public-key`).then((value) => {
        const publicKey = String((value as Record<string, unknown>).publicKey ?? "");
        if (!publicKey) throw new Error("服务器未返回 SSH 公钥");
        return copyToClipboard(publicKey);
      }).then(() => onMessage(t("SSH 公钥已复制；请将它添加到服务器用户的 authorized_keys。"))).catch((cause) => onMessage(cause instanceof Error ? cause.message : t("无法复制 SSH 公钥")))}>{t("复制 SSH 公钥")}</button>}
      {name === "备份仓库" && (
        <>
          {item.status === "uninitialized" && <>
            <button
              className="text-button"
              type="button"
              disabled={initializationBusy}
              onClick={onInitialize}
            >
              {t("初始化")}
            </button>
            <HelpTip label={t("初始化影响说明")} text={t("初始化会在目标路径创建新的 Restic 仓库。请确认该目录未被其他仓库使用。")} />{" "}
          </>}
		  {item.status === "disconnected" && <>
			<button className="text-button" type="button" disabled={repositoryConnectionBusy} onClick={onVerifyExisting}>{t("只读验证")}</button>
			<HelpTip label={t("只读验证影响说明")} text={t("只会读取仓库格式和快照列表，不会初始化、写入或修改仓库内容。")} />{" "}
		  </>}
		  {item.engine !== "rsync" && item.status === "ready" && <>
			<button className="text-button" type="button" onClick={onRotate}>
                {t("轮换密码")}
			</button>
				<HelpTip label={t("密码轮换影响说明")} text={t("轮换会新增并验证新 key，但保留旧 key；只有管理员确认外部客户端已切换后才单独撤销旧 key。")} />{" "}
		  </>}
        </>
      )}
      {name === "备份任务" && (
        <>
          <button className="text-button" type="button" onClick={onOpenTaskHealth}>{t("详情")}</button>
          <button className="text-button" type="button" disabled={taskRunBusy || item.enabled !== true} title={item.enabled === true ? undefined : t("任务未启用，不能立即运行")} onClick={onRun}>
            {t(taskRunActive ? "运行中…" : "立即运行")}
          </button>
        </>
      )}
      <button className="text-button" type="button" onClick={onEdit}>
        {t("编辑")}
      </button>
      <button
        className="text-button danger-text"
        type="button"
        onClick={onDelete}
      >
        {t("删除")}
      </button>
    </>
  );
}

function deleteImpact(name: string) {
  return ({
    远程主机: "依赖此主机的 SFTP 仓库必须先迁移或删除。",
    备份仓库: "依赖此仓库的任务、维护策略、快照浏览和恢复入口将受到影响。",
    数据库实例: "依赖此连接的数据库备份任务必须先迁移或删除。",
    备份任务: "引用此任务的备份计划必须先调整或删除。",
    备份计划: "删除后该计划不会再调度任务，但已有运行记录会保留。",
  } as Record<string, string>)[name] ?? "请确认该资源不再被其他配置引用。";
}

function DeleteResourceDialog({
  name,
  preview,
  deleting,
  locale,
  onClose,
  onConfirm,
}: {
  name: string;
  preview: Record<string, unknown>;
  deleting: boolean;
  locale: Locale;
  onClose(): void;
  onConfirm(): void;
}) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!deleting) onClose(); });
  return (
    <ModalPortal>
      <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="delete-resource-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
        <header>
          <div>
            <h2 id="delete-resource-title">{locale === "en-US" ? `Confirm deletion: ${t(name)}` : `确认删除${name}`}</h2>
            <p>{locale === "en-US" ? <>You are about to delete “{String(preview.name ?? preview.id)}” (ID: <code>{String(preview.id)}</code>).</> : <>即将删除“{String(preview.name ?? preview.id)}”（ID：<code>{String(preview.id)}</code>）。</>}</p>
          </div>
        </header>
        <div className="delete-dialog-body">
          <p>{t(deleteImpact(name))}</p>
          {Array.isArray(preview.dependencies) && preview.dependencies.length > 0 ? (
            <div className="dependency-preview">
              <strong>{t("当前依赖")}</strong>
              <ul>{(preview.dependencies as Array<Record<string, unknown>>).map((dependency) => <li key={String(dependency.type)}>{locale === "en-US" ? `${String(dependency.type)}: ${String(dependency.count)} (${Array.isArray(dependency.names) ? dependency.names.join(", ") : ""})` : `${String(dependency.type)}：${String(dependency.count)} 个（${Array.isArray(dependency.names) ? dependency.names.join("、") : ""}）`}</li>)}</ul>
            </div>
          ) : <p>{t("当前没有阻止删除的资源依赖。")}</p>}
          <p className="warning-text">{t("只删除管理端配置，不会删除远端仓库内容、源目录或外部数据库。存在依赖时服务端会拒绝删除。")}</p>
        </div>
        <footer>
          <button className="secondary-button" type="button" disabled={deleting} onClick={onClose}>{t("取消删除")}</button>
          <button className="danger-button" type="submit" disabled={deleting}>{t(deleting ? "正在删除…" : "确认删除")}</button>
        </footer>
      </form>
    </ModalPortal>
  );
}

function HelpTip({ label, text }: { label: string; text: string }) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<{ left: number; top?: number; bottom?: number }>({ left: 12, top: 12 });
  const trigger = useRef<HTMLButtonElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const place = () => {
      const rect = trigger.current?.getBoundingClientRect();
      if (!rect) return;
      const width = Math.min(300, window.innerWidth - 24);
      const left = Math.max(12, Math.min(window.innerWidth - width - 12, rect.left + rect.width / 2 - width / 2));
      setPosition(rect.top >= 120
        ? { left, bottom: window.innerHeight - rect.top + 8 }
        : { left, top: rect.bottom + 8 });
    };
    place();
    const close = (event: KeyboardEvent) => event.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", close);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("keydown", close);
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open]);
  return (
    <span className="help-popover" onMouseEnter={() => setOpen(true)} onMouseLeave={() => setOpen(false)}>
      <button ref={trigger} className="help-tip" type="button" aria-label={label} aria-expanded={open} aria-describedby={open ? id : undefined} onFocus={() => setOpen(true)} onBlur={() => setOpen(false)} onClick={() => setOpen(true)}>?</button>
      {open && createPortal(<span id={id} className="tooltip" role="tooltip" style={position}>{text}</span>, document.body)}
    </span>
  );
}

function RotatePasswordDialog({
  repositoryId,
  api,
  locale,
  onClose,
}: {
  repositoryId: string;
  api: AppAPI;
  locale: Locale;
  onClose(): void;
}) {
  const t = (source: string) => translate(locale, source);
  const [message, setMessage] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [generated, setGenerated] = useState(true);
  const [showPassword, setShowPassword] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [pendingRevocation, setPendingRevocation] = useState(false);
  const [confirmRevocation, setConfirmRevocation] = useState(false);
  const [revokingOldKey, setRevokingOldKey] = useState(false);
  const [administratorPassword, setAdministratorPassword] = useState("");
  const rotation = useOperation(api);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, onClose);
  useEffect(() => {
    let active = true;
    void api
      .action(`/api/repositories/${repositoryId}/password-rotation`)
      .then((value) => {
        if (active) setPendingRevocation(Boolean((value as Record<string, unknown>).pending));
      })
      .catch(() => active && setMessage("无法读取密码轮换状态"));
    return () => {
      active = false;
    };
  }, [api, repositoryId]);
  useEffect(() => {
    const operation = rotation.operation;
    if (!operation || !["success", "failed", "cancelled"].includes(operation.status)) return;
    if (operation.status === "success") {
      setPendingRevocation(true);
      setMessage(t("新密码已验证并启用；旧 key 仍然有效，确认外部客户端已切换后再单独撤销。"));
      return;
    }
    setMessage(operation.errorSummary || t(operation.status === "cancelled" ? "操作已取消" : "仓库密码轮换失败"));
  }, [locale, rotation.operation]);
  useEffect(() => {
    if (rotation.error) setMessage(rotation.error);
  }, [rotation.error]);
  return (
    <ModalPortal>
      <form
        ref={dialogRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="rotate-password-title"
        onSubmit={(event) => {
          event.preventDefault();
          if (!confirmed || !administratorPassword || password.length < 12 || (!generated && password !== confirmation)) return;
          void rotation.start(`/api/repositories/${repositoryId}/rotate-password`, {
              password,
              passwordConfirmed: true,
			  administratorPassword,
            });
          setPassword("");
          setConfirmation("");
          setConfirmed(false);
		  setAdministratorPassword("");
        }}
      >
        <header>
          <div>
            <h2 id="rotate-password-title">{t("两阶段轮换仓库密码")}</h2>
            <p>{t("第一阶段新增并验证新 key；旧 key 只在管理员单独确认后撤销。")}</p>
          </div>
          <button className="icon-button" type="button" aria-label={t("关闭")} onClick={onClose}>
            ×
          </button>
        </header>
        <div className="form-grid">
          {pendingRevocation ? (
            <div className="full-field">
              <p className="warning-text">{t("旧 key 仍然有效。请先确认所有外部客户端已改用新密码。")}</p>
              <button
                className="danger-button"
                type="button"
                onClick={() => setConfirmRevocation(true)}
              >
                {t("撤销旧 key")}
              </button>
            </div>
          ) : (
            <>
              <label>
                {t("密码来源")}
                <select
                  value={generated ? "generated" : "custom"}
                  onChange={(event) => {
                    setGenerated(event.target.value === "generated");
                    setPassword("");
                    setConfirmation("");
                    setConfirmed(false);
                  }}
                >
                  <option value="generated">{t("由应用生成（推荐）")}</option>
                  <option value="custom">{t("自行输入")}</option>
                </select>
              </label>
              {generated && (
                <button
                  className="secondary-button"
                  type="button"
                  onClick={() => {
                    setPassword(generateRepositoryPassword());
                    setConfirmed(false);
                  }}
                >
                  {t("生成高强度密码")}
                </button>
              )}
              <label className="full-field">
                {t("新仓库密码")}
                <input
                  type={showPassword ? "text" : "password"}
                  minLength={12}
                  value={password}
                  readOnly={generated}
                  onChange={(event) => {
                    setPassword(event.target.value);
                    setConfirmed(false);
                  }}
                  required
                />
              </label>
              {!generated && (
                <label className="full-field">
                  {t("再次输入新仓库密码")}
                  <input
                    type={showPassword ? "text" : "password"}
                    minLength={12}
                    value={confirmation}
                    onChange={(event) => {
                      setConfirmation(event.target.value);
                      setConfirmed(false);
                    }}
                    required
                  />
                </label>
              )}
              <div className="full-field">
                <button className="text-button" type="button" onClick={() => setShowPassword((value) => !value)}>
                  {t(showPassword ? "隐藏密码" : "显示密码")}
                </button>{" "}
                <button
                  className="text-button"
                  type="button"
                  disabled={!password}
                  onClick={() => void navigator.clipboard.writeText(password).then(() => setMessage(t("密码已复制；请保存到密码管理器。"))).catch(() => setMessage(t("复制失败，请显示后手动保存")))}
                >
                  {t("复制仓库密码")}
                </button>
              </div>
              <label className="full-field">
                <input
                  type="checkbox"
                  checked={confirmed}
                  disabled={!password || (!generated && password !== confirmation)}
                  onChange={(event) => setConfirmed(event.target.checked)}
                />
                {t("我已将新仓库密码安全保存到应用之外")}
              </label>
              <label className="full-field">{t("当前管理员密码")}<input type="password" autoComplete="current-password" value={administratorPassword} onChange={(event) => setAdministratorPassword(event.target.value)} required /></label>
            </>
          )}
          {confirmRevocation && (
            <section className="confirmation-panel full-field" aria-labelledby="revoke-key-title">
              <h3 id="revoke-key-title">{t("确认撤销旧仓库 key")}</h3>
              <p className="warning-text">{t("撤销后，仍使用旧仓库密码的外部客户端将立即失去访问权限。新 key 不受影响。")}</p>
              <label>{t("管理员密码")}
                <input type="password" autoComplete="current-password" value={administratorPassword} onChange={(event) => setAdministratorPassword(event.target.value)} />
              </label>
              <div className="dialog-actions">
                <button type="button" className="secondary-button" disabled={revokingOldKey} onClick={() => { setConfirmRevocation(false); setAdministratorPassword(""); }}>{t("取消撤销")}</button>
                <button type="button" className="danger-button" disabled={!administratorPassword || revokingOldKey} onClick={() => {
                  setRevokingOldKey(true);
                  void api.action(`/api/repositories/${repositoryId}/revoke-old-password`, { password: administratorPassword }).then(() => {
                    setPendingRevocation(false);
                    setConfirmRevocation(false);
                    setAdministratorPassword("");
                    setMessage(t("旧 key 已撤销"));
                  }).catch((cause) => setMessage(cause instanceof Error ? cause.message : t("撤销旧 key 失败"))).finally(() => setRevokingOldKey(false));
                }}>{t(revokingOldKey ? "正在撤销…" : "确认撤销旧 key")}</button>
              </div>
            </section>
          )}
          <Toast message={message} locale={locale} onClose={() => setMessage("")} />
          <OperationFeedback operation={rotation} locale={locale} hideTerminal />
        </div>
        <footer>
          <button className="secondary-button" type="button" onClick={onClose}>
            {t("关闭")}
          </button>
          {!pendingRevocation && (
            <button className="primary-button" type="submit" disabled={!confirmed || password.length < 12 || (!generated && password !== confirmation)}>
              {t("新增并验证新 key")}
            </button>
          )}
        </footer>
      </form>
    </ModalPortal>
  );
}

function ResourceDialog({
  name,
  initial,
  api,
  locale,
  onClose,
  onSubmit,
}: {
  name: string;
  initial: Record<string, unknown> | null;
  api: AppAPI;
  locale: Locale;
  onClose(): void;
  onSubmit(payload: Record<string, unknown>): Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const [error, setError] = useState("");
  const [hostKeyMessage, setHostKeyMessage] = useState("");
  const formRef = useRef<HTMLFormElement>(null);
  useModalFocus(formRef, onClose);
  const [databaseEngine, setDatabaseEngine] = useState(String(initial?.engine ?? "mysql"));
  const [databasePurpose, setDatabasePurpose] = useState(String(initial?.purpose ?? "backup"));
  const [databaseNetwork, setDatabaseNetwork] = useState(String(initial?.network ?? "tcp"));
  const [generatedSSHKey, setGeneratedSSHKey] = useState(!initial);
  const visibleFields = resourceFields(name).filter((field) => {
    if (name === "远程主机" && !initial && generatedSSHKey && field.name === "privateKey") return false;
    if (name !== "数据库实例") return true;
    if (["host", "port"].includes(field.name)) return databaseNetwork === "tcp";
    if (field.name === "socketPath") return databaseNetwork === "unix";
    if (field.name === "dump") return databasePurpose === "backup";
    if (["restore", "create"].includes(field.name)) return databasePurpose === "restore";
    return true;
  });
  return (
    <ModalPortal>
      <form
        ref={formRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="resource-dialog-title"
        onSubmit={(event) => {
          event.preventDefault();
          const form = new FormData(event.currentTarget);
          const payload = buildPayload(name, form);
          if (name === "远程主机" && !initial && generatedSSHKey) payload.keyMode = "generated";
          void onSubmit(payload).catch((cause) =>
            setError(cause instanceof Error ? cause.message : "保存失败"),
          );
        }}
      >
        <header>
          <div>
            <h2 id="resource-dialog-title">
              {t(`${initial ? "编辑" : "新建"}${name}`)}
            </h2>
            <p>{t("留空秘密字段表示保持原值。")}</p>
          </div>
          <button
            className="icon-button"
            type="button"
            aria-label={t("关闭")}
            onClick={onClose}
          >
            ×
          </button>
        </header>
        {error && <p className="form-error">{error}</p>}
        {name === "数据库实例" && (
          <p className="field-hint">{t("保存时会实际执行系统数据库客户端，验证客户端身份/版本、网络、TLS、认证和当前用途权限。失败配置仅保存为不可启用的草稿。备份期间的事务一致性与数据库业务写入协调仍由数据库管理员负责。")}</p>
        )}
        <div className="form-grid">
          {name === "远程主机" && !initial && <fieldset className="full-field ssh-key-mode">
            <legend>{t("SSH 登录密钥")}</legend>
            <div className="ssh-key-mode-options">
              <label><input type="radio" name="ssh-key-mode" checked={generatedSSHKey} onChange={() => setGeneratedSSHKey(true)} /> {t("由应用生成密钥")}</label>
              <label><input type="radio" name="ssh-key-mode" checked={!generatedSSHKey} onChange={() => setGeneratedSSHKey(false)} /> {t("导入现有私钥")}</label>
            </div>
            {generatedSSHKey && <p className="field-hint">{t("私钥只加密保存在本机秘密库中，不会显示或导出。")}</p>}
          </fieldset>}
          {visibleFields.map((field) => (
            <label key={field.name} className={`${field.full ? "full-field " : ""}${name === "远程主机" && field.name === "hostFingerprint" ? "host-key-field" : ""}`}>
              {t(field.label)}
              {name === "远程主机" && field.name === "hostFingerprint" && <span className="field-hint">{t("由“获取并核对主机密钥”自动填入；请核对指纹。")}</span>}
              {field.kind === "select" ? (
                <select
                  name={field.name}
                  {...(["engine", "purpose", "network"].includes(field.name) && name === "数据库实例" ? { value: field.name === "engine" ? databaseEngine : field.name === "purpose" ? databasePurpose : databaseNetwork, onChange: (event: ChangeEvent<HTMLSelectElement>) => {
                    if (field.name === "engine") {
                      setDatabaseEngine(event.target.value);
                      const port = formRef.current?.elements.namedItem("port") as HTMLInputElement | null;
                      if (port) port.value = event.target.value === "postgresql" ? "5432" : "3306";
                    }
                    if (field.name === "purpose") setDatabasePurpose(event.target.value);
                    if (field.name === "network") setDatabaseNetwork(event.target.value);
                  }} : { defaultValue: initialFieldValue(field, initial) })}
                >
                  {field.options?.map((option) => (
                    <option key={option} value={option}>
                      {t(localizedOption(option))}
                    </option>
                  ))}
                </select>
              ) : field.kind === "textarea" ? (
                <textarea
                  name={field.name}
                  aria-label={name === "远程主机" && field.name === "hostFingerprint" ? t("known_hosts 固定主机密钥行") : undefined}
                  readOnly={name === "远程主机" && field.name === "hostFingerprint"}
                  required={field.required && !initial}
                  defaultValue={initialFieldValue(field, initial)}
                />
              ) : (
                <input
                  name={field.name}
                  type={field.kind ?? "text"}
                  required={field.required && !initial}
                  defaultValue={initialFieldValue(field, initial)}
                />
              )}
            </label>
          ))}
          {name === "远程主机" && hostKeyMessage && <div className="full-field host-key-confirmation" role="status"><strong>{t("主机密钥已获取")}</strong><span>{hostKeyMessage}</span><p>{t("请通过云服务商控制台、服务器控制台或可信管理员核对该指纹后再保存。")}</p></div>}
        </div>
        <footer>
          {name === "远程主机" && (
            <button
              className="secondary-button"
              type="button"
              onClick={() => {
                const form = formRef.current;
                if (!form) return;
                const data = new FormData(form);
                void api
                  .action("/api/ssh/host-key", {
                    host: String(data.get("host")),
                    port: Number(data.get("port")),
                  })
                  .then((value) => {
                    const result = value as {
                      fingerprint: string;
                      knownHosts: string;
                    };
                    const field = form.elements.namedItem(
                      "hostFingerprint",
                    ) as HTMLTextAreaElement | null;
                    if (field) field.value = result.knownHosts;
                    setHostKeyMessage(
                      `${t("请核对并确认主机指纹：")}${result.fingerprint}`,
                    );
                  })
                  .catch((cause) =>
                    setError(
                      cause instanceof Error
                        ? cause.message
                        : "无法获取主机密钥",
                    ),
                  );
              }}
            >
              {t("获取并核对主机密钥")}
            </button>
          )}
          <button className="secondary-button" type="button" onClick={onClose}>
            {t("取消")}
          </button>
          <button className="primary-button" type="submit">
            {t("保存")}
          </button>
        </footer>
      </form>
    </ModalPortal>
  );
}
function initialFieldValue(
  field: Field,
  initial: Record<string, unknown> | null,
) {
  if (!initial) return field.default ?? "";
  const nested: Record<string, unknown> = {
    path: (initial.directory as Record<string, unknown> | undefined)?.path,
    exclusions: (
      (initial.directory as Record<string, unknown> | undefined)?.exclusions as
        string[] | undefined
    )?.join("\n"),
    connectionId: (initial.database as Record<string, unknown> | undefined)
      ?.connectionId,
    database: (initial.database as Record<string, unknown> | undefined)
      ?.database,
    keepWithinDays: (initial.retention as Record<string, unknown> | undefined)
      ?.keepWithinDays,
    timeOfDay: (initial.schedule as Record<string, unknown> | undefined)
      ?.timeOfDay,
    taskIds: (initial.taskIds as string[] | undefined)?.join(","),
    dump: (initial.toolPaths as Record<string, unknown> | undefined)?.dump,
    restore: (initial.toolPaths as Record<string, unknown> | undefined)
      ?.restore,
    admin: (initial.toolPaths as Record<string, unknown> | undefined)?.admin,
    create: (initial.toolPaths as Record<string, unknown> | undefined)?.create,
    socketPath: initial.socketPath,
    tlsMode: (initial.tls as Record<string, unknown> | undefined)?.mode,
    tlsCA: (initial.tls as Record<string, unknown> | undefined)?.ca,
    tlsClientCert: (initial.tls as Record<string, unknown> | undefined)
      ?.clientCert,
    tlsClientKey: (initial.tls as Record<string, unknown> | undefined)
      ?.clientKey,
    tlsServerName: (initial.tls as Record<string, unknown> | undefined)
      ?.serverName,
  };
  return String(
    initial[field.name] ?? nested[field.name] ?? field.default ?? "",
  );
}
function localizedOption(value: string) {
  return ({ mysql: "MySQL", postgresql: "PostgreSQL", backup: "备份用途", restore: "恢复用途", tcp: "TCP 网络", unix: "Unix Socket", true: "启用", false: "停用", preferred: "优先使用 TLS", required: "必须使用 TLS", "verify-ca": "验证 CA", "verify-full": "验证 CA 与主机名", disabled: "禁用 TLS" } as Record<string, string>)[value] ?? value;
}
type Field = {
  name: string;
  label: string;
  kind?: string;
  required?: boolean;
  default?: string;
  options?: string[];
  full?: boolean;
};
function resourceFields(name: string): Field[] {
  const common: Record<string, Field[]> = {
    远程主机: [
      { name: "name", label: "名称", required: true },
      { name: "host", label: "主机/IP", required: true },
      {
        name: "port",
        label: "SSH 端口",
        kind: "number",
        default: "22",
        required: true,
      },
      { name: "username", label: "SSH 用户", required: true },
      {
        name: "privateKey",
        label: "SSH 私钥",
        kind: "textarea",
        required: true,
        full: true,
      },
      {
        name: "hostFingerprint",
        label: "known_hosts 固定主机密钥行",
        kind: "textarea",
        required: true,
        full: true,
      },
    ],
    备份仓库: [
      { name: "name", label: "名称", required: true },
      { name: "remoteHostId", label: "远程主机 ID", required: true },
      { name: "path", label: "远端绝对路径", required: true },
      { name: "password", label: "仓库密码", kind: "password", required: true },
    ],
    数据库实例: [
      { name: "name", label: "名称", required: true },
      {
        name: "engine",
        label: "数据库",
        kind: "select",
        options: ["mysql", "postgresql"],
        default: "mysql",
      },
      {
        name: "purpose",
        label: "用途",
        kind: "select",
        options: ["backup", "restore"],
        default: "backup",
      },
      {
        name: "network",
        label: "网络",
        kind: "select",
        options: ["tcp", "unix"],
        default: "tcp",
      },
      { name: "host", label: "主机", default: "127.0.0.1" },
      { name: "port", label: "端口", kind: "number", default: "3306" },
      { name: "socketPath", label: "Unix Socket 绝对路径" },
      { name: "username", label: "用户", required: true },
      { name: "password", label: "密码", kind: "password", required: true },
      {
        name: "tlsMode",
        label: "TLS 模式",
        kind: "select",
        options: [
          "preferred",
          "required",
          "verify-ca",
          "verify-full",
          "disabled",
        ],
        default: "preferred",
      },
      { name: "tlsCA", label: "TLS CA 文件绝对路径" },
      { name: "tlsClientCert", label: "TLS 客户端证书绝对路径" },
      { name: "tlsClientKey", label: "TLS 客户端私钥绝对路径" },
      { name: "tlsServerName", label: "TLS 服务端名称" },
      { name: "dump", label: "导出工具绝对路径" },
      { name: "restore", label: "导入工具绝对路径" },
      { name: "admin", label: "元数据查询工具绝对路径（mysql/psql）" },
      { name: "create", label: "createdb 绝对路径" },
    ],
    备份任务: [
      { name: "name", label: "名称", required: true },
      {
        name: "kind",
        label: "类型",
        kind: "select",
        options: ["directory", "database"],
        default: "directory",
      },
      { name: "repositoryId", label: "仓库 ID", required: true },
      { name: "path", label: "目录绝对路径" },
      { name: "connectionId", label: "数据库连接 ID" },
      { name: "database", label: "逻辑数据库名" },
      {
        name: "enabled",
        label: "任务状态",
        kind: "select",
        options: ["true", "false"],
        default: "true",
      },
      {
        name: "keepWithinDays",
        label: "保留天数",
        kind: "number",
        default: "30",
      },
      {
        name: "exclusions",
        label: "建议排除规则（确认后保存，每行一条）",
        kind: "textarea",
        default: "**/.cache\n**/node_modules\n**/.DS_Store\n**/@eaDir",
        full: true,
      },
    ],
  };
  return common[name] ?? [];
}
function buildPayload(name: string, form: FormData): Record<string, unknown> {
  const value = (key: string) => String(form.get(key) ?? "");
  if (name === "远程主机")
    return {
      name: value("name"),
      host: value("host"),
      port: Number(value("port")),
      username: value("username"),
      privateKey: value("privateKey"),
      hostFingerprint: value("hostFingerprint"),
    };
  if (name === "备份仓库")
    return {
      name: value("name"),
      remoteHostId: value("remoteHostId"),
      path: value("path"),
      password: value("password"),
    };
  if (name === "数据库实例") {
    const network = value("network");
    return {
      name: value("name"),
      engine: value("engine"),
      purpose: value("purpose"),
      network,
      ...(network === "unix"
        ? { socketPath: value("socketPath"), host: "", port: 0 }
        : { host: value("host"), port: Number(value("port")), socketPath: "" }),
      username: value("username"),
      password: value("password"),
      tls: {
        mode: value("tlsMode"),
        ca: value("tlsCA"),
        clientCert: value("tlsClientCert"),
        clientKey: value("tlsClientKey"),
        serverName: value("tlsServerName"),
      },
      toolPaths: {
        dump: value("dump"),
        restore: value("restore"),
        admin: value("admin"),
        create: value("create"),
      },
    };
  }
  if (name === "备份任务") {
    const kind = value("kind");
    return {
      name: value("name"),
      kind,
      repositoryId: value("repositoryId"),
      ...(kind === "directory"
        ? {
            directory: {
              path: value("path"),
              exclusions: value("exclusions").split("\n").filter(Boolean),
              skipIfUnchanged: true,
            },
          }
        : {
            database: {
              connectionId: value("connectionId"),
              database: value("database"),
            },
          }),
      retention: { keepWithinDays: Number(value("keepWithinDays")) },
      resources: { compression: "auto" },
      enabled: value("enabled") === "true",
    };
  }
  return {};
}

function Summary({
  label,
  value,
  tone = "default",
}: {
  label: string;
  value: ReactNode;
  tone?: string;
}) {
  return (
    <div className="summary-item">
      <span>{label}</span>
      <strong className={`tone-${tone}`}>{value}</strong>
    </div>
  );
}

function LifecycleSettings({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [policy, setPolicy] = useState<LifecyclePolicy | null>(null);
  const [report, setReport] = useState<LifecycleReport | null>(null);
  const [preview, setPreview] = useState<LifecycleReport | null>(null);
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [cleaning, setCleaning] = useState(false);
  useEffect(() => {
    void api
      .lifecyclePolicy()
      .then(setPolicy)
      .catch(() => setError("无法读取数据生命周期策略"));
  }, [api]);
  if (!policy) {
    return (
      <>
        <header className="system-page-intro">
          <p>{t("正在加载策略…")}</p>
        </header>
        {error && <p className="form-error">{error}</p>}
      </>
    );
  }
  const update = (key: keyof LifecyclePolicy, value: number) =>
    setPolicy((current) => (current ? { ...current, [key]: value } : current));
  return (
    <>
      <header className="system-page-intro">
        <p>{t("每日清理过期运行摘要、原始日志与审计，并限制日志总容量。")}</p>
      </header>
      <section className="content-section">
        <form
          className="form-grid"
          onSubmit={(event) => {
            event.preventDefault();
            setError("");
            void api
              .saveLifecyclePolicy(policy)
              .then(() => setMessage("生命周期策略已保存"))
              .catch((cause) =>
                setError(cause instanceof Error ? cause.message : "保存失败"),
              );
          }}
        >
          <label>
            {t("运行摘要保留天数")}
            <input
              type="number"
              min="0"
              max="36500"
              value={policy.runDays}
              onChange={(event) => update("runDays", Number(event.target.value))}
            />
          </label>
          <label>
            {t("原始日志保留天数")}
            <input
              type="number"
              min="0"
              max="36500"
              value={policy.rawLogDays}
              onChange={(event) =>
                update("rawLogDays", Number(event.target.value))
              }
            />
          </label>
          <label>
            {t("审计记录保留天数")}
            <input
              type="number"
              min="0"
              max="36500"
              value={policy.auditDays}
              onChange={(event) =>
                update("auditDays", Number(event.target.value))
              }
            />
          </label>
          <label>
            {t("原始日志总容量（MiB）")}
            <input
              type="number"
              min="0"
              max="1048576"
              value={Math.floor(policy.rawLogMaxBytes / (1024 * 1024))}
              onChange={(event) =>
                update(
                  "rawLogMaxBytes",
                  Number(event.target.value) * 1024 * 1024,
                )
              }
            />
          </label>
          <div className="full-field">
            <button className="primary-button" type="submit">
              {t("保存生命周期策略")}
            </button>{" "}
            <button
              className="secondary-button"
              type="button"
              onClick={() => {
                setError("");
                void api
                  .previewLifecycleCleanup()
                  .then(setPreview)
                  .catch((cause) =>
                    setError(
                      cause instanceof Error ? cause.message : "无法预览清理影响",
                    ),
                  );
              }}
            >
              {t("立即清理")}
            </button>
        </div>
        <Toast message={message} locale={locale} onClose={() => setMessage("")} />
          {error && <p className="form-error full-field">{error}</p>}
        </form>
        {preview && (
          <LifecycleCleanupDialog
            preview={preview}
            password={password}
            cleaning={cleaning}
            locale={locale}
            onPassword={setPassword}
            onClose={() => { if (!cleaning) { setPreview(null); setPassword(""); } }}
            onConfirm={() => {
              setCleaning(true);
              setError("");
              void api.cleanupLifecycle(password).then((value) => {
                setReport(value);
                setPreview(null);
                setPassword("");
                setMessage(t("清理完成"));
              }).catch((cause) => setMessage(cause instanceof Error ? cause.message : t("清理失败"))).finally(() => setCleaning(false));
            }}
          />
        )}
        {report && (
          <p>
            上次清理：清空 {report.logsCleared} 条日志，删除 {report.runsDeleted}
            条运行摘要与 {report.auditsDeleted} 条审计；日志容量降至{" "}
            {(report.rawLogBytesAfter / (1024 * 1024)).toFixed(2)} MiB。
          </p>
        )}
      </section>
    </>
  );
}

function LifecycleCleanupDialog({ preview, password, cleaning, locale, onPassword, onClose, onConfirm }: { preview: LifecycleReport; password: string; cleaning: boolean; locale: Locale; onPassword(value: string): void; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!cleaning) onClose(); });
  return <ModalPortal>
    <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="cleanup-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
      <header><div><h2 id="cleanup-title">{t("确认清理执行数据")}</h2><p>{t("清理前请核对影响范围。")}</p></div></header>
      <div className="dialog-body">
        <p>{locale === "en-US" ? `This will clear ${preview.logsCleared} log entries and delete ${preview.runsDeleted} run summaries and ${preview.auditsDeleted} audit records. This action cannot be undone.` : `将清空 ${preview.logsCleared} 条日志，删除 ${preview.runsDeleted} 条运行摘要和 ${preview.auditsDeleted} 条审计记录。此操作不可撤销。`}</p>
        <label>{t("管理员密码")}<input type="password" autoComplete="current-password" value={password} onChange={(event) => onPassword(event.target.value)} /></label>
      </div>
      <footer>
        <button className="secondary-button" type="button" disabled={cleaning} onClick={onClose}>{t("取消")}</button>
        <button className="danger-button" type="submit" disabled={!password || cleaning}>{t(cleaning ? "正在清理…" : "确认清理")}</button>
      </footer>
    </form>
  </ModalPortal>;
}

function VaultSettings({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [status, setStatus] = useState<Awaited<
    ReturnType<AppAPI["vaultStatus"]>
  > | null>(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [automaticDialog, setAutomaticDialog] = useState(false);
  const [administratorPassword, setAdministratorPassword] = useState("");
  const [automaticRiskConfirmed, setAutomaticRiskConfirmed] = useState(false);
  const [automaticSaving, setAutomaticSaving] = useState(false);
  const [automaticError, setAutomaticError] = useState("");
  async function refresh() {
    setStatus(await api.vaultStatus());
  }
  useEffect(() => {
    void refresh().catch(() => setError("无法读取秘密库状态"));
  }, []);
  return (
    <>
      <header className="system-page-intro">
        <p>{t("选择应用重启后自动解锁，或使用独立口令保护已托管密钥。")}</p>
      </header>
      <section className="content-section">
        <h2>{t("秘密库启动模式")}</h2>
        <p>
          {t("当前模式：")}
          <strong>
            {t(status?.mode === "lock-on-restart" ? "重启后锁定" : "自动解锁")}
          </strong>
        </p>
        <form
          className="form-grid"
          onSubmit={(event) => {
            event.preventDefault();
            const formElement = event.currentTarget;
            const form = new FormData(formElement);
            setError("");
			const passphrase = String(form.get("passphrase"));
			if (passphrase !== String(form.get("passphraseConfirmation"))) {
			  setError("两次输入的秘密库口令不一致");
			  return;
			}
            void api
              .setVaultLockOnRestart(passphrase)
              .then(async () => {
                formElement.reset();
                await refresh();
                setMessage("已启用重启后锁定；请妥善保管秘密库口令");
              })
              .catch((cause) =>
                setError(cause instanceof Error ? cause.message : "设置失败"),
              );
          }}
        >
          <label className="full-field">
            {t("新秘密库口令（至少 12 个字符）")}
            <input name="passphrase" type="password" minLength={12} required />
          </label>
          <label className="full-field">
            {t("再次输入秘密库口令")}
            <input
              name="passphraseConfirmation"
              type="password"
              minLength={12}
              required
            />
          </label>
          <div className="full-field">
            <button className="primary-button" type="submit">
              {t("启用重启后锁定")}
            </button>{" "}
            <button
              className="secondary-button"
              type="button"
              onClick={() => { setError(""); setAutomaticError(""); setAutomaticDialog(true); }}
            >
              {t("改为自动解锁")}
            </button>
          </div>
          <Toast message={message} locale={locale} onClose={() => setMessage("")} />
          {error && <p className="form-error full-field">{error}</p>}
        </form>
      </section>
      {automaticDialog && (
        <VaultAutomaticDialog
          administratorPassword={administratorPassword}
          confirmed={automaticRiskConfirmed}
          error={automaticError}
          saving={automaticSaving}
          locale={locale}
          onPassword={setAdministratorPassword}
          onConfirmed={setAutomaticRiskConfirmed}
          onClose={() => {
            if (automaticSaving) return;
            setAutomaticDialog(false);
            setAdministratorPassword("");
            setAutomaticRiskConfirmed(false);
            setAutomaticError("");
          }}
          onConfirm={() => {
            setAutomaticSaving(true);
            setAutomaticError("");
            void api.setVaultAutomatic(administratorPassword, automaticRiskConfirmed).then(async () => {
              await refresh();
              setMessage(t("已启用自动解锁"));
              setAutomaticDialog(false);
              setAdministratorPassword("");
              setAutomaticRiskConfirmed(false);
            }).catch((cause) => setAutomaticError(cause instanceof Error ? cause.message : t("设置失败"))).finally(() => setAutomaticSaving(false));
          }}
        />
      )}
    </>
  );
}

function VaultAutomaticDialog({ administratorPassword, confirmed, error, saving, locale, onPassword, onConfirmed, onClose, onConfirm }: { administratorPassword: string; confirmed: boolean; error: string; saving: boolean; locale: Locale; onPassword(value: string): void; onConfirmed(value: boolean): void; onClose(): void; onConfirm(): void }) {
  const t = (source: string) => translate(locale, source);
  const dialogRef = useRef<HTMLFormElement>(null);
  useModalFocus(dialogRef, () => { if (!saving) onClose(); });
  return <ModalPortal>
    <form ref={dialogRef} className="dialog" role="dialog" aria-modal="true" aria-labelledby="automatic-vault-title" onSubmit={(event) => { event.preventDefault(); onConfirm(); }}>
      <header><div><h2 id="automatic-vault-title">{t("确认降低秘密库保护强度")}</h2><p>{t("启用前请确认风险和管理员身份。")}</p></div></header>
      <div className="dialog-body">
        <p className="warning-text">{t("自动解锁会把解锁材料保存在本机。主机或服务账号失陷时，攻击者可能直接读取托管的仓库、数据库和 SSH 秘密。")}</p>
        <label>{t("当前管理员密码")}<input type="password" autoComplete="current-password" value={administratorPassword} onChange={(event) => onPassword(event.target.value)} /></label>
        <label><input type="checkbox" checked={confirmed} onChange={(event) => onConfirmed(event.target.checked)} /> {t("我理解主机失陷后托管秘密可被自动解锁")}</label>
        {error && <p className="form-error">{error}</p>}
      </div>
      <footer>
        <button className="secondary-button" type="button" disabled={saving} onClick={onClose}>{t("取消")}</button>
        <button className="danger-button" type="submit" disabled={!administratorPassword || !confirmed || saving}>{t(saving ? "正在保存…" : "确认启用自动解锁")}</button>
      </footer>
    </form>
  </ModalPortal>;
}

function UnlockScreen({
  error,
  locale,
  onUnlock,
}: {
  error?: string;
  locale: Locale;
  onUnlock(passphrase: string): Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const [submitting, setSubmitting] = useState(false);
  return (
    <main className="access-layout">
      <form
        className="access-panel"
        onSubmit={(event) => {
          event.preventDefault();
          const form = new FormData(event.currentTarget);
          setSubmitting(true);
          void onUnlock(String(form.get("passphrase"))).finally(() =>
            setSubmitting(false),
          );
        }}
      >
        <div className="brand">
          <span className="brand-mark"><img src="/shadoc-icon.png" alt="" /></span>
          <span>影刻 <small>Shadoc</small></span>
        </div>
        <h1>{t("解锁秘密库")}</h1>
        <p>{t("应用已重启。输入独立秘密库口令后，计划与备份功能才会恢复。")}</p>
        {error && <p className="form-error">{error}</p>}
        <label>
          {t("秘密库口令")}
          <input name="passphrase" type="password" required autoFocus />
        </label>
        <button className="primary-button" type="submit" disabled={submitting}>
          {t(submitting ? "正在解锁…" : "解锁")}
        </button>
      </form>
    </main>
  );
}

function AccessScreen({
  title,
  action,
  mode,
  error: initialError,
  onSubmit,
  locale,
}: {
  title: string;
  action: string;
  mode: "setup" | "login";
  locale: Locale;
  error?: string;
  onSubmit(
    mode: "setup" | "login",
    username: string,
    password: string,
    token?: string,
  ): Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [passwordConfirmation, setPasswordConfirmation] = useState("");
  const [error, setError] = useState(initialError ?? "");
  const [submitting, setSubmitting] = useState(false);
  const [token, setToken] = useState("");
  return (
    <main className="access-layout">
      <form
        className="access-panel"
        onSubmit={(event) => {
          event.preventDefault();
          setSubmitting(true);
          setError("");
          if (mode === "setup" && password !== passwordConfirmation) {
            setError("两次输入的密码不一致");
            setSubmitting(false);
            return;
          }
          void onSubmit(mode, username, password, token).catch((cause) => {
            setError(cause instanceof Error ? cause.message : "请求失败");
            setSubmitting(false);
          });
        }}
      >
        <div className="brand">
          <span className="brand-mark"><img src="/shadoc-icon.png" alt="" /></span>
          <span>影刻 <small>Shadoc</small></span>
        </div>
        <h1>{title}</h1>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <label>
          {t("管理员名称")}
          <input
            autoComplete="username"
            required
            minLength={3}
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
        </label>
        {mode === "setup" && (
          <label>
            {t("首次初始化令牌（仅 LAN 安装需要）")}
            <input
              type="password"
              autoComplete="off"
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
          </label>
        )}
        <label>
          {t("密码")}
          <input
            type="password"
            autoComplete={
              mode === "setup" ? "new-password" : "current-password"
            }
            required
            minLength={12}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </label>
        {mode === "setup" && (
          <label>
            {t("再次输入密码")}
            <input
              type="password"
              autoComplete="new-password"
              required
              minLength={12}
              value={passwordConfirmation}
              onChange={(event) => setPasswordConfirmation(event.target.value)}
            />
          </label>
        )}
        <button className="primary-button" type="submit" disabled={submitting}>
          {submitting ? t("请稍候…") : action}
        </button>
      </form>
    </main>
  );
}
