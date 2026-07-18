import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { ControlPlaneRecovery } from "./ControlPlaneRecovery";
import type { ControlPlaneImportPreview } from "./controlPlaneTypes";

const importablePreview: ControlPlaneImportPreview = {
  previewId: "preview-1",
  expiresAt: "2026-07-15T12:15:00Z",
  canImport: true,
  sourceApplicationVersion: "1.2.3",
  resourceCounts: { repositories: 2, tasks: 3 },
  conflicts: [],
  missingTools: [{ tool: "pg_restore", path: "/opt/postgres/bin/pg_restore", requiredBy: ["database_connection:db-a"] }],
  revalidation: [{ resourceType: "repository", resourceId: "repo-a", action: "verify_existing_repository_read_only" }],
  excludedTransientClasses: ["sessions", "active_operations", "agent_enrollment_tokens"],
  restartRequired: true,
  warnings: [],
};

const originalCreateObjectURL = URL.createObjectURL;
const originalRevokeObjectURL = URL.revokeObjectURL;

afterEach(() => {
  vi.restoreAllMocks();
  if (originalCreateObjectURL) Object.defineProperty(URL, "createObjectURL", { configurable: true, value: originalCreateObjectURL });
  else Reflect.deleteProperty(URL, "createObjectURL");
  if (originalRevokeObjectURL) Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: originalRevokeObjectURL });
  else Reflect.deleteProperty(URL, "revokeObjectURL");
});

