import { afterEach, describe, expect, it, vi } from "vitest";
import { supportsMongoAdvancedOperations, mongoAdvancedUnavailableMessage } from "@/lib/mongo/mongoCapabilities";
import { serializeMongoDocumentId } from "@/lib/mongo/mongoDocumentValues";

const backend = vi.hoisted(() => ({ go: false }));
vi.mock("@/lib/backend/goCapabilities", () => ({ isGoBackend: () => backend.go }));
afterEach(() => {
  backend.go = false;
});

describe("Go MongoDB operation boundaries", () => {
  it("gates unported operations only in the Go runtime", () => {
    expect(supportsMongoAdvancedOperations()).toBe(true);
    backend.go = true;
    expect(supportsMongoAdvancedOperations()).toBe(false);
    expect(mongoAdvancedUnavailableMessage("zh-CN")).toContain("暂不支持");
    expect(mongoAdvancedUnavailableMessage("en")).toContain("not available");
  });

  it("preserves compound and typed IDs rather than targeting a string document", () => {
    const compound = { z: { $numberInt: "1" }, a: { $numberInt: "2" } };
    expect(serializeMongoDocumentId(compound)).toBe('{"z":{"$numberInt":"1"},"a":{"$numberInt":"2"}}');
    expect(serializeMongoDocumentId({ $date: { $numberLong: "1234" } })).toBe('{"$date":{"$numberLong":"1234"}}');
    expect(serializeMongoDocumentId({ $binary: { base64: "AAEC", subType: "80" } })).toBe('{"$binary":{"base64":"AAEC","subType":"80"}}');
    expect(serializeMongoDocumentId("[object Object]")).toBe('__dbx_mongo_string_id__"[object Object]"');
    expect(serializeMongoDocumentId(false)).toBe("false");
  });

  it("rejects compound IDs whose BSON key order JavaScript cannot preserve", () => {
    for (const id of [JSON.parse('{"2":"a","1":"b"}'), { nested: { "0": "value" } }, { nested: [{ "4294967294": "last index" }] }]) {
      expect(() => serializeMongoDocumentId(id)).toThrow("integer-index keys");
    }
    expect(serializeMongoDocumentId({ "01": "leading zero", "-0": "negative zero", "4294967295": "not an array index" })).toContain("leading zero");
    expect(serializeMongoDocumentId({ $timestamp: { t: 1, i: 2 } })).toBe('{"$timestamp":{"t":1,"i":2}}');
    expect(serializeMongoDocumentId({ nested: { $date: { $numberLong: "1" } } })).toBe('{"nested":{"$date":{"$numberLong":"1"}}}');
  });
});
