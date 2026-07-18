import { useCallback, useEffect, useRef, useState } from "react";
import { translate, type Locale } from "../i18n";
import type { AppAPI } from "./App";
import { OperationFeedback, useOperation } from "./OperationFeedback";
import { StatusIndicator, type StatusTone } from "./StatusIndicator";

const gibibyte = 1024 ** 3;
const maximumSafeGiB = Math.floor(Number.MAX_SAFE_INTEGER / gibibyte);

type Capacity = {
  totalBytes: number;
  usedBytes?: number;
  availableBytes: number;
  checkedAt: string;
  sourceAgentId?: string;
};

export type RepositoryCapacityPolicy = {
  repositoryId: string;
  enabled: boolean;
  probeIntervalMinutes: number;
  minimumAvailableBytes: number;
  minimumAvailablePercent: number;
  exhaustionWarningDays: number;
  nextProbeAt?: string;
  lastAttemptAt?: string;
  lastSuccessAt?: string;
  lastError?: string;
  updatedAt: string;
  stale?: boolean;
};

type CapacitySample = Capacity & {
  id: number;
  repositoryId: string;
  usedBytes: number;
};

type CapacityForecast = {
  status: "ready" | "insufficient_samples" | "insufficient_span" | "non_positive_growth" | "beyond_supported_range";
  sampleCount: number;
  observationStartedAt?: string;
  observationEndedAt?: string;
  growthBytesPerDay?: number;
  estimatedExhaustionAt?: string;
};

type CapacityForm = {
  enabled: boolean;
  probeIntervalMinutes: string;
  minimumAvailableGiB: string;
  minimumAvailablePercent: string;
  exhaustionWarningDays: string;
};

