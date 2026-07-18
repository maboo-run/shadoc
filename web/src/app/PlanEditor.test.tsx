import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { PlanEditor, type PlanValue } from "./PlanEditor";

const tasks = [
  { id: "task-a", name: "照片", enabled: true },
  { id: "task-b", name: "数据库", enabled: true },
  { id: "task-disabled", name: "旧任务", enabled: false },
];

describe("structured plan editor", () => {
  it.each([
    ["daily", { kind: "daily", timeOfDay: "02:30" }],
    ["weekly", { kind: "weekly", dayOfWeek: 1, timeOfDay: "03:15" }],
    ["interval", { kind: "interval", intervalHours: 6 }],
  ] as const)("serializes and refills %s schedules", async (_kind, schedule) => {
    const user = userEvent.setup();
    const onSubmit = vi.fn(async (_value: PlanValue) => undefined);
    render(
      <PlanEditor
        api={{ listResource: async () => tasks }}
        initial={{
          id: "plan-a",
          name: "计划 A",
          timezone: "Asia/Shanghai",
          maxParallel: 1,
          enabled: true,
          catchUpWindowMinutes: 45,
          taskIds: ["task-a", "task-b"],
          schedule,
        }}
        onClose={() => undefined}
        onSubmit={onSubmit}
      />,
    );

    expect(await screen.findByLabelText("计划名称")).toHaveValue("计划 A");
    expect(screen.getByLabelText("计划类型")).toHaveValue(schedule.kind);
    expect(screen.getByLabelText("离线补跑宽限（分钟）")).toHaveValue(45);
    if (schedule.kind === "daily")
      expect(screen.getByLabelText("执行时间")).toHaveValue("02:30");
    if (schedule.kind === "weekly") {
      expect(screen.getByLabelText("星期")).toHaveValue("1");
      expect(screen.getByLabelText("执行时间")).toHaveValue("03:15");
    }
    if (schedule.kind === "interval")
      expect(screen.getByLabelText("间隔小时数")).toHaveValue(6);
    expect(screen.getByLabelText("照片")).toBeChecked();
    expect(screen.getByLabelText("数据库")).toBeChecked();

    await user.click(screen.getByRole("button", { name: "保存计划" }));
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "计划 A",
        schedule,
        timezone: "Asia/Shanghai",
        maxParallel: 1,
        catchUpWindowMinutes: 45,
        taskIds: ["task-a", "task-b"],
        enabled: true,
      }),
    );
  });

  it("shows disabled tasks but prevents selecting them in an enabled plan", async () => {
    render(
      <PlanEditor
        api={{ listResource: async () => tasks }}
        initial={null}
        onClose={() => undefined}
        onSubmit={async () => undefined}
      />,
    );
    expect(await screen.findByText("旧任务（已停用）")).toBeVisible();
    expect(screen.getByLabelText("旧任务（已停用）")).toBeDisabled();
    expect(screen.getByLabelText("离线补跑宽限（分钟）")).toHaveValue(60);
  });

	it("accepts zero to disable offline catch-up", async () => {
		const user = userEvent.setup();
		const onSubmit = vi.fn(async (_value: PlanValue) => undefined);
		render(
			<PlanEditor
				api={{ listResource: async () => tasks }}
				initial={{ name: "不补跑", schedule: { kind: "interval", intervalHours: 6 }, timezone: "UTC", maxParallel: 1, taskIds: ["task-a"], enabled: true, catchUpWindowMinutes: 60 }}
				onClose={() => undefined}
				onSubmit={onSubmit}
			/>,
		);
		const catchUp = await screen.findByLabelText("离线补跑宽限（分钟）");
		await user.clear(catchUp);
		await user.type(catchUp, "0");
		await user.click(screen.getByRole("button", { name: "保存计划" }));
		expect(onSubmit).toHaveBeenCalledWith(expect.objectContaining({ catchUpWindowMinutes: 0 }));
	});
});
