import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI, DashboardTask } from "./App";
import { OperationFeedback, useOperation } from "./OperationFeedback";

type VerificationSchedule = {
  kind: "daily" | "weekly" | "interval";
  timeOfDay?: string;
  dayOfWeek?: number;
  intervalHours?: number;
};

export type RestoreVerificationPolicy = {
  taskId: string;
  schedule: VerificationSchedule;
  timezone: string;
  selectionPath: string;
  maximumBytes: number;
  maximumSuccessAgeHours: number;
  enabled: boolean;
  catchUpWindowMinutes: number;
  nextRun?: string;
  lastScheduleStatus?: string;
};

export type RestoreVerificationRecord = {
  id: string;
  taskId: string;
  repositoryId: string;
  snapshotId: string;
  selectionPath: string;
  trigger: "manual" | "scheduled";
  status: string;
  startedAt: string;
  finishedAt?: string;
  fileCount: number;
  byteCount: number;
  manifestSha256?: string;
  cleanupStatus: string;
  errorSummary?: string;
};

type RestoreVerificationOverview = {
  policies: RestoreVerificationPolicy[];
  records: RestoreVerificationRecord[];
  cleanupRequired: RestoreVerificationRecord[];
};

type VerificationTask = {
  id: string;
  name: string;
  engine?: string;
  kind: string;
  enabled?: boolean;
  executionTarget?: { kind?: string };
};

type PolicyForm = {
  scheduleKind: VerificationSchedule["kind"];
  timeOfDay: string;
  dayOfWeek: number;
  intervalHours: number;
  timezone: string;
  selectionPath: string;
  maximumMiB: number;
  maximumSuccessAgeHours: number;
  catchUpWindowMinutes: number;
  enabled: boolean;
};

const emptyOverview: RestoreVerificationOverview = { policies: [], records: [], cleanupRequired: [] };