export function RepositoryCapacityPanel({
  api,
  repository,
  locale,
  timeZone,
  onClose,
  onUpdated,
}: {
  api: AppAPI;
  repository: Record<string, unknown>;
  locale: Locale;
  timeZone: string;
  onClose(): void;
  onUpdated(): Promise<void>;
}) {
  const t = (source: string) => translate(locale, source);
  const repositoryId = String(repository.id ?? "");
  const repositoryName = String(repository.name ?? repositoryId);
  const capacity = asCapacity(repository.capacity);
  const manualProbe = useOperation(api);
  const handledOperation = useRef("");
  const loadRequest = useRef(0);
  const [policy, setPolicy] = useState<RepositoryCapacityPolicy | null>(null);
  const [samples, setSamples] = useState<CapacitySample[]>([]);
  const [forecast, setForecast] = useState<CapacityForecast | null>(null);
  const [form, setForm] = useState<CapacityForm | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [refreshKey, setRefreshKey] = useState(0);

  const load = useCallback(async () => {
    const request = ++loadRequest.current;
    setLoading(true);
    setError("");
    try {
      const [policyValue, sampleValue, forecastValue] = await Promise.all([
        api.action(`/api/repositories/${encodeURIComponent(repositoryId)}/capacity-policy`),
        api.action(`/api/repositories/${encodeURIComponent(repositoryId)}/capacity-samples?limit=30`),
        api.action(`/api/repositories/${encodeURIComponent(repositoryId)}/capacity-forecast`),
      ]);
      if (loadRequest.current !== request) return;
      const loadedPolicy = policyValue as RepositoryCapacityPolicy;
      setPolicy(loadedPolicy);
      setSamples(Array.isArray(sampleValue) ? sampleValue as CapacitySample[] : []);
      setForecast(forecastValue as CapacityForecast);
      setForm(policyForm(loadedPolicy));
    } catch (cause) {
      if (loadRequest.current === request) setError(cause instanceof Error ? cause.message : t("无法读取容量健康状态"));
    } finally {
      if (loadRequest.current === request) setLoading(false);
    }
  }, [api, locale, repositoryId]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void load();
    return () => { loadRequest.current += 1; };
  }, [load, refreshKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const operation = manualProbe.operation;
    if (!operation || !["success", "partial", "failed", "cancelled", "cleanup_required"].includes(operation.status)) return;
    const key = `${operation.id}:${operation.status}`;
    if (handledOperation.current === key) return;
    handledOperation.current = key;
    setRefreshKey((current) => current + 1);
    void onUpdated();
  }, [manualProbe.operation, onUpdated]);

  const panelLabel = locale === "en-US" ? `Capacity health for ${repositoryName}` : `${repositoryName}容量健康`;
  return (
    <section className="content-section repository-capacity-panel" aria-label={panelLabel}>
      <div className="section-heading capacity-panel-heading">
        <div>
          <h2>{t("容量健康")}</h2>
          <p>{locale === "en-US" ? `Durable capacity status and monitoring policy for ${repositoryName}.` : `${repositoryName}的持久化容量状态与后台监控策略。`}</p>
        </div>
        <button className="secondary-button" type="button" onClick={onClose}>{t("关闭容量健康")}</button>
      </div>
      {loading && <p role="status">{t("正在读取容量健康状态…")}</p>}
      {error && <p className="error-message" role="alert">{error}</p>}
      {!loading && policy && form && <>
        <div className="capacity-health-summary">
          <CapacitySummary capacity={capacity} policy={policy} locale={locale} timeZone={timeZone} />
          <div className="capacity-health-actions">
            <button
              className="primary-button"
              type="button"
              disabled={manualProbe.active || repository.status !== "ready"}
              onClick={() => {
                handledOperation.current = "";
                void manualProbe.start(`/api/repositories/${encodeURIComponent(repositoryId)}/capacity`, {});
              }}
            >{t(manualProbe.active ? "正在检测容量…" : "立即检测容量")}</button>
            <small>{t("手动检测会创建可追踪的持久化操作；后台检测不依赖此页面保持打开。")}</small>
          </div>
        </div>
        <OperationFeedback operation={manualProbe} locale={locale} />

        <form className="capacity-policy-form" noValidate onSubmit={(event) => {
          event.preventDefault();
          const validation = validateCapacityForm(form, locale);
          if (validation.error) {
            setError(validation.error);
            return;
          }
          setSaving(true);
          setError("");
          setMessage("");
          void api.saveRepositoryCapacityPolicy(repositoryId, validation.value!).then((saved) => {
            const loaded = saved as RepositoryCapacityPolicy;
            setPolicy(loaded);
            setForm(policyForm(loaded));
            setMessage(t("容量策略已保存"));
            void onUpdated();
          }).catch((cause) => {
            setError(cause instanceof Error ? cause.message : t("无法保存容量策略"));
          }).finally(() => setSaving(false));
        }}>
          <div className="capacity-policy-heading">
            <div><h3>{t("后台容量策略")}</h3><p>{t("停用策略后不再自动检测，也不产生容量阈值、预测、陈旧或探测失败告警。")}</p></div>
            <label className="checkbox-field"><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} />{t("启用后台容量监控")}</label>
          </div>
          <div className="form-grid">
            <label>{t("后台检测间隔（分钟）")}<input type="number" min="15" max="10080" step="1" required value={form.probeIntervalMinutes} onChange={(event) => setForm({ ...form, probeIntervalMinutes: event.target.value })} /></label>
            <label>{t("最低可用容量（GiB）")}<input type="number" min="0" max={maximumSafeGiB} step="0.1" required value={form.minimumAvailableGiB} onChange={(event) => setForm({ ...form, minimumAvailableGiB: event.target.value })} /></label>
            <label>{t("最低可用比例（%）")}<input type="number" min="0" max="100" step="0.1" required value={form.minimumAvailablePercent} onChange={(event) => setForm({ ...form, minimumAvailablePercent: event.target.value })} /></label>
            <label>{t("预计耗尽预警（天）")}<input type="number" min="0" max="3650" step="1" required value={form.exhaustionWarningDays} onChange={(event) => setForm({ ...form, exhaustionWarningDays: event.target.value })} /></label>
            <p className="field-hint full-field">{t("绝对容量、百分比或预测阈值填 0 表示不启用该项判断；停用后台监控会暂停全部容量告警。")}</p>
            <button className="primary-button" type="submit" disabled={saving}>{t(saving ? "正在保存…" : "保存容量策略")}</button>
            {message && <p className="success-message" role="status">{message}</p>}
          </div>
        </form>

        <CapacityForecastView forecast={forecast} policy={policy} locale={locale} timeZone={timeZone} />
        <CapacitySamples samples={samples} locale={locale} timeZone={timeZone} />
      </>}
    </section>
  );
}

