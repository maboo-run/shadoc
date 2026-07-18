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
