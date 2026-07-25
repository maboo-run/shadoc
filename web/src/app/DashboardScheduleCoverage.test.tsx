import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DashboardScheduleCoverage } from "./DashboardScheduleCoverage";
import type { Dashboard } from "./App";

describe("DashboardScheduleCoverage", () => {
  it("renders each plan as a gauge row with label and percent", () => {
    const dashboard: Dashboard = {
      tasks: [],
      alerts: [],
      scheduleCoverage: [
        { planId: "p1", planName: "每日全量", total: 100, success: 96, partial: 0, missed: 4, failed: 0, cancelled: 0, skipped: 0, interrupted: 0, coveragePercent: 96 },
        { planId: "p2", planName: "每周归档", total: 50, success: 36, partial: 0, missed: 14, failed: 0, cancelled: 0, skipped: 0, interrupted: 0, coveragePercent: 72 },
      ],
    };
    render(<DashboardScheduleCoverage dashboard={dashboard} locale="zh-CN" />);
    expect(screen.getByText("每日全量")).toBeInTheDocument();
    expect(screen.getByText("96%")).toBeInTheDocument();
    expect(screen.getByText("每周归档")).toBeInTheDocument();
    expect(screen.getByText("72%")).toBeInTheDocument();
  });

  it("marks coverage below 80 percent with warning tone", () => {
    const dashboard: Dashboard = {
      tasks: [],
      alerts: [],
      scheduleCoverage: [
        { planId: "p2", planName: "每周归档", total: 50, success: 36, partial: 0, missed: 14, failed: 0, cancelled: 0, skipped: 0, interrupted: 0, coveragePercent: 72 },
      ],
    };
    render(<DashboardScheduleCoverage dashboard={dashboard} locale="zh-CN" />);
    const gauge = screen.getByLabelText("每周归档");
    expect(gauge).toHaveClass("warn");
  });

  it("shows empty state when no schedule data", () => {
    render(<DashboardScheduleCoverage dashboard={{ tasks: [], alerts: [] }} locale="zh-CN" />);
    expect(screen.getByText("尚无调度覆盖数据")).toBeInTheDocument();
  });
});
