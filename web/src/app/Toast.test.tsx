import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Toast } from "./Toast";

describe("toast notification", () => {
  afterEach(() => vi.useRealTimers());

  it("shows a visible countdown and closes automatically when it expires", () => {
    vi.useFakeTimers();
    const onClose = vi.fn();
    render(<Toast message="保存完成" onClose={onClose} />);

    const toast = screen.getByRole("status");
    expect(toast.parentElement).toBe(document.body);
    expect(toast.querySelector(".toast-progress")).toHaveStyle({ animationDuration: "4500ms" });

    act(() => vi.advanceTimersByTime(4499));
    expect(onClose).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("supports closing the notification manually", async () => {
    const onClose = vi.fn();
    render(<Toast message="保存完成" onClose={onClose} />);

    await userEvent.click(screen.getByRole("button", { name: "关闭通知" }));
    expect(onClose).toHaveBeenCalledOnce();
  });
});
