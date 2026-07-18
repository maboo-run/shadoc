import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { RestoreVerificationPanel } from "./RestoreVerificationPanel";

const task = {
  id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: true,
  executionTarget: { kind: "local" },
};

const policy = {
  taskId: "task-a", schedule: { kind: "interval", intervalHours: 24 }, timezone: "UTC",
  selectionPath: "album/sample.jpg", maximumBytes: 1048576, maximumSuccessAgeHours: 168,
  enabled: true, catchUpWindowMinutes: 60, nextRun: "2026-07-16T02:00:00Z",
};

const verification = {
  id: "verification-a", taskId: "task-a", repositoryId: "repo-a", snapshotId: "snapshot-a",
  selectionPath: "album/sample.jpg", trigger: "scheduled", status: "success", startedAt: "2026-07-15T02:00:00Z",
  finishedAt: "2026-07-15T02:00:03Z", fileCount: 1, byteCount: 7, manifestSha256: "sha256:verified", cleanupStatus: "removed",
};

function apiFixture(overrides: Partial<AppAPI> = {}): AppAPI {
  return {
    listResource: vi.fn(async () => [task]),
    dashboard: vi.fn(async () => ({ tasks: [{ ...task, status: "success", repository: "仓库", lastRun: "2026-07-15T01:00:00Z", nextRun: "—", lastCompleteBackup: { snapshotId: "snapshot-a", startedAt: "2026-07-15T01:00:00Z", finishedAt: "2026-07-15T01:00:02Z" }, latestVerifiedRestore: verification }], alerts: [] })),
    action: vi.fn(async (path: string) => path === "/api/restore-verifications" ? {
      policies: [{ ...policy, schedule: { ...policy.schedule } }], records: [{ ...verification }], cleanupRequired: [],
    } : {}),
    saveRestoreVerificationPolicy: vi.fn(async () => policy),
    deleteRestoreVerificationPolicy: vi.fn(async () => undefined),
    ...overrides,
  } as unknown as AppAPI;
}

describe("RestoreVerificationPanel", () => {
  it("pairs the latest complete backup with durable restore evidence and saves a structured policy", async () => {
    const user = userEvent.setup();
    const saveRestoreVerificationPolicy = vi.fn(async () => policy);
    const api = apiFixture({ saveRestoreVerificationPolicy });
    render(<RestoreVerificationPanel api={api} locale="zh-CN" />);

    expect(await screen.findAllByText("snapshot-a")).toHaveLength(2);
    expect(screen.getByText("sha256:verified")).toBeVisible();
    expect(screen.getByText("已清理")).toBeVisible();
    await user.clear(screen.getByLabelText("验证路径"));
    await user.type(screen.getByLabelText("验证路径"), "album/critical.jpg");
    await user.selectOptions(screen.getByLabelText("计划类型"), "weekly");
    await user.selectOptions(screen.getByLabelText("星期"), "1");
    await user.click(screen.getByRole("button", { name: "保存恢复验证策略" }));

    await waitFor(() => expect(saveRestoreVerificationPolicy).toHaveBeenCalled());
    expect(saveRestoreVerificationPolicy).toHaveBeenCalledWith("task-a", expect.objectContaining({
      selectionPath: "album/critical.jpg",
      schedule: expect.objectContaining({ kind: "weekly", dayOfWeek: 1 }),
      maximumBytes: 1048576,
    }));
    await waitFor(() => expect(api.action).toHaveBeenCalledTimes(2));
    expect(screen.getByText("恢复验证策略已保存")).toBeVisible();
  });

  it("starts a persistent manual verification and a safe cleanup retry", async () => {
    const user = userEvent.setup();
    const cleanup = { ...verification, id: "verification-cleanup", status: "cleanup_required", cleanupStatus: "required" };
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/restore-verifications") return { policies: [policy], records: [cleanup], cleanupRequired: [cleanup] };
      if (path === "/api/tasks/task-a/restore-verification/run" && payload) return { operationId: "operation-run", status: "queued" };
      if (path === "/api/restore-verifications/verification-cleanup/cleanup" && payload) return { operationId: "operation-cleanup", status: "queued" };
      if (path === "/api/operations/operation-run") return { id: "operation-run", kind: "restore_verification", status: "success", stage: "success" };
      if (path === "/api/operations/operation-cleanup") return { id: "operation-cleanup", kind: "restore_verification_cleanup", status: "success", stage: "success" };
      return {};
    });
    render(<RestoreVerificationPanel api={apiFixture({ action })} locale="zh-CN" />);

    await user.click(await screen.findByRole("button", { name: "立即执行恢复验证" }));
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/tasks/task-a/restore-verification/run", {}));
    await waitFor(() => expect(screen.getByText("操作完成")).toBeVisible());
    await user.click(screen.getByRole("button", { name: "重试清理 verification-cleanup" }));
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/restore-verifications/verification-cleanup/cleanup", {}));
  });
});
