import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DashboardAlerts } from "./DashboardAlerts";
import type { Dashboard } from "./App";

const dashboard: Dashboard = {
  tasks: [],
  alerts: [
    { id: "a1", object: "db-prod", message: "备份失败", severity: "critical", lastAt: "2026-07-20T06:12:00Z", targetPage: "备份任务" },
    { id: "a2", object: "logs-sync", message: "部分成功", severity: "warning", lastAt: "2026-07-20T05:58:00Z" },
    { id: "a3", object: "repo-mirror", message: "容量 82%", severity: "info", lastAt: "2026-07-20T04:00:00Z" },
  ],
};

describe("DashboardAlerts", () => {
  it("renders at most 3 alerts with severity tone", () => {
    render(<DashboardAlerts dashboard={dashboard} locale="zh-CN" timeZone="UTC" onViewAll={() => undefined} />);
    expect(screen.getByText("备份失败")).toBeInTheDocument();
    expect(screen.getByText("部分成功")).toBeInTheDocument();
    expect(screen.getByText("容量 82%")).toBeInTheDocument();
  });

  it("shows empty state when no alerts", () => {
    render(<DashboardAlerts dashboard={{ tasks: [], alerts: [] }} locale="zh-CN" timeZone="UTC" onViewAll={() => undefined} />);
    expect(screen.getByText("当前无告警")).toBeInTheDocument();
  });

  it("invokes onViewAll when the link is clicked", async () => {
    const onViewAll = vi.fn();
    render(<DashboardAlerts dashboard={dashboard} locale="zh-CN" timeZone="UTC" onViewAll={onViewAll} />);
    await userEvent.click(screen.getByRole("button", { name: /查看全部/ }));
    expect(onViewAll).toHaveBeenCalledOnce();
  });

  it("invokes onHandle when the handle button is clicked", async () => {
    const onHandle = vi.fn();
    render(<DashboardAlerts dashboard={dashboard} locale="zh-CN" timeZone="UTC" onViewAll={() => undefined} onHandle={onHandle} />);
    const handleButtons = screen.getAllByRole("button", { name: /处理/ });
    await userEvent.click(handleButtons[0]);
    expect(onHandle).toHaveBeenCalledWith("备份任务");
  });
});
