import { describe, expect, it } from "vitest";
import { timestampAtSecond } from "./dateTime";

describe("timestampAtSecond", () => {
  it("normalizes a valid timestamp without fractional seconds", () => {
    expect(timestampAtSecond("2026-07-12T10:02:30.508331Z")).toBe("2026-07-12T10:02:30Z");
  });
});