export function RestoreVerificationPanel({ api, locale }: { api: AppAPI; locale: Locale }) {
  const t = (source: string) => translate(locale, source);
  const [tasks, setTasks] = useState<VerificationTask[]>([]);
  const [dashboardTasks, setDashboardTasks] = useState<Array<DashboardTask & { latestVerifiedRestore?: RestoreVerificationRecord }>>([]);
  const [overview, setOverview] = useState<RestoreVerificationOverview>(emptyOverview);
  const [taskID, setTaskID] = useState("");
  const [form, setForm] = useState<PolicyForm>(() => defaultForm());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [refresh, setRefresh] = useState(0);
  const operation = useOperation(api);
  const handledOperation = useRef("");

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    void Promise.all([
      api.listResource("tasks"),
      api.dashboard(),
      api.action("/api/restore-verifications"),
    ]).then(([taskItems, dashboard, verification]) => {
      if (!active) return;
      const eligible = (taskItems as VerificationTask[]).filter((task) => task.kind === "directory" && (task.engine ?? "restic") === "restic" && (task.executionTarget?.kind ?? "local") === "local");
      setTasks(eligible);
      setDashboardTasks(Array.isArray(dashboard.tasks) ? dashboard.tasks as Array<DashboardTask & { latestVerifiedRestore?: RestoreVerificationRecord }> : []);
      const candidate = verification as Partial<RestoreVerificationOverview>;
      setOverview({
        policies: Array.isArray(candidate.policies) ? candidate.policies : [],
        records: Array.isArray(candidate.records) ? candidate.records : [],
        cleanupRequired: Array.isArray(candidate.cleanupRequired) ? candidate.cleanupRequired : [],
      });
      setTaskID((current) => eligible.some((task) => task.id === current) ? current : eligible[0]?.id ?? "");
    }).catch((reason) => {
      if (active) setError(reason instanceof Error ? reason.message : t("无法读取恢复验证状态"));
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, [api, refresh]);

  const policy = overview.policies.find((item) => item.taskId === taskID);
  useLayoutEffect(() => {
    setForm(policy ? formFromPolicy(policy) : defaultForm());
  }, [taskID, policy]);

  useEffect(() => {
    setMessage("");
    setError("");
    handledOperation.current = "";
  }, [taskID]);

  useEffect(() => {
    const record = operation.operation;
    if (!record || !["success", "failed", "cancelled", "cleanup_required"].includes(record.status)) return;
    const key = `${record.id}:${record.status}`;
    if (handledOperation.current === key) return;
    handledOperation.current = key;
    setMessage(record.status === "success" ? t("恢复验证操作已完成") : record.errorSummary || t("恢复验证操作未成功"));
    setRefresh((value) => value + 1);
  }, [locale, operation.operation]);

  const selectedTask = tasks.find((item) => item.id === taskID);
  const dashboardTask = dashboardTasks.find((item) => item.id === taskID);
  const records = overview.records.filter((item) => item.taskId === taskID);
  const latestAttempt = records[0];
  const latestVerified = dashboardTask?.latestVerifiedRestore ?? records.find((item) => item.status === "success");
  const cleanupRequired = overview.cleanupRequired.filter((item) => item.taskId === taskID);

  const save = async () => {
    if (!taskID) return;
    setSaving(true);
    setError("");
    setMessage("");
    try {
      const schedule: VerificationSchedule = { kind: form.scheduleKind };
      if (form.scheduleKind === "interval") schedule.intervalHours = form.intervalHours;
      if (form.scheduleKind === "daily" || form.scheduleKind === "weekly") schedule.timeOfDay = form.timeOfDay;
      if (form.scheduleKind === "weekly") schedule.dayOfWeek = form.dayOfWeek;
      await api.saveRestoreVerificationPolicy(taskID, {
        schedule, timezone: form.timezone, selectionPath: form.selectionPath.trim(),
        maximumBytes: Math.round(form.maximumMiB * 1024 * 1024), maximumSuccessAgeHours: form.maximumSuccessAgeHours,
        catchUpWindowMinutes: form.catchUpWindowMinutes, enabled: form.enabled,
      });
      setMessage(t("恢复验证策略已保存"));
      setRefresh((value) => value + 1);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("恢复验证策略保存失败"));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!taskID || !policy) return;
    setSaving(true);
    setError("");
    try {
      await api.deleteRestoreVerificationPolicy(taskID);
      setMessage(t("恢复验证策略已删除"));
      setRefresh((value) => value + 1);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : t("恢复验证策略删除失败"));
    } finally {
      setSaving(false);
    }
  };

  return <section className="content-section restore-verification-panel" aria-labelledby="restore-verification-title">
    <div className="section-heading">
      <div>
        <h2 id="restore-verification-title">{t("自动恢复验证")}</h2>
        <p>{t("系统把选定的快照路径恢复到应用专属临时目录，逐字节读取并记录哈希，随后删除临时内容；不会写入或覆盖源目录。")}</p>
      </div>
      <button className="primary-button" type="button" disabled={!taskID || !policy || operation.active || selectedTask?.enabled === false} onClick={() => void operation.start(`/api/tasks/${encodeURIComponent(taskID)}/restore-verification/run`, {})}>{t("立即执行恢复验证")}</button>
    </div>
    {loading ? <p>{t("正在读取恢复验证状态…")}</p> : !tasks.length ? <p>{t("暂无可配置的本机 Restic 目录任务")}</p> : <>
      <div className="form-grid restore-verification-form">
        <label className="full-field">{t("备份任务")}<select value={taskID} onChange={(event) => setTaskID(event.target.value)}>{tasks.map((task) => <option key={task.id} value={task.id}>{task.name}{task.enabled === false ? ` · ${t("已停用")}` : ""}</option>)}</select></label>
        <label className="full-field">{t("验证路径")}<input aria-label={t("验证路径")} value={form.selectionPath} onChange={(event) => setForm({ ...form, selectionPath: event.target.value })} placeholder="album/sample.jpg" required /><span className="field-hint">{t("填写快照内相对于任务源目录的文件或目录路径。")}</span></label>
        <label>{t("计划类型")}<select value={form.scheduleKind} onChange={(event) => setForm({ ...form, scheduleKind: event.target.value as PolicyForm["scheduleKind"] })}><option value="interval">{t("间隔")}</option><option value="daily">{t("每日")}</option><option value="weekly">{t("每周")}</option></select></label>
        {form.scheduleKind === "interval" ? <label>{t("间隔小时数")}<input type="number" min="1" max="8760" value={form.intervalHours} onChange={(event) => setForm({ ...form, intervalHours: Number(event.target.value) })} /></label> : <label>{t("执行时间")}<input type="time" value={form.timeOfDay} onChange={(event) => setForm({ ...form, timeOfDay: event.target.value })} /></label>}
        {form.scheduleKind === "weekly" && <label>{t("星期")}<select value={form.dayOfWeek} onChange={(event) => setForm({ ...form, dayOfWeek: Number(event.target.value) })}>{weekdayOptions(locale).map((label, day) => <option key={day} value={day}>{label}</option>)}</select></label>}
        <label>{t("时区")}<input value={form.timezone} onChange={(event) => setForm({ ...form, timezone: event.target.value })} /></label>
        <label>{t("最大恢复量（MiB）")}<input type="number" min="1" max="1048576" value={form.maximumMiB} onChange={(event) => setForm({ ...form, maximumMiB: Number(event.target.value) })} /></label>
        <label>{t("最长成功间隔（小时）")}<input type="number" min="1" max="8760" value={form.maximumSuccessAgeHours} onChange={(event) => setForm({ ...form, maximumSuccessAgeHours: Number(event.target.value) })} /></label>
        <label>{t("离线补跑窗口（分钟）")}<input type="number" min="0" max="10080" value={form.catchUpWindowMinutes} onChange={(event) => setForm({ ...form, catchUpWindowMinutes: Number(event.target.value) })} /></label>
        <label className="full-field checkbox-field"><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} /> {t("启用自动恢复验证")}</label>
        <div className="full-field form-actions">
          <button className="primary-button" type="button" disabled={saving || operation.active || !form.selectionPath.trim()} onClick={() => void save()}>{t("保存恢复验证策略")}</button>
          {policy && <button className="text-button" type="button" disabled={saving || operation.active} onClick={() => void remove()}>{t("删除恢复验证策略")}</button>}
        </div>
      </div>

      <div className="restore-proof-grid">
        <EvidenceCard title={t("最近完整备份")} empty={t("尚无完整成功备份")} values={dashboardTask?.lastCompleteBackup ? [
          [t("快照 ID"), dashboardTask.lastCompleteBackup.snapshotId],
          [t("完成时间"), formatTime(dashboardTask.lastCompleteBackup.finishedAt ?? dashboardTask.lastCompleteBackup.startedAt, locale)],
        ] : []} />
        <EvidenceCard title={t("最近验证成功")} empty={t("尚无成功恢复验证")} values={latestVerified ? verificationValues(latestVerified, locale, t) : []} />
      </div>
      {policy && <p className="field-help">{t("下次验证")}：{policy.nextRun ? formatTime(policy.nextRun, locale) : t("尚未安排")}；{t("最近计划状态")}：{policy.lastScheduleStatus ? verificationStatus(policy.lastScheduleStatus, t) : t("尚无记录")}</p>}
      {latestAttempt && latestAttempt.status !== "success" && <p className="warning-text" role="status">{t("最近一次验证")}：{verificationStatus(latestAttempt.status, t)}{latestAttempt.errorSummary ? ` · ${latestAttempt.errorSummary}` : ""}</p>}
      {cleanupRequired.map((record) => <div className="restore-cleanup-callout" role="alert" key={record.id}><span>{t("临时内容仍需清理")} · {record.id}</span><button className="danger-button" type="button" aria-label={`${t("重试清理")} ${record.id}`} disabled={operation.active} onClick={() => void operation.start(`/api/restore-verifications/${encodeURIComponent(record.id)}/cleanup`, {})}>{t("重试清理")}</button></div>)}
    </>}
    {message && <p className="success-message" role="status">{message}</p>}
    {error && <p className="error-message" role="alert">{error}</p>}
    <OperationFeedback operation={operation} locale={locale} />
  </section>;
}

