import { useEffect, useState } from "react";
import { translate, type Locale } from "../i18n";

export type RetentionPolicy = {
  keepWithinDays: number;
  keepLast: number;
  keepHourly: number;
  keepDaily: number;
  keepWeekly: number;
  keepMonthly: number;
  keepYearly: number;
};

export type ResourcePolicy = {
  uploadKiBPerSecond: number;
  downloadKiBPerSecond: number;
  readConcurrency: number;
  compression: "" | "auto" | "off" | "max";
};

export const emptyRetentionPolicy: RetentionPolicy = {
  keepWithinDays: 0,
  keepLast: 0,
  keepHourly: 0,
  keepDaily: 0,
  keepWeekly: 0,
  keepMonthly: 0,
  keepYearly: 0,
};

export const defaultRetentionPolicy: RetentionPolicy = {
  ...emptyRetentionPolicy,
  keepWithinDays: 30,
  keepLast: 3,
};

export const emptyResourcePolicy: ResourcePolicy = {
  uploadKiBPerSecond: 0,
  downloadKiBPerSecond: 0,
  readConcurrency: 0,
  compression: "",
};

export function normalizeRetentionPolicy(value: unknown, fallback: RetentionPolicy = emptyRetentionPolicy): RetentionPolicy {
  const input = value && typeof value === "object" ? value as Record<string, unknown> : {};
  const number = (key: keyof RetentionPolicy) => {
    const parsed = Number(input[key] ?? fallback[key]);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback[key];
  };
  return {
    keepWithinDays: number("keepWithinDays"),
    keepLast: number("keepLast"),
    keepHourly: number("keepHourly"),
    keepDaily: number("keepDaily"),
    keepWeekly: number("keepWeekly"),
    keepMonthly: number("keepMonthly"),
    keepYearly: number("keepYearly"),
  };
}

export function normalizeResourcePolicy(value: unknown, fallback: ResourcePolicy = emptyResourcePolicy): ResourcePolicy {
  const input = value && typeof value === "object" ? value as Record<string, unknown> : {};
  const number = (key: "uploadKiBPerSecond" | "downloadKiBPerSecond" | "readConcurrency") => {
    const parsed = Number(input[key] ?? fallback[key]);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback[key];
  };
  const compression = String(input.compression ?? fallback.compression);
  return {
    uploadKiBPerSecond: number("uploadKiBPerSecond"),
    downloadKiBPerSecond: number("downloadKiBPerSecond"),
    readConcurrency: number("readConcurrency"),
    compression: compression === "auto" || compression === "off" || compression === "max" ? compression : "",
  };
}

type Props = {
  retention: RetentionPolicy;
  onRetentionChange(value: RetentionPolicy): void;
  resources?: ResourcePolicy;
  onResourcesChange?(value: ResourcePolicy): void;
  readOnly?: boolean;
  showRetention?: boolean;
  locale?: Locale;
};

export function ProtectionPolicyEditor({
  retention,
  onRetentionChange,
  resources,
  onResourcesChange,
  readOnly = false,
  showRetention = true,
  locale = "zh-CN",
}: Props) {
  const t = (source: string) => translate(locale, source);
  const advancedConfigured = retention.keepHourly > 0
    || retention.keepDaily > 0
    || retention.keepWeekly > 0
    || retention.keepMonthly > 0
    || retention.keepYearly > 0;
  const [mode, setMode] = useState<"simple" | "advanced">(advancedConfigured ? "advanced" : "simple");
  useEffect(() => {
    if (advancedConfigured) setMode("advanced");
  }, [advancedConfigured]);
  const updateRetention = (key: keyof RetentionPolicy, raw: string) => {
    const value = raw === "" ? 0 : Math.max(0, Number(raw));
    onRetentionChange({ ...retention, [key]: Number.isFinite(value) ? value : 0 });
  };
  const updateResource = (key: keyof ResourcePolicy, raw: string) => {
    if (!resources || !onResourcesChange) return;
    if (key === "compression") {
      onResourcesChange({ ...resources, compression: raw as ResourcePolicy["compression"] });
      return;
    }
    const value = raw === "" ? 0 : Math.max(0, Number(raw));
    onResourcesChange({ ...resources, [key]: Number.isFinite(value) ? value : 0 });
  };
  const retentionInput = (key: keyof RetentionPolicy, label: string) => <label>
    {t(label)}
    <input
      aria-label={t(label)}
      type="number"
      min="0"
      value={retention[key]}
      disabled={readOnly}
      onChange={(event) => updateRetention(key, event.target.value)}
    />
  </label>;

  return <section className="protection-policy-editor full-field" aria-label={t(showRetention ? "保护策略" : "资源策略")}>
    {showRetention && <><div className="policy-section-heading">
      <strong>{t("保留策略")}</strong>
      <span>{t("Restic 会组合应用所有非零保留规则。")}</span>
    </div>
    <div className="segmented-control policy-mode" role="radiogroup" aria-label={t("保留策略模式")}>
      <label className={mode === "simple" ? "selected" : ""}>
        <input type="radio" name="retention-policy-mode" checked={mode === "simple"} disabled={readOnly} onChange={() => setMode("simple")} />
        {t("简单策略")}
      </label>
      <label className={mode === "advanced" ? "selected" : ""}>
        <input type="radio" name="retention-policy-mode" checked={mode === "advanced"} disabled={readOnly} onChange={() => setMode("advanced")} />
        {t("高级策略")}
      </label>
    </div>
    <div className="form-grid policy-fields">
      {retentionInput("keepWithinDays", "保留窗口（天）")}
      {retentionInput("keepLast", "至少保留最近快照数")}
      {mode === "advanced" && <>
        {retentionInput("keepHourly", "每小时保留数")}
        {retentionInput("keepDaily", "每日保留数")}
        {retentionInput("keepWeekly", "每周保留数")}
        {retentionInput("keepMonthly", "每月保留数")}
        {retentionInput("keepYearly", "每年保留数")}
      </>}
    </div>
    {mode === "simple" && advancedConfigured && <p className="field-hint warning-text">{t("高级规则仍会保留并继续生效；切回高级策略可查看或修改。")}</p>}
    </>}
    {resources && onResourcesChange && <>
      <div className="policy-section-heading resource-policy-heading">
        <strong>{t("资源策略")}</strong>
        <span>{t("限制由控制服务写入受控 Restic 参数。")}</span>
      </div>
      <div className="form-grid policy-fields">
        <label>{t("读取并发数")}<input aria-label={t("读取并发数")} type="number" min="0" value={resources.readConcurrency} disabled={readOnly} onChange={(event) => updateResource("readConcurrency", event.target.value)} /></label>
        <label>{t("压缩模式")}<select aria-label={t("压缩模式")} value={resources.compression} disabled={readOnly} onChange={(event) => updateResource("compression", event.target.value)}>
          <option value="">{t("使用 Restic 默认值")}</option>
          <option value="auto">{t("自动")}</option>
          <option value="off">{t("关闭")}</option>
          <option value="max">{t("最大压缩")}</option>
        </select></label>
      </div>
      <p className="field-hint">{t("读取并发填 0 表示不额外限制。")}</p>
    </>}
  </section>;
}
