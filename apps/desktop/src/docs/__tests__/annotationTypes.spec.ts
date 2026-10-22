import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { ANNOTATION_FILE_KEYS, COLUMN_ANNOTATION_KEYS, GROUP_ANNOTATION_KEYS, PROJECT_ANNOTATION_KEYS, TABLE_ANNOTATION_KEYS } from "../types";

// The Go structs are decoded with DisallowUnknownFields, so any property
// TypeScript adds that Go does not declare turns every save into a
// deserialization error at runtime. vue-tsc cannot see across the language
// boundary, so this reads the Go source and compares the field sets directly.
const goSource = readFileSync(path.resolve(__dirname, "../../../../../internal/server/cache_annotations.go"), "utf8");

function goFields(structName: string): string[] {
  const start = goSource.indexOf(`type ${structName} struct {`);
  expect(start, `struct ${structName} not found in cache_annotations.go`).toBeGreaterThan(-1);
  const body = goSource.slice(start, goSource.indexOf("\n}", start));
  return [...body.matchAll(/`json:"([a-zA-Z0-9]+)[",]/g)].map((match) => match[1]).sort();
}

describe("annotation types match the Go structs", () => {
  it.each([
    ["docsAnnotation", ANNOTATION_FILE_KEYS],
    ["docsProjectNote", PROJECT_ANNOTATION_KEYS],
    ["docsGroupNote", GROUP_ANNOTATION_KEYS],
    ["docsTableNote", TABLE_ANNOTATION_KEYS],
    ["docsColumnNote", COLUMN_ANNOTATION_KEYS],
  ])("%s", (structName, witness) => {
    // Go source on one side, the TS interface's own keys on the other —
    // so this fails whichever side drifts. Comparing Go against a hardcoded
    // list would only ever catch the Go side.
    expect(goFields(structName as string)).toEqual(Object.keys(witness).sort());
  });
});
