import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RepositoryEditor, TaskEditor } from "./ResourceEditors";

describe("RepositoryEditor connection mode", () => {
  it("submits an existing repository for read-only connection without a maintenance policy", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn(async (_payload: Record<string, unknown>) => undefined);
    const api = {
      listResource: async () => [],
      createResource: async () => ({}),
      updateResource: async () => undefined,
      action: async () => ({}),
      saveMaintenance: async () => undefined,
    };
    render(<RepositoryEditor api={api} initial={null} onClose={() => undefined} onSubmit={onSubmit} />);

    await user.selectOptions(screen.getByLabelText("仓库接入方式"), "existing");
    expect(screen.getByText(/只会执行固定的只读快照验证/)).toBeVisible();
    expect(screen.queryByRole("heading", { name: "定时维护" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("密码来源")).toHaveValue("custom");

    await user.type(screen.getByLabelText("名称"), "既有照片仓库");
    await user.type(screen.getByLabelText("本机绝对路径"), "/backup/existing");
    await user.type(screen.getByLabelText("仓库密码"), "old-key");
    await user.type(screen.getByLabelText("再次输入仓库密码"), "old-key");
    await user.click(screen.getByLabelText("我已将仓库密码安全保存到应用之外"));
    await user.click(screen.getByRole("button", { name: "验证并连接" }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce());
    expect(onSubmit).toHaveBeenCalledWith({
      connectionMode: "existing",
      name: "既有照片仓库",
      engine: "restic",
      kind: "local",
      remoteHostId: "",
      path: "/backup/existing",
      password: "old-key",
      passwordConfirmed: true,
    });
  });

  it("submits an S3 repository as fixed structured fields without an arbitrary backend URL", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn(async (_payload: Record<string, unknown>) => undefined);
    const api = {
      listResource: async () => [], createResource: async () => ({}), updateResource: async () => undefined,
      action: async () => ({}), saveMaintenance: async () => undefined,
    };
    render(<RepositoryEditor api={api} initial={null} onClose={() => undefined} onSubmit={onSubmit} />);

    await user.selectOptions(screen.getByLabelText("仓库接入方式"), "existing");
    await user.selectOptions(screen.getByLabelText("仓库类型"), "s3");
    expect(screen.getByText("仅支持结构化 S3 配置")).toBeVisible();
    expect(screen.queryByLabelText(/自定义.*URL|任意.*参数|环境变量/)).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("名称"), "对象归档");
    await user.type(screen.getByLabelText("S3 端点"), "https://objects.example.com");
    await user.type(screen.getByLabelText("存储桶"), "backup-prod");
    await user.type(screen.getByLabelText("区域"), "us-east-1");
    await user.type(screen.getByLabelText("对象前缀（可选）"), "photos/main");
    await user.click(screen.getByLabelText("使用 Path-style 存储桶寻址"));
    await user.type(screen.getByLabelText("S3 Access Key"), "access-private");
    await user.type(screen.getByLabelText("S3 Secret Key"), "secret-private");
    await user.click(screen.getByLabelText("确认 S3 凭据用途"));
    await user.type(screen.getByLabelText("仓库密码"), "old-password");
    await user.type(screen.getByLabelText("再次输入仓库密码"), "old-password");
    await user.click(screen.getByLabelText("我已将仓库密码安全保存到应用之外"));
    await user.click(screen.getByRole("button", { name: "验证并连接" }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce());
    expect(onSubmit).toHaveBeenCalledWith({
      connectionMode: "existing", name: "对象归档", engine: "restic", kind: "s3", remoteHostId: "", path: "",
      password: "old-password", passwordConfirmed: true,
      s3: { endpoint: "https://objects.example.com", bucket: "backup-prod", region: "us-east-1", prefix: "photos/main", pathStyle: true, accessKey: "access-private", secretKey: "secret-private", credentialsConfirmed: true },
    });
  });
});

describe("TaskEditor health policy", () => {
  it("round-trips the configured maximum age without changing task scope", async () => {
    const user = userEvent.setup();
    const updateResource = vi.fn(async (_resource: string, _id: string, _payload: Record<string, unknown>) => undefined);
    const initial = {
      id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: false,
      executionTarget: { kind: "local" }, directory: { path: "/srv/photos", exclusions: [], skipIfUnchanged: true },
      retention: {}, resources: { compression: "auto" }, health: { maxSuccessAgeHours: 72 },
    };
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
      createResource: async () => ({}),
      updateResource,
      action: async () => ({}),
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={initial} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    const input = await screen.findByLabelText("最长无完整成功（小时）");
    expect(input).toHaveValue(72);
    await user.clear(input);
    await user.type(input, "96");
    await user.click(screen.getByRole("button", { name: "保存任务" }));
    await waitFor(() => expect(updateResource).toHaveBeenCalledOnce());
    expect(updateResource.mock.calls[0][2]).toMatchObject({ health: { maxSuccessAgeHours: 96 } });
  });

  it("reports a task save API failure with a toast instead of a page banner", async () => {
    const user = userEvent.setup();
    const updateResource = vi.fn(async () => {
      throw new Error("rsync 目标主机不存在或未固定 SSH 主机密钥");
    });
    const initial = {
      id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: false,
      executionTarget: { kind: "local" }, directory: { path: "/srv/photos", exclusions: [], skipIfUnchanged: true },
      retention: {}, resources: { compression: "auto" }, health: { maxSuccessAgeHours: 72 },
    };
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
      createResource: async () => ({}),
      updateResource,
      action: async () => ({}),
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={initial} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    await user.click(await screen.findByRole("button", { name: "保存任务" }));

    const failure = await screen.findByText("rsync 目标主机不存在或未固定 SSH 主机密钥");
    expect(failure.closest(".toast")).toBeVisible();
    expect(failure.closest(".toast")?.parentElement).toBe(document.body);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("TaskEditor activation guidance", () => {
  it("generates a scope preview and asks for confirmation when saving an unpreviewed task as enabled", async () => {
    const user = userEvent.setup();
    const updateResource = vi.fn(async () => undefined);
    const onSaved = vi.fn(async () => undefined);
    const action = vi.fn(async (path: string) => {
      if (path === "/api/tasks/task-a/preview") {
        return {
          previewId: "preview-a",
          fingerprint: "fingerprint-a",
          requiresDeleteConfirmation: false,
          summary: { scannedItems: 10, includedFiles: 8, includedBytes: 4096, excludedFiles: 2, excludedBytes: 1024, unreadableItems: 0 },
        };
      }
      return {};
    });
    const initial = {
      id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: false,
      executionTarget: { kind: "local" }, directory: { path: "/srv/photos", exclusions: [], skipIfUnchanged: true },
      retention: {}, resources: { compression: "auto" }, health: { maxSuccessAgeHours: 72 },
    };
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
      createResource: async () => ({}),
      updateResource,
      action,
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={initial} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={onSaved} />);

    await user.selectOptions(await screen.findByLabelText("任务状态"), "true");
    await user.click(screen.getByRole("button", { name: "保存任务" }));

    await waitFor(() => expect(action).toHaveBeenCalledWith("/api/tasks/task-a/preview", {}));
    const dialog = await screen.findByRole("dialog", { name: "确认任务范围" });
    expect(within(dialog).getByText("8 个文件将纳入保护，2 个文件被排除，0 项无法读取。")).toBeVisible();
    await user.click(within(dialog).getByRole("button", { name: "确认范围并启用任务" }));

    await waitFor(() => expect(updateResource).toHaveBeenCalledWith("tasks", "task-a", expect.objectContaining({
      enabled: true,
      previewId: "preview-a",
    })));
    expect(onSaved).toHaveBeenCalledOnce();
  });
});

describe("TaskEditor protection policy", () => {
  it("keeps repository retention and transfer speed controls out of task editing", async () => {
    const user = userEvent.setup();
    const updateResource = vi.fn(async () => undefined);
    const retention = { keepWithinDays: 45, keepLast: 4, keepHourly: 24, keepDaily: 8, keepWeekly: 6, keepMonthly: 18, keepYearly: 5 };
    const resources = { uploadKiBPerSecond: 128, downloadKiBPerSecond: 256, readConcurrency: 3, compression: "max" };
    const initial = {
      id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: false,
      executionTarget: { kind: "local" }, directory: { path: "/srv/photos", exclusions: [], skipIfUnchanged: true },
      retention, resources, health: { maxSuccessAgeHours: 72 },
    };
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
      createResource: async () => ({}),
      updateResource,
      action: async (path: string) => path.endsWith("/maintenance-policy") ? { retention } : {},
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={initial} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    const name = await screen.findByLabelText("任务名称");
    await user.clear(name);
    await user.type(name, "照片归档");
    expect(screen.queryByRole("button", { name: "保留策略" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("上传限速（KiB/s）")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("下载限速（KiB/s）")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存任务" }));

    await waitFor(() => expect(updateResource).toHaveBeenCalledOnce());
    expect(updateResource).toHaveBeenCalledWith("tasks", "task-a", expect.objectContaining({
      name: "照片归档",
      retention: {},
      resources: { ...resources, uploadKiBPerSecond: 0, downloadKiBPerSecond: 0 },
    }));
  });
});

describe("TaskEditor page navigation", () => {
  it("uses in-page section navigation instead of a modal", async () => {
    const user = userEvent.setup();
    const original = HTMLElement.prototype.scrollIntoView;
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
      createResource: async () => ({}),
      updateResource: async () => undefined,
      action: async () => ({}),
      saveMaintenance: async () => undefined,
    };
    try {
      render(<TaskEditor api={api} initial={null} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

      expect(await screen.findByRole("heading", { name: "新建备份任务" })).toBeVisible();
      expect(screen.queryByRole("dialog", { name: "新建备份任务" })).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "定时执行" }));
      expect(scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
      expect(screen.getByRole("button", { name: "定时执行" })).toHaveAttribute("aria-current", "location");
    } finally {
      if (original) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: original });
      else delete (HTMLElement.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    }
  });
});

describe("TaskEditor schedule", () => {
  it("keeps scheduling off by default and creates a task-bound plan when enabled", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => ({ id: "plan-a" }));
    const updateResource = vi.fn(async () => undefined);
    const initial = {
      id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: false,
      executionTarget: { kind: "local" }, directory: { path: "/srv/photos", exclusions: [], skipIfUnchanged: true },
      resources: { compression: "auto" }, health: { maxSuccessAgeHours: 72 },
    };
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : [],
      createResource,
      updateResource,
      action: async () => ({ retention: { keepWithinDays: 30, keepLast: 3 } }),
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={initial} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    await user.click(await screen.findByRole("button", { name: "定时执行" }));
    const enabled = screen.getByLabelText("启用定时执行");
    expect(enabled).not.toBeChecked();
    await user.click(enabled);
    await user.click(screen.getByRole("button", { name: "保存任务" }));

    await waitFor(() => expect(createResource).toHaveBeenCalledWith("plans", expect.objectContaining({
      enabled: true,
      taskIds: ["task-a"],
      schedule: { kind: "daily", timeOfDay: "02:30" },
    })));
  });

  it("detaches an edited task from a legacy multi-task plan", async () => {
    const user = userEvent.setup();
    const createResource = vi.fn(async () => ({ id: "plan-task-a" }));
    const updateResource = vi.fn(async () => undefined);
    const initial = {
      id: "task-a", name: "照片", engine: "restic", kind: "directory", repositoryId: "repo-a", enabled: false,
      executionTarget: { kind: "local" }, directory: { path: "/srv/photos", exclusions: [], skipIfUnchanged: true },
      resources: { compression: "auto" }, health: { maxSuccessAgeHours: 72 },
    };
    const sharedPlan = {
      id: "plan-shared", name: "夜间备份", schedule: { kind: "daily", timeOfDay: "01:15" }, timezone: "Asia/Shanghai",
      maxParallel: 2, catchUpWindowMinutes: 90, taskIds: ["task-a", "task-b"], enabled: true,
    };
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : resource === "plans" ? [sharedPlan] : [],
      createResource,
      updateResource,
      action: async () => ({ retention: { keepWithinDays: 30, keepLast: 3 } }),
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={initial} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    await user.click(await screen.findByRole("button", { name: "定时执行" }));
    expect(screen.getByLabelText("启用定时执行")).toBeChecked();
    await user.click(screen.getByRole("button", { name: "保存任务" }));

    await waitFor(() => expect(createResource).toHaveBeenCalledWith("plans", expect.objectContaining({
      taskIds: ["task-a"], enabled: true, maxParallel: 1,
    })));
    expect(updateResource).toHaveBeenCalledWith("plans", "plan-shared", expect.objectContaining({
      taskIds: ["task-b"], enabled: true, maxParallel: 2,
    }));
  });
});

describe("TaskEditor Agent readiness", () => {
  it("labels ineligible Agents, keeps draft binding available, and blocks enabling", async () => {
    const user = userEvent.setup();
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : resource === "agents"
          ? [
              { id: "offline-a", status: "offline", taskEligible: false, compatibilityStatus: "offline" },
              { id: "ready-a", status: "online", taskEligible: true, compatibilityStatus: "compatible", capabilities: ["restic"] },
            ]
          : [],
      createResource: async () => ({}),
      updateResource: async () => undefined,
      action: async () => ({}),
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={null} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    await user.selectOptions(screen.getByLabelText("执行位置"), "agent");
    const agentSelect = await screen.findByLabelText("源端 Agent");
    const options = Array.from((agentSelect as HTMLSelectElement).options);
    expect(options.find((option) => option.textContent === "offline-a · 心跳已超时")).toBeEnabled();
    expect(options.find((option) => option.textContent === "ready-a · 可用于任务")).toBeEnabled();
    await user.selectOptions(agentSelect, "offline-a");
    expect(screen.getByText(/仍可绑定并保存为停用草稿/)).toBeVisible();
    expect(screen.getByRole("option", { name: "启用" })).toBeDisabled();
    expect(screen.getByLabelText("任务状态")).toHaveValue("false");
  });

  it("keeps a compatible Agent disabled when it lacks the selected engine", async () => {
    const user = userEvent.setup();
    const api = {
      listResource: async (resource: string) => resource === "repositories"
        ? [{ id: "repo-a", name: "照片仓库", engine: "restic", kind: "local", path: "/backup/photos", status: "ready" }]
        : resource === "agents"
          ? [{ id: "rsync-only", status: "online", taskEligible: true, compatibilityStatus: "compatible", capabilities: ["rsync"] }]
          : [],
      createResource: async () => ({}),
      updateResource: async () => undefined,
      action: async () => ({}),
      saveMaintenance: async () => undefined,
    };
    render(<TaskEditor api={api} initial={null} onClose={() => undefined} onDraftSaved={async () => undefined} onSaved={async () => undefined} />);

    await user.selectOptions(screen.getByLabelText("执行位置"), "agent");
    const agentSelect = await screen.findByLabelText("源端 Agent");
    expect(screen.getByRole("option", { name: "rsync-only · 缺少 Restic 能力" })).toBeEnabled();
    await user.selectOptions(agentSelect, "rsync-only");
    expect(screen.getByRole("option", { name: "启用" })).toBeDisabled();
    expect(screen.getByText(/仍可绑定并保存为停用草稿/)).toBeVisible();
  });
});
