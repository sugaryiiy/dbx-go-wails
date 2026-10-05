import type { ConnectionConfig } from "@/types/database";

export interface SampleWorkspace {
  connection: ConnectionConfig;
  sql: string;
}
