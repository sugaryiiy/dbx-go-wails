import { describe, expect, it } from "vitest";
import type { ConnectionConfig } from "@/types/database";
import { influxTableQueryOptions } from "@/lib/table/influxTableQuery";

describe("InfluxDB preview protocol", () => {
  const config = (extra: Partial<ConnectionConfig>): ConnectionConfig => ({ id: "influx", name: "influx", db_type: "influxdb", host: "localhost", port: 8086, username: "", password: "", ...extra });

  it("uses the configured protocol even when health is unavailable or misleading", () => {
    expect(influxTableQueryOptions(config({ external_config: { version: "2" } }))).toEqual({ influxDbVersion: "2" });
    expect(influxTableQueryOptions(config({ external_config: { version: "1" }, database_info: { productVersion: "v2.7.12" } }))).toEqual({ influxDbVersion: "1" });
  });

  it("falls back to server version and leaves other builders' options unchanged", () => {
    expect(influxTableQueryOptions(config({ database_info: { productVersion: "v2.7.12" } }))).toEqual({ influxDbVersion: "2" });
    expect(influxTableQueryOptions(config({}))).toEqual({ influxDbVersion: "1" });
    expect(influxTableQueryOptions(config({ db_type: "mysql" }))).toEqual({});
  });

  it("carries aliases for colliding tag and field names into every preview query", () => {
    expect(
      influxTableQueryOptions(config({ external_config: { version: "2" } }), [
        { name: "dev_name [tag]", extra: "influxdb:tag:dev_name" },
        { name: "dev_name", extra: "influxdb:field" },
        { name: "time [field]", extra: "influxdb:field:time" },
      ]),
    ).toEqual({ influxDbVersion: "2", influxDbTagRenames: { dev_name: "dev_name [tag]" }, influxDbFieldRenames: { time: "time [field]" } });
  });
});
