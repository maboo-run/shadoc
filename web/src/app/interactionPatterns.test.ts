import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const sourceFiles = [
  "./App.tsx",
  "./AgentServiceSettings.tsx",
  "./ResourceEditors.tsx",
  "./PlanEditor.tsx",
  "./OperationFeedback.tsx",
];

const sources = sourceFiles
  .map((path) => readFileSync(new URL(path, import.meta.url), "utf8"))
  .join("\n");

describe("administration interaction patterns", () => {
  it("does not use browser-native confirmation or prompt APIs", () => {
    expect(sources).not.toMatch(/window\.(?:alert|confirm|prompt)\s*\(/);
  });

  it("does not render legacy in-flow dialogs or transient success messages", () => {
    expect(sources).not.toContain("dialog-card");
    expect(sources).not.toContain("success-message");
  });

  it("mounts every modal through the shared top-level portal", () => {
    expect(sources).not.toContain('<div className="dialog-backdrop">');
  });

  it("does not leave manual-run operation results in page flow", () => {
    expect(sources).not.toMatch(/<OperationFeedback\s+operation=\{manualRun\}/);
  });

  it("routes terminal long-operation results away from persistent inline feedback", () => {
    const usages = [...sources.matchAll(/<OperationFeedback\b[^>]*>/g)].map((match) => match[0]);
    expect(usages.length).toBeGreaterThan(0);
    expect(usages.filter((usage) => !usage.includes("persistTerminal")).every((usage) => usage.includes("hideTerminal"))).toBe(true);
  });
});
