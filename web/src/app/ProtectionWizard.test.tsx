import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { ProtectionWizard } from "./ProtectionWizard";

function wizardAPI() {
  const createResource = vi.fn(async (resource: string, payload: Record<string, unknown>) => {
    if (resource === "protection-templates") return { id: "template-created", ...payload };
    if (resource === "protection-drafts") {
      const items = (payload.items as Array<Record<string, unknown>>).map((item, index) => ({
        id: `item-${index + 1}`, draftId: "draft-1", repositoryId: `repo-${index + 1}`, taskId: `task-${index + 1}`,
        status: "pending", hasPassword: true, ...item, password: undefined,
      }));
      return { id: "draft-1", name: payload.name, templateId: payload.templateId, planId: "plan-1", status: "pending", items };
    }
    return {};
  });
  const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
    if (path === "/api/local-filesystem/settings") return { roots: ["/srv"] };
    if (path === "/api/local-filesystem/browse") return { path: "/srv", entries: [{ name: "photos", path: "/srv/photos", directory: true }] };
    if (path === "/api/local-filesystem/directories") return { path: String(payload?.path ?? ""), created: true };
    if (path === "/api/ntfy") return { configured: true, enabled: true, topic: "backup" };
    if (path === "/api/database-connections/connection-1/databases") return { items: ["accounts", "orders"] };
    if (path === "/api/protection-drafts/draft-1/apply") return { operationId: "operation-1", status: "queued" };
    if (path === "/api/operations/operation-1") return { id: "operation-1", kind: "protection_setup", status: "success", stage: "completed" };
    if (path === "/api/protection-drafts/draft-1") return {
      id: "draft-1", planId: "plan-1", status: "ready", items: [
        { id: "item-1", taskId: "task-1", repositoryId: "repo-1", taskName: "accounts", repositoryName: "accounts repository", repositoryPath: "/backup/accounts", status: "ready", hasPassword: true, database: { connectionId: "connection-1", database: "accounts" } },
        { id: "item-2", taskId: "task-2", repositoryId: "repo-2", taskName: "orders", repositoryName: "orders repository", repositoryPath: "/backup/orders", status: "ready", hasPassword: true, database: { connectionId: "connection-1", database: "orders" } },
      ],
    };
    if (path === "/api/protection-drafts/draft-1/checklist") return { draftId: "draft-1", draftStatus: "ready", planId: "plan-1", planStatus: "disabled", notificationStatus: "ready", complete: false, items: [] };
    return payload === undefined ? [] : {};
  });
  const api = {
    listResource: vi.fn(async (resource: string) => {
      if (resource === "protection-templates") return [{ id: "template-1", name: "Daily", retention: { keepDaily: 7 }, resources: { compression: "auto" }, health: { maxSuccessAgeHours: 30 }, schedule: { kind: "daily", timeOfDay: "02:00" }, timezone: "UTC", maxParallel: 2, catchUpWindowMinutes: 120 }];
      if (resource === "protection-drafts") return [];
      if (resource === "database-connections") return [{ id: "connection-1", name: "Production", purpose: "backup", status: "ready", engine: "mysql" }];
      if (resource === "agents") return [];
      if (resource === "remote-hosts") return [];
      if (resource === "tasks" || resource === "plans") return [];
      return [];
    }),
    createResource,
    updateResource: vi.fn(async () => undefined),
    deleteResource: vi.fn(async () => undefined),
    runTask: vi.fn(async () => undefined),
    action,
  } as unknown as AppAPI;
  return { api, action, createResource };
}

