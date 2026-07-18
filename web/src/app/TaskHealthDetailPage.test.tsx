import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { TaskHealthDetailPage } from "./TaskHealthDetailPage";

const trendReport = {
  generatedAt: "2026-07-16T14:06:00Z",
  eligibleStatuses: ["success", "partial", "failed"],
  excludedStatuses: ["queued", "running", "cancelled", "skipped"],
  tasks: [{
    taskId: "task-a",
    taskName: "照片备份",
    engine: "restic",
    latestCompleteSuccessAt: "2026-07-16T13:30:00Z",
    windows: [
      { windowDays: 7, windowStart: "2026-07-09T14:06:00Z", windowEnd: "2026-07-16T14:06:00Z", eligibleCount: 2, completeSuccessCount: 1, partialCount: 0, failedCount: 1, excludedCount: 1, successRate: 50, retryCount: 1, averageDurationMilliseconds: 3000, p95DurationMilliseconds: 5000, metricCoverage: { duration: 2, filesProcessed: 1, filesChanged: 1, bytesProcessed: 1, bytesChanged: 1 } },
      { windowDays: 30, windowStart: "2026-06-16T14:06:00Z", windowEnd: "2026-07-16T14:06:00Z", eligibleCount: 3, completeSuccessCount: 2, partialCount: 0, failedCount: 1, excludedCount: 1, successRate: 66.7, retryCount: 1, averageDurationMilliseconds: 4000, p95DurationMilliseconds: 6000, metricCoverage: { duration: 3, filesProcessed: 2, filesChanged: 2, bytesProcessed: 2, bytesChanged: 2 } },
      { windowDays: 90, windowStart: "2026-04-17T14:06:00Z", windowEnd: "2026-07-16T14:06:00Z", eligibleCount: 4, completeSuccessCount: 3, partialCount: 0, failedCount: 1, excludedCount: 1, successRate: 75, retryCount: 2, averageDurationMilliseconds: 5000, p95DurationMilliseconds: 7000, metricCoverage: { duration: 4, filesProcessed: 3, filesChanged: 3, bytesProcessed: 3, bytesChanged: 3 } },
    ],
    daily: [],
  }],
};

const activityPage = {
  generatedAt: "2026-07-16T14:06:00Z",
  truncated: false,
  items: [
    {
      recordType: "run", id: "run-complete", kind: "backup", engine: "restic", status: "success", trigger: "manual",
      objectId: "task-a", objectName: "照片备份", occurredAt: "2026-07-16T13:29:55Z", startedAt: "2026-07-16T13:29:55Z", finishedAt: "2026-07-16T13:30:00Z", attemptCount: 1,
      metrics: { durationMilliseconds: 5000, filesProcessed: 120, filesChanged: 8, bytesProcessed: 10485760, bytesChanged: 2097152 },
    },
    {
      recordType: "run", id: "run-old", kind: "backup", engine: "restic", status: "success", trigger: "schedule",
      objectId: "task-a", objectName: "照片备份", occurredAt: "2026-07-15T13:29:55Z", startedAt: "2026-07-15T13:29:55Z", finishedAt: "2026-07-15T13:30:00Z", attemptCount: 1,
    },
  ],
};

describe("TaskHealthDetailPage", () => {
  it("shows real per-run copy metrics and renders missing measurements as a dash", async () => {
    const user = userEvent.setup();
    const onBack = vi.fn();
    const action = vi.fn(async (path: string) => path === "/api/task-trends" ? trendReport : activityPage);
    render(<TaskHealthDetailPage taskId="task-a" api={{ action } as unknown as AppAPI} locale="zh-CN" onBack={onBack} />);

    expect(await screen.findByRole("heading", { name: "照片备份" })).toBeVisible();
    expect(screen.getByText("66.7%（2/3）")).toBeVisible();
    expect(await screen.findByText("10.0 MiB")).toBeVisible();
    expect(screen.getByText("2.0 MiB")).toBeVisible();
    expect(screen.getByText("120")).toBeVisible();
    expect(screen.getByText("8")).toBeVisible();

    const oldRow = screen.getByText("run-old").closest("tr")!;
    expect(within(oldRow).getAllByText("—")).toHaveLength(6);
    expect(screen.queryByText("指标不可用")).not.toBeInTheDocument();
    expect(action).toHaveBeenCalledWith("/api/task-trends");
    expect(action.mock.calls.some(([path]) => String(path).startsWith("/api/activity?recordType=run&objectId=task-a"))).toBe(true);

    await user.click(screen.getByRole("button", { name: "返回任务列表" }));
    expect(onBack).toHaveBeenCalledOnce();

    await user.click(screen.getByRole("button", { name: "过去 7 天" }));
    await waitFor(() => expect(screen.getByText("50%（1/2）")).toBeVisible());
  });

  it("shows the bounded failure reason returned for a failed run", async () => {
    const failurePage = {
      ...activityPage,
      items: [{
        recordType: "run", id: "run-failed", kind: "backup", engine: "rsync", status: "failed", trigger: "manual",
        objectId: "task-a", objectName: "照片备份", occurredAt: "2026-07-16T13:29:55Z", startedAt: "2026-07-16T13:29:55Z", finishedAt: "2026-07-16T13:30:00Z", attemptCount: 1,
        errorSummary: "run rsync: exit status 1: rsync: unrecognized option `--protect-args'",
      }],
    };
    const action = vi.fn(async (path: string) => path === "/api/task-trends" ? trendReport : failurePage);
    const runDetail = vi.fn(async () => ({ id: "run-failed", status: "failed", summary: { error: failurePage.items[0].errorSummary }, rawLogExpired: false }));
    const runLog = vi.fn(async () => "rsync: unrecognized option `--protect-args'");
    render(<TaskHealthDetailPage taskId="task-a" api={{ action, runDetail, runLog } as unknown as AppAPI} locale="zh-CN" onBack={() => undefined} />);

    const row = (await screen.findByText("run-failed")).closest("tr")!;
    expect(within(row).getByText("run rsync: exit status 1: rsync: unrecognized option `--protect-args'")).toBeVisible();
    expect(within(row).getByText("失败原因")).toBeVisible();
    await userEvent.setup().click(within(row).getByRole("button", { name: "查看失败详情" }));
    const dialog = await screen.findByRole("dialog", { name: "运行详情" });
    expect(within(dialog).getByText("rsync: unrecognized option `--protect-args'")).toBeVisible();
  });
});