function CapacitySummary({ capacity, policy, locale, timeZone }: { capacity: Capacity | null; policy: RepositoryCapacityPolicy; locale: Locale; timeZone: string }) {
  const t = (source: string) => translate(locale, source);
  const status = !policy.enabled ? "后台监控已停用" : policy.stale ? "数据已过期" : policy.lastError ? "最近检测失败" : policy.lastSuccessAt ? "数据新鲜" : "等待首次后台检测";
  const tone: StatusTone = !policy.enabled ? "stopped" : policy.lastError || policy.stale ? "warning" : policy.lastSuccessAt ? "active" : "pending";
  const source = capacity?.sourceAgentId ? `Agent ${capacity.sourceAgentId}` : "Service";
  return <dl className="capacity-health-facts">
    <div><dt>{t("监控状态")}</dt><dd><StatusIndicator value="unknown" locale={locale} label={t(status)} tone={tone} variant="pill" /></dd></div>
    <div><dt>{t("最新容量")}</dt><dd>{capacity ? capacityText(capacity, locale) : t("尚无成功检测结果")}</dd></div>
    <div><dt>{t("检测来源")}</dt><dd>{capacity ? source : "—"}</dd></div>
    <div><dt>{t("最近成功")}</dt><dd>{formatDateTime(policy.lastSuccessAt, locale, timeZone)}</dd></div>
    <div><dt>{t("下次后台检测")}</dt><dd>{policy.enabled ? formatDateTime(policy.nextProbeAt, locale, timeZone) : t("后台监控已停用")}</dd></div>
    {policy.lastError && <div className="wide capacity-health-error"><dt>{t("最近检测错误")}</dt><dd>{policy.lastError}</dd></div>}
  </dl>;
}

function CapacityForecastView({ forecast, policy, locale, timeZone }: { forecast: CapacityForecast | null; policy: RepositoryCapacityPolicy; locale: Locale; timeZone: string }) {
  const t = (source: string) => translate(locale, source);
  if (!forecast) return null;
  let result = t("容量预测暂不可用");
  if (forecast.status === "ready" && forecast.estimatedExhaustionAt) {
    result = locale === "en-US"
      ? `Estimated exhaustion: ${formatDateTime(forecast.estimatedExhaustionAt, locale, timeZone)} at ${formatBytes(forecast.growthBytesPerDay ?? 0, locale)} of used-space growth per day.`
      : `预计耗尽：${formatDateTime(forecast.estimatedExhaustionAt, locale, timeZone)}；已用容量每天增长 ${formatBytes(forecast.growthBytesPerDay ?? 0, locale)}。`;
  } else if (forecast.status === "insufficient_samples") result = t("样本不足，至少需要 3 个有效样本");
  else if (forecast.status === "insufficient_span") result = t("观察跨度不足 24 小时");
  else if (forecast.status === "non_positive_growth") result = t("近期已用容量没有正向增长，无需估算耗尽时间");
  else if (forecast.status === "beyond_supported_range") result = t("预计耗尽时间超出支持范围");
  return <section className="capacity-forecast" aria-labelledby="capacity-forecast-title">
    <h3 id="capacity-forecast-title">{t("容量预测")}</h3>
    <p className="capacity-forecast-result">{result}</p>
    <p>{locale === "en-US"
      ? `The forecast requires at least 3 valid samples spanning 24 hours and only uses positive used-space growth. The warning horizon is ${policy.exhaustionWarningDays} days.`
      : `预测至少需要 3 个有效样本且时间跨度达到 24 小时，只使用正向已用容量增长；当前预警窗口为 ${policy.exhaustionWarningDays} 天。`}</p>
    <p>{locale === "en-US" ? `${forecast.sampleCount} retained samples are currently available.` : `当前共有 ${forecast.sampleCount} 个保留样本。`}</p>
  </section>;
}

