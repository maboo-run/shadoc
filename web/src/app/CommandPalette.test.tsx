import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CommandPalette } from "./CommandPalette";
import type { AppAPI } from "./App";

const fakeAPI: AppAPI = {
  listResource: vi.fn(async (resource: string) => {
    if (resource === "tasks") return [{ id: "task-1", name: "web备份" }];
    if (resource === "repositories") return [{ id: "repo-1", name: "本地仓库" }];
    if (resource === "agents") return [{ id: "agent-1", status: "online" }];
    return [];
  }),
} as unknown as AppAPI;

describe("CommandPalette", () => {
  it("renders a dialog with search input focused on mount", () => {
    render(<CommandPalette api={fakeAPI} locale="zh-CN" onClose={() => undefined} onNavigate={() => undefined} />);
    const dialog = screen.getByRole("dialog", { name: "命令面板" });
    expect(dialog).toBeInTheDocument();
    const input = screen.getByRole("textbox", { name: "搜索页面、任务、仓库…" });
    expect(input).toHaveFocus();
  });

  it("shows page results by default and navigates on click", async () => {
    const onNavigate = vi.fn();
    render(<CommandPalette api={fakeAPI} locale="zh-CN" onClose={() => undefined} onNavigate={onNavigate} />);
    const input = screen.getByRole("textbox", { name: "搜索页面、任务、仓库…" });
    await userEvent.type(input, "仪表");
    const option = await screen.findByRole("option", { name: /仪表盘/ });
    await userEvent.click(option);
    expect(onNavigate).toHaveBeenCalledWith("仪表盘");
  });

  it("closes on Escape", async () => {
    const onClose = vi.fn();
    render(<CommandPalette api={fakeAPI} locale="zh-CN" onClose={onClose} onNavigate={() => undefined} />);
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("filters results by query across pages, tasks, repositories, and agents", async () => {
    render(<CommandPalette api={fakeAPI} locale="zh-CN" onClose={() => undefined} onNavigate={() => undefined} />);
    const input = screen.getByRole("textbox", { name: "搜索页面、任务、仓库…" });
    await userEvent.type(input, "web");
    const option = await screen.findByRole("option", { name: /web备份/ });
    expect(option).toBeInTheDocument();
  });

  it("shows empty state when no matches", async () => {
    render(<CommandPalette api={fakeAPI} locale="zh-CN" onClose={() => undefined} onNavigate={() => undefined} />);
    const input = screen.getByRole("textbox", { name: "搜索页面、任务、仓库…" });
    await userEvent.type(input, "zzznomatch");
    expect(await screen.findByText("无匹配结果")).toBeInTheDocument();
  });

  it("supports arrow key navigation through results", async () => {
    const onNavigate = vi.fn();
    render(<CommandPalette api={fakeAPI} locale="zh-CN" onClose={() => undefined} onNavigate={onNavigate} />);
    const input = screen.getByRole("textbox", { name: "搜索页面、任务、仓库…" });
    await userEvent.type(input, "备");
    const options = await screen.findAllByRole("option");
    expect(options.length).toBeGreaterThan(0);
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.keyboard("{Enter}");
    expect(onNavigate).toHaveBeenCalled();
  });
});
