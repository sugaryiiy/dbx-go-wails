import { isWailsRuntime } from "./wailsRuntime";

export const GO_CONNECTION_PROFILES = new Set(["sqlite", "mysql", "mariadb", "postgres"]);

export function isGoBackend(): boolean {
  return import.meta.env.VITE_DBX_BACKEND === "go" || isWailsRuntime();
}
