# Go host driver migration

The Go host launches the existing standalone DBX driver components directly. It
does not launch a Rust backend or proxy requests to one. JDBC components still
need Java and an installed driver artifact. SQL Server defaults to the
`sqlserver-legacy` JDBC component in the Go host.

## Implemented host behavior

- JSON-RPC handshake, legacy protocol fallback, correlated concurrent replies,
  and version 2 session IDs.
- Separate driver processes for connection workloads, metadata, databases and
  editor sessions; cursor ownership and editor/connection cleanup.
- Query, multi-query, batch, transaction, manual transaction, paged query,
  schema metadata and the installed agent's etcd/ZooKeeper KV operations.
- Context cancellation and timeout quarantine. A cancelled agent process is
  terminated; queries are never replayed automatically. Reconnect after a
  process failure or timeout.
- Registry and local driver lists, downloads, SHA-256/size verification,
  Java configuration, managed JRE installation, offline packages, cancellation,
  storage usage, uninstall, runtime listing and process stop.
- Archive traversal, duplicate paths, links and special files are rejected.
  Driver/JRE replacement rolls back if persistent state cannot be saved.
- Write locks reject SQL outside a conservative read-only allowlist and reject
  KV/MQ mutations before a protocol request is sent. Rollback remains available.
- Error messages redact connection passwords, transport secrets, service/MQ
  auth, token signing keys, Kafka JAAS passwords and URL-encoded credentials.

## HTTP services

| Service | Implemented operations | Current boundary |
| --- | --- | --- |
| Elasticsearch | Connection/version, index metadata, document query/count/write/delete, SQL endpoint and REST queries | Locked REST uses an allowlist. SQL pagination closes the server cursor after the returned page. |
| Consul | KV list/get/CAS put/delete, flags preservation, session-lock checks, transactions; mapped agent/catalog/health/session/prepared query/ACL/mesh routes | Bulk archive/import, enterprise impact previews and unimplemented routes return an explicit unsupported error. |
| Nacos | Token login/refresh, namespaces, config list/get/publish/delete/history, V1 service/instance and mapped user/role operations | The adapter targets V1 APIs. V3/rNacos-specific administrative planes and raw APIs are not migrated. Config bodies retain their original text. |
| Meilisearch | Connection, index metadata, document query/count | Async writes and task acknowledgement are not implemented. |
| InfluxDB 1.x/2.x | Native HTTP connection/version, Basic or Token auth, organization bucket pagination, measurements/columns, InfluxQL or Flux results, native table previews with time windows, filters, sorting and offset pages; 2.x scalar row counts | No standalone agent download or DBRP mapping for 2.x table previews. Editor InfluxQL on 2.x uses `/query` and needs a DBRP mapping. Preview filters support comparisons, AND/OR and IS NULL; unsupported expressions fail explicitly. InfluxDB 3.x, transactions and point editing are not implemented. Arbitrary Flux is rejected while writes are locked; canonical generated read queries remain available. |

HTTP uses the Go network transport and TLS configuration, cancels active
requests on connection close, rejects redirects and relative-path traversal,
and limits request and response bodies. Capability probes use actual API
responses rather than a canned connection-success result.

## Message queues

Kafka, RabbitMQ and RocketMQ use their installed standalone agents. The host
translates `external_config.systemKind`, auth and `extra` into each agent's
connection envelope. It maps topics, topic/config operations, consumer groups,
message peeking/production, and supported broker-specific routes. RabbitMQ
includes virtual hosts, exchanges/bindings, users/permissions, client
connections/channels, policies, overview and nodes. RocketMQ includes routes,
message query/trace and consumer-group configuration. List envelopes are
converted to frontend arrays; RocketMQ bounded list pages are drained rather
than silently truncated.

Pulsar is not implemented in this migration. Agent MQ auth currently accepts
none/basic; unsupported token/OAuth2 modes are rejected. Direct certificate
fields and SSH for standalone agents are rejected. Kafka ACL management,
MQ token issuance/raw APIs, broker-inapplicable operations and omitted
operations return unsupported errors. Capability flags reflect these limits.
RocketMQ per-topic retention is not advertised because the native component
rejects it as a broker-level setting.

## Validation and remaining limits

Tests exercise real subprocess JSON-RPC fixtures and local HTTP/TLS servers,
including out-of-order responses, EOF, malformed protocol, independent sessions,
cursor ownership, cancellation, write locks, MQ connection/result translation,
Consul CAS conflicts, Nacos concurrent token refresh, content preservation,
TLS trust rejection, archive integrity and transactional installation rollback.
These tests do not claim compatibility testing against externally running
production database or broker instances.

Runtime CPU/RSS sampling and automatic runtime restart are not implemented.
Archives containing symlinks are rejected, including JRE distributions that
require links. Agent-specific connection options outside the mapped contract,
OCI environment setup, arbitrary JDBC plugins and complete service/broker
administrative feature parity still need follow-up. An unsupported operation
fails explicitly; it does not return a fabricated success or empty dataset.

Run checks with `-mod=mod`; the repository's legacy vendor tree does not match
its Go module dependencies.
