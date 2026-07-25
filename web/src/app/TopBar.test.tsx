import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TopBar } from "./TopBar";

describe("TopBar", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  afterEach(() => vi.restoreAllMocks());

  it("renders breadcrumb with the active page name", () => {
    render(<TopBar locale="zh-CN" activePage="仪表盘" onOpenCommandPalette={() => undefined} />);
    expect(screen.getByText("仪表盘")).toBeInTheDocument();
  });

  it("renders a command palette trigger showing the shortcut hint", () => {
    render(<TopBar locale="zh-CN" activePage="仪表盘" onOpenCommandPalette={() => undefined} />);
    const trigger = screen.getByRole("button", { name: /搜索页面、任务、仓库/ });
    expect(trigger).toHaveTextContent("⌘K");
  });

  it("invokes onOpenCommandPalette when the trigger is clicked", async () => {
    const onOpen = vi.fn();
    render(<TopBar locale="zh-CN" activePage="仪表盘" onOpenCommandPalette={onOpen} />);
    await userEvent.click(screen.getByRole("button", { name: /搜索页面、任务、仓库/ }));
    expect(onOpen).toHaveBeenCalledOnce();
  });

  it("toggles theme between dark and light and persists the choice", async () => {
    render(<TopBar locale="zh-CN" activePage="仪表盘" onOpenCommandPalette={() => undefined} />);
    const toggle = screen.getByRole("button", { name: "切换主题" });
    expect(document.documentElement.dataset.theme).toBe("dark");
    await userEvent.click(toggle);
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(localStorage.getItem("shadoc.theme")).toBe("light");
    await userEvent.click(toggle);
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
