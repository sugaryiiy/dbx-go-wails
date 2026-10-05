import { afterEach, describe, expect, it, vi } from "vitest";
import { beaconPluginInstall } from "@/lib/plugins/pluginMarketplace";

afterEach(() => vi.unstubAllGlobals());

describe("plugin install telemetry", () => {
  it.each(["install", "update"] as const)("does not contact upstream or create installation identifiers for %s", (kind) => {
    const fetch = vi.fn();
    const getItem = vi.fn();
    const setItem = vi.fn();
    vi.stubGlobal("fetch", fetch);
    vi.stubGlobal("localStorage", { getItem, setItem });
    beaconPluginInstall("example.plugin", "1.2.3", kind);
    expect(fetch).not.toHaveBeenCalled();
    expect(getItem).not.toHaveBeenCalled();
    expect(setItem).not.toHaveBeenCalled();
  });
});
