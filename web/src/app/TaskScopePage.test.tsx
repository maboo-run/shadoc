import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { TaskScopePage } from "./TaskScopePage";

describe("TaskScopePage", () => {
  it("browses one directory level at a time and marks partially protected folders", async () => {
    const user = userEvent.setup();
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/tasks/task-a/scope-inventory") {
        expect(payload).toEqual({});
        return { operationId: "op-preview", status: "queued", kind: "task_scope_inventory" };
      }
      if (path === "/api/operations/op-preview") return {
        id: "op-preview", kind: "task_scope_inventory", status: "success", stage: "completed",
        detail: {
          previewId: "preview-a",
          expiresAt: "2026-07-25T08:15:00Z",
          summary: {
            scannedItems: 6, totalFiles: 4, includedFiles: 2, excludedFiles: 1, unreadableItems: 1,
            includedBytes: 2048, excludedBytes: 128, entriesAvailable: true,
            activeRules: [{ rule: "**/*.tmp", matchedFiles: 1, estimatedBytes: 128 }],
          },
        },
      };
      if (path.startsWith("/api/task-scope-previews/preview-a/entries")) {
        const query = new URL(path, "http://localhost").searchParams;
        expect(query.get("children")).toBe("true");
        if (query.get("view") === "excluded") return {
          items: [{ ordinal: 4, path: "cache", type: "directory", disposition: "excluded", coverage: "excluded", totalFiles: 1, excludedFiles: 1 }],
          truncated: false,
        };
        if (query.get("parent") === "photos/2025") return {
          items: [
            { ordinal: 7, path: "photos/2025/b.jpg", type: "file", disposition: "included", size: 1024 },
          ],
          truncated: false,
        };
        if (query.get("parent") === "photos") return {
          items: [
            { ordinal: 2, path: "photos/a.jpg", type: "file", disposition: "included", size: 2048 },
            { ordinal: 3, path: "photos/private.key", type: "file", disposition: "unreadable", reasonCode: "permission_denied" },
            { ordinal: 6, path: "photos/2025", type: "directory", disposition: "included", coverage: "full", totalFiles: 1, includedFiles: 1 },
          ],
          truncated: false,
        };
        return {
          items: [
            { ordinal: 1, path: "photos", type: "directory", disposition: "attention", coverage: "partial", totalFiles: 3, includedFiles: 2, unreadableItems: 1 },
            { ordinal: 5, path: "README.md", type: "file", disposition: "included", size: 64 },
          ],
          truncated: false,
        };
      }
      throw new Error(`unexpected ${path}`);
    });
    render(<TaskScopePage taskId="task-a" taskName="照片备份" api={{ action } as unknown as AppAPI} locale="zh-CN" onBack={() => undefined} />);

    expect(await screen.findByRole("heading", { name: "保护范围透镜" })).toBeVisible();
    expect(await screen.findByText("photos")).toBeVisible();
    expect(screen.queryByText("photos/a.jpg")).not.toBeInTheDocument();
    expect(screen.getByText("部分保护 · 将保护 2 / 3 个文件")).toBeVisible();
    expect(screen.getByText("源目录总文件")).toBeVisible();
    expect(screen.getByText("4")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "打开文件夹 photos" }));
    expect(await screen.findByText("a.jpg")).toBeVisible();
    expect(screen.getByText("private.key")).toBeVisible();
    expect(screen.queryByText("photos/a.jpg")).not.toBeInTheDocument();
    const includedRow = screen.getByText("a.jpg").closest("li")!;
    expect(includedRow).toHaveClass("scope-entry-included");

    await user.click(within(includedRow).getByRole("button", { name: "查看 photos/a.jpg 详情" }));
    expect(within(includedRow).getByText((_, element) => element?.textContent === "估算大小：2.0 KB")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "打开文件夹 2025" }));
    expect(await screen.findByText("b.jpg")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "返回上一层" }));
    expect(await screen.findByText("a.jpg")).toBeVisible();
    expect(screen.queryByText("b.jpg")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "返回上一层" }));
    expect(await screen.findByText("photos")).toBeVisible();
    expect(screen.queryByRole("button", { name: "返回上一层" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "已排除" }));
    expect(await screen.findByText("cache")).toBeVisible();
    expect(action.mock.calls.some(([path]) => String(path).includes("view=excluded"))).toBe(true);

    await user.click(screen.getByRole("tab", { name: "排除规则" }));
    expect(screen.getByText("**/*.tmp")).toBeVisible();
    expect(screen.getByText("1 个文件，128 B")).toBeVisible();
  });

  it("returns from the scope page to the current task editor", async () => {
    const user = userEvent.setup();
    const onBack = vi.fn();
    const action = vi.fn(async (path: string) => path.startsWith("/api/tasks/")
      ? { operationId: "op-back", status: "queued", kind: "task_scope_inventory" }
      : {
          id: "op-back", kind: "task_scope_inventory", status: "success", stage: "completed",
          detail: { previewId: "preview-back", summary: { entriesAvailable: false } },
        });
    render(<TaskScopePage taskId="task-a" taskName="照片备份" api={{ action } as unknown as AppAPI} locale="zh-CN" onBack={onBack} />);
    await user.click(await screen.findByRole("button", { name: "返回任务编辑" }));
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("previews draft exclusions in place and persists them only after explicit save", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    let inventoryCount = 0;
    const action = vi.fn(async (path: string, payload?: Record<string, unknown>) => {
      if (path === "/api/tasks/task-edit/scope-inventory") {
        inventoryCount += 1;
        if (inventoryCount === 1) {
          expect(payload).toEqual({});
          return { operationId: "op-edit", status: "queued", kind: "task_scope_inventory" };
        }
        expect(payload).toEqual({ exclusions: ["README.md", "**/node_modules"] });
        return { operationId: "op-draft", status: "queued", kind: "task_scope_inventory" };
      }
      if (path === "/api/operations/op-edit") return {
        id: "op-edit", kind: "task_scope_inventory", status: "success", stage: "completed",
        detail: {
          previewId: "preview-edit",
          summary: {
            entriesAvailable: true, totalFiles: 2, includedFiles: 1, excludedFiles: 1,
            activeRules: [{ rule: "cache", matchedFiles: 1, estimatedBytes: 10 }],
            suggestions: [{ rule: "**/node_modules", reason: "依赖缓存可重新安装", matchedFiles: 0, estimatedBytes: 0 }],
          },
        },
      };
      if (path === "/api/operations/op-draft") return {
        id: "op-draft", kind: "task_scope_inventory", status: "success", stage: "completed",
        detail: {
          previewId: "preview-draft",
          exclusions: ["README.md", "**/node_modules"],
          draft: true,
          summary: {
            entriesAvailable: true, totalFiles: 2, includedFiles: 0, excludedFiles: 2,
            activeRules: [
              { rule: "README.md", matchedFiles: 1, estimatedBytes: 20 },
              { rule: "**/node_modules", matchedFiles: 1, estimatedBytes: 50 },
            ],
          },
        },
      };
      if (path.startsWith("/api/task-scope-previews/preview-edit/entries")) return {
        items: [
          { ordinal: 1, path: "cache", type: "directory", disposition: "excluded", coverage: "excluded", totalFiles: 1, excludedFiles: 1, ruleIndexes: [0] },
          { ordinal: 3, path: "README.md", type: "file", disposition: "included", size: 20 },
        ],
        truncated: false,
      };
      if (path.startsWith("/api/task-scope-previews/preview-draft/entries")) return {
        items: [
          { ordinal: 3, path: "README.md", type: "file", disposition: "excluded", size: 20, ruleIndexes: [0] },
        ],
        truncated: false,
      };
      if (path === "/api/tasks/task-edit/scope-rules") {
        expect(payload).toEqual({
          previewId: "preview-draft",
          exclusions: ["README.md", "**/node_modules"],
          rsyncDeleteConfirmed: false,
        });
        return {
          id: "task-edit", engine: "restic", kind: "directory",
          directory: { exclusions: ["README.md", "**/node_modules"] },
        };
      }
      throw new Error(`unexpected ${path}`);
    });
    render(<TaskScopePage
      taskId="task-edit"
      taskName="资料"
      task={{ engine: "restic", directory: { exclusions: ["cache"] } }}
      api={{ action } as unknown as AppAPI}
      locale="zh-CN"
      onBack={() => undefined}
      onSaved={onSaved}
    />);

    await user.click(await screen.findByRole("button", { name: "排除此文件" }));
    expect(screen.getByText("1 项规则变更尚未预览")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "恢复保护" }));
    expect(screen.getByText("2 项规则变更尚未预览")).toBeVisible();
    await user.click(screen.getByRole("tab", { name: "排除规则" }));
    expect(screen.getByLabelText("排除规则（每行一条）")).toHaveValue("README.md");
    expect(screen.getByText("当前预览中的规则影响")).toBeVisible();
    await user.click(screen.getByRole("checkbox", { name: "采用建议 **/node_modules" }));
    expect(screen.getByLabelText("排除规则（每行一条）")).toHaveValue("README.md\n**/node_modules");
    expect(screen.getByText("3 项规则变更尚未预览")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "预览规则效果" }));
    expect(await screen.findByText("当前结果是临时预览，尚未保存到任务。")).toBeVisible();
    await user.click(screen.getByRole("tab", { name: "已排除" }));
    expect(await screen.findByText("排除未保存")).toBeVisible();
    expect(screen.queryByText("待排除")).not.toBeInTheDocument();
    const summary = screen.getByRole("region", { name: "范围统计" });
    expect(within(within(summary).getByText("将保护").closest(".scope-lens-stat")!).getByText("0")).toBeVisible();
    expect(onSaved).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "保存规则" }));
    expect(await screen.findByText("规则已保存，当前预览与任务配置一致。")).toBeVisible();
    expect(onSaved).toHaveBeenCalledTimes(1);
  });

  it("explains when an Agent can return only the summary", async () => {
    const action = vi.fn(async (path: string) => path.startsWith("/api/tasks/")
      ? { operationId: "op-agent", status: "queued", kind: "task_scope_inventory" }
      : {
          id: "op-agent", kind: "task_scope_inventory", status: "success", stage: "completed",
          detail: { previewId: "preview-agent", summary: { includedFiles: 10, entriesAvailable: false, entriesUnavailableReason: "agent_inventory_upgrade_required" } },
        });
    render(<TaskScopePage taskId="task-agent" taskName="远程照片" api={{ action } as unknown as AppAPI} locale="zh-CN" onBack={() => undefined} />);
    expect(await screen.findByText("当前 Agent 只能返回范围统计，升级 Agent 后才能查看逐个文件。")).toBeVisible();
    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/tasks/task-agent/scope-inventory", {}));
  });
});
