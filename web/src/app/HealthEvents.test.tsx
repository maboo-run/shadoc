import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { HealthEvents } from "./HealthEvents";

describe("HealthEvents", () => {
  it("shows current actionable alerts, retained recovery history, and notification failures", async () => {
    const action = vi.fn(async () => ({
      active: [{ stateKey: "agent:a:offline", kind: "agent_offline", severity: "critical", status: "active", objectType: "agent", objectId: "a", objectName: "agent-a", reason: "Agent 离线", message: "最后心跳已过期", targetPage: "Agent 节点", recoveryCondition: "Agent 恢复有效心跳", firstAt: "2026-07-15T01:00:00Z", lastAt: "2026-07-15T02:00:00Z", occurrenceCount: 2 }],
      events: [{ id: 2, stateKey: "repository:r:integrity", kind: "repository_abnormal", severity: "critical", status: "resolved", objectType: "repository", objectId: "r", objectName: "异地仓库", reason: "仓库状态异常", message: "检查失败", targetPage: "备份仓库", recoveryCondition: "仓库检查通过", occurredAt: "2026-07-15T03:00:00Z", transition: "resolved", occurrenceCount: 1 }],
      deliveries: [{ id: 3, notificationId: "notification-1", occurredAt: "2026-07-15T02:30:00Z", channel: "ntfy", stateKey: "agent:a:offline", transition: "critical", attempt: 3, maxAttempts: 3, status: "failed_final", errorSummary: "ntfy returned status 503" }],
    }));
    const onNavigate = vi.fn();
    const view = render(<HealthEvents api={{ action }} locale="zh-CN" timeZone="Asia/Shanghai" view="alerts" onNavigate={onNavigate} />);

    const current = await screen.findByRole("region", { name: "当前保护告警" });
    expect(within(current).getByText("agent-a")).toBeVisible();
    expect(within(current).getByText("Agent 恢复有效心跳")).toBeVisible();
    expect(screen.getByRole("heading", { name: "告警历史" }).parentElement).toHaveTextContent("恢复");
    expect(screen.queryByRole("heading", { name: "通知投递记录" })).not.toBeInTheDocument();
    expect(action).toHaveBeenCalledWith("/api/alerts?limit=100");
    await userEvent.setup().click(within(current).getByRole("button", { name: "处理" }));
    expect(onNavigate).toHaveBeenCalledWith("Agent 节点", "a", "agent_offline");

    view.rerender(<HealthEvents api={{ action }} locale="zh-CN" timeZone="Asia/Shanghai" view="deliveries" />);
    expect(await screen.findByRole("heading", { name: "通知投递记录" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "告警历史" })).not.toBeInTheDocument();
    expect(screen.getByText("ntfy returned status 503")).toBeVisible();
  });
});
