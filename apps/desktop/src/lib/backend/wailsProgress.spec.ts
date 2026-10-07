import { afterEach, describe, expect, it, vi } from "vitest";
import { pollWailsProgress } from "./wailsProgress";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Wails job polling", () => {
  it("reports progress and stops polling after the terminal state", async () => {
    vi.useFakeTimers();
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: "running" })))
      .mockResolvedValueOnce(new Response(JSON.stringify({ status: "done" })));
    vi.stubGlobal("fetch", fetch);
    const progress = vi.fn();
    const result = pollWailsProgress<{ status: string }>("/api/import/progress/job", progress, (value) => value.status === "done");
    await vi.advanceTimersByTimeAsync(250);
    expect(await result).toEqual({ status: "done" });
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[0]?.[0]).toBe("/api/import/progress/job?poll=1");
    expect(progress).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("rejects HTTP errors without leaving polling timers behind", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ message: "Job not found" }), { status: 404 })));
    await expect(pollWailsProgress("/api/import/progress/missing", vi.fn(), () => false)).rejects.toThrow("Job not found");
    expect(vi.getTimerCount()).toBe(0);
  });

  it("aborts an outstanding wait and removes the pending timer", async () => {
    vi.useFakeTimers();
    const controller = new AbortController();
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ status: "running" })));
    vi.stubGlobal("fetch", fetch);
    const result = pollWailsProgress("/api/import/progress/job", vi.fn(), () => false, controller.signal);
    const rejection = expect(result).rejects.toThrow("Cancelled by user");
    await vi.advanceTimersByTimeAsync(0);
    controller.abort(new Error("Cancelled by user"));
    await rejection;
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
});
