import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Sidebar } from "./Sidebar";
import type { AppAPI } from "./App";

const fakeAPI: Pick<AppAPI, "applicationVersion" | "applicationReleases"> = {
  applicationVersion: vi.fn(async () => ({ version: "2.0.0" })),
  applicationReleases: vi.fn(async () => ({ currentState: "current" })),
} as unknown as Pick<AppAPI, "applicationVersion" | "applicationReleases">;

describe("Sidebar", () => {
  it("renders grouped navigation with section labels", () => {
    render(<Sidebar api={fakeAPI as AppAPI} locale="zh-CN" username="admin" activePage="仪表盘" mobileNavigationOpen={false} onNavigate={() => undefined} onCloseMobile={() => undefined} onLogout={() => undefined} />);

    expect(screen.getByRole("navigation", { name: "主导航" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /仪表盘/ })).toHaveClass("nav-item");
    expect(screen.getByText("备份仓库")).toBeInTheDocument();
    expect(screen.getByText("连接管理")).toBeInTheDocument();
  });

  it("marks the active page as selected", () => {
    render(<Sidebar api={fakeAPI as AppAPI} locale="zh-CN" username="admin" activePage="备份任务" mobileNavigationOpen={false} onNavigate={() => undefined} onCloseMobile={() => undefined} onLogout={() => undefined} />);

    const active = screen.getByRole("button", { name: /备份任务/ });
    expect(active).toHaveClass("selected");
  });

  it("invokes onNavigate with the first child page of a group", async () => {
    const onNavigate = vi.fn();
    render(<Sidebar api={fakeAPI as AppAPI} locale="zh-CN" username="admin" activePage="仪表盘" mobileNavigationOpen={false} onNavigate={onNavigate} onCloseMobile={() => undefined} onLogout={() => undefined} />);

    await userEvent.click(screen.getByRole("button", { name: /连接管理/ }));
    expect(onNavigate).toHaveBeenCalledWith("远程主机");
  });

  it("shows username and logout button", () => {
    render(<Sidebar api={fakeAPI as AppAPI} locale="zh-CN" username="alice" activePage="仪表盘" mobileNavigationOpen={false} onNavigate={() => undefined} onCloseMobile={() => undefined} onLogout={() => undefined} />);

    expect(screen.getByText("alice")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "退出登录" })).toBeInTheDocument();
  });
});
