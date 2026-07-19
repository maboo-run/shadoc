import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { AgentFleet } from "./AgentFleet";
import type { OperationController } from "./OperationFeedback";

describe("Agent fleet health", () => {
  it("keeps Agent upgrade progress inside only the affected Agent record", () => {
    const operation = {
      operation: { id: "op-upgrade", kind: "agent_upgrade", status: "running", stage: "staging_agent_upgrade" },
      active: true,
      error: "",
    } as unknown as OperationController;
    render(<AgentFleet
      agents={[
        { id: "agent-a", status: "online", runtimeStatus: "running", platform: "linux/amd64" },
        { id: "agent-b", status: "online", runtimeStatus: "running", platform: "linux/amd64" },
      ]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      busy
      upgradeOperation={{ agentId: "agent-b", operation }}
      onUpgrade={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);

    const firstAgent = screen.getByText("agent-a").closest("article");
    const affectedAgent = screen.getByText("agent-b").closest("article");
    expect(firstAgent).not.toHaveTextContent("正在暂存新 Agent 程序");
    expect(affectedAgent).toHaveTextContent("正在暂存新 Agent 程序");
  });

  it("keeps managed Restic progress inside only the affected Agent record", async () => {
    const user = userEvent.setup();
    const onCancelRestic = vi.fn();
    render(<AgentFleet
      agents={[
        { id: "agent-a", status: "online", runtimeStatus: "running", platform: "linux/amd64" },
        { id: "agent-b", status: "online", runtimeStatus: "running", platform: "linux/amd64" },
      ]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      busy
      resticOperation={{ agentId: "agent-b", active: true, status: "running", stage: "downloading_agent_restic" }}
      onCancelRestic={onCancelRestic}
      onUpgrade={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);

    const firstAgent = screen.getByText("agent-a").closest("article");
    const affectedAgent = screen.getByText("agent-b").closest("article");
    expect(firstAgent).not.toHaveTextContent("正在下载并校验 Agent Restic");
    expect(affectedAgent).toHaveTextContent("正在下载并校验 Agent Restic");
    await user.click(screen.getByRole("button", { name: "取消操作" }));
    expect(onCancelRestic).toHaveBeenCalledOnce();
  });

  it("shows one compact record and reveals operational details on demand", async () => {
    const user = userEvent.setup();
    const onUpgrade = vi.fn();
    const onInstallRestic = vi.fn();
    const onReprobeTools = vi.fn();
    const onProbeHeartbeat = vi.fn();
    render(<AgentFleet
      agents={[{
        id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", compatibilityStatus: "compatible", taskEligible: true,
        buildVersion: "v1.3.0", targetVersion: "v1.4.0", upgradeAvailable: true, platform: "linux/arm64",
        protocolMin: 1, protocolMax: 1, protocolCompatible: true, certificateStatus: "expiring_30",
        certificateNotAfter: "2026-08-04T12:00:00Z", renewalStatus: "healthy", resticVersion: "0.18.0",
        capabilities: ["managed-restic-install-v1", "restic", "filesystem-browse"], endpointStatus: "current",
        serviceUrl: "https://control.internal:9443", lastHeartbeatAt: "2026-07-15T11:59:30Z",
      }]}
      remoteHosts={[{ id: "host-a", name: "备份主机", host: "nas.example", port: 22, username: "backup" }]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      latestResticVersion="0.19.1"
      busy={false}
      onUpgrade={onUpgrade}
      onInstallRestic={onInstallRestic}
      onReprobeTools={onReprobeTools}
      onProbeHeartbeat={onProbeHeartbeat}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);

    expect(screen.getByText("agent-a")).toBeVisible();
    expect(screen.getByText("在线")).toBeVisible();
    expect(screen.getByText("nas.example:22")).toBeVisible();
    expect(screen.getByText("2026年7月15日 19:59:30")).toBeVisible();
    expect(screen.queryByText("通信兼容性")).not.toBeInTheDocument();
    expect(screen.queryByText("Restic 0.18.0")).not.toBeInTheDocument();
    expect(screen.queryByText("浏览目录")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "agent-a 查看详情" }));

    expect(screen.getByText("通信兼容性")).toBeVisible();
    expect(screen.getByText("兼容")).toBeVisible();
    expect(screen.queryByText("协议 1–1")).not.toBeInTheDocument();
    expect(screen.getByText("Restic 0.18.0")).toBeVisible();
    expect(screen.getByText("https://control.internal:9443")).toBeVisible();
    expect(screen.queryByText("地址一致")).not.toBeInTheDocument();
    expect(screen.getByText("30 天内到期")).toBeVisible();
    expect(screen.getByText("备份主机 · backup@nas.example:22")).toBeVisible();
    expect(screen.queryByText("能力探测")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "升级 Agent 至 v1.4.0" }));
    expect(onUpgrade).toHaveBeenCalledWith(expect.objectContaining({ id: "agent-a" }));
    await user.click(screen.getByRole("button", { name: "升级 Agent Restic" }));
    expect(onInstallRestic).toHaveBeenCalledWith(expect.objectContaining({ id: "agent-a" }));
    await user.click(screen.getByRole("button", { name: "重新探测工具" }));
    expect(onReprobeTools).toHaveBeenCalledWith(expect.objectContaining({ id: "agent-a" }));
    await user.click(screen.getByRole("button", { name: "主动探测心跳" }));
    expect(onProbeHeartbeat).toHaveBeenCalledWith(expect.objectContaining({ id: "agent-a" }));
  });

  it("shows heartbeat probing on the action button without an inline operation row", async () => {
    const user = userEvent.setup();
    const operation = {
      operation: { id: "op-heartbeat", kind: "agent_heartbeat_probe", status: "running", stage: "restarting_agent_for_heartbeat" },
      active: true,
      error: "",
    } as unknown as OperationController;
    render(<AgentFleet
      agents={[{ id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", platform: "linux/amd64" }]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      busy={false}
      heartbeatOperation={{ agentId: "agent-a", operation }}
      onProbeHeartbeat={vi.fn()}
      onUpgrade={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);

    await user.click(screen.getByRole("button", { name: "agent-a 查看详情" }));

    const button = screen.getByRole("button", { name: "探测中…" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveClass("agent-heartbeat-button-probing");
    expect(button.querySelector(".agent-heartbeat-spinner")).not.toBeNull();
    expect(screen.queryByText("正在重启 Agent 以主动探测心跳")).not.toBeInTheDocument();
  });

  it("omits no-action Restic and Service health confirmations", async () => {
    const user = userEvent.setup();
    render(<AgentFleet
      agents={[{
        id: "healthy", remoteHostId: "host-a", status: "online", runtimeStatus: "running", compatibilityStatus: "compatible", taskEligible: true,
        buildVersion: "v1.4.0", targetVersion: "v1.4.0", upgradeAvailable: false, platform: "linux/amd64",
        protocolMin: 1, protocolMax: 1, protocolCompatible: true, certificateStatus: "valid", resticVersion: "0.19.1",
        capabilities: ["managed-restic-install-v1", "restic"], endpointStatus: "current", serviceUrl: "https://control.internal:9443",
      }]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      latestResticVersion="0.19.1"
      busy={false}
      onUpgrade={vi.fn()}
      onInstallRestic={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);

    await user.click(screen.getByRole("button", { name: "healthy 查看详情" }));
    expect(screen.getByText("通信兼容性")).toBeVisible();
    expect(screen.getByText("兼容")).toBeVisible();
    expect(screen.getByText("https://control.internal:9443")).toBeVisible();
    expect(screen.queryByText("地址一致")).not.toBeInTheDocument();
    expect(screen.queryByText("Agent Restic 已是最新版本。")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /升级 Agent 至/ })).not.toBeInTheDocument();
  });

  it("keeps managed Restic installation visible while explaining an old Agent binary", async () => {
    const user = userEvent.setup();
    render(<AgentFleet
      agents={[{ id: "legacy", remoteHostId: "host-a", status: "online", runtimeStatus: "running", platform: "linux/amd64", capabilities: [] }]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      latestResticVersion="0.19.1"
      busy={false}
      onUpgrade={vi.fn()}
      onInstallRestic={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);
    await user.click(screen.getByRole("button", { name: "legacy 查看详情" }));
    expect(screen.getByRole("button", { name: "安装 Agent Restic" })).toBeDisabled();
    expect(screen.getByText("该 Agent 版本不支持一键安装 Restic，请先升级或重新部署 Agent。")).toBeVisible();
  });

  it("offers a same-version Agent repair when the managed Restic capability is missing", async () => {
    const user = userEvent.setup();
    const onUpgrade = vi.fn();
    render(<AgentFleet
      agents={[{
        id: "legacy", remoteHostId: "host-a", status: "online", runtimeStatus: "running", platform: "linux/amd64",
        buildVersion: "v1.4.0", targetVersion: "v1.4.0", upgradeAvailable: true, capabilities: [],
      }]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      latestResticVersion="0.19.1"
      busy={false}
      onUpgrade={onUpgrade}
      onInstallRestic={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);
    await user.click(screen.getByRole("button", { name: "legacy 查看详情" }));
    await user.click(screen.getByRole("button", { name: "更新 Agent 以启用 Restic 安装" }));
    expect(onUpgrade).toHaveBeenCalledWith(expect.objectContaining({ id: "legacy" }));
  });

  it("keeps the install action visible when the official version catalog is temporarily unavailable", async () => {
    const user = userEvent.setup();
    render(<AgentFleet
      agents={[{ id: "agent-a", remoteHostId: "host-a", status: "online", runtimeStatus: "running", platform: "linux/amd64", capabilities: ["managed-restic-install-v1"] }]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      latestResticVersion=""
      busy={false}
      onUpgrade={vi.fn()}
      onInstallRestic={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);
    await user.click(screen.getByRole("button", { name: "agent-a 查看详情" }));
    expect(screen.getByRole("button", { name: "安装 Agent Restic" })).toBeDisabled();
    expect(screen.getByText("暂时无法读取官方 Restic 版本，页面会自动重试。")).toBeVisible();
  });

  it("makes endpoint migration and manual update steps explicit", async () => {
    const user = userEvent.setup();
    render(<AgentFleet
      agents={[{
        id: "manual-a", runtimeStatus: "running", compatibilityStatus: "incompatible", taskEligible: false,
        buildVersion: "v1.2.0", targetVersion: "v1.4.0", upgradeAvailable: true, platform: "linux/amd64",
        protocolMin: 2, protocolMax: 2, protocolCompatible: false, certificateStatus: "valid",
        certificateNotAfter: "2027-07-15T12:00:00Z", endpointStatus: "migration_required",
        serviceUrl: "https://old-control.internal:9443", lastHeartbeatAt: "2026-07-15T11:59:30Z",
      }]}
      remoteHosts={[]}
      locale="zh-CN"
      timeZone="Asia/Shanghai"
      currentServiceURL="https://control.internal:9443"
      busy={false}
      onUpgrade={vi.fn()}
      onRedeploy={vi.fn()}
      onRemove={vi.fn()}
    />);

    await user.click(screen.getByRole("button", { name: "manual-a 查看详情" }));

    expect(screen.getByText("协议不兼容")).toBeVisible();
    expect(screen.getByText("未检测到 Restic 或 rsync")).toBeVisible();
    expect(screen.getByRole("heading", { name: "需要迁移 Service 地址" })).toBeVisible();
    expect(screen.getByText("https://old-control.internal:9443")).toBeVisible();
    expect(screen.getByText("https://control.internal:9443")).toBeVisible();
    expect(screen.getByRole("heading", { name: "手动安装的 Agent 需要手工更新" })).toBeVisible();
    expect(screen.getByRole("heading", { name: "此 Agent 需要手工安装 Restic" })).toBeVisible();
    expect(screen.queryByRole("button", { name: /升级到/ })).not.toBeInTheDocument();
  });
});
