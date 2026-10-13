# Go 原生 Redis、MongoDB 与 ClickHouse

这三类引擎使用 Go 客户端或 HTTP 协议，不启动 Rust、JDBC 或旧驱动代理。
桌面与 Web 使用同一套接口；MCP 目前仍限 SQLite、MySQL/MariaDB、PostgreSQL。

## 已支持的功能

| 引擎 | 连接 | 读写和结构 |
| --- | --- | --- |
| Redis | 单机、用户名/密码、TLS、SSH | SCAN 浏览、string/hash/list/set/zset 编辑、二进制键/值、TTL、重命名、受限控制台 |
| MongoDB | 单节点直连、SCRAM-SHA-1/256、TLS、SSH | 文档增删改查、分页/排序、聚合、count/distinct、集合、索引、统计、Explain、字段采样 |
| ClickHouse | HTTP 8123 / HTTPS 通常 8443、用户名/密码、SSH | SQL 查询与写入、数据库/表/视图/列/索引、SHOW CREATE、分页、查询取消 |

连接编辑器中的 TLS 验证证书链和原数据库主机名。自签名部署需填写 CA 文件；不提供跳过证书校验。
SSH 与 TLS 可叠加，支持共享隧道配置。证书文件由 Go 进程读取，详见 [安全连接](go-secure-connections.md)。
三类引擎均遵守连接只读/生产保护、限时解锁、超时和取消；断开、重连或退出会关闭旧连接并取消操作。

## 使用边界

### Redis

- 原子编辑脚本使用 `redis.acl_check_cmd`，需要 Redis 7+；已验证 Redis 7.4。
- hash 字段与 zset 成员重命名检查目标冲突；列表按索引编辑，保留其他相同值。
- 字符串预览最多 64 KiB，完整值读取/输入最多 8 MiB，单次协议回复最多 16 MiB；集合每页最多 500 项，批量写键最多 1000 个。
- 不支持 Cluster、Sentinel、Streams、RedisJSON、订阅、监控、全库清空、按模式删除或按值筛选。
- 控制台只允许已实现命令；只读连接不能通过控制台参数跳过保护。控制台不是完整 redis-cli。
- 每个请求独立选择逻辑数据库，避免多个标签页相互影响。

### MongoDB

- 连接单个明确主机并使用 `directConnection=true`；不进行副本集/分片拓扑发现，不支持 SRV、URI 连接、OIDC 或旧驱动。
- URL 参数仅允许 `authSource`、`authMechanism=SCRAM-SHA-1|SCRAM-SHA-256`、`directConnection=true`，不允许重复字段。常见管理认证账户使用 `authSource=admin`。
- 文档复制和保存使用规范 Extended JSON，保留 ObjectId、Int64、Decimal128、Binary、Date 等 BSON 类型；界面显示值与可保存值分开处理。
- 文档编辑器和表格暂不支持编辑、删除含整数索引字段名的复合 `_id`（包括嵌套对象，例如 `{"2":"a","1":"b"}`）。JavaScript 会重排这些字段，而 BSON 标识依赖字段顺序；界面会在发送写入前拒绝此类操作，避免误改其他文档。
- Shell 输入通过本地命令解析器转换为结构化请求，不执行任意 JavaScript。聚合 `$out`/`$merge` 与 JavaScript 操作符会被拒绝。
- `runCommand` 只开放 ping、hello、buildInfo、dbStats、collStats、listDatabases 和受写入保护的 create。
- 单次结果最多 10000 行、32 MiB；不支持 bulkWrite、集合克隆、用户管理、GridFS、备份恢复。

### ClickHouse

- 使用 HTTP 接口，不使用原生 TCP 9000 端口。UInt64/Int64 和 Decimal 在结果中保留精确字符串，NULL 保持为空值。
- 只读操作设置服务端 `readonly=1`；取消时中断 HTTP 请求并尝试终止对应服务端查询。
- 不支持事务、原子批处理、持久游标、通用表格直接编辑和参数化导入；可通过 SQL 编辑器执行允许的写入。
- 支持普通 SELECT 的文本 EXPLAIN；查询结果可经后端导出 CSV/XLSX/JSON/文本。表格当前页可导出 CSV/XLSX/文本，单对象 DDL 可导出；整表/整库导出及页面 JSON/HTML/Markdown 格式化尚未迁移。
- SQL 不允许 `SETTINGS`、`FORMAT`、会话/事务覆盖或反斜杠转义。受保护查询对外部及未知函数采用保守限制。
- 主键和跳数索引不是唯一约束，不用于生成按唯一键编辑的 SQL。

## 实库验证与复现

已对 Redis 7.4、MongoDB 8、ClickHouse 25.8 执行真实 TLS、SSH、CRUD、类型保真、只读保护、取消和错误 CA 拒绝测试，并启用 Go race 检查。
HTTP 集成测试另验证登录、加密配置保存后重启、原生连接、元数据、读写解锁/重新上锁和 MCP 拒绝未迁移引擎。

测试只应指向可丢弃数据库：会创建和删除测试库、集合、表、键，Redis ACL 用例还需要用户管理权限。
每个引擎使用一个权限为 `0600` 的 JSON 配置文件，例如（使用自己的测试地址和凭据）：

```json
{
  "id": "test-mongo",
  "db_type": "mongodb",
  "host": "127.0.0.1",
  "port": 27017,
  "database": "dbx_test",
  "username": "test-user",
  "password": "replace-with-test-password",
  "save_password": true,
  "ssl": true,
  "ca_cert_path": "/absolute/path/to/test-ca.pem",
  "url_params": "authSource=admin"
}
```

Redis 改用 `db_type=redis`、数据库 `0`，ClickHouse 改用 `db_type=clickhouse` 和 HTTP(S) 端口；两者删除 MongoDB 的 `url_params`。Redis 默认用户可留空用户名。

```sh
GOFLAGS=-mod=mod \
DBX_TEST_REDIS_CONFIG=/private/redis.json \
DBX_TEST_MONGO_CONFIG=/private/mongo.json \
DBX_TEST_CLICKHOUSE_CONFIG=/private/clickhouse.json \
DBX_TEST_NATIVE_CONFIGS=/private/native-config-array.json \
go test -race ./internal/... ./cmd/...
```

`DBX_TEST_NATIVE_CONFIGS` 是上述三份配置组成的 JSON 数组；不要将凭据文件加入 Git。
未设置这些变量时，实库测试自动跳过，普通协议、生命周期与权限测试仍运行。