describe("create protection wizard", () => {
  it("enumerates databases and previews an exact N-to-N task and repository mapping before creation", async () => {
    const user = userEvent.setup();
    const { api, createResource } = wizardAPI();
    render(<ProtectionWizard api={api} locale="zh-CN" timeZone="Asia/Shanghai" onNavigate={vi.fn()} />);

    await user.selectOptions(await screen.findByLabelText("保护对象类型"), "database");
    await user.selectOptions(screen.getByLabelText("数据库连接"), "connection-1");
    await user.click(screen.getByRole("button", { name: "读取逻辑数据库" }));
    await user.click(await screen.findByRole("checkbox", { name: "accounts" }));
    await user.click(screen.getByRole("checkbox", { name: "orders" }));
    await user.click(screen.getByRole("button", { name: "下一步：仓库映射" }));

    const mapping = await screen.findByRole("table", { name: "保护映射预览" });
    expect(within(mapping).getByText("accounts")).toBeVisible();
    expect(within(mapping).getByText("orders")).toBeVisible();
    expect(within(mapping).getAllByText(/独立仓库/)).toHaveLength(2);
    await user.clear(screen.getByLabelText("仓库基础路径"));
    await user.type(screen.getByLabelText("仓库基础路径"), "/backup");
    await user.click(screen.getByRole("button", { name: "更新映射路径" }));
    expect(within(mapping).getByDisplayValue("/backup/accounts")).toBeVisible();
    expect(within(mapping).getByDisplayValue("/backup/orders")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "下一步：保护策略" }));
    await user.selectOptions(screen.getByLabelText("保护模板"), "template-1");
    await user.click(screen.getByRole("button", { name: "下一步：确认创建" }));
    await user.click(screen.getByRole("checkbox", { name: "我已安全保存全部独立仓库密码" }));
    await user.click(screen.getByRole("button", { name: "保存草稿并创建保护" }));

    await waitFor(() => expect(createResource).toHaveBeenCalledWith("protection-drafts", expect.objectContaining({ templateId: "template-1" })));
    const payload = createResource.mock.calls.find(([resource]) => resource === "protection-drafts")?.[1] as Record<string, unknown>;
    const items = payload.items as Array<Record<string, unknown>>;
    expect(items).toHaveLength(2);
    expect(new Set(items.map((item) => item.repositoryPath))).toHaveLength(2);
    expect(items.every((item) => item.passwordConfirmed === true)).toBe(true);
    expect(await screen.findByRole("heading", { name: "保护检查表" })).toBeVisible();
  });

  it("uses the Service allowed roots for local browsing and can update those roots", async () => {
    const user = userEvent.setup();
    const { api, action } = wizardAPI();
    render(<ProtectionWizard api={api} locale="zh-CN" timeZone="Asia/Shanghai" onNavigate={vi.fn()} />);

    await screen.findByLabelText("目录路径");
    await user.clear(screen.getByLabelText("目录路径"));
    await user.type(screen.getByLabelText("目录路径"), "/srv");
    await user.click(screen.getByRole("button", { name: "浏览本机目录" }));
    await user.click(await screen.findByRole("button", { name: "选择 photos" }));
    expect(screen.getByLabelText("待保护目录（每行一个）")).toHaveValue("/srv/photos");

    await user.click(screen.getByRole("button", { name: "配置允许根目录" }));
    await user.clear(screen.getByLabelText("允许根目录（每行一个）"));
    await user.type(screen.getByLabelText("允许根目录（每行一个）"), "/srv\n/mnt/data");
    await user.click(screen.getByRole("button", { name: "保存允许根目录" }));
    await waitFor(() => expect(api.updateResource).toHaveBeenCalledWith("local-filesystem", "settings", { roots: ["/srv", "/mnt/data"] }));
    expect(action).toHaveBeenCalledWith("/api/local-filesystem/browse", { path: "/srv" });

    await user.type(screen.getByLabelText("在当前目录中新建"), "archive");
    await user.click(screen.getByRole("button", { name: "创建并选择" }));
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/local-filesystem/directories", { path: "/srv/photos/archive" }));
  });

  it("disambiguates repository paths for different directories with the same basename", async () => {
    const user = userEvent.setup();
    const { api } = wizardAPI();
    render(<ProtectionWizard api={api} locale="zh-CN" timeZone="Asia/Shanghai" onNavigate={vi.fn()} />);

    const sources = await screen.findByLabelText("待保护目录（每行一个）");
    await user.type(sources, "/srv/team-a/photos\n/srv/team-b/photos");
    await user.click(screen.getByRole("button", { name: "下一步：仓库映射" }));

    expect(screen.getByLabelText("仓库路径 1")).toHaveValue("/srv/shadoc/photos-1");
    expect(screen.getByLabelText("仓库路径 2")).toHaveValue("/srv/shadoc/photos-2");
  });
});
