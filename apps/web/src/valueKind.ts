import { ValueKind, type Frame } from "@gantry/api-client";

/**
 * Extract a plottable number from a Frame's oneof value, or null for
 * non-numeric kinds (text/raw/unset). Booleans map to 0/1 so they can render as
 * a state strip.
 */
export function frameNumeric(f: Frame): number | null {
  const k = f.value?.kind;
  if (!k) return null;
  switch (k.case) {
    case "f64":
      return k.value;
    case "i64":
      return Number(k.value);
    case "flag":
      return k.value ? 1 : 0;
    default:
      return null; // text, raw
  }
}

export function isNumericKind(kind: ValueKind): boolean {
  return kind === ValueKind.F64 || kind === ValueKind.I64;
}

export function isBoolKind(kind: ValueKind): boolean {
  return kind === ValueKind.BOOL;
}

/** Whether a channel of this kind gets a chart at all. */
export function isPlottable(kind: ValueKind): boolean {
  return isNumericKind(kind) || isBoolKind(kind);
}

export function kindLabel(kind: ValueKind): string {
  switch (kind) {
    case ValueKind.F64:
      return "f64";
    case ValueKind.I64:
      return "i64";
    case ValueKind.BOOL:
      return "bool";
    case ValueKind.TEXT:
      return "text";
    case ValueKind.RAW:
      return "raw";
    default:
      return "?";
  }
}

/** Format a current value for the readout, given its channel kind. */
export function formatValue(value: number | null, kind: ValueKind): string {
  if (value === null) return "—";
  if (isBoolKind(kind)) return value >= 0.5 ? "true" : "false";
  if (kind === ValueKind.I64) return value.toFixed(0);
  // f64: compact but precise-ish
  if (Math.abs(value) >= 1e6 || (value !== 0 && Math.abs(value) < 1e-3)) {
    return value.toExponential(3);
  }
  return value.toFixed(3);
}
