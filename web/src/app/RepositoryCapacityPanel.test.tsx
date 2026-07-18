import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { RepositoryCapacityPanel } from "./RepositoryCapacityPanel";

const gibibyte = 1024 ** 3;
const repository = {
  id: "repo-a",
  name: "照片仓库",
  status: "ready",
  capacity: {
    totalBytes: 100 * gibibyte,
    availableBytes: 8 * gibibyte,
    usedBytes: 92 * gibibyte,
    checkedAt: "2026-07-14T08:00:00Z",
    sourceAgentId: "agent-a",
  },
};
const policy = {
  repositoryId: "repo-a",
  enabled: true,
  probeIntervalMinutes: 60,
  minimumAvailableBytes: 5 * gibibyte,
  minimumAvailablePercent: 10,
  exhaustionWarningDays: 30,
  nextProbeAt: "2026-07-15T09:00:00Z",
  lastAttemptAt: "2026-07-15T08:00:00Z",
  lastSuccessAt: "2026-07-14T08:00:00Z",
  lastError: "Agent 暂时不可用",
  updatedAt: "2026-07-15T07:00:00Z",
  stale: true,
};
const samples = [
  { id: 3, repositoryId: "repo-a", totalBytes: 100 * gibibyte, usedBytes: 92 * gibibyte, availableBytes: 8 * gibibyte, checkedAt: "2026-07-14T08:00:00Z", sourceAgentId: "agent-a" },
  { id: 2, repositoryId: "repo-a", totalBytes: 100 * gibibyte, usedBytes: 90 * gibibyte, availableBytes: 10 * gibibyte, checkedAt: "2026-07-13T08:00:00Z", sourceAgentId: "agent-a" },
  { id: 1, repositoryId: "repo-a", totalBytes: 100 * gibibyte, usedBytes: 88 * gibibyte, availableBytes: 12 * gibibyte, checkedAt: "2026-07-12T08:00:00Z", sourceAgentId: "agent-a" },
];
const forecast = {
  status: "ready",
  sampleCount: 3,
  observationStartedAt: "2026-07-12T08:00:00Z",
  observationEndedAt: "2026-07-14T08:00:00Z",
  growthBytesPerDay: 2 * gibibyte,
  estimatedExhaustionAt: "2026-07-18T08:00:00Z",
};

function capacityAPI(overrides: Partial<AppAPI> = {}) {
  const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
    if (path === "/api/repositories/repo-a/capacity-policy" && payload === undefined) return policy;
    if (path === "/api/repositories/repo-a/capacity-samples?limit=30") return samples;
    if (path === "/api/repositories/repo-a/capacity-forecast") return forecast;
    if (path === "/api/repositories/repo-a/capacity") return { operationId: "capacity-op", status: "queued" };
    if (path === "/api/operations/capacity-op") return { id: "capacity-op", kind: "repository_capacity_probe", status: "success", stage: "completed" };
    return {};
  });
  return {
    action,
    saveRepositoryCapacityPolicy: vi.fn(async () => ({ ...policy, probeIntervalMinutes: 120 })),
    ...overrides,
  } as unknown as AppAPI;
}

describe("RepositoryCapacityPanel", () => {
  it("shows durable freshness, source, bounded history and explicit forecast assumptions without probing on mount", async () => {
    const api = capacityAPI();
    render(<RepositoryCapacityPanel api={api} repository={repository} locale="zh-CN" timeZone="Asia/Shanghai" onClose={() => undefined} onUpdated={async () => undefined} />);

    const panel = await screen.findByRole("region", { name: "照片仓库容量健康" });
    expect(within(panel).getByText("数据已过期")).toBeVisible();
    expect(within(panel).getByText(/Agent 暂时不可用/)).toBeVisible();
    expect(within(panel).getAllByText(/Agent agent-a/).length).toBeGreaterThan(0);
    expect(within(panel).getByText(/下次后台检测/)).toBeVisible();
    expect(within(panel).getByRole("table", { name: "容量样本历史" })).toBeVisible();
    expect(within(panel).getByText(/至少需要 3 个有效样本且时间跨度达到 24 小时/)).toBeVisible();
    expect(within(panel).getByText(/预计耗尽：/)).toBeVisible();
    expect(api.action).not.toHaveBeenCalledWith("/api/repositories/repo-a/capacity", {});
  });

  it("validates unit-aware policy fields and explains zero and disabled semantics", async () => {
    const user = userEvent.setup();
    const saveRepositoryCapacityPolicy = vi.fn(async (_id: string, value: Record<string, unknown>) => ({ ...policy, ...value }));
    const onUpdated = vi.fn(async () => undefined);
    const api = capacityAPI({ saveRepositoryCapacityPolicy });
    render(<RepositoryCapacityPanel api={api} repository={repository} locale="zh-CN" timeZone="Asia/Shanghai" onClose={() => undefined} onUpdated={onUpdated} />);
    await screen.findByRole("region", { name: "照片仓库容量健康" });

    expect(screen.getByText(/阈值填 0 表示不启用该项判断/)).toBeVisible();
    await user.clear(screen.getByLabelText("后台检测间隔（分钟）"));
    await user.type(screen.getByLabelText("后台检测间隔（分钟）"), "5");
    await user.click(screen.getByRole("button", { name: "保存容量策略" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("检测间隔必须在 15 分钟到 7 天之间");
    expect(saveRepositoryCapacityPolicy).not.toHaveBeenCalled();

    await user.clear(screen.getByLabelText("后台检测间隔（分钟）"));
    await user.type(screen.getByLabelText("后台检测间隔（分钟）"), "120");
    await user.clear(screen.getByLabelText("最低可用容量（GiB）"));
    await user.type(screen.getByLabelText("最低可用容量（GiB）"), "6");
    await user.click(screen.getByRole("button", { name: "保存容量策略" }));
    await waitFor(() => expect(saveRepositoryCapacityPolicy).toHaveBeenCalledWith("repo-a", expect.objectContaining({
      enabled: true,
      probeIntervalMinutes: 120,
      minimumAvailableBytes: 6 * gibibyte,
      minimumAvailablePercent: 10,
      exhaustionWarningDays: 30,
    })));
    await waitFor(() => expect(onUpdated).toHaveBeenCalled());
  });

  it("starts a persistent manual probe only on explicit request and refreshes durable state after completion", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn(async () => undefined);
    const api = capacityAPI();
    render(<RepositoryCapacityPanel api={api} repository={repository} locale="zh-CN" timeZone="Asia/Shanghai" onClose={() => undefined} onUpdated={onUpdated} />);
    await screen.findByRole("region", { name: "照片仓库容量健康" });

    await user.click(screen.getByRole("button", { name: "立即检测容量" }));
    await waitFor(() => expect(api.action).toHaveBeenCalledWith("/api/repositories/repo-a/capacity", {}));
    await waitFor(() => expect(onUpdated).toHaveBeenCalled());
    expect(api.action).toHaveBeenCalledWith("/api/operations/capacity-op");
  });
});