function defaultForm(): PolicyForm {
  return {
    scheduleKind: "interval", timeOfDay: "03:00", dayOfWeek: 0, intervalHours: 168,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC", selectionPath: "",
    maximumMiB: 1024, maximumSuccessAgeHours: 24 * 8, catchUpWindowMinutes: 60, enabled: false,
  };
}

function formFromPolicy(policy: RestoreVerificationPolicy): PolicyForm {
  return {
    scheduleKind: policy.schedule.kind,
    timeOfDay: policy.schedule.timeOfDay ?? "03:00",
    dayOfWeek: policy.schedule.dayOfWeek ?? 0,
    intervalHours: policy.schedule.intervalHours ?? 168,
    timezone: policy.timezone,
    selectionPath: policy.selectionPath,
    maximumMiB: Math.max(1, policy.maximumBytes / 1024 / 1024),
    maximumSuccessAgeHours: policy.maximumSuccessAgeHours,
    catchUpWindowMinutes: policy.catchUpWindowMinutes,
    enabled: policy.enabled,
  };
}

function weekdayOptions(locale: Locale): string[] {
  const formatter = new Intl.DateTimeFormat(locale, { weekday: "long", timeZone: "UTC" });
  return Array.from({ length: 7 }, (_, day) => formatter.format(new Date(Date.UTC(2026, 6, 12 + day))));
}

