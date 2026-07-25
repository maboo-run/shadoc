import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DashboardTrendChart } from "./DashboardTrendChart";
import type { Dashboard } from "./App";

describe("DashboardTrendChart", () => {
  it("shows empty state when no schedule data", () => {
    render(<DashboardTrendChart dashboard={{ tasks: [], alerts: [] }} locale="zh-CN" />);
    expect(screen.getByRole("heading", { name: /运行趋势/ })).toBeInTheDocument();
    expect(screen.getByText("暂无趋势数据")).toBeInTheDocument();
    const chart = screen.getByRole("img", { name: /运行趋势/ });
    expect(chart).toHaveClass("trend-chart-empty");
  });

  it("shows coverage percent bars from scheduleCoverage", () => {
    const dashboard: Dashboard = {
      tasks: [],
      alerts: [],
      scheduleCoverage: [
        { planId: "p1", planName: "每日全量", total: 100, success: 96, partial: 0, missed: 4, failed: 0, cancelled: 0, skipped: 0, interrupted: 0, coveragePercent: 96 },
        { planId: "p2", planName: "每小时增量", total: 200, success: 176, partial: 0, missed: 24, failed: 0, cancelled: 0, skipped: 0, interrupted: 0, coveragePercent: 88 },
      ],
    };
    render(<DashboardTrendChart dashboard={dashboard} locale="zh-CN" />);
    const chart = screen.getByRole("img", { name: /运行趋势/ });
    expect(chart).toHaveClass("trend-chart");
    const bars = chart.querySelectorAll(".trend-bar");
    expect(bars.length).toBe(14);
  });
});