function CapacitySamples({ samples, locale, timeZone }: { samples: CapacitySample[]; locale: Locale; timeZone: string }) {
  const t = (source: string) => translate(locale, source);
  return <section className="capacity-samples" aria-labelledby="capacity-samples-title">
    <div className="section-heading"><div><h3 id="capacity-samples-title">{t("容量样本历史")}</h3><p>{t("最多显示最近 30 个持久化成功样本；更早样本按生命周期策略清理。")}</p></div></div>
    <div className="table-frame"><table aria-label={t("容量样本历史")}>
      <thead><tr><th>{t("检测时间")}</th><th>{t("可用容量")}</th><th>{t("已用容量")}</th><th>{t("总容量")}</th><th>{t("来源")}</th></tr></thead>
      <tbody>{samples.map((sample) => <tr key={sample.id}><td>{formatDateTime(sample.checkedAt, locale, timeZone)}</td><td>{formatBytes(sample.availableBytes, locale)}</td><td>{formatBytes(sample.usedBytes, locale)}</td><td>{formatBytes(sample.totalBytes, locale)}</td><td>{sample.sourceAgentId ? `Agent ${sample.sourceAgentId}` : "Service"}</td></tr>)}
      {samples.length === 0 && <tr><td className="empty-row" colSpan={5}>{t("尚无容量样本")}</td></tr>}</tbody>
    </table></div>
  </section>;
}

function policyForm(policy: RepositoryCapacityPolicy): CapacityForm {
  return {
    enabled: policy.enabled,
    probeIntervalMinutes: String(policy.probeIntervalMinutes),
    minimumAvailableGiB: trimDecimal(policy.minimumAvailableBytes / gibibyte),
    minimumAvailablePercent: trimDecimal(policy.minimumAvailablePercent),
    exhaustionWarningDays: String(policy.exhaustionWarningDays),
  };
}

function validateCapacityForm(form: CapacityForm, locale: Locale): { value?: Record<string, unknown>; error?: string } {
  const interval = Number(form.probeIntervalMinutes);
  const minimumGiB = Number(form.minimumAvailableGiB);
  const minimumPercent = Number(form.minimumAvailablePercent);
  const warningDays = Number(form.exhaustionWarningDays);
  if (!Number.isInteger(interval) || interval < 15 || interval > 10080) return { error: translate(locale, "检测间隔必须在 15 分钟到 7 天之间") };
  if (!Number.isFinite(minimumGiB) || minimumGiB < 0 || minimumGiB > maximumSafeGiB) return { error: translate(locale, "最低可用容量超出支持范围") };
  if (!Number.isFinite(minimumPercent) || minimumPercent < 0 || minimumPercent > 100) return { error: translate(locale, "最低可用比例必须在 0% 到 100% 之间") };
  if (!Number.isInteger(warningDays) || warningDays < 0 || warningDays > 3650) return { error: translate(locale, "预计耗尽预警必须在 0 到 3650 天之间") };
  return { value: {
    enabled: form.enabled,
    probeIntervalMinutes: interval,
    minimumAvailableBytes: Math.round(minimumGiB * gibibyte),
    minimumAvailablePercent: minimumPercent,
    exhaustionWarningDays: warningDays,
  } };
}

function asCapacity(value: unknown): Capacity | null {
  if (!value || typeof value !== "object") return null;
  const capacity = value as Partial<Capacity>;
  if (!Number.isFinite(capacity.totalBytes) || !Number.isFinite(capacity.availableBytes) || Number(capacity.totalBytes) <= 0 || Number(capacity.availableBytes) < 0 || Number(capacity.availableBytes) > Number(capacity.totalBytes) || typeof capacity.checkedAt !== "string") return null;
  return capacity as Capacity;
}

function capacityText(capacity: Capacity, locale: Locale) {
  return locale === "en-US"
    ? `${formatBytes(capacity.availableBytes, locale)} available of ${formatBytes(capacity.totalBytes, locale)}`
    : `${formatBytes(capacity.availableBytes, locale)} 可用，共 ${formatBytes(capacity.totalBytes, locale)}`;
}

function formatDateTime(value: string | undefined, locale: Locale, timeZone: string) {
  if (!value) return "—";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short", timeZone }).format(parsed);
}

function formatBytes(value: number, locale: Locale) {
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let amount = value;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: amount < 10 && unit > 0 ? 1 : 0 }).format(amount)} ${units[unit]}`;
}

function trimDecimal(value: number) {
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(3)));
}
