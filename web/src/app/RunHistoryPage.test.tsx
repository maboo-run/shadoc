import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { RunHistoryPage } from "./RunHistoryPage";

beforeEach(() => window.history.replaceState({}, "", "/admin/runs?status=failed&engine=restic"));

describe("RunHistoryPage", () => {
  it("loads server filters, navigates numbered pages and exports the same selection", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      const requestedPage = Number(new URL(path, "http://localhost").searchParams.get("page") ?? "1");
      if (requestedPage === 2) return {
        items: [{ recordType: "operation", id: "operation-old", kind: "directory_restore", status: "failed", objectName: "仓库", occurredAt: "2026-07-01T01:00:00Z", attemptCount: 1, errorSummary: "target unavailable" }],
        page: 2, pageSize: 50, total: 500, truncated: true, generatedAt: "2026-07-15T15:00:00Z", filter: {},
      };
      if (requestedPage > 2) return {
        items: [{ recordType: "run", id: `run-page-${requestedPage}`, kind: "backup", engine: "restic", status: "failed", objectName: "照片", occurredAt: "2026-07-01T01:00:00Z", attemptCount: 1 }],
        page: requestedPage, pageSize: 50, total: 500, truncated: requestedPage < 10, generatedAt: "2026-07-15T15:00:00Z", filter: {},
      };
      return {
        items: [{ recordType: "run", id: "run-new", kind: "backup", engine: "restic", status: "failed", trigger: "manual", objectName: "照片", occurredAt: "2026-07-15T01:00:00Z", startedAt: "2026-07-15T01:00:00Z", finishedAt: "2026-07-15T01:01:00Z", attemptCount: 2, errorSummary: "safe failure", metrics: { durationMilliseconds: 60000, bytesChanged: 2048 } }],
        page: 1, pageSize: 50, total: 500, truncated: true, generatedAt: "2026-07-15T15:00:00Z", filter: {},
      };
    });
    render(<RunHistoryPage api={{ action, runDetail: vi.fn(), runLog: vi.fn() } as unknown as AppAPI} locale="zh-CN" />);

    expect(await screen.findByText("run-new")).toBeVisible();
    expect(action).toHaveBeenCalledWith(expect.stringMatching(/^\/api\/activity\?.*engine=restic.*status=failed/));
    expect(screen.getByText("2.0 KiB")).toBeVisible();
    expect(screen.queryByRole("heading", { name: "运行记录" })).not.toBeInTheDocument();
    expect(screen.getByText("共 500 条 · 第 1/10 页")).toBeVisible();
    expect(screen.getByRole("button", { name: "1" })).toHaveAttribute("aria-current", "page");
    const exportLink = screen.getByRole("link", { name: "导出当前筛选" });
    expect(exportLink).toHaveAttribute("href", expect.stringContaining("/api/activity/export?"));
    expect(exportLink).toHaveAttribute("href", expect.stringContaining("status=failed"));

    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(await screen.findByText("operation-old")).toBeVisible();
    expect(screen.getByText("target unavailable")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "上一页" }));
    expect(await screen.findByText("run-new")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "10" }));
    expect(await screen.findByText("run-page-10")).toBeVisible();
    expect(window.location.search).toContain("page=10");
    await user.clear(screen.getByLabelText("跳转页码"));
    await user.type(screen.getByLabelText("跳转页码"), "7");
    await user.click(screen.getByRole("button", { name: "跳转" }));
    expect(await screen.findByText("run-page-7")).toBeVisible();
    expect(window.location.search).toContain("page=7");

    await user.clear(screen.getByLabelText("对象 ID"));
    await user.type(screen.getByLabelText("对象 ID"), "task-a");
    await user.selectOptions(screen.getByLabelText("记录类型"), "run");
    await user.click(screen.getByRole("button", { name: "应用筛选" }));
    await waitFor(() => expect(window.location.search).toContain("objectId=task-a"));
    expect(window.location.search).toContain("recordType=run");
    expect(action).toHaveBeenLastCalledWith(expect.stringContaining("objectId=task-a"));
  });

  it("loads one run detail and its raw log only after the administrator opens it", async () => {
    const user = userEvent.setup();
    const runDetail = vi.fn(async () => ({ id: "run-one", status: "failed", attemptCount: 2, summary: { error: "safe failure" }, rawLogExpired: false }));
    const runLog = vi.fn(async () => "safe log line");
    const action = vi.fn(async () => ({ items: [{ recordType: "run", id: "run-one", kind: "backup", status: "failed", objectName: "任务", occurredAt: "2026-07-15T01:00:00Z", attemptCount: 2 }], truncated: false, generatedAt: "2026-07-15T15:00:00Z", filter: {} }));
    render(<RunHistoryPage api={{ action, runDetail, runLog } as unknown as AppAPI} locale="zh-CN" />);

    const row = (await screen.findByText("run-one")).closest("tr")!;
    expect(runDetail).not.toHaveBeenCalled();
    await user.click(within(row).getByRole("button", { name: "查看详情" }));
    expect(await screen.findByText("safe log line")).toBeVisible();
    expect(screen.getByText("错误")).toBeVisible();
    expect(screen.getByText("safe failure")).toBeVisible();
    expect(screen.queryByText('{"error":"safe failure"}')).not.toBeInTheDocument();
    expect(runDetail).toHaveBeenCalledWith("run-one");
    expect(runLog).toHaveBeenCalledWith("run-one");
    expect(screen.getByRole("link", { name: "下载日志" })).toHaveAttribute("href", "/api/runs/run-one/log?download=1");
  });
});
