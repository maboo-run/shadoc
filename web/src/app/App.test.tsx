import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { App, type AppAPI } from "./App";

async function openConnectionPage(user: ReturnType<typeof userEvent.setup>, name: string) {
  const parent = screen.queryByRole("button", { name: "连接管理" }) ?? screen.getByRole("button", { name: "Connections" });
  await user.click(parent);
  await user.click(await screen.findByRole("tab", { name }));
}

const fakeAPI: AppAPI = {
  async setupStatus() {
    return { initialized: true };
  },
  async setup() {
    return { username: "admin" };
  },
  async login() {
    return { username: "admin" };
  },
  async session() {
    return { username: "admin" };
  },
  async logout() {},
  async vaultStatus() {
    return { mode: "automatic" as const, locked: false };
  },
  async unlockVault() {},
  async setVaultLockOnRestart() {},
  async setVaultAutomatic() {},
  async exportControlPlane() { return { blob: new Blob([]), filename: "recovery.rcbundle" }; },
  async preflightControlPlaneImport() {
    return { canImport: false, sourceApplicationVersion: "1.2.3", resourceCounts: {}, conflicts: [], missingTools: [], revalidation: [], excludedTransientClasses: [], restartRequired: false, warnings: [] };
  },
  async importControlPlane() { return { operationId: "op-import", status: "queued" }; },
  async agentServiceStatus() {
    return { enabled: true, running: true, port: 9443, advertisedHost: "control.internal", listenAddress: "0.0.0.0:9443", serviceUrl: "https://control.internal:9443" };
  },
  async saveAgentServiceSettings(settings) {
    return {
      ...settings, running: settings.enabled, listenAddress: `0.0.0.0:${settings.port}`,
      serviceUrl: settings.enabled ? `https://${settings.advertisedHost}:${settings.port}` : "",
    };
  },
  async lifecyclePolicy() {
    return {
      runDays: 365,
      rawLogDays: 30,
      auditDays: 365,
      rawLogMaxBytes: 1024 * 1024 * 1024,
    };
  },
  async saveLifecyclePolicy() {},
  async previewLifecycleCleanup() {
    return {
      logsCleared: 2, runsDeleted: 1, auditsDeleted: 3,
      rawLogBytesBefore: 100, rawLogBytesAfter: 0,
      completedAt: "2026-07-11T12:00:00Z",
    };
  },
  async cleanupLifecycle() {
    return {
      logsCleared: 0,
      runsDeleted: 0,
      auditsDeleted: 0,
      rawLogBytesBefore: 0,
      rawLogBytesAfter: 0,
      completedAt: "2026-07-11T12:00:00Z",
    };
  },
  async applicationVersion() {
    return { version: "1.2.3" };
  },
  async applicationReleases() {
    return {
      currentVersion: "1.2.3",
      latest: { version: "v1.3.0", publishedAt: "2026-07-15T08:00:00Z", summary: "Reliable upgrades", compatible: true, platform: "darwin_arm64" },
      updateAvailable: true,
      managed: false,
    };
  },
  async exportDiagnostics() {
    return { blob: new Blob([]), filename: "shadoc-diagnostics.json" };
  },
  async dashboard() {
    return {
      tasks: [
        {
          id: "task-a",
          name: "照片",
          kind: "directory",
          status: "success",
          repository: "photos repo",
          lastRun: "今天 02:30",
          nextRun: "明天 02:30",
        },
      ],
      alerts: [],
    };
  },
  async compatibility() {
    return {
      blocked: false,
      findings: [
        {
          capability: "restic",
          tool: "restic",
          severity: "info",
          message: "restic 可用",
          version: "0.18.0",
          path: "/usr/bin/restic",
        },
      ],
    };
  },
  async runTask() {},
  async listResource() {
    return [];
  },
  async createResource() {},
  async updateResource() {},
  async deleteResource() {},
  async runDetail(id) { return { id, status: "success", attemptCount: 1, summary: {} }; },
  async runLog() { return ""; },
  async saveMaintenance() {},
  async saveRepositoryCapacityPolicy() { return {}; },
  async saveRestoreVerificationPolicy() { return {}; },
  async deleteRestoreVerificationPolicy() {},
  async action() {
    return {};
  },
};

beforeEach(() => {
  window.history.replaceState({}, "", "/");
  localStorage.clear();
});

async function openGroupedPage(user: ReturnType<typeof userEvent.setup>, group: "活动与记录" | "系统", page: string) {
  await user.click(screen.getByRole("button", { name: group }));
  if (screen.getByRole("tab", { name: page }).getAttribute("aria-selected") !== "true") {
    await user.click(screen.getByRole("tab", { name: page }));
  }
}

