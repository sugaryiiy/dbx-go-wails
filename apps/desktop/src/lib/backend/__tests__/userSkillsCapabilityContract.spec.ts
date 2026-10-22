import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const tauri = readFileSync(new URL("../tauri.ts", import.meta.url), "utf8");
const http = readFileSync(new URL("../http.ts", import.meta.url), "utf8");
const api = readFileSync(new URL("../api.ts", import.meta.url), "utf8");
const goSkills = readFileSync(new URL("../../../../../../internal/server/skills.go", import.meta.url), "utf8");

function functionBody(source: string, operation: string): string {
  const start = source.indexOf(`export async function ${operation}(`);
  expect(start, `${operation} transport function`).toBeGreaterThanOrEqual(0);
  const end = source.indexOf("\nexport async function ", start + 1);
  return source.slice(start, end === -1 ? source.length : end);
}

describe("user skills transport contract", () => {
  it("routes list/read through the desktop Tauri backend", () => {
    expect(functionBody(tauri, "listUserSkills")).toContain('invoke("list_user_skills"');
    expect(functionBody(tauri, "readUserSkills")).toContain('invoke("read_user_skills"');
    expect(api).toContain('listUserSkills = forward("listUserSkills")');
    expect(api).toContain('readUserSkills = forward("readUserSkills")');
  });

  it("keeps the Go skill service strictly read-only", () => {
    expect(goSkills).not.toContain("os.WriteFile");
    expect(goSkills).not.toContain("os.Remove");
    expect(goSkills).not.toContain("os.MkdirAll");
    expect(goSkills).not.toContain("os.Create(");
  });

  it("uses the internal HTTP transport only inside Wails, served by the Go skills endpoints", () => {
    for (const operation of ["listUserSkills", "readUserSkills"]) {
      const body = functionBody(http, operation);
      expect(body).toContain("only available in the desktop app");
      expect(body).toContain("if (!isWailsRuntime()) throw");
      expect(body.indexOf("if (!isWailsRuntime())")).toBeLessThan(body.indexOf("return post("));
      expect(body).toContain('return post("/api/ai/skills/');
    }
    expect(goSkills).toContain('"/api/ai/skills/list"');
    expect(goSkills).toContain('"/api/ai/skills/read"');
  });
});
