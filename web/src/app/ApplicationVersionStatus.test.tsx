import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { AppAPI } from "./App";
import { ApplicationVersionStatus } from "./ApplicationVersionStatus";

describe("application version status", () => {
  it("shows the current version and a concise update notice", async () => {
    const api = {
      applicationVersion: vi.fn(async () => ({ version: "v1.2.0" })),
      applicationReleases: vi.fn(async () => ({
        currentVersion: "v1.2.0",
        latest: { version: "v1.3.0", publishedAt: "2026-07-15T08:00:00Z", summary: "", compatible: true, platform: "darwin_arm64" },
        updateAvailable: true,
        managed: false,
      })),
    } as unknown as AppAPI;

    render(<ApplicationVersionStatus api={api} locale="zh-CN" />);

    expect(await screen.findByText("v1.2.0")).toBeVisible();
    expect(screen.getByText("当前有新版本")).toBeVisible();
  });

  it("keeps the current version without an error block when GitHub is unavailable", async () => {
    const api = {
      applicationVersion: vi.fn(async () => ({ version: "v1.2.0" })),
      applicationReleases: vi.fn(async () => { throw new Error("offline"); }),
    } as unknown as AppAPI;

    render(<ApplicationVersionStatus api={api} locale="zh-CN" />);

    expect(await screen.findByText("v1.2.0")).toBeVisible();
    expect(screen.queryByText("当前有新版本")).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
