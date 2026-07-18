import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import {
  loadLocale,
  loadTimeZone,
  localeLabel,
  saveLocale,
  saveTimeZone,
  translate,
  type Locale,
} from "./i18n";

describe("interface locale", () => {
  it("translates known messages and safely falls back to the source text", () => {
    expect(translate("en-US", "仪表盘")).toBe("Dashboard");
    expect(translate("en-US", "unregistered text")).toBe("unregistered text");
    expect(translate("zh-CN", "仪表盘")).toBe("仪表盘");
    expect(localeLabel("en-US", "zh-CN")).toBe("Simplified Chinese");
  });

  it("persists only supported locales", () => {
    localStorage.clear();
    saveLocale("en-US");
    expect(loadLocale()).toBe("en-US");
    localStorage.setItem("shadoc.locale", "unsafe");
    expect(loadLocale()).toBe("zh-CN");
    localStorage.clear();
    localStorage.setItem("restic-control.locale", "en-US");
    expect(loadLocale()).toBe("en-US");
    expect(["zh-CN", "en-US"] satisfies Locale[]).toHaveLength(2);
  });

  it("persists only valid interface time zones", () => {
    localStorage.clear();
    saveTimeZone("Asia/Shanghai");
    expect(loadTimeZone()).toBe("Asia/Shanghai");

    localStorage.setItem("shadoc.timezone", "not/a-time-zone");
    const fallback = loadTimeZone();
    expect(fallback).not.toBe("not/a-time-zone");
    expect(() => new Intl.DateTimeFormat("zh-CN", { timeZone: fallback })).not.toThrow();
  });

  it("has an English translation for every literal passed to the translation helper", () => {
    const sources = [
      "./app/App.tsx",
      "./app/ResourceEditors.tsx",
      "./app/PlanEditor.tsx",
      "./app/OperationFeedback.tsx",
      "./app/AgentServiceSettings.tsx",
      "./app/HealthEvents.tsx",
      "./app/ControlPlaneRecovery.tsx",
      "./app/ProtectionPolicyEditor.tsx",
      "./app/SnapshotBrowser.tsx",
      "./app/RestoreVerificationPanel.tsx",
      "./app/TaskHealthTrends.tsx",
      "./app/TaskHealthDetailPage.tsx",
      "./app/RunHistoryPage.tsx",
      "./app/RepositoryCapacityPanel.tsx",
      "./app/AgentFleet.tsx",
      "./app/ProtectionWizard.tsx",
      "./app/NotificationChannels.tsx",
	  "./app/ApplicationVersionStatus.tsx",
    ].map((path) => readFileSync(new URL(path, import.meta.url), "utf8")).join("\n");
    const literals = [...sources.matchAll(/\bt\(\s*"([^"]+)"\s*\)/g)].map((match) => match[1]);
    const missing = [...new Set(literals.filter((source) => translate("en-US", source) === source))];
    expect(missing).toEqual([]);
  });
});