describe("control-plane disaster recovery", () => {
  it("reauthenticates, downloads an encrypted export, and clears all password fields", async () => {
    const user = userEvent.setup();
    const exportControlPlane = vi.fn(async () => ({ blob: new Blob(["sealed"]), filename: "recovery.rcbundle" }));
    const createObjectURL = vi.fn(() => "blob:recovery");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(<ControlPlaneRecovery api={{ exportControlPlane } as unknown as AppAPI} locale="zh-CN" />);

    await user.type(screen.getByLabelText("恢复包独立口令"), "recovery-passphrase");
    await user.type(screen.getByLabelText("再次输入恢复包口令"), "recovery-passphrase");
    await user.type(screen.getByLabelText("导出时的管理员密码"), "admin-password");
    await user.click(screen.getByRole("button", { name: "生成并下载恢复包" }));

    await waitFor(() => expect(exportControlPlane).toHaveBeenCalledWith({
      administratorPassword: "admin-password",
      recoveryPassphrase: "recovery-passphrase",
      recoveryPassphraseConfirmation: "recovery-passphrase",
    }));
    expect(createObjectURL).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:recovery"));
    expect(screen.getByLabelText("恢复包独立口令")).toHaveValue("");
    expect(screen.getByLabelText("再次输入恢复包口令")).toHaveValue("");
    expect(screen.getByLabelText("导出时的管理员密码")).toHaveValue("");
    expect(await screen.findByText("加密恢复包已生成，请立即保存到控制服务之外。" )).toBeVisible();
  });

  it("preflights a bundle, shows impact, and requires a fresh confirmation before import", async () => {
    const user = userEvent.setup();
    const bundle = new File(["sealed"], "recovery.rcbundle", { type: "application/octet-stream" });
    const preflightControlPlaneImport = vi.fn(async () => importablePreview);
    const importControlPlane = vi.fn(async () => ({ operationId: "op-import", status: "queued" }));
    const action = vi.fn(async () => ({
      id: "op-import", kind: "control_plane_import", status: "success", stage: "completed",
      detail: { importedCounts: { repositories: 2, tasks: 3 }, restartRequired: true },
    }));
    render(<ControlPlaneRecovery api={{ preflightControlPlaneImport, importControlPlane, action } as unknown as AppAPI} locale="zh-CN" />);

    const fileInput = screen.getByLabelText("控制面恢复包") as HTMLInputElement;
    await user.upload(fileInput, bundle);
    await user.type(screen.getByLabelText("预检恢复口令"), "recovery-passphrase");
    await user.click(screen.getByRole("button", { name: "执行只读导入预检" }));

    await waitFor(() => expect(preflightControlPlaneImport).toHaveBeenCalled());
    expect(await screen.findByText("源版本：1.2.3")).toBeVisible();
    expect(screen.getByText("备份仓库：2")).toBeVisible();
    expect(screen.getByText("/opt/postgres/bin/pg_restore")).toBeVisible();
    expect(screen.getByText("repo-a")).toBeVisible();
    expect(screen.getByText("登录会话")).toBeVisible();
    expect(screen.queryByText("sessions")).not.toBeInTheDocument();
    expect(screen.getByText("导入 Agent CA 后必须重启控制服务，才能重新启用 Agent 身份。" )).toBeVisible();
    expect(screen.getByLabelText("预检恢复口令")).toHaveValue("");
    expect(preflightControlPlaneImport).toHaveBeenCalledWith(bundle, "recovery-passphrase");

    await user.click(screen.getByRole("button", { name: "确认导入控制面" }));
    const firstDialog = screen.getByRole("dialog", { name: "确认导入控制面" });
    expect(firstDialog.parentElement?.parentElement).toBe(document.body);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "确认导入控制面" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "确认导入控制面" }));
    const dialog = screen.getByRole("dialog", { name: "确认导入控制面" });
    const importButton = within(dialog).getByRole("button", { name: "开始导入" });
    expect(importButton).toBeDisabled();
    await user.type(within(dialog).getByLabelText("导入恢复口令"), "recovery-passphrase");
    await user.type(within(dialog).getByLabelText("当前管理员密码"), "admin-password");
    await user.click(within(dialog).getByLabelText("我确认目标中没有同名资源，并理解导入资源会保持停用直到逐项复验"));
    await user.click(importButton);

    await waitFor(() => expect(importControlPlane).toHaveBeenCalledWith(bundle, {
      recoveryPassphrase: "recovery-passphrase",
      previewId: "preview-1",
      administratorPassword: "admin-password",
      impactConfirmed: true,
    }));
    expect(await screen.findByText("控制面恢复导入完成；请按复验清单逐项重新接入。", {}, { timeout: 1500 })).toBeVisible();
    expect(action).toHaveBeenCalledWith("/api/operations/op-import");
    expect(fileInput.files).toHaveLength(0);
    expect(screen.queryByText("源版本：1.2.3")).not.toBeInTheDocument();
  });

  it("blocks import when preflight finds conflicts and rejects oversized files locally", async () => {
    const user = userEvent.setup();
    const preview = {
      ...importablePreview,
      previewId: undefined,
      canImport: false,
      conflicts: [{ resourceType: "repository", resourceId: "repo-a", field: "name", value: "photos", existingId: "repo-existing" }],
    };
    const preflightControlPlaneImport = vi.fn(async () => preview);
    render(<ControlPlaneRecovery api={{ preflightControlPlaneImport, action: vi.fn() } as unknown as AppAPI} locale="zh-CN" />);
    const fileInput = screen.getByLabelText("控制面恢复包") as HTMLInputElement;
    const oversized = new File(["small"], "too-large.rcbundle");
    Object.defineProperty(oversized, "size", { value: 33 * 1024 * 1024 });

    await user.upload(fileInput, oversized);
    expect(await screen.findByRole("alert")).toHaveTextContent("恢复包不能超过 32 MiB");
    expect(fileInput.files).toHaveLength(0);

    const bundle = new File(["sealed"], "recovery.rcbundle");
    await user.upload(fileInput, bundle);
    await user.type(screen.getByLabelText("预检恢复口令"), "recovery-passphrase");
    await user.click(screen.getByRole("button", { name: "执行只读导入预检" }));

    await waitFor(() => expect(preflightControlPlaneImport).toHaveBeenCalled());
    expect(await screen.findByText("目标中存在冲突，不能导入。请先处理冲突并重新预检。" )).toBeVisible();
    expect(screen.getByText("photos")).toBeVisible();
    expect(screen.getByText("repo-existing")).toBeVisible();
    expect(screen.queryByRole("button", { name: "确认导入控制面" })).not.toBeInTheDocument();
  });

  it("renders the complete recovery and revalidation workflow in English", async () => {
    const user = userEvent.setup();
    const preflightControlPlaneImport = vi.fn(async () => importablePreview);
    const { container } = render(<ControlPlaneRecovery api={{ preflightControlPlaneImport, action: vi.fn() } as unknown as AppAPI} locale="en-US" timeZone="UTC" />);
    const bundle = new File(["sealed"], "recovery.rcbundle", { type: "application/octet-stream" });

    expect(screen.getByText("Export an authenticated encrypted control-plane recovery bundle, or preflight and restore durable configuration on a fresh Service.")).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Configuration backup & restore", level: 1 })).not.toBeInTheDocument();
    await user.upload(screen.getByLabelText("Control-plane recovery bundle"), bundle);
    await user.type(screen.getByLabelText("Recovery passphrase for preflight"), "recovery-passphrase");
    await user.click(screen.getByRole("button", { name: "Run read-only import preflight" }));

    expect(await screen.findByText("Source version: 1.2.3")).toBeVisible();
    expect(screen.getByText("Login sessions")).toBeVisible();
    expect(screen.queryByText("sessions")).not.toBeInTheDocument();
    expect(screen.getByText("Verify the existing repository read-only and confirm snapshots are readable")).toBeVisible();
    expect(screen.getByRole("button", { name: "Confirm control-plane import" })).toBeVisible();
    expect(container.textContent).not.toMatch(/[\u3400-\u9fff]/);
  });
});
