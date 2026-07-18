import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const stylesheet = readFileSync(resolve(process.cwd(), "src/styles/app.css"), "utf8");

describe("form action alignment", () => {
  it("aligns direct form-grid action buttons with their controls", () => {
    expect(stylesheet).toMatch(/\.form-grid > :is\(\.primary-button, \.secondary-button, \.danger-button\)\s*\{\s*align-self: end;/);
  });
});

describe("select overflow", () => {
  it("clips long selected values inside the control", () => {
    expect(stylesheet).toMatch(/select\s*\{(?=[^}]*min-width: 0;)(?=[^}]*max-width: 100%;)(?=[^}]*overflow: hidden;)(?=[^}]*white-space: nowrap;)(?=[^}]*text-overflow: ellipsis;)/s);
  });
});

describe("wide table scrolling", () => {
  it("reserves a separate bottom gutter so the horizontal scrollbar cannot cover row text", () => {
    expect(stylesheet).toMatch(/\.table-frame\s*\{(?=[^}]*overflow-x: auto;)(?=[^}]*padding-bottom: 16px;)/s);
    expect(stylesheet).toMatch(/\.table-frame::-webkit-scrollbar\s*\{\s*height: 10px;/);
  });
});

describe("compact capacity refresh", () => {
  it("keeps the refresh control close to and vertically aligned with the value", () => {
    expect(stylesheet).toMatch(/\.capacity-cell-compact\s*\{(?=[^}]*justify-content: flex-start;)(?=[^}]*align-items: center;)(?=[^}]*gap: 6px;)/s);
    expect(stylesheet).toMatch(/\.capacity-refresh-button\s*\{(?=[^}]*display: grid;)(?=[^}]*place-items: center;)(?=[^}]*padding: 0;)/s);
  });
});

describe("restore workflow density", () => {
  it("uses a compact two-stage workbench and bounded Agent directory browser", () => {
    expect(stylesheet).toMatch(/\.restore-workbench\s*\{(?=[^}]*grid-template-columns: minmax\(280px, \.78fr\) minmax\(480px, 1\.22fr\);)(?=[^}]*gap: 14px;)/s);
    expect(stylesheet).toMatch(/\.restore-workflow-form\s*\{(?=[^}]*padding: 12px 16px 16px;)(?=[^}]*gap: 12px;)/s);
    expect(stylesheet).toMatch(/\.restore-snapshot-loading\s*\{(?=[^}]*display: inline-flex;)(?=[^}]*align-items: center;)/s);
    expect(stylesheet).toMatch(/\.agent-restore-directory-list\s*\{(?=[^}]*height: min\(220px, 30vh\);)(?=[^}]*overflow-y: auto;)/s);
  });

  it("renders Agent Restic progress as a compact record-owned strip", () => {
    expect(stylesheet).toMatch(/\.agent-inline-operation\s*\{(?=[^}]*min-height: 44px;)(?=[^}]*grid-template-columns: auto minmax\(0, 1fr\) auto;)/s);
  });
});

describe("page density", () => {
  it("uses compact vertical spacing for the shared workspace and editor sections", () => {
    expect(stylesheet).toMatch(/\.main-content\s*\{(?=[^}]*padding: 20px clamp\(20px, 3vw, 40px\) 72px;)/s);
    expect(stylesheet).toMatch(/\.content-section\s*\{\s*margin-top: 16px;/);
    expect(stylesheet).toMatch(/\.resource-editor-form\s*\{(?=[^}]*gap: 12px;)/s);
    expect(stylesheet).toMatch(/input, select, textarea\s*\{(?=[^}]*min-height: 38px;)(?=[^}]*padding: 8px 10px;)/s);
    expect(stylesheet).toMatch(/th, td\s*\{(?=[^}]*height: 42px;)(?=[^}]*padding: 0 12px;)/s);
  });
});

describe("sidebar application version", () => {
  it("keeps the version and optional update notice in a compact account footer", () => {
    expect(stylesheet).toMatch(/\.sidebar-account\s*\{(?=[^}]*margin-top: auto;)(?=[^}]*display: grid;)/s);
    expect(stylesheet).toMatch(/\.sidebar-version\s*\{(?=[^}]*padding: 8px 12px;)(?=[^}]*grid-template-columns: minmax\(0, 1fr\) auto;)(?=[^}]*font-size: 11px;)/s);
  });
});

describe("task editor section navigation", () => {
  it("keeps task section links in a single horizontal, scrollable row", () => {
    expect(stylesheet).toMatch(/\.task-editor-page \.editor-subtabs\s*\{(?=[^}]*flex-direction: row;)(?=[^}]*flex-wrap: nowrap;)(?=[^}]*overflow-x: auto;)/s);
  });
});

describe("toast interaction", () => {
  it("does not block page actions behind the notification", () => {
    expect(stylesheet).toMatch(/\.toast\s*\{(?=[^}]*pointer-events: none;)/s);
    expect(stylesheet).toMatch(/\.toast-close\s*\{(?=[^}]*pointer-events: auto;)/s);
  });
});