function EvidenceCard({ title, empty, values }: { title: string; empty: string; values: string[][] }) {
  return <article className="restore-proof-card"><h3>{title}</h3>{values.length ? <dl>{values.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value || "—"}</dd></div>)}</dl> : <p>{empty}</p>}</article>;
}

function verificationValues(record: RestoreVerificationRecord, locale: Locale, t: (source: string) => string): string[][] {
  return [
    [t("快照 ID"), record.snapshotId],
    [t("完成时间"), formatTime(record.finishedAt ?? record.startedAt, locale)],
    [t("耗时"), duration(record.startedAt, record.finishedAt, locale)],
    [t("读取数据"), `${new Intl.NumberFormat(locale).format(record.fileCount)} ${t("个文件")} · ${formatBytes(record.byteCount, locale)}`],
    [t("清理状态"), record.cleanupStatus === "removed" ? t("已清理") : t("待清理")],
    ["SHA-256", record.manifestSha256 ?? "—"],
  ];
}

function duration(startedAt: string, finishedAt: string | undefined, locale: Locale): string {
  if (!finishedAt) return "—";
  const milliseconds = new Date(finishedAt).getTime() - new Date(startedAt).getTime();
  return Number.isFinite(milliseconds) && milliseconds >= 0 ? `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(milliseconds / 1000)}s` : "—";
}

function formatBytes(bytes: number, locale: Locale): string {
  if (bytes < 1024) return `${new Intl.NumberFormat(locale).format(bytes)} B`;
  if (bytes < 1024 * 1024) return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(bytes / 1024)} KiB`;
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(bytes / 1024 / 1024)} MiB`;
}

function formatTime(value: string | undefined, locale: Locale): string {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium" }).format(date);
}

function verificationStatus(status: string, t: (source: string) => string): string {
  return t(({ success: "成功", failed: "失败", cancelled: "已取消", interrupted: "已中断", cleanup_required: "待清理", missed: "已错过", running: "运行中", pending: "等待执行" } as Record<string, string>)[status] ?? status);
}
