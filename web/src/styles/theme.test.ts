import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyTheme, loadTheme, saveTheme, type Theme } from "./theme";

describe("theme persistence", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("defaults to dark theme when no preference is stored", () => {
    expect(loadTheme()).toBe("dark");
  });

  it("persists and reloads a stored theme", () => {
    saveTheme("light");
    expect(loadTheme()).toBe("light");
  });

  it("falls back to dark when stored value is invalid", () => {
    localStorage.setItem("shadoc.theme", "high-contrast");
    expect(loadTheme()).toBe("dark");
  });

  it("migrates legacy restic-control.theme key", () => {
    localStorage.setItem("restic-control.theme", "light");
    expect(loadTheme()).toBe("light");
    expect(localStorage.getItem("shadoc.theme")).toBe("light");
  });

  it("applies theme by setting data-theme attribute on document element", () => {
    applyTheme("light" as Theme);
    expect(document.documentElement.dataset.theme).toBe("light");
    applyTheme("dark" as Theme);
    expect(document.documentElement.dataset.theme).toBe("dark");
  });
});
