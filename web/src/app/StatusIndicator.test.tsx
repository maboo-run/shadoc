import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusIndicator } from "./StatusIndicator";

describe("StatusIndicator", () => {
  it.each([
    ["running", "运行中", "status-active"],
    ["stopped", "已停止", "status-stopped"],
    ["queued", "等待执行", "status-pending"],
    ["partial", "部分成功", "status-warning"],
    ["failed", "失败", "status-danger"],
  ])("presents %s with a stable user-facing label and semantic tone", (value, label, tone) => {
    render(<StatusIndicator value={value} locale="zh-CN" />);

    const status = screen.getByRole("status", { name: `状态：${label}` });
    expect(status).toHaveTextContent(label);
    expect(status).toHaveClass(tone);
  });

  it("does not expose unknown internal status values", () => {
    render(<StatusIndicator value="internal_retry_window" locale="zh-CN" />);

    expect(screen.getByRole("status", { name: "状态：状态未知" })).toHaveTextContent("状态未知");
    expect(screen.queryByText(/internal_retry_window/)).not.toBeInTheDocument();
  });

  it("supports readable resource lifecycle states", () => {
    const { rerender } = render(<StatusIndicator value="ready" locale="zh-CN" />);
    expect(screen.getByRole("status", { name: "状态：已验证" })).toHaveClass("status-success");

    rerender(<StatusIndicator value="draft" locale="zh-CN" />);
    expect(screen.getByRole("status", { name: "状态：草稿（不可启用）" })).toHaveClass("status-stopped");
  });
});
