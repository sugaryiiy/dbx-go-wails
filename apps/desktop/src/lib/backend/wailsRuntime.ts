export interface WailsWindowState {
  maximized: boolean;
  fullscreen: boolean;
  alwaysOnTop: boolean;
}

export interface WailsFileDialogOptions {
  title?: string;
  defaultPath?: string;
  directory?: boolean;
  multiple?: boolean;
  filters?: Array<{ name: string; extensions: string[] }>;
}

export interface WailsApp {
  GetWindowState(): Promise<WailsWindowState>;
  Minimize(): Promise<void>;
  ToggleMaximize(): Promise<void>;
  SetAlwaysOnTop(value: boolean): Promise<void>;
  Close(): Promise<void>;
  ClipboardReadText(): Promise<string>;
  ClipboardWriteText(text: string): Promise<void>;
  OpenFile(options: WailsFileDialogOptions): Promise<string[] | null>;
  SaveFile(options: WailsFileDialogOptions): Promise<string>;
  SaveFileBytes(defaultFilename: string, contentBase64: string): Promise<string>;
  SaveQueryResultExport(exportId: string, defaultFilename: string): Promise<string>;
}

type WailsGlobal = { go?: { main?: { App?: WailsApp } } };

// This intentionally does not change isTauriRuntime: Wails database calls must
// use the shared HTTP API instead of Tauri's Rust command transport.
export function isWailsRuntime(globalObject: unknown = globalThis): boolean {
  return typeof (globalObject as WailsGlobal)?.go?.main?.App?.GetWindowState === "function";
}

export function wailsApp(globalObject: unknown = globalThis): WailsApp {
  const app = (globalObject as WailsGlobal)?.go?.main?.App;
  if (!app) throw new Error("Wails desktop runtime is unavailable");
  return app;
}

export async function openWailsFile(options: WailsFileDialogOptions = {}): Promise<string | string[] | null> {
  const paths = await wailsApp().OpenFile(options);
  if (!paths?.length) return null;
  return options.multiple ? paths : (paths[0] ?? null);
}

export async function saveWailsFile(options: WailsFileDialogOptions = {}): Promise<string | null> {
  return (await wailsApp().SaveFile(options)) || null;
}

export async function saveWailsBytes(defaultFilename: string, bytes: Uint8Array): Promise<void> {
  let binary = "";
  for (let index = 0; index < bytes.length; index += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
  }
  const path = await wailsApp().SaveFileBytes(defaultFilename, btoa(binary));
  if (!path) throw new Error("Export cancelled");
}
