import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useCloseActionPrompt } from "@/composables/useCloseActionPrompt";

const settingsState = {
  quit_on_close: false,
  close_action_prompted: true,
};

vi.mock("@/stores/settingsStore", () => ({
  useSettingsStore: () => ({
    desktopSettings: settingsState,
    updateDesktopSettings: vi.fn().mockResolvedValue(undefined),
  }),
}));

type EventCallback = (...data: unknown[]) => void;

let eventsOn: ReturnType<typeof vi.fn>;
let eventHandlers: Map<string, EventCallback>;
let markFrontendReady: ReturnType<typeof vi.fn>;
let completeAppClose: ReturnType<typeof vi.fn>;

beforeEach(() => {
  settingsState.quit_on_close = false;
  settingsState.close_action_prompted = true;
  eventHandlers = new Map();
  eventsOn = vi.fn((name: string, callback: EventCallback) => {
    eventHandlers.set(name, callback);
    return () => eventHandlers.delete(name);
  });
  markFrontendReady = vi.fn().mockResolvedValue(undefined);
  completeAppClose = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("go", {
    main: {
      App: {
        GetWindowState: vi.fn(),
        MarkFrontendReady: markFrontendReady,
        CompleteAppClose: completeAppClose,
      },
    },
  });
  vi.stubGlobal("runtime", { EventsOn: eventsOn });
});

afterEach(() => vi.unstubAllGlobals());

describe("useCloseActionPrompt in the Wails runtime", () => {
  it("subscribes to the shell close request and marks the frontend ready", () => {
    const requestClose = vi.fn();
    const prompt = useCloseActionPrompt({ requestClose });
    prompt.setupCloseActionPromptListener();
    expect(eventsOn).toHaveBeenCalledWith("dbx-app-close-requested", expect.any(Function));
    expect(markFrontendReady).toHaveBeenCalledTimes(1);
    prompt.cleanupCloseActionPromptListener();
    expect(eventHandlers.size).toBe(0);
  });

  it("maps a tray quit request to the quit action without a choice prompt", () => {
    const requestClose = vi.fn();
    const prompt = useCloseActionPrompt({ requestClose });
    prompt.setupCloseActionPromptListener();
    eventHandlers.get("dbx-app-close-requested")?.("quit");
    expect(requestClose).toHaveBeenCalledWith("quit", { requireCloseActionChoice: false });
    prompt.cleanupCloseActionPromptListener();
  });

  it("maps a window close request to the settings-driven action", () => {
    const requestClose = vi.fn();
    const prompt = useCloseActionPrompt({ requestClose });
    prompt.setupCloseActionPromptListener();
    eventHandlers.get("dbx-app-close-requested")?.("settings");
    expect(requestClose).toHaveBeenCalledWith("hide", { requireCloseActionChoice: false });
    settingsState.quit_on_close = true;
    settingsState.close_action_prompted = false;
    eventHandlers.get("dbx-app-close-requested")?.();
    expect(requestClose).toHaveBeenCalledWith("quit", { requireCloseActionChoice: true });
    prompt.cleanupCloseActionPromptListener();
  });

  it("executes the decided action through the Go shell", async () => {
    const prompt = useCloseActionPrompt({ requestClose: vi.fn() });
    await prompt.performCloseAction("hide");
    expect(completeAppClose).toHaveBeenCalledWith("hide");
    await prompt.performCloseAction("quit");
    expect(completeAppClose).toHaveBeenCalledWith("quit");
  });
});
