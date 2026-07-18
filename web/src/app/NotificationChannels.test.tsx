import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { NotificationChannels } from "./NotificationChannels";

describe("notification channels", () => {
  it("loads ntfy safely and preserves the explicit secret-clear action", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/ntfy" && payload === undefined) return { configured: true, baseUrl: "https://notify.example", topic: "backups", hasToken: true, enabled: true };
      if (path === "/api/webhook" && payload === undefined) return { configured: false };
      return { configured: true };
    });
    render(<NotificationChannels api={{ action } as unknown as AppAPI} locale="zh-CN" />);
    expect(await screen.findByDisplayValue("https://notify.example")).toBeVisible();
    await user.click(screen.getByLabelText("清除已保存令牌"));
    await user.click(screen.getByLabelText("启用 ntfy 通知"));
    await user.click(screen.getByRole("button", { name: "保存通知配置" }));
    expect(action).toHaveBeenCalledWith("/api/ntfy", { baseUrl: "https://notify.example", topic: "backups", token: "", clearToken: true, enabled: false });
  });

  it("saves only a structured webhook and automatically clears an obsolete secret", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/ntfy" && payload === undefined) return { configured: false };
      if (path === "/api/webhook" && payload === undefined) return { configured: true, endpoint: "https://hooks.example.com/alerts", authMode: "bearer", hasSecret: true, enabled: true };
      return { configured: true };
    });
    render(<NotificationChannels api={{ action } as unknown as AppAPI} locale="zh-CN" />);
    await screen.findByDisplayValue("https://hooks.example.com/alerts");
    await user.selectOptions(screen.getByLabelText("认证方式"), "none");
    expect(screen.getByLabelText("清除已保存认证秘密")).toBeChecked();
    await user.click(screen.getByRole("button", { name: "保存 Webhook" }));
    expect(action).toHaveBeenCalledWith("/api/webhook", { endpoint: "https://hooks.example.com/alerts", authMode: "none", secret: "", clearSecret: true, enabled: true });
    const section = screen.getByRole("heading", { name: "Webhook 通知" }).closest("section");
    if (!section) throw new Error("webhook section missing");
    await user.click(within(section).getByRole("button", { name: "发送测试" }));
    expect(action).toHaveBeenCalledWith("/api/webhook/test", {});
  });

  it("does not load or display the removed email channel", async () => {
    const action = vi.fn(async () => ({ configured: false }));
    render(<NotificationChannels api={{ action } as unknown as AppAPI} locale="zh-CN" />);
    await screen.findByRole("heading", { name: "ntfy 通知" });
    expect(await screen.findByLabelText("启用 ntfy 通知")).not.toBeChecked();
    expect(screen.getByLabelText("启用 Webhook 通知")).not.toBeChecked();
    expect(action).not.toHaveBeenCalledWith("/api/email");
    expect(screen.queryByText("邮件通知")).not.toBeInTheDocument();
    expect(screen.getByText(/固定 JSON 格式/)).toBeVisible();
    expect(screen.getByText(/所有通知通道默认关闭/)).toBeVisible();
  });
});
