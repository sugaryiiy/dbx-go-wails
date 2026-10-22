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
  SetTheme(mode: "light" | "dark" | "system"): Promise<void>;
  Close(): Promise<void>;
  MarkFrontendReady(): Promise<void>;
  CompleteAppClose(action: "quit" | "hide"): Promise<void>;
  RequestAppClose(): Promise<void>;
  SetTrayVisible(visible: boolean): Promise<void>;
  ClipboardReadText(): Promise<string>;
  ClipboardWriteText(text: string): Promise<void>;
  OpenFile(options: WailsFileDialogOptions): Promise<string[] | null>;
  SaveFile(options: WailsFileDialogOptions): Promise<string>;
  SaveFileBytes(defaultFilename: string, contentBase64: string): Promise<string>;
  SaveQueryResultExport(exportId: string, defaultFilename: string): Promise<string>;
  ReadExternalSQL(path: string, maxBytes: number): Promise<import("./backendTypes").ExternalSqlFileSnapshot>;
  InspectExternalSQL(path: string): Promise<import("./backendTypes").ExternalSqlFileStatus>;
  WriteExternalSQL(path: string, content: string, options: { expectedContentHash?: string; expectedMissing?: boolean }): Promise<import("./backendTypes").ExternalSqlFileWriteResult>;
  SaveExternalSQL(name: string, content: string, extension: string): Promise<{ path: string; version: import("@/types/database").ExternalSqlFileVersion } | null>;
  ListSQLFiles(root: string, filter: string): Promise<import("./http").SqlFileEntry[]>;
  CreateSQLFile(root: string, directory: string, name: string): Promise<string>;
  RenameSQLFile(root: string, path: string, name: string): Promise<string>;
  DeleteSQLFile(root: string, path: string): Promise<void>;
  IsSQLiteDatabaseFile(path: string): Promise<boolean>;
  BackupSQLiteDatabase(id: string, path: string): Promise<void>;
  RestoreSQLiteDatabase(id: string, path: string): Promise<void>;
}

type WailsGlobal = { go?: { main?: { App?: WailsApp } } };

export interface WailsRuntimeEvents {
  EventsOn(eventName: string, callback: (...data: unknown[]) => void): () => void;
}

type WailsRuntimeGlobal = { runtime?: WailsRuntimeEvents };

// Subscribes to an event emitted by the Go shell through runtime.EventsEmit.
// Returns an unsubscribe function; outside the Wails runtime it is a no-op.
export function onWailsEvent(eventName: string, callback: (...data: unknown[]) => void, globalObject: unknown = globalThis): () => void {
  const runtime = (globalObject as WailsRuntimeGlobal)?.runtime;
  if (typeof runtime?.EventsOn !== "function") return () => {};
  return runtime.EventsOn(eventName, callback);
}

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
