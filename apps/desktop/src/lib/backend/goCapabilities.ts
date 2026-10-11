import { isWailsRuntime } from "./wailsRuntime";

/** Agent profiles require their installed standalone driver; the Go host owns
 * JSON-RPC sessions, cancellation and write protection for these engines. */
export const GO_CONNECTION_PROFILES = new Set([
  "sqlite",
  "mysql",
  "mariadb",
  "postgres",
  "redis",
  "mongodb",
  "clickhouse",
  "sqlserver",
  "oracle",
  "dameng",
  "kingbase",
  "highgo",
  "uxdb",
  "vastbase",
  "goldendb",
  "databend",
  "yashandb",
  "databricks",
  "saphana",
  "teradata",
  "vertica",
  "firebird",
  "exasol",
  "oceanbase-oracle",
  "gbase",
  "access",
  "h2",
  "snowflake",
  "trino",
  "hive",
  "argo",
  "transwarp",
  "kyuubi",
  "impala",
  "db2",
  "informix",
  "neo4j",
  "nebula",
  "cassandra",
  "bigquery",
  "spanner",
  "kylin",
  "ignite",
  "ignite3",
  "sundb",
  "oscar",
  "tdengine",
  "xugu",
  "etcd",
  "zookeeper",
  "iotdb",
  "iris",
  "influxdb",
  "spark",
  "elasticsearch",
  "consul",
  "nacos",
  "meilisearch",
  "kafka",
  "rocketmq",
  "rabbitmq",
]);

const GO_AGENT_DRIVER_KEYS: Readonly<Record<string, string>> = {
  oracle: "oracle",
  dameng: "dameng",
  kingbase: "kingbase",
  highgo: "highgo",
  uxdb: "uxdb",
  vastbase: "vastbase",
  goldendb: "goldendb",
  databend: "databend",
  yashandb: "yashandb",
  databricks: "databricks",
  saphana: "saphana",
  teradata: "teradata",
  vertica: "vertica",
  firebird: "firebird",
  exasol: "exasol",
  "oceanbase-oracle": "oceanbase-oracle",
  gbase: "gbase8a",
  access: "access",
  h2: "h2",
  snowflake: "snowflake",
  trino: "trino",
  hive: "hive",
  argo: "argo",
  transwarp: "transwarp",
  kyuubi: "hive",
  impala: "hive",
  db2: "db2",
  informix: "informix",
  neo4j: "neo4j",
  nebula: "nebula",
  cassandra: "cassandra",
  bigquery: "bigquery",
  spanner: "spanner",
  kylin: "kylin",
  ignite: "ignite",
  ignite3: "ignite3",
  sundb: "sundb",
  oscar: "oscar",
  tdengine: "tdengine",
  xugu: "xugu",
  etcd: "etcd",
  zookeeper: "zookeeper",
  iotdb: "iotdb",
  iris: "iris",
  spark: "spark",
  sqlserver: "sqlserver-legacy",
};
const GO_AGENT_PROFILE_KEYS: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  sqlserver: {
    "sqlserver-legacy": "sqlserver-legacy",
  },
  oracle: {
    "oracle-legacy": "oracle",
    "oracle-10g": "oracle",
    oci: "oracle-oci",
  },
  gbase: {
    gbase8s: "gbase8s",
    gbase8a: "gbase8a",
  },
  h2: {
    "h2-legacy": "h2-legacy",
    "h2-v1": "h2",
    "h2-v2": "h2",
    "h2-v3": "h2",
    "h2-custom": "h2",
  },
  transwarp: {
    "transwarp-inceptor": "transwarp",
  },
  nebula: {
    "nebula-v3": "nebula",
  },
  etcd: {
    "etcd-v2": "etcd2",
  },
  mq: {
    kafka: "kafka",
    rocketmq: "rocketmq",
    rabbitmq: "rabbitmq",
  },
  iris: {
    cache: "cache",
  },
};

export function goAgentDriverKey(dbType: string, profile?: string | null): string | undefined {
  return (profile ? GO_AGENT_PROFILE_KEYS[dbType]?.[profile] : undefined) ?? GO_AGENT_DRIVER_KEYS[dbType];
}

export function supportsGoConnectionProfile(dbType: string, profile?: string | null): boolean {
  if (dbType === "mq") return Boolean(profile && GO_AGENT_PROFILE_KEYS.mq[profile]);
  return GO_CONNECTION_PROFILES.has(dbType) && (!profile || profile === "native" || profile === dbType || (dbType === "mysql" && profile === "mariadb") || Boolean(GO_AGENT_PROFILE_KEYS[dbType]?.[profile]));
}

export function isGoBackend(): boolean {
  return import.meta.env.VITE_DBX_BACKEND === "go" || isWailsRuntime();
}

/** Availability of visual table writers; executing SQL has its own permission checks. */
export function supportsBackendTableEditing(dbType?: string): boolean {
  return !isGoBackend() || dbType !== "clickhouse";
}

/** Formats whose current-page writer works without a backend formatting route. */
export function goClickHouseGridExportActions(context: string | undefined, hasQueryExport: boolean, completeLocalResult: boolean): ReadonlySet<string> {
  const actions = new Set(["page-csv", "page-xlsx", "page-xlsx-with-sql", "page-txt", "selected-csv", "selected-xlsx", "selected-xlsx-with-sql", "selected-txt", "all-results-xlsx", "all-results-xlsx-with-sql"]);
  if (context !== "table-data") {
    actions.add("csv");
    actions.add("xlsx");
    actions.add("txt");
    if (hasQueryExport && !completeLocalResult) actions.add("json");
  }
  return actions;
}

/** Whole-table/database export uses routes not yet implemented for Go ClickHouse. */
export function supportsBackendTableExport(dbType?: string): boolean {
  return !isGoBackend() || dbType !== "clickhouse";
}
