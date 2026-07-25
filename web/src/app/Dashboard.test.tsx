import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Dashboard } from "./Dashboard";
import type { AppAPI, Dashboard as DashboardData } from "./App";

const fakeAPI: Pick<AppAPI, "listResource"> = {
  listResource: vi.fn(async (resource: string) => {
    if (resource === "agents") return [{ id: "a1", status: "online", runtimeStatus: "running" }];
    return [];
  }),
} as unknown as Pick<AppAPI, "listResource">;

const dashboard: DashboardData = {
  tasks: [
    { id: "t1", name: "web-data", kind: "directory", status: "success", repository: "r1", lastRun: "2026-07-20T06:30:00Z", nextRun: "-" },
  ],
  alerts: [],
  repositoryStatus: "healthy",
  runOverview: { total: 100, succeeded: 98, failed: 1, partial: 1, successRate: 98.4 },
};

describe("Dashboard", () => {
  it("renders page header, KPIs, and all panels", async () => {
    render(<Dashboard api={fakeAPI as AppAPI} locale="zh-CN" timeZone="UTC" dashboard={dashboard} onNavigate={() => undefined} />);
    expect(screen.getByRole("heading", { name: "仪表盘" })).toBeInTheDocument();
    expect(screen.getByText("98.4%")).toBeInTheDocument();
    expect(await screen.findByLabelText("AGENT 在线")).toHaveTextContent("1/1");
    expect(screen.getByRole("heading", { name: /运行趋势/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /最近运行/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /调度覆盖/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /当前告警/ })).toBeInTheDocument();
  });

  it("invokes onNavigate with 备份任务 when the create button is clicked", async () => {
    const onNavigate = vi.fn();
    render(<Dashboard api={fakeAPI as AppAPI} locale="zh-CN" timeZone="UTC" dashboard={dashboard} onNavigate={onNavigate} />);
    await userEvent.click(screen.getByRole("button", { name: /新建备份任务/ }));
    expect(onNavigate).toHaveBeenCalledWith("备份任务", "?view=create");
  });
});
