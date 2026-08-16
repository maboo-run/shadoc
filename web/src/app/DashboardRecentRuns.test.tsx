import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DashboardRecentRuns } from "./DashboardRecentRuns";
import type { DashboardTask } from "./App";

const tasks: DashboardTask[] = [
  { id: "t1", name: "web-data", kind: "directory", status: "success", repository: "repo-1", lastRun: "2026-07-20T06:30:00Z", nextRun: "-" },
  { id: "t2", name: "db-prod", kind: "database", status: "failed", repository: "repo-2", lastRun: "2026-07-20T06:12:00Z", nextRun: "-" },
];

describe("DashboardRecentRuns", () => {
  it("orders tasks by their latest execution time instead of task name", () => {
    render(<DashboardRecentRuns
      tasks={[
        { id: "older", name: "aardvark", kind: "directory", status: "success", repository: "repo-1", lastRun: "2026-07-26T02:12:46Z", nextRun: "-" },
        { id: "newer", name: "zebra", kind: "directory", status: "success", repository: "repo-2", lastRun: "2026-08-03T21:30:00Z", nextRun: "-" },
      ]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      onViewAll={() => undefined}
    />);

    const rows = screen.getAllByRole("row");
    expect(rows[1]).toHaveTextContent("zebra");
    expect(rows[2]).toHaveTextContent("aardvark");
  });

  it("renders at most 5 recent tasks with name, status, and time", () => {
    render(<DashboardRecentRuns tasks={tasks} locale="zh-CN" timeZone="UTC" onViewAll={() => undefined} />);
    expect(screen.getByText("web-data")).toBeInTheDocument();
    expect(screen.getByText("db-prod")).toBeInTheDocument();
  });

  it("shows empty state when no tasks", () => {
    render(<DashboardRecentRuns tasks={[]} locale="zh-CN" timeZone="UTC" onViewAll={() => undefined} />);
    expect(screen.getByText("尚未创建备份任务")).toBeInTheDocument();
  });

  it("invokes onViewAll when the link is clicked", async () => {
    const onViewAll = vi.fn();
    render(<DashboardRecentRuns tasks={tasks} locale="zh-CN" timeZone="UTC" onViewAll={onViewAll} />);
    await userEvent.click(screen.getByRole("button", { name: /查看全部/ }));
    expect(onViewAll).toHaveBeenCalledOnce();
  });
});
