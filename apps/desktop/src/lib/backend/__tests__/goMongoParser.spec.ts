import { afterEach, describe, expect, it, vi } from "vitest";
import { mongoParseShellCommand } from "../http";

vi.mock("../goCapabilities", () => ({ isGoBackend: () => true }));
afterEach(() => vi.unstubAllGlobals());

describe("Go MongoDB shell parser", () => {
  it("parses commands locally and preserves Extended JSON without a Rust endpoint", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const command = await mongoParseShellCommand('db.people.find({_id: ObjectId("507f1f77bcf86cd799439011")}).limit(5)');
    expect(command).toMatchObject({ kind: "find", collection: "people", limit: 5 });
    expect(JSON.stringify(command)).toContain("$oid");
    expect(fetch).not.toHaveBeenCalled();
  });
  it("rejects arbitrary JavaScript rather than evaluating it", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    await expect(mongoParseShellCommand("process.exit();")).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });
  it("rejects unsupported writes inside a database selection", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    await expect(mongoParseShellCommand('db.getSiblingDB("app").records.bulkWrite([{insertOne:{document:{n:1}}}])')).rejects.toThrow("bulkWrite has not yet been migrated");
    expect(fetch).not.toHaveBeenCalled();
  });
});
