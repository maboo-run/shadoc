import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { OperationFeedback, useOperation } from "./OperationFeedback";

function Harness({ api }: { api: AppAPI }) {
  const operation = useOperation(api);
  return (
    <>
      <button onClick={() => void operation.start("/start", {})}>开始</button>
      <OperationFeedback operation={operation} />
    </>
  );
}

function AdoptHarness({ api }: { api: AppAPI }) {
  const operation = useOperation(api);
  return (
    <>
      <button onClick={() => operation.adopt({ operationId: "op-upload", status: "queued" })}>接管已上传操作</button>
      <OperationFeedback operation={operation} />
    </>
  );
}

describe("long operation feedback", () => {
  it("can adopt an operation accepted by a non-JSON upload request", async () => {
    const action = vi.fn(async () => ({ id: "op-upload", kind: "control_plane_import", status: "success", stage: "completed" }));
    render(<AdoptHarness api={{ action } as unknown as AppAPI} />);

    await userEvent.click(screen.getByRole("button", { name: "接管已上传操作" }));

    expect(await screen.findByText("操作完成")).toBeVisible();
    expect(action).toHaveBeenCalledWith("/api/operations/op-upload");
  });

  it("describes the Agent heartbeat confirmation stage", async () => {
    const action = vi.fn(async (path: string) => path === "/start"
      ? { operationId: "op-agent", status: "queued" }
      : { id: "op-agent", kind: "agent_deploy", status: "running", stage: "waiting_for_heartbeat" });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    expect(await screen.findByText("正在等待 Agent 注册和心跳")).toBeVisible();
  });

  it("describes the managed Agent stop stage", async () => {
    const action = vi.fn(async (path: string) => path === "/start"
      ? { operationId: "op-agent-uninstall", status: "queued" }
      : { id: "op-agent-uninstall", kind: "agent_uninstall", status: "running", stage: "stopping_agent" });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    expect(await screen.findByText("正在停止 Agent 服务")).toBeVisible();
  });

  it("describes Agent Restic capability verification", async () => {
    const action = vi.fn(async (path: string) => path === "/start"
      ? { operationId: "op-agent-restic", status: "queued" }
      : { id: "op-agent-restic", kind: "agent_restic_install", status: "running", stage: "waiting_for_agent_restic" });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    expect(await screen.findByText("正在验证 Agent Restic 能力心跳")).toBeVisible();
  });

  it("keeps a safe terminal failure detail and operation ID available", async () => {
    const action = vi.fn(async (path: string) => path === "/start"
      ? { operationId: "op-agent-restic", status: "queued" }
      : { id: "op-agent-restic", kind: "agent_restic_install", status: "failed", stage: "failed", errorSummary: "Agent heartbeat reported Restic 0.18.0; missing restic-restore" });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    await userEvent.click(await screen.findByText("查看失败详情"));
    expect(screen.getByText("op-agent-restic")).toBeVisible();
    expect(screen.getByText(/missing restic-restore/)).toBeVisible();
  });

  it("describes itemized protection setup progress", async () => {
    const action = vi.fn(async (path: string) => path === "/start"
      ? { operationId: "op-protection", status: "queued" }
      : { id: "op-protection", kind: "protection_setup", status: "running", stage: "protection_item" });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    expect(await screen.findByText("正在逐项创建独立保护资源")).toBeVisible();
  });

  it("shows accepted work as running and only reports completion after polling", async () => {
    let reads = 0;
    const action = vi.fn(async (path: string) => {
      if (path === "/start") return { operationId: "op-1", status: "queued" };
      reads += 1;
      return reads === 1
        ? { id: "op-1", kind: "directory_restore", status: "running", stage: "restoring" }
        : { id: "op-1", kind: "directory_restore", status: "success", stage: "completed" };
    });
    const api = { action } as unknown as AppAPI;

    render(<Harness api={api} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));

    expect(await screen.findByText("正在恢复")).toBeVisible();
    expect(screen.queryByText("操作完成")).not.toBeInTheDocument();
    expect(await screen.findByText("操作完成", {}, { timeout: 1500 })).toBeVisible();
  });

  it("surfaces cleanup-required state and provides cancellation while active", async () => {
    const action = vi
      .fn()
      .mockResolvedValueOnce({ operationId: "op-2", status: "queued" })
      .mockResolvedValueOnce({ id: "op-2", status: "running", stage: "restoring" })
      .mockResolvedValueOnce({ operationId: "op-2", status: "cancelling" })
      .mockResolvedValue({ id: "op-2", status: "cleanup_required", stage: "cleanup", detail: { residualPath: "/tmp/restore" } });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    await userEvent.click(await screen.findByRole("button", { name: "取消操作" }));

    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/operations/op-2/cancel", {}));
    expect(await screen.findByText("需要人工清理")).toBeVisible();
    expect(screen.getByText(/restore/)).toBeVisible();
  });

  it("preflights and reauthenticates before removing an owned restore residual", async () => {
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/start") return { operationId: "op-clean", status: "queued" };
      if (path === "/api/operations/op-clean") return { id: "op-clean", status: "cleanup_required", stage: "cleanup", detail: { residualPath: "/tmp/.target.restic-control-restore-owned" } };
      if (path.endsWith("/cleanup/preflight")) return { safe: true, kind: "directory_restore", residualPath: "/tmp/.target.restic-control-restore-owned" };
      if (path.endsWith("/cleanup")) {
        expect(payload).toEqual({ password: "admin-secret" });
        return { id: "op-clean", status: "failed", stage: "cleanup_resolved", detail: { cleanupResolution: "removed" } };
      }
      throw new Error(`unexpected path ${path}`);
    });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    expect(await screen.findByText("需要人工清理")).toBeVisible();

    await userEvent.click(screen.getByRole("button", { name: "检查清理条件" }));
    const cleanupDialog = await screen.findByRole("dialog", { name: "删除恢复残留" });
    expect(cleanupDialog.parentElement?.parentElement).toBe(document.body);
    expect(within(cleanupDialog).getByText(/已确认该目录属于本次恢复操作/)).toBeVisible();
    await userEvent.type(within(cleanupDialog).getByLabelText("当前管理员密码"), "admin-secret");
    await userEvent.click(within(cleanupDialog).getByRole("button", { name: "删除恢复残留" }));

    expect(await screen.findByText("残留已安全清理，恢复目标可重新预检")).toBeVisible();
    expect(action).toHaveBeenCalledWith("/api/operations/op-clean/cleanup/preflight", {});
    expect(action).toHaveBeenCalledWith("/api/operations/op-clean/cleanup", { password: "admin-secret" });
  });

  it("explains that database cleanup is externally verified instead of deleting a database", async () => {
    const action = vi.fn(async (path: string) => {
      if (path === "/start") return { operationId: "op-db", status: "queued" };
      if (path === "/api/operations/op-db") return { id: "op-db", kind: "database_restore", status: "cleanup_required", stage: "cleanup" };
      if (path.endsWith("/cleanup/preflight")) return { safe: true, kind: "database_restore", resolution: "external_cleanup_verified" };
      return { id: "op-db", kind: "database_restore", status: "failed", stage: "cleanup_resolved", detail: { cleanupResolution: "external_cleanup_verified" } };
    });
    render(<Harness api={{ action } as unknown as AppAPI} />);
    await userEvent.click(screen.getByRole("button", { name: "开始" }));
    await userEvent.click(await screen.findByRole("button", { name: "重新预检数据库目标" }));

    const cleanupDialog = await screen.findByRole("dialog", { name: "确认数据库已清理" });
    expect(within(cleanupDialog).getByText(/系统不会删除数据库/)).toBeVisible();
    expect(within(cleanupDialog).getByRole("button", { name: "确认数据库已清理" })).toBeDisabled();
  });
});
