import { describe, expect, it } from "vitest";
import {
  formatDateTime,
  formatEmbeddedDateTimes,
  instantToZonedDateTimeInput,
  timestampAtSecond,
  zonedDateTimeInputToISOString,
} from "./dateTime";

describe("timestampAtSecond", () => {
  it("normalizes a valid timestamp without fractional seconds", () => {
    expect(timestampAtSecond("2026-07-12T10:02:30.508331Z")).toBe("2026-07-12T10:02:30Z");
  });
});

describe("configured time zone formatting", () => {
  it("formats an instant in the explicitly selected time zone", () => {
    expect(formatDateTime("2026-07-12T10:00:00Z", "zh-CN", "Asia/Shanghai")).toContain("18:00:00");
  });

  it("converts instants to and from datetime-local values in the selected time zone", () => {
    expect(instantToZonedDateTimeInput("2026-07-12T10:00:00Z", "Asia/Shanghai")).toBe("2026-07-12T18:00");
    expect(zonedDateTimeInputToISOString("2026-07-12T18:00", "Asia/Shanghai")).toBe("2026-07-12T10:00:00.000Z");
  });

  it("does not depend on the browser operating-system time zone", () => {
    expect(instantToZonedDateTimeInput("2026-07-12T10:00:00Z", "America/New_York")).toBe("2026-07-12T06:00");
    expect(zonedDateTimeInputToISOString("2026-07-12T06:00", "America/New_York")).toBe("2026-07-12T10:00:00.000Z");
  });

  it("localizes timestamps embedded in user-visible diagnostics", () => {
    expect(formatEmbeddedDateTimes("检查于 2026-07-12T10:00:00Z 完成", "zh-CN", "Asia/Shanghai"))
      .toMatch(/检查于.*2026.*7.*12.*18:00:00.*完成/);
  });
});