describe("restic-control administration", () => {
  it("keeps the dashboard recent-runs table read-only", async () => {
    render(<App api={fakeAPI} />);

    const heading = await screen.findByRole("heading", { name: "最近运行" });
    const section = heading.closest("section")!;
    expect(within(section).queryByRole("button", { name: "手动运行" })).not.toBeInTheDocument();
    expect(within(section).queryByRole("columnheader", { name: "最近验证成功" })).not.toBeInTheDocument();
    expect(within(section).getAllByRole("columnheader")).toHaveLength(8);
  });

  it("groups hosts, Agents, and databases under one connection entry", async () => {
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    expect(screen.queryByRole("button", { name: "远程主机" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Agent 节点" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "数据库实例" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "连接管理" }));
    expect(await screen.findByRole("tab", { name: "远程主机" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "Agent 节点" })).toBeVisible();
    expect(screen.getByRole("tab", { name: "数据库实例" })).toBeVisible();
  });

  it("routes to configuration backup and restore from the system group", async () => {
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });

    await openGroupedPage(user, "系统", "配置备份与恢复");

    expect(screen.queryByRole("heading", { name: "配置备份与恢复", level: 1 })).not.toBeInTheDocument();
    expect(window.location.pathname).toBe("/admin/disaster-recovery");
    expect(screen.getByRole("heading", { name: "导出控制面恢复包" })).toBeVisible();
    expect(screen.getByRole("heading", { name: "导入控制面恢复包" })).toBeVisible();
  });

  it("switches the interface language from settings and restores it after reload", async () => {
    const user = userEvent.setup();
    const first = render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "界面语言");
    await user.selectOptions(screen.getByLabelText("界面语言"), "en-US");
    expect(await screen.findByLabelText("Interface language")).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Interface language", level: 1 })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Dashboard" })).toBeVisible();
    expect(document.documentElement.lang).toBe("en-US");
    expect(localStorage.getItem("shadoc.locale")).toBe("en-US");

    await user.click(screen.getByRole("button", { name: "Repositories" }));
    expect(await screen.findByRole("heading", { name: "Repositories" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "New repository" }));
    expect(await screen.findByRole("heading", { name: "New repository" })).toBeVisible();
    expect(screen.getByLabelText("Repository engine")).toBeVisible();
    expect(screen.getByText(/Configure the repository connection/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await openConnectionPage(user, "Remote hosts");
    await user.click(await screen.findByRole("button", { name: "New remote host" }));
    expect(screen.getByRole("dialog", { name: "New remote host" })).toBeVisible();
    expect(screen.getByLabelText("Pinned known_hosts entry")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await openConnectionPage(user, "Database instances");
    await user.click(await screen.findByRole("button", { name: "New database instance" }));
    expect(screen.getByRole("dialog", { name: "New database instance" })).toBeVisible();
    expect(screen.getByLabelText("TLS mode")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.click(screen.getByRole("button", { name: "Snapshots & restore" }));
    expect(await screen.findByRole("heading", { name: "Restore from snapshot" })).toBeVisible();
    expect(screen.queryByRole("button", { name: /Restore from snapshot/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "System" }));
    await user.click(screen.getByRole("tab", { name: "Interface language" }));

    first.unmount();
    render(<App api={fakeAPI} />);
    expect(await screen.findByRole("button", { name: "Dashboard" })).toBeVisible();
    expect(await screen.findByLabelText("Interface language")).toBeVisible();
  });
  it("displays concise repository capacity and refreshes only on request", async () => {
    let repositoryReads = 0;
    let resolveRefresh!: (value: Array<Record<string, unknown>>) => void;
    const refreshedRepositories = new Promise<Array<Record<string, unknown>>>((resolve) => { resolveRefresh = resolve; });
    const listResource = vi.fn(async (resource: string) => {
      if (resource !== "repositories") return [];
      repositoryReads += 1;
      if (repositoryReads > 1) return refreshedRepositories;
      return [{
        id: "repo-a", name: "照片仓库", kind: "sftp", path: "/backup/photos", status: "ready",
        capacity: { totalBytes: 1099511627776, usedBytes: 659706976666, availableBytes: 439804651110, checkedAt: "2026-07-15T06:00:00Z", sourceAgentId: "agent-a" },
        capacityPolicy: { enabled: true, nextProbeAt: "2026-07-15T12:00:00Z", stale: false, lastError: "" },
        lastRun: { status: "success", startedAt: "2026-07-12T10:02:30.508331Z", summary: { dataAdded: 5_872_025 } },
      }];
    });
    const action = vi.fn(async (path: string) => {
      if (path === "/api/repositories/repo-a/capacity") return { operationId: "capacity-op", status: "queued" };
      if (path === "/api/operations/capacity-op") return { id: "capacity-op", kind: "repository_capacity_probe", status: "success", stage: "completed" };
      if (path === "/api/repositories/repo-a/capacity-policy") return {
        repositoryId: "repo-a", enabled: true, probeIntervalMinutes: 360, minimumAvailableBytes: 0,
        minimumAvailablePercent: 10, exhaustionWarningDays: 30, nextProbeAt: "2026-07-15T12:00:00Z",
        lastSuccessAt: "2026-07-15T06:00:00Z", updatedAt: "2026-07-15T06:00:00Z", stale: false,
      };
      if (path === "/api/repositories/repo-a/capacity-samples?limit=30") return [];
      if (path === "/api/repositories/repo-a/capacity-forecast") return { status: "insufficient_samples", sampleCount: 1 };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource,
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    expect(await screen.findByText("409.6 GiB 可用 / 共 1 TiB")).toBeVisible();
    expect(screen.getByText("2026-07-12T10:02:30Z · 5.6 MiB")).toBeVisible();
    expect(screen.queryByText(/2026-07-12T10:02:30\.508331Z/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Agent agent-a/)).not.toBeInTheDocument();
    expect(action).not.toHaveBeenCalledWith("/api/repositories/repo-a/capacity", {});
    const tableFrame = screen.getByText("照片仓库").closest(".table-frame") as HTMLElement;
    tableFrame.scrollLeft = 144;
    await user.click(screen.getByRole("button", { name: "刷新存储容量" }));
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/repositories/repo-a/capacity", {}));
    await waitFor(() => expect(listResource).toHaveBeenCalledTimes(2), { timeout: 2000 });
    expect(screen.getByRole("row", { name: /照片仓库/ })).toBeVisible();
    expect(tableFrame.scrollLeft).toBe(144);
    resolveRefresh([{
      id: "repo-a", name: "照片仓库", kind: "sftp", path: "/backup/photos", status: "ready",
      capacity: { totalBytes: 1099511627776, usedBytes: 549755813888, availableBytes: 549755813888, checkedAt: "2026-07-15T07:00:00Z", sourceAgentId: "agent-a" },
    }]);
    expect(await screen.findByText("512 GiB 可用 / 共 1 TiB")).toBeVisible();
    expect(tableFrame.scrollLeft).toBe(144);
  });

  it.each([
    "repository_capacity_low",
    "repository_capacity_forecast",
    "repository_capacity_stale",
    "repository_capacity_probe_failed",
  ])("routes %s alerts to the repository list", async (kind) => {
    const action = vi.fn(async (path: string) => {
      if (path === "/api/repositories/repo-alert/capacity-policy") return {
        repositoryId: "repo-alert", enabled: true, probeIntervalMinutes: 360, minimumAvailableBytes: 0,
        minimumAvailablePercent: 10, exhaustionWarningDays: 30, nextProbeAt: "2026-07-15T12:00:00Z",
        lastSuccessAt: "2026-07-15T06:00:00Z", updatedAt: "2026-07-15T06:00:00Z", stale: kind === "repository_capacity_stale",
      };
      if (path === "/api/repositories/repo-alert/capacity-samples?limit=30") return [];
      if (path === "/api/repositories/repo-alert/capacity-forecast") return { status: "insufficient_samples", sampleCount: 1 };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      dashboard: async () => ({
        tasks: [],
        alerts: [{
          stateKey: `repository:repo-alert:${kind}`, kind, severity: "warning", status: "active",
          objectType: "repository", objectId: "repo-alert", objectName: "告警仓库", reason: "容量需要处理",
          message: `${kind} 告警`, targetPage: "备份仓库", recoveryCondition: "容量恢复健康",
          firstAt: "2026-07-15T01:00:00Z", lastAt: "2026-07-15T02:00:00Z", occurrenceCount: 1,
        }],
      }),
      listResource: async (resource) => resource === "repositories" ? [{
        id: "repo-alert", name: "告警仓库", kind: "local", path: "/backup/alert", status: "ready",
        capacity: { totalBytes: 2048, usedBytes: 1024, availableBytes: 1024, checkedAt: "2026-07-15T06:00:00Z" },
        capacityPolicy: { enabled: true, nextProbeAt: "2026-07-15T12:00:00Z", stale: kind === "repository_capacity_stale" },
      }] : [],
    }} />);

    const alert = await screen.findByText(`${kind} 告警`);
    const user = userEvent.setup();
    await user.click(within(alert.closest("li")!).getByRole("button", { name: "处理" }));

    expect(await screen.findByRole("heading", { name: "备份仓库" })).toBeVisible();
    expect(screen.getByText("1 KiB 可用 / 共 2 KiB")).toBeVisible();
    expect(window.location.pathname).toBe("/admin/repositories");
    expect(window.location.search).toBe("");
  });

  it("keeps fresh repository capacity without starting another probe", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async () => ({}));
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "repositories" ? [{
        id: "repo-fresh", name: "新鲜容量", kind: "local", path: "/backup/fresh", status: "ready",
        capacity: { totalBytes: 2048, usedBytes: 1024, availableBytes: 1024, checkedAt: new Date().toISOString() },
      }] : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    expect(await screen.findByText("1 KiB 可用 / 共 2 KiB")).toBeVisible();
    expect(action).not.toHaveBeenCalledWith("/api/repositories/repo-fresh/capacity", {});
  });

  it("deploys an Agent to a saved remote host and reports completion with a toast", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/agents/deploy") return { operationId: "op-deploy", status: "queued" };
      if (path === "/api/operations/op-deploy") return { id: "op-deploy", kind: "agent_deploy", status: "success", stage: "completed" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "remote-hosts"
        ? [{ id: "host-a", name: "服务器 A", host: "192.168.1.20", port: 22, username: "backup" }]
        : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    const deployButton = await screen.findByRole("button", { name: "远程部署 Agent" });
    expect(deployButton.parentElement).toHaveClass("page-header-actions");
    await user.click(deployButton);
    const dialog = screen.getByRole("dialog", { name: "远程部署 Agent" });
    await user.type(within(dialog).getByLabelText("Agent ID"), "backup-node");
    expect(within(dialog).getByLabelText("Service HTTPS 地址")).toHaveValue("https://control.internal:9443");
    await user.click(within(dialog).getByRole("button", { name: "开始部署" }));

    expect(action).toHaveBeenCalledWith("/api/agents/deploy", {
      hostId: "host-a", agentId: "backup-node", serviceUrl: "https://control.internal:9443",
    });
    const completion = await screen.findByText("Agent 部署成功");
    expect(completion.closest(".toast")).toBeVisible();
    expect(screen.queryByText("操作完成")).not.toBeInTheDocument();
  });

  it("confirms a managed Agent upgrade and verifies it through a tracked operation", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/agents/agent-a/upgrade") return { operationId: "op-upgrade", status: "queued" };
      if (path === "/api/operations/op-upgrade") return { id: "op-upgrade", kind: "agent_upgrade", status: "success", stage: "agent_upgrade_verified" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "agents" ? [{
        id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", taskEligible: true,
        compatibilityStatus: "compatible", buildVersion: "v1.3.0", targetVersion: "v1.4.0", upgradeAvailable: true,
        protocolMin: 1, protocolMax: 1, protocolCompatible: true, certificateStatus: "valid", endpointStatus: "current",
      }] : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    await user.click(await screen.findByRole("button", { name: "agent-a 查看详情" }));
    await user.click(await screen.findByRole("button", { name: "升级 Agent 至 v1.4.0" }));
    const dialog = screen.getByRole("dialog", { name: "确认升级 Agent" });
    expect(within(dialog).getByText(/旧程序在验证完成前保留/)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "开始托管升级" }));

    expect(action).toHaveBeenCalledWith("/api/agents/agent-a/upgrade", {});
    const completion = await screen.findByText("Agent 升级完成，新版本心跳已验证");
    expect(completion.closest(".toast")).toBeVisible();
  });

  it("restarts a managed Agent to refresh its fixed backup-tool probes", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/agents/agent-a/tools/reprobe") return { operationId: "op-tool-probe", status: "queued" };
      if (path === "/api/operations/op-tool-probe") return { id: "op-tool-probe", kind: "agent_tool_probe", status: "success", stage: "agent_tool_probe_verified" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "agents" ? [{
        id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", taskEligible: true,
        compatibilityStatus: "compatible", buildVersion: "v1.4.0", targetVersion: "v1.4.0", upgradeAvailable: false,
        protocolMin: 1, protocolMax: 1, protocolCompatible: true, certificateStatus: "valid", endpointStatus: "current",
      }] : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    await user.click(await screen.findByRole("button", { name: "agent-a 查看详情" }));
    await user.click(await screen.findByRole("button", { name: "重新探测工具" }));
    const dialog = screen.getByRole("dialog", { name: "确认重新探测 Agent 工具" });
    expect(within(dialog).getByText(/固定的 Restic 和 rsync 版本探测/)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "开始重新探测" }));

    expect(action).toHaveBeenCalledWith("/api/agents/agent-a/tools/reprobe", {});
    const completion = await screen.findByText("Agent 工具重新探测完成，新能力心跳已验证");
    expect(completion.closest(".toast")).toBeVisible();
  });

  it("installs official Restic on a managed Linux Agent and waits for capability verification", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/restic/versions") return { versions: ["0.19.1"] };
      if (path === "/api/agents/agent-a/restic/install") return { operationId: "op-restic", status: "queued" };
      if (path === "/api/operations/op-restic") return { id: "op-restic", kind: "agent_restic_install", status: "success", stage: "agent_restic_verified" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "agents" ? [{
        id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", taskEligible: false,
        compatibilityStatus: "compatible", buildVersion: "v1.4.0", platform: "linux/amd64",
        protocolMin: 1, protocolMax: 1, protocolCompatible: true, certificateStatus: "valid", endpointStatus: "current",
        capabilities: ["managed-restic-install-v1", "filesystem-browse", "filesystem-restore-target"],
      }] : resource === "remote-hosts" ? [{ id: "host-a", name: "Agent 主机", host: "agent.example", port: 22, username: "backup" }] : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    await user.click(await screen.findByRole("button", { name: "agent-a 查看详情" }));
    await user.click(await screen.findByRole("button", { name: "安装 Agent Restic" }));
    const dialog = screen.getByRole("dialog", { name: "确认安装 Agent Restic" });
    expect(within(dialog).getByText("0.19.1")).toBeVisible();
    expect(within(dialog).getByText(/不调用系统包管理器/)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "开始安装 Restic" }));

    expect(action).toHaveBeenCalledWith("/api/agents/agent-a/restic/install", { version: "0.19.1" });
    const completion = await screen.findByText("Agent Restic 安装完成，备份与恢复能力已验证");
    expect(completion.closest(".toast")).toBeVisible();
  });

  it("keeps a failed Agent Restic installation diagnostic on the Agent page", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/restic/versions") return { versions: ["0.19.1"] };
      if (path === "/api/agents/agent-a/restic/install") return { operationId: "op-restic-failed", status: "queued" };
      if (path === "/api/operations/op-restic-failed") return { id: "op-restic-failed", kind: "agent_restic_install", status: "failed", stage: "failed", errorSummary: "Agent heartbeat did not report restic-restore" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "agents" ? [{
        id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", compatibilityStatus: "compatible", platform: "linux/amd64",
        capabilities: ["managed-restic-install-v1", "filesystem-restore-target"],
      }] : resource === "remote-hosts" ? [{ id: "host-a", host: "agent.example", port: 22, username: "backup" }] : [],
    }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    await user.click(await screen.findByRole("button", { name: "agent-a 查看详情" }));
    await user.click(await screen.findByRole("button", { name: "安装 Agent Restic" }));
    await user.click(within(screen.getByRole("dialog", { name: "确认安装 Agent Restic" })).getByRole("button", { name: "开始安装 Restic" }));
    expect(await screen.findByText("查看失败详情")).toBeVisible();
  });

  it("stops and uninstalls a managed Agent before refreshing its runtime status", async () => {
    const user = userEvent.setup();
    let uninstalled = false;
    const listResource = vi.fn(async (resource: string) => resource === "agents"
      ? [{
          id: "mini-debian", remoteHostId: "host-1", status: uninstalled ? "revoked" : "online",
          runtimeStatus: uninstalled ? "stopped" : "running",
          capabilities: ["restic", "filesystem-browse", "os:linux", "arch:amd64"],
          lastHeartbeatAt: "2026-07-14T14:47:37.568669Z",
          ...(uninstalled ? { uninstalledAt: "2026-07-14T14:47:38.498771Z" } : {}),
        }]
      : []);
    const action = vi.fn(async (path: string) => {
      if (path === "/api/agents/mini-debian/uninstall") return { operationId: "op-uninstall", status: "queued" };
      if (path === "/api/operations/op-uninstall") {
        uninstalled = true;
        return { id: "op-uninstall", kind: "agent_uninstall", status: "success", stage: "completed" };
      }
      return {};
    });
    render(<App api={{ ...fakeAPI, action, listResource }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    const detailButton = await screen.findByRole("button", { name: "mini-debian 查看详情" });
    const agentCard = detailButton.closest("article");
    expect(agentCard).not.toBeNull();
    expect(within(agentCard as HTMLElement).getByText("在线")).toBeVisible();
    await user.click(detailButton);
    await user.click(screen.getByRole("button", { name: "停止并卸载" }));
    const dialog = screen.getByRole("dialog", { name: "确认停止并卸载 Agent" });
    expect(within(dialog).getByText(/先停止远程 Agent 服务/)).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "确认停止并卸载" }));

    expect(action).toHaveBeenCalledWith("/api/agents/mini-debian/uninstall", {});
    const completion = await screen.findByText("Agent 已停止并卸载");
    expect(completion.closest(".toast")).toBeVisible();
    expect((await screen.findAllByText("已停止"))[0]).toBeVisible();
    expect(screen.getByText("凭据已撤销")).toBeVisible();
    expect(screen.getByRole("button", { name: "重新部署" })).toBeEnabled();
    expect(screen.queryByRole("columnheader", { name: "引擎" })).not.toBeInTheDocument();
    expect(screen.queryByText(/filesystem-browse/)).not.toBeInTheDocument();
    expect(listResource.mock.calls.filter(([resource]) => resource === "agents").length).toBeGreaterThan(1);
  });

  it("prefills the managed host and Agent ID when redeploying an uninstalled Agent", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/agents/deploy") return { operationId: "op-redeploy", status: "queued" };
      if (path === "/api/operations/op-redeploy") return { id: "op-redeploy", kind: "agent_deploy", status: "success", stage: "completed" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => {
        if (resource === "agents") return [{ id: "mini-debian", remoteHostId: "host-1", status: "revoked", runtimeStatus: "stopped", uninstalledAt: "2026-07-14T14:47:38Z" }];
        if (resource === "remote-hosts") return [{ id: "host-1", name: "迷你主机", host: "192.168.0.104", port: 22, username: "tmen" }];
        return [];
      },
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    await user.click(await screen.findByRole("button", { name: "mini-debian 查看详情" }));
    await user.click(await screen.findByRole("button", { name: "重新部署" }));
    const dialog = screen.getByRole("dialog", { name: "重新部署 Agent" });
    expect(within(dialog).getByLabelText("远程主机")).toHaveValue("host-1");
    expect(within(dialog).getByLabelText("Agent ID")).toHaveValue("mini-debian");
    expect(within(dialog).getByLabelText("Agent ID")).toHaveAttribute("readonly");
    await user.click(within(dialog).getByRole("button", { name: "开始重新部署" }));

    expect(action).toHaveBeenCalledWith("/api/agents/deploy", {
      hostId: "host-1", agentId: "mini-debian", serviceUrl: "https://control.internal:9443",
    });
    const completion = await screen.findByText("Agent 已重新部署");
    expect(completion.closest(".toast")).toBeVisible();
  });

  it("shows Agent runtime and credential status in English", async () => {
    localStorage.setItem("restic-control.locale", "en-US");
    const user = userEvent.setup();
    render(<App api={{
      ...fakeAPI,
      listResource: async (resource) => resource === "agents"
        ? [{ id: "mini-debian", remoteHostId: "host-1", status: "revoked", runtimeStatus: "stopped", uninstalledAt: "2026-07-14T14:47:38.498771Z" }]
        : [],
    }} />);

    await screen.findByRole("heading", { name: "Dashboard" });
    await openConnectionPage(user, "Agents");
    await user.click(await screen.findByRole("button", { name: "mini-debian View details" }));
    expect(await screen.findByText("Stopped")).toBeVisible();
    expect(screen.getAllByText("Credentials revoked")[0]).toBeVisible();
    expect(screen.getByRole("button", { name: "Redeploy" })).toBeEnabled();
  });

  it("displays the UTC Agent heartbeat in the configured interface time zone", async () => {
    localStorage.setItem("restic-control.timezone", "Asia/Shanghai");
    const user = userEvent.setup();
    render(<App api={{
      ...fakeAPI,
      listResource: async (resource) => resource === "agents"
        ? [{ id: "mini-debian", status: "online", runtimeStatus: "running", lastHeartbeatAt: "2026-07-14T14:47:37.568669Z" }]
        : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    const heartbeat = await screen.findByText(/22:47:37/);
    expect(heartbeat.closest("time")).toHaveAttribute("dateTime", "2026-07-14T14:47:37Z");
    expect(screen.queryByText("2026-07-14T14:47:37.568669Z")).not.toBeInTheDocument();
  });

  it("keeps low-frequency Agent HTTPS configuration and raw capabilities out of the Agent list page", async () => {
    const user = userEvent.setup();
    render(<App api={{
      ...fakeAPI,
      listResource: async (resource) => resource === "agents"
        ? [{ id: "mini-debian", status: "online", runtimeStatus: "running", capabilities: ["restic", "filesystem-browse", "filesystem-create-directory", "repository-capacity"] }]
        : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    expect(await screen.findByText("Agent 服务运行中")).toBeVisible();
    expect(screen.getByRole("button", { name: "Agent 服务设置" })).toBeVisible();
    expect(screen.queryByLabelText("HTTPS 监听端口")).not.toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "引擎" })).not.toBeInTheDocument();
    expect(screen.queryByText("能力探测")).not.toBeInTheDocument();
    expect(screen.queryByText("创建目录")).not.toBeInTheDocument();
    expect(screen.queryByText(/filesystem-create-directory/)).not.toBeInTheDocument();
  });

  it("starts the Agent HTTPS service from System settings with a custom port", async () => {
    const user = userEvent.setup();
    const saveAgentServiceSettings = vi.fn(async (settings: { enabled: boolean; port: number; advertisedHost: string }) => ({
      ...settings,
      running: settings.enabled,
      listenAddress: `0.0.0.0:${settings.port}`,
      serviceUrl: `https://${settings.advertisedHost}:${settings.port}`,
    }));
    render(<App api={{
      ...fakeAPI,
      agentServiceStatus: async () => ({
        enabled: false, running: false, port: 9443, advertisedHost: "", listenAddress: "0.0.0.0:9443", serviceUrl: "",
      }),
      saveAgentServiceSettings,
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "Agent 服务");
    const portInput = screen.getByLabelText("HTTPS 监听端口");
    expect(screen.queryByRole("heading", { name: "Agent 服务", level: 1 })).not.toBeInTheDocument();
    const addressInput = screen.getByLabelText("控制服务访问地址（IP 或域名）");
    expect(portInput).toHaveValue(9443);
    expect(addressInput).toBeEnabled();
    expect(addressInput).not.toHaveAttribute("placeholder");
    await user.clear(portInput);
    await user.type(portInput, "10443");
    await user.type(addressInput, "192.168.0.20");
    await user.click(screen.getByLabelText("启用 Agent HTTPS 服务"));
    await user.click(screen.getByRole("button", { name: "保存并应用" }));

    expect(saveAgentServiceSettings).toHaveBeenCalledWith({ enabled: true, port: 10443, advertisedHost: "192.168.0.20" });
    const completion = await screen.findByText("Agent HTTPS 服务已启动");
    expect(completion.closest(".toast")).toBeVisible();
    expect(completion.closest(".toast")?.parentElement).toBe(document.body);
    expect(document.querySelector(".success-message")).toBeNull();
    expect(screen.getByText(/https:\/\/192\.168\.0\.20:10443/)).toBeVisible();
  });

  it("refreshes Agent HTTPS runtime status when the window regains focus", async () => {
    const user = userEvent.setup();
    let running = false;
    render(<App api={{
      ...fakeAPI,
      agentServiceStatus: async () => ({
        enabled: running, running, port: 9443, advertisedHost: running ? "control.lan" : "",
        listenAddress: "0.0.0.0:9443", serviceUrl: running ? "https://control.lan:9443" : "",
      }),
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "Agent 节点");
    expect(await screen.findByRole("button", { name: "生成注册令牌" })).toBeDisabled();
    running = true;
    window.dispatchEvent(new Event("focus"));
    expect(await screen.findByRole("button", { name: "生成注册令牌" })).toBeEnabled();
    expect(screen.getByText("Agent 服务运行中")).toBeVisible();
  });

  it("logs out through the server and clears the administration session", async () => {
    const user = userEvent.setup();
    const logout = vi.fn(async () => undefined);
    render(<App api={{ ...fakeAPI, logout }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "退出登录" }));
    expect(logout).toHaveBeenCalledOnce();
    expect(await screen.findByRole("heading", { name: "登录影刻 · Shadoc" })).toBeVisible();
  });

  it("shows abnormal repositories and distinct failed or partial task states on dashboard", async () => {
    render(<App api={{ ...fakeAPI, dashboard: async () => ({
      repositoryStatus: "abnormal", nextRun: "2026-07-12T03:00:00Z",
      tasks: [
		{ id: "failed", name: "失败任务", kind: "directory", status: "failed", repository: "损坏仓库", lastScheduledAt: "2026-07-12T01:00:00Z", lastRun: "2026-07-12T01:02:00Z", nextRun: "2026-07-12T03:00:00Z" },
		{ id: "partial", name: "部分成功任务", kind: "database", status: "partial", repository: "数据库仓库", lastScheduledAt: "尚无计划发生记录", lastRun: "刚刚", nextRun: "稍后" },
	  ], alerts: [{ stateKey: "repository:repo:integrity", objectName: "损坏仓库", severity: "critical", status: "active", message: "仓库完整性异常", reason: "仓库状态异常", firstAt: "2026-07-12T01:00:00Z", lastAt: "2026-07-12T02:00:00Z", recoveryCondition: "仓库检查通过", targetPage: "备份仓库" }],
	  runOverview: { total: 10, succeeded: 8, failed: 1, partial: 1, successRate: 80 },
    }) }} />);
    expect(await screen.findByText("异常")).toBeVisible();
    expect(screen.getByText("失败")).toBeVisible();
    expect(screen.getByText("部分成功")).toBeVisible();
	expect(screen.getByRole("region", { name: "当前告警" })).toHaveTextContent("仓库完整性异常");
	expect(screen.getByRole("region", { name: "当前告警" })).toHaveTextContent("仓库检查通过");
	expect(screen.queryByText("80%")).not.toBeInTheDocument();
	expect(screen.getByRole("region", { name: "任务健康趋势" })).toHaveTextContent("成功率分母只包含完整成功、部分成功和失败");
	const dashboardTable = screen.getByRole("table");
	expect(within(dashboardTable).getByRole("columnheader", { name: "上次应运行" })).toBeVisible();
	expect(within(dashboardTable).getByRole("columnheader", { name: "实际运行" })).toBeVisible();
	expect(within(dashboardTable).getByRole("columnheader", { name: "下次计划" })).toBeVisible();
	const failedRow = screen.getByText("失败任务").closest("tr")!;
	expect(failedRow.querySelector('time[datetime="2026-07-12T01:00:00Z"]')).not.toBeNull();
	expect(failedRow.querySelector('time[datetime="2026-07-12T01:02:00Z"]')).not.toBeNull();
	expect(failedRow.querySelector('time[datetime="2026-07-12T03:00:00Z"]')).not.toBeNull();
  });

  it("opens the selected task health detail from the dashboard summary", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/task-trends") return {
        generatedAt: "2026-07-16T14:06:00Z",
        eligibleStatuses: ["success", "partial", "failed"],
        excludedStatuses: ["queued", "running", "cancelled", "skipped"],
        tasks: [{
          taskId: "task-a", taskName: "照片", engine: "restic", latestCompleteSuccessAt: "2026-07-16T13:30:00Z", daily: [],
          windows: [7, 30, 90].map((windowDays) => ({
            windowDays, windowStart: "2026-04-17T14:06:00Z", windowEnd: "2026-07-16T14:06:00Z",
            eligibleCount: 2, completeSuccessCount: 2, partialCount: 0, failedCount: 0, excludedCount: 0,
            successRate: 100, retryCount: 0, averageDurationMilliseconds: 5000, p95DurationMilliseconds: 5000,
            metricCoverage: { duration: 2, filesProcessed: 0, filesChanged: 0, bytesProcessed: 0, bytesChanged: 0 },
          })),
        }],
      };
      if (path.startsWith("/api/activity?")) return { generatedAt: "2026-07-16T14:06:00Z", truncated: false, items: [] };
      return {};
    });
    render(<App api={{ ...fakeAPI, action }} />);

    const trends = await screen.findByRole("region", { name: "任务健康趋势" });
    await user.click(await within(trends).findByRole("button", { name: "详情" }));

    expect(await screen.findByRole("heading", { name: "照片" })).toBeVisible();
    expect(window.location.pathname).toBe("/admin/tasks");
    expect(new URLSearchParams(window.location.search).get("task")).toBe("task-a");
    expect(new URLSearchParams(window.location.search).get("view")).toBe("health");
  });

  it("loads the dashboard and opens the create task workflow", async () => {
    const user = userEvent.setup();
    render(
      <App
        api={{
          ...fakeAPI,
          listResource: async (resource) =>
            resource === "repositories"
              ? [{ id: "repo-ready", name: "照片仓库", kind: "local", path: "/backup", status: "ready" }]
              : [],
        }}
      />,
    );

    expect(
      await screen.findByRole("heading", { name: "仪表盘" }),
    ).toBeVisible();
    expect(screen.getByText("照片")).toBeVisible();
    expect(within(screen.getByRole("navigation", { name: "主导航" })).queryByRole("button", { name: "创建保护" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "创建保护" })).not.toBeInTheDocument();

    const createTaskButton = screen.getByRole("button", { name: "新建备份任务" });
    expect(createTaskButton).toHaveClass("primary-button");
    await user.click(createTaskButton);
    expect(screen.getByRole("heading", { name: "新建备份任务" })).toBeVisible();
    expect(screen.getByLabelText("任务类型")).toBeVisible();
  });

  it("groups secondary pages in the sidebar and exposes them as URL-backed tabs", async () => {
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(within(navigation).queryByRole("button", { name: "仓库维护" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("button", { name: "运行记录" })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("button", { name: "安全设置" })).not.toBeInTheDocument();
    await user.click(within(navigation).getByRole("button", { name: "活动与记录" }));
    expect(await screen.findByRole("tablist", { name: "活动与记录" })).toBeVisible();
    expect(screen.getByRole("tab", { name: "运行记录" })).toHaveAttribute("aria-selected", "true");
    await user.click(screen.getByRole("tab", { name: "告警历史" }));
    expect(window.location.pathname).toBe("/admin/alerts");
    await user.click(screen.getByRole("tab", { name: "投递记录" }));
    expect(window.location.pathname).toBe("/admin/deliveries");
    await user.click(screen.getByRole("tab", { name: "审计日志" }));
    expect(window.location.pathname).toBe("/admin/audits");
    await openGroupedPage(user, "系统", "通知配置");
    expect(window.location.pathname).toBe("/admin/notifications");
  });

  it("presents resource types and structured execution targets as administrator-facing labels", async () => {
    const user = userEvent.setup();
    render(<App api={{
      ...fakeAPI,
      listResource: async (resource) => resource === "tasks"
        ? [{
            id: "task-agent", name: "照片保护", engine: "restic", kind: "directory",
            executionTarget: { kind: "agent", agentId: "agent-a" }, repositoryId: "repo-a", enabled: true,
          }]
        : resource === "repositories"
          ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "sftp", path: "/backups/photos", status: "uninitialized" }]
          : resource === "database-connections"
            ? [{ id: "db-a", name: "业务库", engine: "postgresql", purpose: "restore", host: "db.internal", status: "draft" }]
            : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    const taskRow = (await screen.findByText("照片保护")).closest("tr")!;
    expect(taskRow).toHaveTextContent("Restic");
    expect(taskRow).toHaveTextContent("目录备份");
    expect(taskRow).toHaveTextContent("远程 Agent · agent-a");
    expect(taskRow).toHaveTextContent("已启用");
    expect(taskRow).not.toHaveTextContent('{"kind":"agent"');

    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    const repositoryRow = (await screen.findByText("照片仓库")).closest("tr")!;
    expect(repositoryRow).toHaveTextContent("Restic");
    expect(repositoryRow).toHaveTextContent("远程 SFTP");
    expect(repositoryRow).toHaveTextContent("尚未初始化");

    await openConnectionPage(user, "数据库实例");
    const databaseRow = (await screen.findByText("业务库")).closest("tr")!;
    expect(databaseRow).toHaveTextContent("PostgreSQL");
    expect(databaseRow).toHaveTextContent("恢复用途");
    expect(databaseRow).toHaveTextContent("草稿（不可启用）");
  });

  it("uses active green semantics for running work and neutral semantics for stopped work", async () => {
    render(<App api={{
      ...fakeAPI,
      dashboard: async () => ({
        tasks: [
          { id: "running", name: "运行任务", kind: "directory", status: "running", repository: "仓库 A", lastRun: "", nextRun: "" },
          { id: "stopped", name: "停止任务", kind: "directory", status: "stopped", repository: "仓库 B", lastRun: "", nextRun: "" },
        ],
        alerts: [],
      }),
    }} />);

    const runningRow = (await screen.findByText("运行任务")).closest("tr")!;
    expect(within(runningRow).getByRole("status", { name: "状态：运行中" })).toHaveClass("status-active");
    const stoppedRow = screen.getByText("停止任务").closest("tr")!;
    expect(within(stoppedRow).getByRole("status", { name: "状态：已停止" })).toHaveClass("status-stopped");
  });

  it("opens the task editor as a page and explains when no repository is available", async () => {
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    await screen.findByRole("heading", { name: "备份任务" });
    await user.click(screen.getByRole("button", { name: "新建备份任务" }));

    expect(await screen.findByRole("heading", { name: "新建备份任务" })).toBeVisible();
    expect(screen.queryByRole("dialog", { name: "新建备份任务" })).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: "无可选仓库" })).toBeVisible();
    expect(window.location.pathname).toBe("/admin/tasks");
    expect(new URLSearchParams(window.location.search).get("view")).toBe("create");
  });

  it("creates an agent local-to-local rsync task without a remote host", async () => {
	const user = userEvent.setup();
	const createResource = vi.fn(async () => undefined);
	render(<App api={{
	  ...fakeAPI,
	  createResource,
	  listResource: async (resource) => resource === "agents"
	    ? [{ id: "agent-1", status: "online", capabilities: ["rsync"] }]
	    : resource === "repositories"
	      ? [{ id: "sync-repo", name: "第二硬盘", engine: "rsync", kind: "local", path: "/mnt/disk-b/photos", status: "ready" }]
	      : [],
	}} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "备份任务" }));
	await user.click(await screen.findByRole("button", { name: "新建备份任务" }));

	await user.selectOptions(screen.getByLabelText("执行引擎"), "rsync");
	await user.selectOptions(screen.getByLabelText("执行位置"), "agent");
	await user.type(screen.getByLabelText("任务名称"), "双硬盘同步");
	await user.selectOptions(screen.getByLabelText("源端 Agent"), "agent-1");
	await user.selectOptions(screen.getByLabelText("同步仓库"), "sync-repo");
	await user.type(screen.getByLabelText("源目录绝对路径"), "/mnt/disk-a/photos");
	await user.click(screen.getByRole("button", { name: "保存任务" }));

	expect(createResource).toHaveBeenCalledWith("tasks", expect.objectContaining({
	  engine: "rsync",
	  enabled: false,
	  repositoryId: "sync-repo",
	  executionTarget: { kind: "agent", agentId: "agent-1" },
	  rsync: expect.objectContaining({
		path: "/mnt/disk-a/photos",
		exclusions: [],
	  }),
	}));
  });

  it("saves a disabled task draft, previews its explicit scope, and invalidates the preview when a suggestion is adopted", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => ({ id: "task-draft" }));
    const updateResource = vi.fn(async () => undefined);
    let previewNumber = 0;
    const action = vi.fn(async (path: string) => {
      if (path === "/api/tasks/task-draft/preview") {
        previewNumber += 1;
        return {
          previewId: `preview-${previewNumber}`,
          fingerprint: `fingerprint-${previewNumber}`,
          requiresDeleteConfirmation: false,
          summary: {
            scannedItems: 14,
            includedFiles: 10,
            includedBytes: 8192,
            excludedFiles: previewNumber === 1 ? 0 : 3,
            excludedBytes: previewNumber === 1 ? 0 : 4096,
            unreadableItems: 1,
            truncated: previewNumber === 1,
            activeRules: previewNumber === 1 ? [] : [{ rule: "**/node_modules", matchedFiles: 3, estimatedBytes: 4096 }],
            suggestions: [{ rule: "**/node_modules", reason: "依赖缓存可重新安装", matchedFiles: 3, estimatedBytes: 4096 }],
          },
        };
      }
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      createResource,
      updateResource,
      listResource: async (resource) => resource === "repositories"
        ? [{ id: "repo-1", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    await user.click(await screen.findByRole("button", { name: "新建备份任务" }));

    await user.type(screen.getByLabelText("任务名称"), "照片备份");
    await user.selectOptions(screen.getByLabelText("备份仓库"), "repo-1");
    await user.type(screen.getByLabelText("源目录绝对路径"), "/srv/photos");
    expect(screen.getByLabelText("排除规则（每行一条）")).toHaveValue("");
    expect(screen.getByLabelText("任务状态")).toHaveValue("false");

    await user.click(screen.getByRole("button", { name: "保存草稿并预览范围" }));

    expect(await screen.findByText("范围预览已生成")).toBeVisible();
    expect(createResource).toHaveBeenCalledWith("tasks", expect.objectContaining({
      enabled: false,
      directory: expect.objectContaining({ exclusions: [] }),
    }));
    expect(action).toHaveBeenCalledWith("/api/tasks/task-draft/preview", {});
    expect(screen.getByText("10 个文件将纳入保护，0 个文件被排除，1 项无法读取。")).toBeVisible();
    expect(screen.getByRole("alert")).toHaveTextContent("预览达到扫描上限，结果已截断");

    await user.click(screen.getByRole("checkbox", { name: "采用建议 **/node_modules" }));
    expect(screen.getByLabelText("排除规则（每行一条）")).toHaveValue("**/node_modules");
    expect(screen.getByText("任务范围已改变，需要重新生成预览。")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "保存草稿并重新预览" }));
    expect((await screen.findAllByText("3 个文件，4 KB", {}, { timeout: 3000 }))[0]).toBeVisible();
    expect(updateResource).toHaveBeenCalledWith("tasks", "task-draft", expect.objectContaining({
      enabled: false,
      directory: expect.objectContaining({ exclusions: ["**/node_modules"] }),
    }));

    await user.selectOptions(screen.getByLabelText("任务状态"), "true");
    await user.click(screen.getByRole("button", { name: "保存任务" }));
    await waitFor(() => expect(updateResource).toHaveBeenLastCalledWith("tasks", "task-draft", expect.objectContaining({
      enabled: true,
      previewId: "preview-2",
    })));
  });

  it("requires a separate confirmation for an rsync delete preview", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => ({ id: "rsync-draft" }));
    const updateResource = vi.fn(async () => undefined);
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/tasks/rsync-draft/preview") {
        return {
          previewId: "delete-preview",
          fingerprint: "delete-fingerprint",
          requiresDeleteConfirmation: true,
          summary: {
            scannedItems: 20,
            includedFiles: 20,
            includedBytes: 10240,
            excludedFiles: 0,
            excludedBytes: 0,
            unreadableItems: 0,
            truncated: false,
            activeRules: [],
            suggestions: [],
            deleteFiles: 3,
            deleteDirectories: 1,
            targetIdentity: "agent-1:/mnt/archive",
          },
        };
      }
      if (path.includes("/filesystem/browse")) return { path: String(payload?.path ?? ""), entries: [] };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      createResource,
      updateResource,
      listResource: async (resource) => resource === "agents"
        ? [{ id: "agent-1", status: "online", capabilities: ["rsync", "filesystem-browse"] }]
        : resource === "repositories"
          ? [{ id: "sync-repo", name: "归档盘", engine: "rsync", kind: "local", path: "/mnt/archive", status: "ready" }]
          : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    await user.click(await screen.findByRole("button", { name: "新建备份任务" }));
    await user.selectOptions(screen.getByLabelText("执行引擎"), "rsync");
    await user.selectOptions(screen.getByLabelText("执行位置"), "agent");
    await user.type(screen.getByLabelText("任务名称"), "镜像同步");
    await user.selectOptions(screen.getByLabelText("源端 Agent"), "agent-1");
    await user.selectOptions(screen.getByLabelText("同步仓库"), "sync-repo");
    await user.type(screen.getByLabelText("源目录绝对路径"), "/mnt/source");
    await user.selectOptions(screen.getByLabelText("目标清理策略"), "true");
    await user.click(screen.getByRole("button", { name: "保存草稿并预览范围" }));

    expect(await screen.findByText("目标 agent-1:/mnt/archive 将删除 3 个文件和 1 个目录。")).toBeVisible();
    await user.selectOptions(screen.getByLabelText("任务状态"), "true");
    await user.click(screen.getByRole("button", { name: "保存任务" }));
    const dialog = await screen.findByRole("dialog", { name: "确认任务范围" });
    expect(within(dialog).getByRole("checkbox", { name: "我确认按此预览删除目标中的额外内容" })).toBeVisible();
    expect(updateResource).not.toHaveBeenCalledWith("tasks", "rsync-draft", expect.objectContaining({ enabled: true }));

    await user.click(within(dialog).getByRole("checkbox", { name: "我确认按此预览删除目标中的额外内容" }));
    await user.click(within(dialog).getByRole("button", { name: "确认范围并启用任务" }));
    await waitFor(() => expect(updateResource).toHaveBeenLastCalledWith("tasks", "rsync-draft", expect.objectContaining({
      enabled: true,
      previewId: "delete-preview",
      rsyncDeleteConfirmed: true,
    })));
  });

  it("uses resource dropdowns and switches task source fields by task type", async () => {
    const user = userEvent.setup();
    const records: Record<string, Array<Record<string, unknown>>> = {
      repositories: [
        { id: "repo-ready", name: "可用仓库", kind: "local", path: "/backup/ready", status: "ready" },
        { id: "repo-pending", name: "待初始化仓库", kind: "local", path: "/backup/pending", status: "uninitialized" },
      ],
      tasks: [],
      "database-connections": [
        { id: "db-backup", name: "生产 MySQL", engine: "mysql", purpose: "backup", host: "127.0.0.1" },
        { id: "db-restore", name: "恢复 MySQL", engine: "mysql", purpose: "restore", host: "127.0.0.1" },
      ],
    };
    render(<App api={{ ...fakeAPI, listResource: async (resource) => records[resource] ?? [] }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    await user.click(await screen.findByRole("button", { name: "新建备份任务" }));

    expect(await screen.findByRole("option", { name: /可用仓库.*本地.*\/backup\/ready/ })).toBeVisible();
    expect(screen.queryByRole("option", { name: /待初始化仓库/ })).not.toBeInTheDocument();
    expect(screen.getByLabelText("源目录绝对路径")).toBeVisible();
    expect(screen.queryByLabelText("数据库连接")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText("任务类型"), "database");
    expect(screen.queryByLabelText("源目录绝对路径")).not.toBeInTheDocument();
    expect(await screen.findByLabelText("数据库连接")).toBeVisible();
    expect(screen.getByRole("option", { name: /生产 MySQL/ })).toBeVisible();
    expect(screen.queryByRole("option", { name: /恢复 MySQL/ })).not.toBeInTheDocument();
    expect(screen.getByLabelText("逻辑数据库名")).toBeVisible();
  });

  it("creates local and remote repositories with type-specific fields", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => undefined);
    render(
      <App
        api={{
          ...fakeAPI,
          createResource,
          listResource: async (resource) =>
            resource === "remote-hosts"
              ? [{ id: "nas-1", name: "家庭 NAS", host: "nas.local", username: "backup" }]
              : [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));

    expect(screen.getByLabelText("仓库类型")).toHaveValue("local");
    expect(screen.getByLabelText("本机绝对路径")).toBeVisible();
    expect(screen.queryByLabelText("远程主机")).not.toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("仓库类型"), "sftp");
    expect(await screen.findByLabelText("远程主机")).toBeVisible();
    expect(screen.getByRole("option", { name: /家庭 NAS.*nas.local/ })).toBeVisible();
    expect(screen.getByLabelText("远端绝对路径")).toBeVisible();
  });

  it("automatically uses the remote host Agent to browse Restic SFTP repository paths", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/agents/agent-1/filesystem/browse" && payload?.path === "/") {
        return { path: "/", entries: [{ name: "home", path: "/home", directory: true }] };
      }
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "remote-hosts"
        ? [{ id: "host-1", name: "迷你主机", host: "192.168.0.104", username: "tmen" }]
        : resource === "agents"
          ? [{ id: "agent-1", remoteHostId: "host-1", status: "online", capabilities: ["filesystem-browse", "filesystem-create-directory"] }]
          : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));
    await user.selectOptions(screen.getByLabelText("仓库类型"), "sftp");
    await user.selectOptions(screen.getByLabelText("远程主机"), "host-1");
    expect(screen.queryByLabelText("目录浏览 Agent（可选）")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "浏览" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("远端绝对路径"), "/");

    const home = await screen.findByRole("button", { name: /home/ });
    expect(home).toBeVisible();
    expect(action).toHaveBeenCalledWith("/api/agents/agent-1/filesystem/browse", { path: "/" });
    await user.click(home);

    expect(screen.getByLabelText("远端绝对路径")).toHaveValue("/home");
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/agents/agent-1/filesystem/browse", { path: "/home" }));
  });

  it("creates a child directory inside the directory selected through the remote host Agent", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path.endsWith("/filesystem/directories")) return { path: "/home/example/restic", created: true };
      if (payload?.path === "/") return { path: "/", entries: [{ name: "home", path: "/home", directory: true }] };
      if (payload?.path === "/home") return { path: "/home", entries: [{ name: "tmen", path: "/home/example", directory: true }] };
      return { path: String(payload?.path ?? ""), entries: [] };
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "remote-hosts"
        ? [{ id: "host-1", name: "迷你主机", host: "192.168.0.104", username: "tmen" }]
        : resource === "agents"
          ? [{ id: "agent-1", remoteHostId: "host-1", status: "online", capabilities: ["filesystem-browse", "filesystem-create-directory"] }]
          : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));
    await user.selectOptions(screen.getByLabelText("仓库类型"), "sftp");
    await user.selectOptions(screen.getByLabelText("远程主机"), "host-1");
    await user.type(screen.getByLabelText("远端绝对路径"), "/");
    await user.click(await screen.findByRole("button", { name: /home/ }));
    expect(await screen.findByText("“/home”通常由系统账户管理。请先进入 Agent 运行账户自己的用户目录，再新建目录。")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "在此新建目录" }));
    expect(within(screen.getByRole("dialog", { name: "新建目录" })).getByText("该父目录通常不可由普通 Agent 账户写入。建议取消并进入该账户自己的用户主目录。")).toBeVisible();
    await user.click(within(screen.getByRole("dialog", { name: "新建目录" })).getByRole("button", { name: "取消" }));
    await user.click(await screen.findByRole("button", { name: /tmen/ }));

    expect(await screen.findByText("当前目录：/home/example")).toBeVisible();
    expect(screen.getByText("0 个子目录")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "在此新建目录" }));

    const dialog = screen.getByRole("dialog", { name: "新建目录" });
    await user.type(within(dialog).getByLabelText("新目录名称"), "restic");
    expect(within(dialog).getByText("/home/example/restic")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "创建目录" }));

    expect(confirm).not.toHaveBeenCalled();
    expect(action).toHaveBeenCalledWith("/api/agents/agent-1/filesystem/directories", { path: "/home/example/restic" });
    expect(screen.getByLabelText("远端绝对路径")).toHaveValue("/home/example/restic");
    const created = await screen.findByText("目录已创建");
    expect(created.closest(".toast")?.parentElement).toBe(document.body);
    confirm.mockRestore();
  });

  it("offers the application directory dialog when an entered absolute path cannot be found", async () => {
    const user = userEvent.setup();
    let created = false;
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path.endsWith("/filesystem/directories")) {
        created = true;
        return { path: String(payload?.path ?? ""), created: true };
      }
      if (created) return { path: String(payload?.path ?? ""), entries: [] };
      throw new Error("Agent 目录操作失败：browse directory: open /home/example/restic: no such file or directory");
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "remote-hosts"
        ? [{ id: "host-1", name: "迷你主机", host: "192.168.0.104", username: "tmen" }]
        : resource === "agents"
          ? [{ id: "agent-1", remoteHostId: "host-1", status: "online", capabilities: ["filesystem-browse", "filesystem-create-directory", "path-style:posix"] }]
          : [],
    }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));
    await user.selectOptions(screen.getByLabelText("仓库类型"), "sftp");
    await user.selectOptions(screen.getByLabelText("远程主机"), "host-1");
    await user.type(screen.getByLabelText("远端绝对路径"), "/home/example/restic");

    await user.click(await screen.findByRole("button", { name: "创建此路径" }));
    const dialog = screen.getByRole("dialog", { name: "新建目录" });
    expect(within(dialog).getByLabelText("父目录")).toHaveValue("/home/example");
    expect(within(dialog).getByLabelText("新目录名称")).toHaveValue("restic");
    await user.click(within(dialog).getByRole("button", { name: "创建目录" }));

    expect(action).toHaveBeenCalledWith("/api/agents/agent-1/filesystem/directories", { path: "/home/example/restic" });
    expect(await screen.findByText("当前目录：/home/example/restic")).toBeVisible();
  });

  it("creates an rsync repository without Restic-only capabilities", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => undefined);
    render(<App api={{ ...fakeAPI, createResource }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));

    await user.selectOptions(screen.getByLabelText("仓库引擎"), "rsync");
    expect(screen.queryByLabelText("仓库密码")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "定时维护" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("名称"), "照片同步目标");
    await user.type(screen.getByLabelText("本机绝对路径"), "/mnt/disk-b/photos");
    await user.click(screen.getByRole("button", { name: "保存仓库" }));

    expect(createResource).toHaveBeenCalledWith("repositories", expect.objectContaining({
      engine: "rsync", kind: "local", path: "/mnt/disk-b/photos",
    }));
  });

  it("hides Restic-only repository actions for rsync targets", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async () => ({}));
    render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "repositories" ? [{
      id: "sync-repo", name: "同步目标", engine: "rsync", kind: "local", path: "/mirror", status: "ready",
    }] : [] }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await screen.findByText("同步目标");
    expect(screen.queryByRole("button", { name: "轮换密码" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "初始化" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "检测容量" })).not.toBeInTheDocument();
    expect(action).not.toHaveBeenCalledWith("/api/repositories/sync-repo/capacity", {});
  });

  it("generates an SSH key on the application host for a new remote host", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => ({ id: "host-generated", publicKey: "ssh-ed25519 AAAA-generated" }));
    const action = vi.fn(async (path: string) => path === "/api/ssh/host-key" ? { fingerprint: "SHA256:server-a", knownHosts: "192.168.1.20 ssh-ed25519 AAAA-host" } : {});
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    render(<App api={{ ...fakeAPI, createResource, action }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
    await user.click(await screen.findByRole("button", { name: "新建远程主机" }));

    expect(screen.getByRole("radio", { name: "由应用生成密钥" })).toBeChecked();
    expect(screen.queryByLabelText("SSH 私钥")).not.toBeInTheDocument();
    expect(screen.getByText("私钥只加密保存在本机秘密库中，不会显示或导出。" )).toBeVisible();

    await user.type(screen.getByLabelText("名称"), "服务器 A");
    await user.type(screen.getByLabelText("主机/IP"), "192.168.1.20");
    await user.type(screen.getByLabelText("SSH 用户"), "backup");
    const hostKey = screen.getByLabelText("known_hosts 固定主机密钥行") as HTMLTextAreaElement;
    expect(hostKey).toHaveAttribute("readonly");
    await user.click(screen.getByRole("button", { name: "获取并核对主机密钥" }));
    expect(await screen.findByRole("status")).toHaveTextContent("SHA256:server-a");
    expect(hostKey).toHaveValue("192.168.1.20 ssh-ed25519 AAAA-host");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createResource).toHaveBeenCalledWith("remote-hosts", expect.objectContaining({ keyMode: "generated" }));
    expect(await screen.findByRole("heading", { name: "将 SSH 公钥授权到服务器" })).toBeVisible();
    expect(screen.getByLabelText("SSH 公钥")).toHaveValue("ssh-ed25519 AAAA-generated");
    expect(document.querySelector(".ssh-key-instructions li")).toHaveTextContent(/服务器 A.*SSH 用户 backup/);
    expect((screen.getByLabelText("服务器授权命令") as HTMLTextAreaElement).value).toContain("~/.ssh/authorized_keys");
    await user.click(screen.getByRole("button", { name: "复制 SSH 公钥" }));
    expect(writeText).toHaveBeenCalledWith("ssh-ed25519 AAAA-generated");
  });

  it("tests an existing remote host without changing remote data", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => path.endsWith("/connection-test") ? { status: "connected" } : {});
    render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "remote-hosts" ? [{ id: "host-a", name: "服务器 A", host: "192.168.1.20", port: 22, username: "backup" }] : [] }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");

    await user.click(await screen.findByRole("button", { name: "测试连接" }));
    expect(action).toHaveBeenCalledWith("/api/remote-hosts/host-a/connection-test", {});
    expect(await screen.findByText("SSH 连接验证成功")).toBeVisible();
  });

  it("generates and confirms a safely stored repository password", async () => {
	const user = userEvent.setup();
	const createResource = vi.fn(async () => undefined);
	const writeText = vi.fn(async () => undefined);
	Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
	render(<App api={{ ...fakeAPI, createResource }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "备份仓库" }));
	await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));

	expect(screen.getByLabelText("仓库密码操作")).toBeVisible();
	await user.click(screen.getByRole("button", { name: "生成密码" }));
	const password = screen.getByLabelText("仓库密码") as HTMLInputElement;
	expect(password.value.length).toBeGreaterThanOrEqual(32);
	expect(screen.getByRole("button", { name: "保存仓库" })).toBeDisabled();
	await user.click(screen.getByRole("button", { name: "复制仓库密码" }));
	expect(writeText).toHaveBeenCalledWith(password.value);
	const copied = await screen.findByText("密码已复制；请保存到密码管理器。");
	expect(copied.closest(".toast")?.parentElement).toBe(document.body);
	await user.click(screen.getByLabelText("我已将仓库密码安全保存到应用之外"));
	await user.type(screen.getByLabelText("名称"), "照片仓库");
	await user.type(screen.getByLabelText("本机绝对路径"), "/backup/photos");
	await user.click(screen.getByRole("button", { name: "保存仓库" }));

	expect(createResource).toHaveBeenCalledWith("repositories", expect.objectContaining({
		password: password.value,
		passwordConfirmed: true,
		maintenance: expect.objectContaining({ enabled: false, timezone: "Asia/Shanghai" }),
	}));
  });

  it("blocks mismatched custom repository passwords", async () => {
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));
    await user.selectOptions(screen.getByLabelText("密码来源"), "custom");
    await user.type(screen.getByLabelText("仓库密码"), "custom-password-long");
    await user.type(screen.getByLabelText("再次输入仓库密码"), "different-password-long");
    expect(screen.getByLabelText("我已将仓库密码安全保存到应用之外")).toBeDisabled();
    expect(screen.getByRole("button", { name: "保存仓库" })).toBeDisabled();
    await user.clear(screen.getByLabelText("再次输入仓库密码"));
    await user.type(screen.getByLabelText("再次输入仓库密码"), "custom-password-long");
    await user.click(screen.getByLabelText("我已将仓库密码安全保存到应用之外"));
    expect(screen.getByRole("button", { name: "保存仓库" })).toBeEnabled();
  });

  it("connects an existing repository through a persistent read-only operation", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => undefined);
    let operationReads = 0;
    const action = vi.fn(async (path: string) => {
      if (path === "/api/repositories/connect") return { operationId: "op-connect", status: "queued" };
      if (path === "/api/operations/op-connect") {
        operationReads += 1;
        return operationReads === 1
          ? { id: "op-connect", kind: "repository_connect", status: "running", stage: "verifying_read_only" }
          : { id: "op-connect", kind: "repository_connect", status: "success", stage: "connected" };
      }
      return {};
    });
    render(<App api={{ ...fakeAPI, createResource, action }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "新建备份仓库" }));
    await user.selectOptions(screen.getByLabelText("仓库接入方式"), "existing");
    await user.type(screen.getByLabelText("名称"), "既有仓库");
    await user.type(screen.getByLabelText("本机绝对路径"), "/backup/existing");
    await user.type(screen.getByLabelText("仓库密码"), "old-key");
    await user.type(screen.getByLabelText("再次输入仓库密码"), "old-key");
    await user.click(screen.getByLabelText("我已将仓库密码安全保存到应用之外"));
    await user.click(screen.getByRole("button", { name: "验证并连接" }));

    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/repositories/connect", expect.objectContaining({
      name: "既有仓库", path: "/backup/existing", password: "old-key",
    })));
    expect(createResource).not.toHaveBeenCalled();
    expect(await screen.findByText("已有仓库连接完成", {}, { timeout: 2000 })).toBeVisible();
  });

  it("offers only read-only verification for a disconnected existing repository", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (path === "/api/repositories/repo-existing/verify-existing") return { operationId: "op-verify", status: "queued" };
      if (path === "/api/operations/op-verify") return { id: "op-verify", kind: "repository_verify_existing", status: "success", stage: "connected" };
      return {};
    });
    render(<App api={{
      ...fakeAPI,
      action,
      listResource: async (resource) => resource === "repositories" ? [{
        id: "repo-existing", name: "待验证旧仓库", engine: "restic", kind: "local", path: "/backup/existing", status: "disconnected",
      }] : [],
    }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    const row = await screen.findByRole("row", { name: /待验证旧仓库/ });

    expect(within(row).getByRole("button", { name: "只读验证" })).toBeVisible();
    expect(within(row).queryByRole("button", { name: "初始化" })).not.toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: "轮换密码" })).not.toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: "只读验证" }));
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/repositories/repo-existing/verify-existing", {}));
    expect(action).not.toHaveBeenCalledWith("/api/repositories/repo-existing/capacity", {});
  });

  it("removes the standalone backup-plan navigation entry", async () => {
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    expect(screen.queryByRole("button", { name: "备份计划" })).not.toBeInTheDocument();
  });

  it("edits repository maintenance on the repository detail page", async () => {
    const user = userEvent.setup();
    const saveMaintenance = vi.fn(async () => undefined);
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path.endsWith("/maintenance-policy") && payload === undefined) return {
        enabled: true,
        updatedAt: "2026-07-12T02:00:00Z",
        nextRun: "2026-07-13T03:00:00Z",
		catchUpWindowMinutes: 45,
        boundTask: { id: "task-1", name: "照片任务" },
        retention: { keepWithinDays: 45, keepLast: 4, keepDaily: 7 },
        policyFingerprint: "sha256:policy-version",
      };
      if (path.endsWith("/maintenance")) return { previewId: "preview-1", keepCount: 7, removeCount: 2, expiresAt: "2026-07-12T03:00:00Z" };
      return {};
    });
    render(
      <App
        api={{
          ...fakeAPI,
          action,
          saveMaintenance,
          listResource: async (resource) =>
            resource === "repositories"
              ? [
                  { id: "repo-ready", name: "照片仓库", kind: "local", path: "/backup/photos", status: "ready" },
                  { id: "repo-pending", name: "待初始化仓库", kind: "local", path: "/backup/pending", status: "uninitialized" },
                ]
              : [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click((await screen.findAllByRole("button", { name: "编辑" }))[0]);

    expect(await screen.findByRole("heading", { name: "编辑备份仓库" })).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "返回仓库列表" })).toBeVisible();
    expect(screen.getByRole("button", { name: "仓库维护影响说明" })).toBeVisible();
    expect(screen.getByText(/保留策略属于仓库/)).toBeVisible();
    expect(screen.getByLabelText("保留窗口（天）")).toBeEnabled();
    expect(screen.getByLabelText("每日保留数")).toBeEnabled();
    expect(screen.getByText("sha256:policy-version")).toBeVisible();
    expect(screen.getByText(/下次执行：/)).toBeVisible();
	expect(screen.getByLabelText("离线补跑宽限（分钟）")).toHaveValue(45);
    await user.click(screen.getByRole("button", { name: "生成 dry-run 预览" }));
    expect(await screen.findByText(/保留 7 个快照，移除 2 个快照/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "保存仓库" }));
    expect(saveMaintenance).toHaveBeenCalledWith("repo-ready", expect.objectContaining({ enabled: true, catchUpWindowMinutes: 45, previewId: "preview-1" }));
  });

  it("keeps maintenance schedule editable while automatic execution is switched off", async () => {
    const user = userEvent.setup();
    render(<App api={{ ...fakeAPI, action: async (path) => path.endsWith("/maintenance-policy") ? { enabled: true, schedule: { dayOfWeek: 0, timeOfDay: "03:00" }, timezone: "Asia/Shanghai", retention: { keepWithinDays: 30, keepLast: 3 } } : {}, listResource: async (resource) => resource === "repositories" ? [{ id: "repo-1", name: "照片仓库", kind: "local", path: "/backup", status: "ready" }] : [] }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "编辑" }));
    const toggle = await screen.findByLabelText("启用定时维护");
    await user.click(toggle);
    expect(screen.getByLabelText("时区")).toBeEnabled();
    expect(screen.getByLabelText("星期")).toBeEnabled();
    expect(screen.getByLabelText("执行时间")).toBeEnabled();
    expect(screen.getByLabelText("执行时间")).toHaveValue("03:00");
  });

  it("treats migrated retention as an editable repository policy", async () => {
    const user = userEvent.setup();
    render(<App api={{
      ...fakeAPI,
      action: async (path) => path.endsWith("/maintenance-policy") ? {
        enabled: false,
        retentionConflict: true,
        retentionSource: "task",
        retention: { keepLast: 9, keepDaily: 7 },
        reviewedRetention: { keepLast: 1 },
        boundTask: { id: "task-1", name: "照片任务" },
      } : {},
      listResource: async (resource) => resource === "repositories" ? [{ id: "repo-1", name: "照片仓库", kind: "local", path: "/backup", status: "ready" }] : [],
    }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "编辑" }));

    await screen.findByRole("heading", { name: "编辑备份仓库" });
    expect(screen.queryByText(/旧仓库策略与任务策略冲突/)).not.toBeInTheDocument();
    expect(screen.getByLabelText("启用定时维护")).not.toBeChecked();
    expect(screen.getByLabelText("至少保留最近快照数")).toHaveValue(9);
    expect(screen.getByLabelText("至少保留最近快照数")).toBeEnabled();
  });

  it("disables the repository password field when editing", async () => {
    const user = userEvent.setup();
    render(<App api={{ ...fakeAPI, action: async (path) => path.endsWith("/maintenance-policy") ? { enabled: false } : {}, listResource: async (resource) => resource === "repositories" ? [{ id: "repo-1", name: "照片仓库", kind: "local", path: "/backup", status: "ready" }] : [] }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "编辑" }));
    await screen.findByText(/密码只能通过仓库操作菜单中的“轮换密码”修改/);
    expect(document.querySelector('input[name="password"]')).toBeDisabled();
    expect(screen.getByText(/密码只能通过仓库操作菜单中的“轮换密码”修改/)).toBeVisible();
  });

  it("shows initialization only for uninitialized repositories and removes stale maintenance guidance", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async () => ({}));
    render(
      <App
        api={{
          ...fakeAPI,
          action,
          listResource: async (resource) =>
            resource === "repositories"
              ? [
                  { id: "repo-ready", name: "照片仓库", kind: "local", path: "/backup/photos", status: "ready" },
                  { id: "repo-new", name: "新仓库", kind: "local", path: "/backup/new", status: "uninitialized" },
                ]
              : [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));

    expect(screen.getAllByRole("button", { name: "初始化" })).toHaveLength(1);
    expect(screen.queryByText(/真实维护将在 dry-run 预览与确认流程完成后开放/)).not.toBeInTheDocument();
    expect(action).not.toHaveBeenCalledWith(expect.stringContaining("/initialize"), {});
  });

  it("shows repository initialization in the recent-run cell and refreshes the row after completion", async () => {
    const user = userEvent.setup();
    let repositoryReads = 0;
    let operationReads = 0;
    const listResource = vi.fn(async (resource: string) => {
      if (resource !== "repositories") return [];
      repositoryReads += 1;
      return [{
        id: "repo-new",
        name: "新仓库",
        engine: "restic",
        kind: "remote",
        path: "/home/example/resback",
        status: repositoryReads > 1 ? "ready" : "uninitialized",
        capacity: { totalBytes: 1024, availableBytes: 512, checkedAt: new Date().toISOString() },
      }];
    });
    const action = vi.fn(async (path: string) => {
      if (path.endsWith("/initialize")) return { operationId: "op-init", status: "queued" };
      if (path === "/api/operations/op-init") {
        operationReads += 1;
        return operationReads === 1
          ? { id: "op-init", kind: "repository_initialize", status: "running", stage: "initializing" }
          : { id: "op-init", kind: "repository_initialize", status: "success", stage: "completed" };
      }
      throw new Error(`unexpected action ${path}`);
    });
    render(<App api={{ ...fakeAPI, listResource, action }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));

    const row = await screen.findByRole("row", { name: /新仓库/ });
    await user.click(within(row).getByRole("button", { name: "初始化" }));
    await waitFor(() => expect(row.querySelector(".repository-operation-state")).not.toBeNull());
    const running = row.querySelector(".repository-operation-state") as HTMLElement;
    expect(running).toHaveClass("repository-operation-state");
    expect(running).toHaveTextContent("正在初始化");
    expect(running.querySelector(".repository-operation-spinner")).not.toBeNull();
    expect(screen.queryByText("操作完成")).not.toBeInTheDocument();

    const completion = await screen.findByText("仓库初始化完成", {}, { timeout: 2000 });
    const toast = completion.closest(".toast");
    expect(toast).toBeVisible();
    expect(toast?.parentElement).toBe(document.body);
    await waitFor(() => expect(repositoryReads).toBeGreaterThanOrEqual(2));
    expect(await within(row).findByText("初始化完成")).toBeVisible();
    expect(within(row).getByText("已验证")).toBeVisible();
    await waitFor(() => expect(within(row).queryByText("初始化完成")).not.toBeInTheDocument(), { timeout: 2500 });
  });

  it("guides restore through resource selection, preflight, and administrator reauthentication", async () => {
	const user = userEvent.setup();
	const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
	  if (path.endsWith("/snapshots")) return [
		{ id: "dir-snap", time: "2026-07-12T01:00:00Z", paths: ["/srv/photos"], tags: ["rc:source=directory"] },
		{ id: "db-snap", time: "2026-07-12T02:00:00Z", paths: [], tags: ["rc:source=database"] },
	  ];
	  if (path.includes("/snapshots/dir-snap/contents?")) return {
		items: [
		  { name: "album", type: "dir", path: "/srv/photos/album" },
		  { name: "one.jpg", type: "file", path: "/srv/photos/album/one.jpg", size: 42 },
		],
		path: "/srv/photos", recursive: false, truncated: false,
	  };
	  if (path.endsWith("/restore-directory/preflight")) return { confirmationId: "confirm-1", summary: { behavior: "create_directory", downloadKiBPerSecond: 256, resourcePolicySource: "task" } };
	  if (path.endsWith("/authorize")) return {};
	  if (path.endsWith("/restore-directory")) return { operationId: "op-1", status: "queued" };
	  if (path === "/api/operations/op-1") return { id: "op-1", status: "success", stage: "completed" };
	  return {};
	});
	render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "repositories" ? [
	  { id: "sync-repo", name: "同步目标", engine: "rsync", kind: "local", path: "/mirror/photos", status: "ready" },
	  { id: "repo-1", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" },
	] : resource === "database-connections" ? [{ id: "restore-1", name: "恢复连接", purpose: "restore" }] : resource === "agents" ? [{ id: "agent-1", status: "online", capabilities: ["restic-restore", "filesystem-restore-target"] }] : [] }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "快照与恢复" }));
	expect(screen.getByRole("heading", { name: "从快照恢复" })).toBeVisible();
	expect(screen.queryByRole("button", { name: /从快照恢复/ })).not.toBeInTheDocument();
	expect(await screen.findByRole("option", { name: /照片仓库/ })).toBeVisible();
	expect(screen.queryByRole("option", { name: /同步目标/ })).not.toBeInTheDocument();
	expect(action).toHaveBeenCalledWith("/api/repositories/repo-1/snapshots");
	expect(action).not.toHaveBeenCalledWith("/api/repositories/sync-repo/snapshots");
	expect(screen.queryByText("已自动识别为目录备份")).not.toBeInTheDocument();
	expect(screen.queryByLabelText("数据库快照")).not.toBeInTheDocument();
	expect(screen.queryByRole("columnheader", { name: "路径" })).not.toBeInTheDocument();
	expect(screen.queryByRole("columnheader", { name: "元数据标签" })).not.toBeInTheDocument();
	expect(screen.getByRole("radio", { name: "远程 Agent" })).toBeDisabled();
	expect(screen.getByText("当前备份仓库位于控制服务本机，远程 Agent 无法直接访问；请选择 SFTP 或 S3 仓库。")).toBeVisible();
	expect(screen.queryByLabelText("仓库 ID")).not.toBeInTheDocument();
	await user.selectOptions(await screen.findByLabelText("目录快照"), "dir-snap");
	expect(screen.getByLabelText("目录快照")).toHaveTextContent("dir-snap · 2026-07-12 01:00");
	expect(screen.getByLabelText("目录快照")).not.toHaveTextContent("2026-07-12T01:00:00Z");
	expect(screen.getByLabelText("目录快照")).not.toHaveTextContent("/srv/photos");
	await user.click(screen.getByRole("button", { name: "浏览并选择快照内容" }));
	expect(await screen.findByRole("heading", { name: "浏览快照内容" })).toBeVisible();
	await user.click(screen.getByRole("checkbox", { name: "选择 /srv/photos/album" }));
	await user.click(screen.getByRole("button", { name: /返回恢复设置/ }));
	await user.click(screen.getByRole("button", { name: "浏览并选择快照内容" }));
	await waitFor(() => expect(action.mock.calls.filter(([path]) => String(path).includes("/snapshots/dir-snap/contents?")).length).toBe(1));
	await user.click(screen.getByRole("button", { name: /返回恢复设置/ }));
	await user.type(screen.getByLabelText("新目标绝对路径"), "/tmp/restored");
	await user.click(screen.getByRole("button", { name: "执行只读预检" }));
	const restoreDialog = await screen.findByRole("dialog", { name: "确认并开始目录恢复" });
	expect(restoreDialog.parentElement?.parentElement).toBe(document.body);
	expect(within(restoreDialog).getByText("预检通过", { selector: "strong" })).toBeVisible();
	expect(within(restoreDialog).getByText("有效下载限速：256 KiB/s（来自绑定任务）")).toBeVisible();
	expect(screen.getByText("目录恢复预检通过，请继续确认恢复信息。")).toBeVisible();
	await user.type(within(restoreDialog).getByLabelText("当前管理员密码"), "correct horse battery staple");
	await user.click(within(restoreDialog).getByRole("button", { name: "确认并开始目录恢复" }));
	expect(action).toHaveBeenCalledWith("/api/restores/confirm-1/authorize", { password: "correct horse battery staple" });
	expect(action).toHaveBeenCalledWith("/api/repositories/repo-1/restore-directory/preflight", { snapshotId: "dir-snap", target: "/tmp/restored", includes: ["album"], targetKind: "local", agentId: "" });
  });

  it("keeps snapshot loading visible while switching the restore repository", async () => {
    const user = userEvent.setup();
    let snapshotRead = 0;
    let resolveFirstSnapshots!: (value: Array<Record<string, unknown>>) => void;
    let resolveSecondSnapshots!: (value: Array<Record<string, unknown>>) => void;
    const action = vi.fn((path: string) => {
      if (path.endsWith("/snapshots")) {
        snapshotRead += 1;
        return new Promise<Array<Record<string, unknown>>>((resolve) => {
          if (snapshotRead === 1) resolveFirstSnapshots = resolve;
          else resolveSecondSnapshots = resolve;
        });
      }
      return Promise.resolve({});
    });
    render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "repositories" ? [
      { id: "repo-1", name: "照片仓库", kind: "local", path: "/backup/photos", status: "ready" },
      { id: "repo-2", name: "归档仓库", kind: "sftp", path: "/backup/archive", status: "ready" },
    ] : [] }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "快照与恢复" }));
    const repository = await screen.findByLabelText("备份仓库");
    expect(await screen.findByRole("status", { name: "正在读取…" })).toBeVisible();
    resolveFirstSnapshots([{ id: "first", time: "2026-07-12T01:00:00Z", paths: ["/srv/photos"], tags: ["rc:source=directory"] }]);
    await screen.findByRole("option", { name: /first/ });

    await user.selectOptions(repository, "repo-2");
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/repositories/repo-2/snapshots"));
    expect(screen.getByRole("status", { name: "正在读取…" })).toBeVisible();
    resolveSecondSnapshots([{ id: "second", time: "2026-07-13T01:00:00Z", paths: ["/srv/archive"], tags: ["rc:source=directory"] }]);
    expect(await screen.findByRole("option", { name: /second/ })).toBeVisible();
    expect(screen.queryByRole("status", { name: "正在读取…" })).not.toBeInTheDocument();
  });

  it("uses temporary database credentials unless the administrator explicitly saves them", async () => {
	const user = userEvent.setup();
	const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
	  if (path.endsWith("/snapshots")) return [{ id: "db-snap", time: "2026-07-12T02:00:00Z", paths: [], tags: ["rc:source=database"] }];
	  if (path === "/api/database-connections/temporary") return { id: "temporary-dbconn_1", temporary: true };
	  if (path.endsWith("/restore-database/preflight")) return { confirmationId: "confirm-db" };
	  return {};
	});
	render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "repositories" ? [{ id: "repo-1", name: "数据库仓库", kind: "local", path: "/backup/db", status: "ready" }] : [] }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "快照与恢复" }));
	expect(await screen.findByLabelText("数据库快照")).toBeVisible();
	expect(screen.queryByText("已自动识别为数据库备份")).not.toBeInTheDocument();
	expect(screen.queryByLabelText("目录快照")).not.toBeInTheDocument();
	await user.selectOptions(await screen.findByLabelText("数据库快照"), "db-snap");
	await user.click(screen.getByRole("radio", { name: "本次使用临时凭据" }));
	await user.type(screen.getByLabelText("临时连接名称"), "紧急恢复");
	await user.type(screen.getByLabelText("数据库地址"), "db.internal");
	await user.type(screen.getByLabelText("数据库用户名"), "restore");
	await user.type(screen.getByLabelText("数据库密码"), "one-time-secret");
	await user.type(screen.getByLabelText("目标数据库名"), "restored_db");
	await user.click(screen.getByRole("button", { name: "执行数据库只读预检" }));

	expect(action).toHaveBeenCalledWith("/api/database-connections/temporary", expect.objectContaining({ name: "紧急恢复", purpose: "restore", host: "db.internal", username: "restore", password: "one-time-secret" }));
	expect(action).toHaveBeenCalledWith("/api/repositories/repo-1/restore-database/preflight", { snapshotId: "db-snap", connectionId: "temporary-dbconn_1", database: "restored_db" });
	expect(screen.getByRole("checkbox", { name: "另存为长期恢复连接" })).not.toBeChecked();
  });

  it("browses an Agent parent directory and submits a remote restore target", async () => {
	const user = userEvent.setup();
	const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
	  if (path.endsWith("/snapshots")) return [{ id: "dir-snap", time: "2026-07-12T01:00:00Z", paths: ["/srv/photos"], tags: ["rc:source=directory"] }];
	  if (path === "/api/agents/agent-1/filesystem/browse") {
		if (payload?.path === "/srv/restore") return { path: "/srv/restore", entries: [] };
		return { path: "/srv", entries: [{ name: "restore", path: "/srv/restore", directory: true }] };
	  }
	  if (path.endsWith("/restore-directory/preflight")) return { confirmationId: "confirm-agent" };
	  return {};
	});
	render(<App api={{ ...fakeAPI, action, listResource: async (resource) => {
	  if (resource === "repositories") return [{ id: "repo-1", name: "照片仓库", kind: "sftp", path: "/backup/photos", status: "ready" }];
	  if (resource === "agents") return [{ id: "agent-1", status: "online", os: "linux", capabilities: ["restic-restore", "filesystem-restore-target", "filesystem-browse", "path-style:posix"] }];
	  return [];
	} }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "快照与恢复" }));
	await user.selectOptions(await screen.findByLabelText("目录快照"), "dir-snap");
	await user.click(screen.getByRole("radio", { name: "远程 Agent" }));
	await user.click(await screen.findByRole("button", { name: /restore/ }));
	await user.type(screen.getByLabelText("新恢复目录名称"), "photos-recovered");
	await user.click(screen.getByRole("button", { name: "执行只读预检" }));
	expect(action).toHaveBeenCalledWith("/api/repositories/repo-1/restore-directory/preflight", { snapshotId: "dir-snap", target: "/srv/restore/photos-recovered", includes: [], targetKind: "agent", agentId: "agent-1" });
  });

  it("refreshes online Agent restore candidates when restore settings opens", async () => {
	const user = userEvent.setup();
	let agentReads = 0;
	const action = vi.fn(async (path: string) => {
	  if (path.endsWith("/snapshots")) return [{ id: "dir-snap", time: "2026-07-12T01:00:00Z", paths: ["/srv/photos"], tags: ["rc:source=directory"] }];
	  return {};
	});
	render(<App api={{ ...fakeAPI, action, listResource: async (resource) => {
	  if (resource === "repositories") return [{ id: "repo-1", name: "远程照片仓库", kind: "sftp", path: "/backup/photos", status: "ready" }];
	  if (resource === "agents") {
		agentReads += 1;
		return agentReads === 1 ? [] : [{ id: "agent-1", status: "online", platform: "linux/amd64", capabilities: ["restic", "restic-restore", "filesystem-restore-target", "filesystem-browse", "path-style:posix"] }];
	  }
	  return [];
	} }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "快照与恢复" }));
	const remoteAgent = await screen.findByRole("radio", { name: "远程 Agent" });
	await waitFor(() => expect(remoteAgent).toBeEnabled());
	await user.click(remoteAgent);
	expect(screen.getByRole("option", { name: "agent-1 · linux/amd64" })).toBeVisible();
	expect(screen.getByLabelText("目标 Agent")).toHaveValue("agent-1");
	expect(agentReads).toBeGreaterThanOrEqual(2);
  });

  it("loads saved ntfy settings and preserves an existing token when left blank", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/ntfy" && payload === undefined) return { configured: true, enabled: true, baseUrl: "https://notify.example", topic: "backups", hasToken: true };
      return { configured: true };
    });
    render(<App api={{ ...fakeAPI, action }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "通知配置");
    expect(await screen.findByDisplayValue("https://notify.example")).toBeVisible();
    expect(screen.getByDisplayValue("backups")).toBeVisible();
    expect(screen.getByText("令牌已配置；留空将保留现有令牌")).toBeVisible();
	await user.click(screen.getByLabelText("清除已保存令牌"));
	await user.click(screen.getByLabelText("启用 ntfy 通知"));
    await user.click(screen.getByRole("button", { name: "保存通知配置" }));
    expect(action).toHaveBeenCalledWith("/api/ntfy", { baseUrl: "https://notify.example", topic: "backups", token: "", clearToken: true, enabled: false });
  });

  it("loads backup runs and durable operations through server-filtered activity history", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => {
      if (!path.startsWith("/api/activity?")) return {};
      const kind = new URL(path, "http://localhost").searchParams.get("kind");
      const items = [
        { recordType: "run", id: "run-1", objectName: "任务", occurredAt: "2026-07-12T01:00:00Z", kind: "backup", status: "success", startedAt: "2026-07-12T01:00:00Z", finishedAt: "2026-07-12T01:01:00Z", attemptCount: 1 },
        { recordType: "operation", id: "op-1", objectName: "仓库", occurredAt: "2026-07-12T02:00:00Z", kind: "directory_restore", status: "failed", startedAt: "2026-07-12T02:00:00Z", finishedAt: "2026-07-12T02:01:00Z", attemptCount: 1, errorSummary: "target unavailable" },
      ];
      return { items: kind ? items.filter((item) => item.kind === kind) : items, truncated: false, generatedAt: "2026-07-15T12:00:00Z", filter: {} };
    });
    render(<App api={{ ...fakeAPI, action }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "活动与记录", "运行记录");
    expect(await screen.findByText("run-1")).toBeVisible();
    expect(screen.getByText("op-1")).toBeVisible();
    await user.type(screen.getByLabelText("操作类型筛选"), "directory_restore");
    await user.click(screen.getByRole("button", { name: "应用筛选" }));
    await waitFor(() => expect(action).toHaveBeenCalledWith(expect.stringContaining("kind=directory_restore")));
    await waitFor(() => expect(screen.queryByText("run-1")).not.toBeInTheDocument());
    expect(await screen.findByText("target unavailable")).toBeVisible();
  });

  it("loads a run summary and log only after opening details", async () => {
    const user = userEvent.setup();
    const runDetail = vi.fn(async () => ({ id: "run-1", status: "failed", attemptCount: 2, summary: { error: "safe failure" }, rawLogExpired: false }));
    const runLog = vi.fn(async () => "safe log line");
    render(<App api={{ ...fakeAPI, runDetail, runLog, async action(path) {
      if (path.startsWith("/api/activity?")) return { items: [{ recordType: "run", id: "run-1", objectName: "任务", occurredAt: "2026-07-12T10:00:00Z", kind: "backup", status: "failed", startedAt: "2026-07-12T10:00:00Z", attemptCount: 2 }], truncated: false, generatedAt: "2026-07-15T12:00:00Z", filter: {} };
      return {};
    } }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "活动与记录", "运行记录");
    expect(runDetail).not.toHaveBeenCalled();
    const opener = await screen.findByRole("button", { name: "查看详情" });
    await user.click(opener);
    expect(await screen.findByText("safe log line")).toBeVisible();
    expect(runDetail).toHaveBeenCalledWith("run-1");
    expect(runLog).toHaveBeenCalledWith("run-1");
    const dialog = screen.getByRole("dialog", { name: /运行详情/ });
    expect(dialog).toHaveClass("dialog");
    expect(dialog.closest(".dialog-backdrop")?.parentElement).toBe(document.body);
    expect(screen.getByRole("link", { name: "下载日志" })).toHaveAttribute("href", "/api/runs/run-1/log?download=1");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: /运行详情/ })).not.toBeInTheDocument();
    expect(opener).toHaveFocus();
  });

  it("filters audits and exports the same action filter", async () => {
    const user = userEvent.setup();
    render(<App api={{ ...fakeAPI, async action(path) {
      if (path.startsWith("/api/audits?")) {
        const records = [
        { id: 1, occurredAt: "2026-07-12T10:00:00Z", actor: "admin", action: "task.delete", targetType: "task", targetId: "t1" },
        { id: 2, occurredAt: "2026-07-12T11:00:00Z", actor: "admin", action: "repository.delete", targetType: "repository", targetId: "r1" },
        ];
        const selected = new URL(path, "http://localhost").searchParams.get("action");
        const items = selected ? records.filter((item) => item.action === selected) : records;
        return { items, page: 1, pageSize: 25, total: items.length };
      }
      return {};
    } }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "活动与记录", "审计日志");
    await user.selectOptions(await screen.findByLabelText("动作筛选"), "task.delete");
    expect(screen.getByRole("row", { name: /task.delete/ })).toBeVisible();
    expect(screen.queryByRole("row", { name: /repository.delete/ })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "导出当前筛选 CSV" })).toHaveAttribute("href", "/api/audits/export?action=task.delete");
  });

  it("retains the old repository key until explicit revocation", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path.endsWith("/password-rotation") && payload === undefined) return { pending: false };
	  if (path.endsWith("/rotate-password")) return { operationId: "rotate-op", status: "queued" };
	  if (path === "/api/operations/rotate-op") return { id: "rotate-op", kind: "repository_password_rotation", status: "success", stage: "completed" };
      return {};
    });
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    render(
      <App
        api={{
          ...fakeAPI,
          action,
          listResource: async (resource) =>
            resource === "repositories"
              ? [{ id: "repo-ready", name: "照片仓库", kind: "local", path: "/backup/photos", status: "ready" }]
              : [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    await user.click(await screen.findByRole("button", { name: "轮换密码" }));
    await user.click(await screen.findByRole("button", { name: "生成高强度密码" }));
    await user.click(screen.getByRole("button", { name: "复制仓库密码" }));
    await user.click(screen.getByLabelText("我已将新仓库密码安全保存到应用之外"));
	await user.type(screen.getByLabelText("当前管理员密码"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "新增并验证新 key" }));

    expect(action).toHaveBeenCalledWith(
      "/api/repositories/repo-ready/rotate-password",
      expect.objectContaining({ passwordConfirmed: true }),
    );
    const rotationCompleted = await screen.findByText(/新密码已验证并启用/);
    expect(rotationCompleted.closest(".toast")?.parentElement).toBe(document.body);
    expect(screen.queryByText("操作完成")).not.toBeInTheDocument();
    expect((await screen.findAllByText(/旧 key 仍然有效/))[0]).toBeVisible();
    await user.click(screen.getByRole("button", { name: "撤销旧 key" }));
    expect(screen.getByRole("dialog", { name: "两阶段轮换仓库密码" })).toBeVisible();
    expect(screen.getAllByRole("dialog")).toHaveLength(1);
    expect(document.querySelector(".dialog-card")).toBeNull();
    expect(screen.getByRole("heading", { name: "确认撤销旧仓库 key" })).toBeVisible();
    await user.type(screen.getByLabelText("管理员密码"), "correct horse battery staple");
    await user.click(screen.getByRole("button", { name: "确认撤销旧 key" }));
    expect(action).toHaveBeenCalledWith("/api/repositories/repo-ready/revoke-old-password", { password: "correct horse battery staple" });
    const revoked = await screen.findByText("旧 key 已撤销");
    expect(revoked.closest(".toast")?.parentElement).toBe(document.body);
  });

  it("runs compatibility diagnostics from the navigation", async () => {
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "兼容性中心");
    expect(await screen.findByText("0.18.0")).toBeVisible();
    expect(screen.getByRole("tab", { name: "兼容性中心" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByRole("heading", { name: "兼容性中心", level: 1 })).not.toBeInTheDocument();
  });

  it("downloads a bounded redacted diagnostic bundle from the compatibility page", async () => {
    const user = userEvent.setup();
    const exportDiagnostics = vi.fn(async () => ({ blob: new Blob(["diagnostics"]), filename: "shadoc-diagnostics.json" }));
    const createObjectURL = vi.fn(() => "blob:diagnostics");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(<App api={{ ...fakeAPI, exportDiagnostics }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "兼容性中心");

    expect(await screen.findByRole("heading", { name: "脱敏诊断包" })).toBeVisible();
    expect(screen.getByText(/包含应用版本、兼容性状态、资源计数、近期失败、活动告警、通知渠道状态和容量健康/)).toBeVisible();
    expect(screen.getByText(/不包含秘密、原始日志、操作详情、路径、主机、用户名、URL、主题或命令参数/)).toBeVisible();
    await user.click(screen.getByRole("button", { name: "下载脱敏诊断包" }));

    await waitFor(() => expect(exportDiagnostics).toHaveBeenCalledTimes(1));
    expect(createObjectURL).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    expect(await screen.findByText("脱敏诊断包已下载")).toBeVisible();
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:diagnostics"));
  });

  it("keeps the selected administration page in browser history", async () => {
    const user = userEvent.setup();
    window.history.replaceState({}, "", "/");
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
    expect(window.location.pathname).toBe("/admin/remote-hosts");
    window.history.pushState({}, "", "/admin/dashboard");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByRole("heading", { name: "仪表盘" })).toBeVisible();
    window.history.replaceState({}, "", "/");
  });

  it("ignores an out-of-order response from a page that is no longer active", async () => {
	const user = userEvent.setup();
	let resolveHosts!: (value: Array<Record<string, unknown>>) => void;
	const hosts = new Promise<Array<Record<string, unknown>>>((resolve) => { resolveHosts = resolve; });
	render(<App api={{ ...fakeAPI, listResource: async (resource) => {
	  if (resource === "remote-hosts") return hosts;
	  if (resource === "tasks") return [{ id: "task-new", name: "当前任务", kind: "directory", enabled: false }];
	  return [];
	} }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
	await user.click(screen.getByRole("button", { name: "备份任务" }));
	expect(await screen.findByText("当前任务")).toBeVisible();
	resolveHosts([{ id: "host-stale", name: "过期主机响应" }]);
	expect(screen.queryByText("过期主机响应")).not.toBeInTheDocument();
  });

  it("shows skipped and blocked states without treating them as queued", async () => {
	render(<App api={{ ...fakeAPI, dashboard: async () => ({ repositoryStatus: "healthy", tasks: [
	  { id: "skip", name: "跳过任务", kind: "directory", status: "skipped", repository: "仓库", lastRun: "刚刚", nextRun: "稍后" },
	  { id: "block", name: "阻断任务", kind: "database", status: "blocker", repository: "仓库", lastRun: "刚刚", nextRun: "稍后" },
	], alerts: [] }) }} />);
	expect(await screen.findByText("已跳过")).toBeVisible();
	expect(screen.getByText("阻断")).toBeVisible();
	expect(screen.queryByText("等待执行")).not.toBeInTheDocument();
  });

  it("moves to login or vault unlock when a global API state event occurs", async () => {
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    window.dispatchEvent(new CustomEvent("shadoc:access-state", { detail: "locked" }));
    expect(await screen.findByRole("heading", { name: "解锁秘密库" })).toBeVisible();
  });

  it("provides an identified mobile navigation drawer with Escape and focus handling", async () => {
    const original = window.matchMedia;
    Object.defineProperty(window, "matchMedia", { configurable: true, value: vi.fn(() => ({ matches: true, addEventListener: vi.fn(), removeEventListener: vi.fn() })) });
    const user = userEvent.setup();
    render(<App api={fakeAPI} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    const drawer = document.getElementById("administration-navigation")!;
    expect(drawer).toHaveAttribute("aria-hidden", "true");
    const menu = screen.getByRole("button", { name: /打开导航/ });
    await user.click(menu);
    expect(drawer).toHaveClass("mobile-open");
    expect(screen.getByRole("button", { name: "仪表盘" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(drawer).not.toHaveClass("mobile-open");
    expect(menu).toHaveFocus();
    Object.defineProperty(window, "matchMedia", { configurable: true, value: original });
  });

  it("opens help on focus or touch and closes named dialogs with Escape", async () => {
    const user = userEvent.setup();
    render(<App api={{ ...fakeAPI, async listResource(resource) {
      if (resource === "repositories") return [{ id: "repo-1", name: "仓库", kind: "local", path: "/backup", status: "uninitialized" }];
      return [];
    } }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份仓库" }));
    const help = await screen.findByRole("button", { name: "初始化影响说明" });
    await user.click(help);
    expect(screen.getByRole("tooltip")).toBeVisible();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
    const opener = screen.getByRole("button", { name: "新建备份仓库" });
    await user.click(opener);
    expect(await screen.findByRole("heading", { name: "新建备份仓库" })).toBeVisible();
    expect(screen.queryByRole("dialog", { name: "新建备份仓库" })).not.toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("heading", { name: "新建备份仓库" })).toBeVisible();
    await user.click(screen.getByRole("button", { name: "返回仓库列表" }));
    expect(await screen.findByRole("heading", { name: "备份仓库" })).toBeVisible();
  });

  it("shows and copies a remote host ID for dependent resource forms", async () => {
    const user = userEvent.setup();
    const writeText = vi.fn(async () => undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    render(
      <App
        api={{
          ...fakeAPI,
          listResource: async (resource) =>
            resource === "remote-hosts"
              ? [
                  {
                    id: "host-copy-123",
                    name: "绿联 NAS",
                    host: "192.168.1.20",
                    port: 22,
                    username: "backup",
                  },
                ]
              : [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
    expect(await screen.findByText("host-copy-123")).toBeVisible();
    await user.click(
      screen.getByRole("button", { name: "复制 ID host-copy-123" }),
    );
    expect(writeText).toHaveBeenCalledWith("host-copy-123");
    expect(await screen.findByText("已复制 ID：host-copy-123")).toBeVisible();
    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
    await user.click(screen.getByRole("button", { name: "关闭通知" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("confirms every resource deletion with the object identity and supports cancellation", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string) => path.endsWith("/confirm") ? {} : { resourceType: "remote-hosts", id: "host-1", name: "生产 SSH", updatedAt: "2026-07-12T01:00:00Z", dependencies: [{ type: "repositories", count: 2, names: ["照片仓库", "归档仓库"] }] });
    render(<App api={{
      ...fakeAPI,
      action,
      async listResource(resource) {
        if (resource === "remote-hosts") return [{ id: "host-1", name: "生产 SSH", host: "example.test", status: "ready" }];
        return [];
      },
    }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
    await user.click(await screen.findByRole("button", { name: "删除" }));
    const firstDialog = screen.getByRole("dialog", { name: "确认删除远程主机" });
    expect(firstDialog).toHaveTextContent("生产 SSH");
    expect(firstDialog).toHaveTextContent("host-1");
	 expect(firstDialog).toHaveTextContent("照片仓库、归档仓库");
    expect(firstDialog).toHaveClass("dialog");
    expect(firstDialog.closest(".dialog-backdrop")?.parentElement).toBe(document.body);
    await user.click(screen.getByRole("button", { name: "取消删除" }));
    expect(action).not.toHaveBeenCalledWith(expect.stringMatching(/confirm$/), expect.anything());
    await user.click(screen.getByRole("button", { name: "删除" }));
    await user.click(screen.getByRole("button", { name: "确认删除" }));
	 expect(action).toHaveBeenCalledWith("/api/delete-previews/remote-hosts/host-1/confirm", { expectedUpdatedAt: "2026-07-12T01:00:00Z" });
    const deleted = await screen.findByText("已删除远程主机：生产 SSH");
    const toast = deleted.closest(".toast");
    expect(toast).toBeVisible();
    expect(toast?.parentElement).toBe(document.body);
  });

  it("shows repository, database connection, and task IDs where users need them", async () => {
    const user = userEvent.setup();
    const records: Record<string, Array<Record<string, unknown>>> = {
      repositories: [
        { id: "repo-copy-456", name: "照片仓库", path: "/photos", status: "ready" },
      ],
      "database-connections": [
        { id: "db-copy-789", name: "MySQL 备份", engine: "mysql", purpose: "backup", host: "db" },
      ],
      tasks: [
        { id: "task-copy-012", name: "照片任务", kind: "directory", repositoryId: "repo-copy-456", enabled: true },
      ],
    };
    render(
      <App
        api={{
          ...fakeAPI,
          listResource: async (resource) => records[resource] ?? [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    for (const [page, id] of [
      ["备份仓库", "repo-copy-456"],
      ["数据库实例", "db-copy-789"],
      ["备份任务", "task-copy-012"],
    ] as const) {
      if (page === "数据库实例") await openConnectionPage(user, page);
      else await user.click(screen.getByRole("button", { name: page }));
      expect(await screen.findByText(id)).toBeVisible();
      expect(
        screen.getByRole("button", { name: `复制 ID ${id}` }),
      ).toBeVisible();
    }
  });

  it("does not offer task copying because every new task needs an explicitly selected repository", async () => {
    const user = userEvent.setup();
    render(<App api={{ ...fakeAPI, listResource: async (resource) => resource === "tasks"
      ? [{ id: "task-1", name: "照片备份", kind: "directory", repositoryId: "repo-1", enabled: true }]
      : [] }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    expect(await screen.findByText("照片备份")).toBeVisible();
    expect(screen.queryByRole("button", { name: "复制为新任务" })).not.toBeInTheDocument();
  });

  it("copies IDs on LAN HTTP pages without the secure Clipboard API", async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: undefined,
    });
    const execCommand = vi.fn(() => true);
    Object.defineProperty(document, "execCommand", {
      configurable: true,
      value: execCommand,
    });
    render(
      <App
        api={{
          ...fakeAPI,
          listResource: async (resource) =>
            resource === "remote-hosts"
              ? [{ id: "host-lan-321", name: "LAN NAS", host: "nas", port: 22, username: "backup" }]
              : [],
        }}
      />,
    );
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
    await user.click(
      await screen.findByRole("button", { name: "复制 ID host-lan-321" }),
    );
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(await screen.findByText("已复制 ID：host-lan-321")).toBeVisible();
  });

  it("creates the first administrator and enters the dashboard", async () => {
    const user = userEvent.setup();
    const setup = vi.fn(async () => ({ username: "owner" }));
    render(
      <App
        api={{
          ...fakeAPI,
          setupStatus: async () => ({ initialized: false }),
          setup,
        }}
      />,
    );

    await user.type(await screen.findByLabelText("管理员名称"), "owner");
    await user.type(screen.getByLabelText("密码"), "correct-horse-battery");
    await user.type(screen.getByLabelText("再次输入密码"), "correct-horse-battery");
    await user.click(screen.getByRole("button", { name: "创建管理员" }));

    expect(setup).toHaveBeenCalledWith("owner", "correct-horse-battery", "");
    expect(
      await screen.findByRole("heading", { name: "仪表盘" }),
    ).toBeVisible();
    expect(screen.getByText("owner")).toBeVisible();
  });

  it("shows a useful error when login is rejected", async () => {
    const user = userEvent.setup();
    const login = vi.fn(async () => {
      throw new Error("用户名或密码错误");
    });
    render(
      <App
        api={{
          ...fakeAPI,
          session: async () => {
            throw new Error("unauthorized");
          },
          login,
        }}
      />,
    );

    await user.type(await screen.findByLabelText("管理员名称"), "admin");
    await user.type(screen.getByLabelText("密码"), "wrong-password");
    await user.click(screen.getByRole("button", { name: "登录" }));

    expect(await screen.findByText("用户名或密码错误")).toBeVisible();
  });

  it("shows the unlock screen after a locked restart", async () => {
    const user = userEvent.setup();
    const unlockVault = vi.fn(async (_passphrase: string) => undefined);
    let locked = true;
    render(
      <App
        api={{
          ...fakeAPI,
          vaultStatus: async () => ({
            mode: "lock-on-restart" as const,
            locked,
          }),
          unlockVault: async (passphrase) => {
            await unlockVault(passphrase);
            locked = false;
          },
        }}
      />,
    );

    await user.type(
      await screen.findByLabelText("秘密库口令"),
      "vault-passphrase",
    );
    await user.click(screen.getByRole("button", { name: "解锁" }));
    expect(unlockVault).toHaveBeenCalledWith("vault-passphrase");
    expect(
      await screen.findByRole("heading", { name: "仪表盘" }),
    ).toBeVisible();
  });

  it("loads and saves execution data lifecycle settings", async () => {
    const user = userEvent.setup();
    const saveLifecyclePolicy = vi.fn(async () => undefined);
    render(<App api={{ ...fakeAPI, saveLifecyclePolicy }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "数据生命周期");
    const runDays = await screen.findByLabelText("运行摘要保留天数");
    expect(screen.queryByRole("heading", { name: "数据生命周期", level: 1 })).not.toBeInTheDocument();
    await user.clear(runDays);
    await user.type(runDays, "90");
    await user.clear(screen.getByLabelText("原始日志总容量（MiB）"));
    await user.type(screen.getByLabelText("原始日志总容量（MiB）"), "64");
    await user.click(screen.getByRole("button", { name: "保存生命周期策略" }));
    expect(saveLifecyclePolicy).toHaveBeenCalledWith(
      expect.objectContaining({ runDays: 90, rawLogMaxBytes: 64 * 1024 * 1024 }),
    );
  });

  it("previews lifecycle cleanup and requires the administrator password", async () => {
    const user = userEvent.setup();
    const cleanupLifecycle = vi.fn(fakeAPI.cleanupLifecycle);
    render(<App api={{ ...fakeAPI, cleanupLifecycle }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "数据生命周期");
    await user.click(await screen.findByRole("button", { name: "立即清理" }));
    expect(await screen.findByText(/将清空 2 条日志/)).toBeVisible();
    const dialog = screen.getByRole("dialog", { name: "确认清理执行数据" });
    expect(dialog).toHaveClass("dialog");
    expect(dialog.closest(".dialog-backdrop")?.parentElement).toBe(document.body);
    expect(cleanupLifecycle).not.toHaveBeenCalled();
    await user.type(within(dialog).getByLabelText("管理员密码"), "correct horse battery staple");
    await user.click(within(dialog).getByRole("button", { name: "确认清理" }));
    expect(cleanupLifecycle).toHaveBeenCalledWith("correct horse battery staple");
    const completion = await screen.findByText("清理完成");
    expect(completion.closest(".toast")?.parentElement).toBe(document.body);
    expect(screen.queryByRole("dialog", { name: "确认清理执行数据" })).not.toBeInTheDocument();
  });

  it("enables restart locking and reports success after the async request", async () => {
    const user = userEvent.setup();
    const setVaultLockOnRestart = vi.fn(async () => undefined);
    render(<App api={{ ...fakeAPI, setVaultLockOnRestart }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openGroupedPage(user, "系统", "安全设置");
    await user.type(screen.getByLabelText("新秘密库口令（至少 12 个字符）"), "independent vault passphrase");
    await user.type(screen.getByLabelText("再次输入秘密库口令"), "independent vault passphrase");
    await user.click(screen.getByRole("button", { name: "启用重启后锁定" }));
    expect(setVaultLockOnRestart).toHaveBeenCalledWith("independent vault passphrase");
    expect(await screen.findByText(/已启用重启后锁定/)).toBeVisible();
  });

  it("requires explicit risk confirmation and administrator password before automatic unlock", async () => {
	const user = userEvent.setup();
	const setVaultAutomatic = vi.fn(async () => undefined);
	let automatic = false;
	render(<App api={{ ...fakeAPI, setVaultAutomatic, vaultStatus: async () => ({ mode: automatic ? "automatic" as const : "lock-on-restart" as const, locked: false }) }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await openGroupedPage(user, "系统", "安全设置");
	await user.click(screen.getByRole("button", { name: "改为自动解锁" }));
	const dialog = screen.getByRole("dialog", { name: "确认降低秘密库保护强度" });
	expect(dialog).toHaveClass("dialog");
	expect(dialog.closest(".dialog-backdrop")?.parentElement).toBe(document.body);
	await user.type(screen.getByLabelText("当前管理员密码"), "correct horse battery staple");
	await user.click(screen.getByLabelText("我理解主机失陷后托管秘密可被自动解锁"));
	automatic = true;
	await user.click(screen.getByRole("button", { name: "确认启用自动解锁" }));
	expect(setVaultAutomatic).toHaveBeenCalledWith("correct horse battery staple", true);
	const completion = await screen.findByText("已启用自动解锁");
	expect(completion.closest(".toast")?.parentElement).toBe(document.body);
  });

  it("reports a manual task run with a toast and refreshes the task list", async () => {
    const user = userEvent.setup();
    let taskReads = 0;
    const action = vi.fn(async (path: string) => {
      if (path === "/api/tasks/task-run/run") return { operationId: "task-run-op", status: "queued" };
      if (path === "/api/operations/task-run-op") return { id: "task-run-op", kind: "backup", status: "success", stage: "completed" };
      return {};
    });
    render(<App api={{ ...fakeAPI, action, listResource: async (resource) => {
      if (resource !== "tasks") return [];
      taskReads += 1;
      return [{ id: "task-run", name: "照片备份", engine: "restic", kind: "directory", repositoryId: "repo-1", enabled: true }];
    } }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    const row = await screen.findByRole("row", { name: /照片备份/ });
    expect(within(row).getByRole("button", { name: "详情" })).toBeVisible();
    await user.click(within(row).getByRole("button", { name: "立即运行" }));
    expect(action).toHaveBeenCalledWith("/api/tasks/task-run/run", {});
    expect(within(row).queryByText("操作完成")).not.toBeInTheDocument();
    const completion = await screen.findByText("备份任务运行完成");
    expect(completion.closest(".toast")?.parentElement).toBe(document.body);
    await waitFor(() => expect(taskReads).toBeGreaterThan(1));
  });

  it("disables immediate runs for tasks that are not enabled", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (_path: string) => ({}));
    render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "tasks"
      ? [{ id: "task-disabled", name: "已停用备份", engine: "restic", kind: "directory", repositoryId: "repo-1", enabled: false }]
      : [] }} />);

    await screen.findByRole("heading", { name: "仪表盘" });
    await user.click(screen.getByRole("button", { name: "备份任务" }));
    const row = await screen.findByRole("row", { name: /已停用备份/ });
    const run = within(row).getByRole("button", { name: "立即运行" });

    expect(run).toBeDisabled();
    expect(run).toHaveAttribute("title", "任务未启用，不能立即运行");
    await user.click(run);
    expect(action.mock.calls.some(([path]) => path === "/api/tasks/task-disabled/run")).toBe(false);
  });

  it("disables a task run immediately and ignores repeated clicks", async () => {
	const user = userEvent.setup();
	const action = vi.fn(async (path: string) => {
	  if (path === "/api/tasks/task-run/run") return { operationId: "task-run-op", status: "queued" };
	  if (path === "/api/operations/task-run-op") return { id: "task-run-op", kind: "backup", status: "running", stage: "running" };
	  return {};
	});
	render(<App api={{ ...fakeAPI, action, listResource: async (resource) => resource === "tasks" ? [{ id: "task-run", name: "照片备份", engine: "restic", kind: "directory", repositoryId: "repo-1", enabled: true }] : [] }} />);
	await screen.findByRole("heading", { name: "仪表盘" });
	await user.click(screen.getByRole("button", { name: "备份任务" }));
	const row = await screen.findByRole("row", { name: /照片备份/ });
	const run = within(row).getByRole("button", { name: "立即运行" });
	await user.dblClick(run);
	expect(action.mock.calls.filter(([path]) => path === "/api/tasks/task-run/run")).toHaveLength(1);
	expect(within(row).getByRole("button", { name: "运行中…" })).toBeDisabled();
  });

  it("shows the installed application version below sign out without a standalone module", async () => {
    const applicationReleases = vi.fn(async () => ({
      currentVersion: "1.2.3",
      latest: { version: "v1.3.0", publishedAt: "2026-07-15T08:00:00Z", summary: "Reliable upgrades", compatible: true, platform: "darwin_arm64" },
      updateAvailable: true,
      managed: false,
    }));
    render(<App api={{ ...fakeAPI, applicationReleases }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    const versionLabel = await screen.findByText("当前应用版本");
    const signOut = screen.getByRole("button", { name: "退出登录" });
    expect(signOut.compareDocumentPosition(versionLabel) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText("1.2.3")).toBeVisible();
    expect(screen.getByText("当前有新版本")).toBeVisible();
    await userEvent.setup().click(screen.getByRole("button", { name: "系统" }));
    expect(screen.queryByRole("tab", { name: "应用版本" })).not.toBeInTheDocument();
    expect(applicationReleases).toHaveBeenCalledOnce();
  });

  it("submits Unix socket and TLS database settings without TCP fields", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => undefined);
    render(<App api={{ ...fakeAPI, createResource }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "数据库实例");
    await screen.findByRole("heading", { name: "数据库实例" });
    await user.click(screen.getByRole("button", { name: "新建数据库实例" }));

    await user.type(screen.getByLabelText("名称"), "本机 PostgreSQL");
    await user.selectOptions(screen.getByLabelText("数据库"), "postgresql");
    await user.selectOptions(screen.getByLabelText("网络"), "unix");
    await user.type(
      screen.getByLabelText("Unix Socket 绝对路径"),
      "/var/run/postgresql",
    );
    await user.type(screen.getByLabelText("用户"), "backup");
    await user.type(screen.getByLabelText("密码"), "database-secret");
    await user.selectOptions(screen.getByLabelText("TLS 模式"), "verify-full");
    await user.type(
      screen.getByLabelText("TLS CA 文件绝对路径"),
      "/etc/ssl/db-ca.pem",
    );
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createResource).toHaveBeenCalledWith(
      "database-connections",
      expect.objectContaining({
        network: "unix",
        socketPath: "/var/run/postgresql",
        host: "",
        port: 0,
        tls: expect.objectContaining({
          mode: "verify-full",
          ca: "/etc/ssl/db-ca.pem",
        }),
      }),
    );
  });

  it("shows database preflight status, versions, and a safe failure reason", async () => {
    const user = userEvent.setup();
    render(<App api={{ ...fakeAPI, async listResource(resource) {
      if (resource === "database-connections") return [{ id: "db-1", name: "生产库", engine: "mysql", purpose: "backup", status: "draft", preflight: { checkedAt: "2026-07-12T10:00:00.508331Z", clientVersion: "8.0.36", error: "数据库认证失败" } }];
      return [];
    } }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "数据库实例");
    expect(await screen.findByText("草稿（不可启用）")).toBeVisible();
    expect(screen.getByText(/客户端 8.0.36.*数据库认证失败/)).toBeVisible();
    expect(screen.getByText(/检查于 2026-07-12T10:00:00Z/)).toBeVisible();
    expect(screen.queryByText(/2026-07-12T10:00:00\.508331Z/)).not.toBeInTheDocument();
  });

  it("probes and displays an SSH host key before saving", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async () => ({
      fingerprint: "SHA256:confirmed",
      knownHosts: "[nas.example]:2222 ssh-ed25519 AAAA",
    }));
    render(<App api={{ ...fakeAPI, action }} />);
    await screen.findByRole("heading", { name: "仪表盘" });
    await openConnectionPage(user, "远程主机");
    await user.click(
      await screen.findByRole("button", { name: "新建远程主机" }),
    );
    await user.type(screen.getByLabelText("主机/IP"), "nas.example");
    await user.clear(screen.getByLabelText("SSH 端口"));
    await user.type(screen.getByLabelText("SSH 端口"), "2222");
    await user.click(
      screen.getByRole("button", { name: "获取并核对主机密钥" }),
    );
    expect(await screen.findByText(/SHA256:confirmed/)).toBeVisible();
    expect(screen.getByLabelText("known_hosts 固定主机密钥行")).toHaveValue(
      "[nas.example]:2222 ssh-ed25519 AAAA",
    );
  });
});
