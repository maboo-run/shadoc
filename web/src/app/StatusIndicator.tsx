import { translate, type Locale } from "../i18n";

export type StatusTone = "active" | "success" | "stopped" | "pending" | "warning" | "danger" | "info";

type StatusPresentation = {
  label: string;
  tone: StatusTone;
};

const presentations: Record<string, StatusPresentation> = {
  running: { label: "运行中", tone: "active" },
  online: { label: "在线", tone: "active" },
  healthy: { label: "正常", tone: "active" },
  success: { label: "成功", tone: "success" },
  ready: { label: "已验证", tone: "success" },
  enabled: { label: "已启用", tone: "success" },
  valid: { label: "有效", tone: "success" },
  compatible: { label: "兼容", tone: "success" },
  delivered: { label: "已送达", tone: "success" },
  stopped: { label: "已停止", tone: "stopped" },
  disabled: { label: "已停用", tone: "stopped" },
  cancelled: { label: "已取消", tone: "stopped" },
  skipped: { label: "已跳过", tone: "stopped" },
  revoked: { label: "身份已撤销", tone: "stopped" },
  draft: { label: "草稿（不可启用）", tone: "stopped" },
  idle: { label: "尚未运行", tone: "pending" },
  queued: { label: "等待执行", tone: "pending" },
  pending: { label: "等待执行", tone: "pending" },
  retrying: { label: "等待重试", tone: "pending" },
  partial: { label: "部分成功", tone: "warning" },
  cleanup_required: { label: "需要清理", tone: "warning" },
  uninitialized: { label: "尚未初始化", tone: "warning" },
  disconnected: { label: "等待只读验证", tone: "warning" },
  interrupted: { label: "已中断", tone: "warning" },
  warning: { label: "警告", tone: "warning" },
  failed: { label: "失败", tone: "danger" },
  failed_final: { label: "最终失败", tone: "danger" },
  blocker: { label: "阻断", tone: "danger" },
  blocked: { label: "阻断", tone: "danger" },
  critical: { label: "严重", tone: "danger" },
  expired: { label: "已过期", tone: "danger" },
  incompatible: { label: "不兼容", tone: "danger" },
  info: { label: "信息", tone: "info" },
  unknown: { label: "状态未知", tone: "warning" },
};

export function statusPresentation(value: string): StatusPresentation {
  return presentations[value] ?? presentations.unknown;
}

export function statusLabel(value: string, locale: Locale): string {
  return translate(locale, statusPresentation(value).label);
}

export function StatusIndicator({
  value,
  locale,
  label,
  tone,
  variant = "inline",
}: {
  value: string;
  locale: Locale;
  label?: string;
  tone?: StatusTone;
  variant?: "inline" | "pill";
}) {
  const presentation = statusPresentation(value);
  const visibleLabel = label ?? translate(locale, presentation.label);
  const visibleTone = tone ?? presentation.tone;
  const ariaLabel = `${locale === "en-US" ? "Status: " : "状态："}${visibleLabel}`;

  return (
    <span
      className={`status-indicator status-${visibleTone}${variant === "pill" ? " status-indicator-pill" : ""}`}
      role="status"
      aria-label={ariaLabel}
    >
      <span className="status-indicator-dot" aria-hidden="true" />
      {visibleLabel}
    </span>
  );
}
