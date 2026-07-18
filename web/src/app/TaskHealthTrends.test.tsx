import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { TaskHealthTrends } from "./TaskHealthTrends";

const report = {
  generatedAt: "2026-07-15T15:00:00Z",
  eligibleStatuses: ["success", "partial", "failed"],
  excludedStatuses: ["queued", "running", "cancelled", "skipped"],
  tasks: [{
    taskId: "task-a", taskName: "照片", engine: "restic", latestCompleteSuccessAt: "2026-07-15T14:00:00Z",
    windows: [
      { windowDays: 7, windowStart: "2026-07-08T15:00:00Z", windowEnd: "2026-07-15T15:00:00Z", eligibleCount: 3, completeSuccessCount: 1, partialCount: 1, failedCount: 1, excludedCount: 2, successRate: 33.3, retryCount: 2, averageDurationMilliseconds: 4000, p95DurationMilliseconds: 6000, bytesChanged: 2048, filesChanged: 6, metricCoverage: { duration: 3, filesProcessed: 0, filesChanged: 3, bytesProcessed: 0, bytesChanged: 3 } },
      { windowDays: 30, windowStart: "2026-06-15T15:00:00Z", windowEnd: "2026-07-15T15:00:00Z", eligibleCount: 4, completeSuccessCount: 2, partialCount: 1, failedCount: 1, excludedCount: 2, successRate: 50, retryCount: 3, averageDurationMilliseconds: 5000, p95DurationMilliseconds: 8000, bytesChanged: 4096, filesChanged: 10, metricCoverage: { duration: 4, filesProcessed: 0, filesChanged: 4, bytesProcessed: 0, bytesChanged: 4 } },
      { windowDays: 90, windowStart: "2026-04-16T15:00:00Z", windowEnd: "2026-07-15T15:00:00Z", eligibleCount: 5, completeSuccessCount: 3, partialCount: 1, failedCount: 1, excludedCount: 2, successRate: 60, retryCount: 3, averageDurationMilliseconds: 6000, p95DurationMilliseconds: 10000, metricCoverage: { duration: 5, filesProcessed: 0, filesChanged: 0, bytesProcessed: 0, bytesChanged: 0 } },
    ],
    daily: [{ date: "2026-07-14", eligibleCount: 1, completeSuccessCount: 1, partialCount: 0, failedCount: 0, excludedCount: 0, retryCount: 0, averageDurationMilliseconds: 4000, bytesChanged: 1024, metricCoverage: { duration: 1, filesProcessed: 0, filesChanged: 0, bytesProcessed: 0, bytesChanged: 1 } }],
  }],
};

describe("TaskHealthTrends", () => {
  it("keeps the dashboard summary compact and opens the task detail page", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async () => report);
    const onOpenTask = vi.fn();
    render(<TaskHealthTrends api={{ action } as unknown as AppAPI} locale="zh-CN" onOpenTask={onOpenTask} />);

    const section = await screen.findByRole("region", { name: "任务健康趋势" });
    expect(within(section).getByText("50%（2/4）")).toBeVisible();
    expect(within(section).getByText(/分母只包含完整成功、部分成功和失败/)).toBeVisible();
    expect(within(section).getByText(/另排除 2 次/)).toBeVisible();
    expect(within(section).getByText(/数据更新于/)).toBeVisible();
    expect(within(section).queryByText("变化数据量")).not.toBeInTheDocument();
    expect(within(section).queryByText("运行耗时")).not.toBeInTheDocument();
    expect(within(section).queryByText("查看每日趋势")).not.toBeInTheDocument();

    await user.click(within(section).getByRole("button", { name: "详情" }));
    expect(onOpenTask).toHaveBeenCalledWith("task-a");
    expect(action).toHaveBeenCalledWith("/api/task-trends");
  });
});
