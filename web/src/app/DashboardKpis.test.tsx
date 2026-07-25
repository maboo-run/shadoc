import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { DashboardKpis } from "./DashboardKpis";
import type { Dashboard } from "./App";

const baseDashboard: Dashboard = { tasks: [], alerts: [] };

describe("DashboardKpis", () => {
  it("shows success rate from runOverview", () => {
    render(<DashboardKpis dashboard={{ ...baseDashboard, runOverview: { total: 100, succeeded: 98, failed: 1, partial: 1, successRate: 98.4 } }} agentOnline={4} agentTotal={4} locale="zh-CN" />);
    expect(screen.getByText("98.4%")).toBeInTheDocument();
  });

  it("explains which terminal run states contribute to the success rate", () => {
    render(<DashboardKpis dashboard={baseDashboard} agentOnline={4} agentTotal={4} locale="zh-CN" />);
    expect(screen.getByText(/成功率分母只包含完整成功、部分成功和失败/)).toBeVisible();
  });

  it("shows dash when runOverview is missing", () => {
    render(<DashboardKpis dashboard={baseDashboard} agentOnline={4} agentTotal={4} locale="zh-CN" />);
    expect(screen.getByText("-")).toBeInTheDocument();
  });

  it("shows alert count with warning tone when alerts exist", () => {
    render(<DashboardKpis dashboard={{ ...baseDashboard, alerts: [{ message: "x" }, { message: "y" }] }} agentOnline={4} agentTotal={4} locale="zh-CN" />);
    const alertCard = screen.getByLabelText("当前告警");
    expect(alertCard).toHaveTextContent("2");
    expect(alertCard).toHaveClass("warn");
  });

  it("shows repository status as healthy by default", () => {
    render(<DashboardKpis dashboard={baseDashboard} agentOnline={4} agentTotal={4} locale="zh-CN" />);
    const repoCard = screen.getByLabelText("仓库状态");
    expect(repoCard).toHaveTextContent("正常");
    expect(repoCard).not.toHaveClass("warn");
  });

  it("shows repository status as abnormal with warning tone", () => {
    render(<DashboardKpis dashboard={{ ...baseDashboard, repositoryStatus: "abnormal" }} agentOnline={4} agentTotal={4} locale="zh-CN" />);
    const repoCard = screen.getByLabelText("仓库状态");
    expect(repoCard).toHaveTextContent("异常");
    expect(repoCard).toHaveClass("warn");
  });

  it("shows agent online ratio", () => {
    render(<DashboardKpis dashboard={baseDashboard} agentOnline={3} agentTotal={4} locale="zh-CN" />);
    expect(screen.getByLabelText("AGENT 在线")).toHaveTextContent("3/4");
  });
});
