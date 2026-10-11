import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "../http";

afterEach(() => vi.unstubAllGlobals());
describe("Go Wails native SQL files", () => {
  it("reads and saves through native version checks", async () => {
    const version = { sizeBytes: 9, modifiedNs: "123", contentHash: "hash" };
    const app = {
      GetWindowState: vi.fn(),
      ReadExternalSQL: vi.fn().mockResolvedValue({ content: "SELECT 1;", version }),
      WriteExternalSQL: vi.fn().mockResolvedValue({ kind: "written", version }),
      InspectExternalSQL: vi.fn().mockResolvedValue({ kind: "missing" }),
      SaveExternalSQL: vi.fn().mockResolvedValue({ path: "/selected/query.sql", version }),
    };
    vi.stubGlobal("go", { main: { App: app } });
    await expect(api.readExternalSqlFileSnapshot("/selected/query.sql", 1024)).resolves.toEqual({ content: "SELECT 1;", version });
    await api.writeExternalSqlFile("/selected/query.sql", "SELECT 2;", { expectedContentHash: "hash" });
    expect(app.WriteExternalSQL).toHaveBeenCalledWith("/selected/query.sql", "SELECT 2;", { expectedContentHash: "hash" });
    await expect(api.inspectExternalSqlFile("/selected/query.sql")).resolves.toEqual({ kind: "missing" });
    await expect(api.saveExternalSqlFile("query.sql", "SELECT 1;")).resolves.toEqual({ path: "/selected/query.sql", version });
  });
  it("routes database backups through the native picker grants", async () => {
    const app = { GetWindowState: vi.fn(), BackupSQLiteDatabase: vi.fn().mockResolvedValue(undefined), RestoreSQLiteDatabase: vi.fn().mockResolvedValue(undefined), IsSQLiteDatabaseFile: vi.fn().mockResolvedValue(true) };
    vi.stubGlobal("go", { main: { App: app } });
    await api.backupSqliteDatabase("db", "/selected/backup.sqlite");
    expect(app.BackupSQLiteDatabase).toHaveBeenCalledWith("db", "/selected/backup.sqlite");
    await api.restoreSqliteDatabase("db", "/selected/backup.sqlite");
    expect(app.RestoreSQLiteDatabase).toHaveBeenCalledWith("db", "/selected/backup.sqlite");
    await expect(api.isSqliteDatabaseFile("/selected/backup.sqlite")).resolves.toBe(true);
  });
});
