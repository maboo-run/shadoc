const rfc3339Timestamp = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})$/i;

export function timestampAtSecond(value: string | Date): string {
  const source = value instanceof Date ? value.toISOString() : value;
  if (!(value instanceof Date) && !rfc3339Timestamp.test(source)) return source;
  const parsed = value instanceof Date ? value : new Date(source);
  if (Number.isNaN(parsed.getTime())) return source;
  return parsed.toISOString().replace(/\.\d{3}Z$/, "Z");
}

export function isRFC3339Timestamp(value: unknown): value is string {
  return typeof value === "string" && rfc3339Timestamp.test(value);
}

export function formatDateTime(
  value: unknown,
  locale: string,
  timeZone: string,
  options: Intl.DateTimeFormatOptions = { dateStyle: "medium", timeStyle: "medium" },
): string {
  if (value == null || value === "") return "—";
  const date = value instanceof Date ? value : new Date(String(value));
  if (Number.isNaN(date.getTime())) return String(value);
  return new Intl.DateTimeFormat(locale, { ...options, timeZone }).format(date);
}

export function formatEmbeddedDateTimes(value: string, locale: string, timeZone: string): string {
  return value.replace(
    /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})/gi,
    (timestamp) => formatDateTime(timestamp, locale, timeZone),
  );
}

export function instantToZonedDateTimeInput(value: string, timeZone: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const parts = zonedParts(date, timeZone);
  return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
}

export function zonedDateTimeInputToISOString(value: string, timeZone: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  if (!match) return "";
  const desired = match.slice(1).map(Number);
  const desiredUTC = Date.UTC(desired[0], desired[1] - 1, desired[2], desired[3], desired[4]);
  let instant = desiredUTC;
  for (let iteration = 0; iteration < 3; iteration += 1) {
    const current = zonedParts(new Date(instant), timeZone);
    const currentUTC = Date.UTC(
      Number(current.year),
      Number(current.month) - 1,
      Number(current.day),
      Number(current.hour),
      Number(current.minute),
    );
    instant += desiredUTC - currentUTC;
  }
  const resolved = new Date(instant);
  const verified = zonedParts(resolved, timeZone);
  if (`${verified.year}-${verified.month}-${verified.day}T${verified.hour}:${verified.minute}` !== value) return "";
  return resolved.toISOString();
}

function zonedParts(date: Date, timeZone: string): Record<"year" | "month" | "day" | "hour" | "minute", string> {
  const result = {} as Record<"year" | "month" | "day" | "hour" | "minute", string>;
  const formatter = new Intl.DateTimeFormat("en-CA", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
  for (const part of formatter.formatToParts(date)) {
    if (part.type === "year" || part.type === "month" || part.type === "day" || part.type === "hour" || part.type === "minute") {
      result[part.type] = part.value;
    }
  }
  return result;
}
