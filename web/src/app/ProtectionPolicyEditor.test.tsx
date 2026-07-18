import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import {
  ProtectionPolicyEditor,
  type ResourcePolicy,
  type RetentionPolicy,
} from "./ProtectionPolicyEditor";

function Harness() {
  const [retention, setRetention] = useState<RetentionPolicy>({
    keepWithinDays: 30,
    keepLast: 3,
    keepHourly: 24,
    keepDaily: 7,
    keepWeekly: 5,
    keepMonthly: 12,
    keepYearly: 3,
  });
  const [resources, setResources] = useState<ResourcePolicy>({
    uploadKiBPerSecond: 128,
    downloadKiBPerSecond: 256,
    readConcurrency: 4,
    compression: "max",
  });
  return <ProtectionPolicyEditor
    retention={retention}
    onRetentionChange={setRetention}
    resources={resources}
    onResourcesChange={setResources}
  />;
}

describe("ProtectionPolicyEditor", () => {
  it("preserves every advanced value while switching modes", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    expect(screen.getByRole("radio", { name: "高级策略" })).toBeChecked();
    expect(screen.getByLabelText("每小时保留数")).toHaveValue(24);
    await user.clear(screen.getByLabelText("每日保留数"));
    await user.type(screen.getByLabelText("每日保留数"), "8");
    await user.click(screen.getByRole("radio", { name: "简单策略" }));
    expect(screen.queryByLabelText("每日保留数")).not.toBeInTheDocument();
    expect(screen.getByLabelText("保留窗口（天）")).toHaveValue(30);
    await user.click(screen.getByRole("radio", { name: "高级策略" }));
    expect(screen.getByLabelText("每日保留数")).toHaveValue(8);
    expect(screen.getByLabelText("每月保留数")).toHaveValue(12);
  });

  it("keeps execution controls while omitting transfer speed limits", async () => {
    render(<Harness />);

    expect(screen.queryByLabelText("上传限速（KiB/s）")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("下载限速（KiB/s）")).not.toBeInTheDocument();
    expect(screen.getByLabelText("读取并发数")).toHaveValue(4);
    expect(screen.getByLabelText("压缩模式")).toHaveValue("max");
    expect(screen.getByText("读取并发填 0 表示不额外限制。")).toBeVisible();
  });
});
