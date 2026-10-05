import type { SqlFormatterCase } from "./sqlFormatterConfig";

/** Completion has its own preference; formatting existing SQL remains independent. */
export const DEFAULT_SQL_COMPLETION_CASE: SqlFormatterCase = "lower";

export function normalizeSqlCompletionCase(value: unknown): SqlFormatterCase {
  return value === "upper" || value === "preserve" || value === "lower" ? value : DEFAULT_SQL_COMPLETION_CASE;
}
