import { describe, expect, it } from "vitest";
import { createI18n } from "vue-i18n";
import { translateBackendError, type BackendErrorTranslate } from "@/i18n/backend-errors";
import { BackendErrorException, type BackendError } from "@/lib/backend/errorUtils";
import en from "@/i18n/locales/en";
import zhCN from "@/i18n/locales/zh-CN";
import zhTW from "@/i18n/locales/zh-TW";

function translator(locale = "zh-CN") {
  return createI18n({ legacy: false, locale, fallbackLocale: "en", messages: { en, "zh-CN": zhCN, "zh-TW": zhTW }, missingWarn: false, fallbackWarn: false }).global.t as BackendErrorTranslate;
}

function envelope(detail: string): BackendError {
  return { version: 1, code: "DBX-LEGACY-0001", messageKey: "backendErrors.legacy", messageParams: {}, source: "go", operationOutcome: "unknown", detail };
}

describe("common backend error translation", () => {
  const t = translator();
  const cases = [
    ["database connection is not open", "数据库尚未连接，请先连接数据库后重试。"],
    ["database connection has been closed", "数据库连接已关闭，请重新连接后重试。"],
    ["context deadline exceeded", "操作超时，请检查连接或调整超时时间后重试。"],
    ["context canceled", "操作已取消。"],
    ["writes are locked for this read-only or production connection; unlock writes explicitly before executing this statement", "此只读或生产库连接的写入已锁定，请先解锁写入再进行修改。"],
    ["query result session expired or was closed; execute the query again", "查询结果已过期或关闭，请重新执行查询。"],
    ["unbalanced SQL parentheses", "SQL 括号不匹配，请检查左右括号。"],
    ["Object name is ambiguous; provide its routine signature or trigger relation_name", "存在多个同名对象，请指定例程签名或触发器所属表。"],
    ["Tool is not allowed by MCP policy", "当前 MCP 权限不允许使用此工具，请检查设置中的工具授权。"],
  ];

  it.each(cases)("localizes raw, HTTP and native errors: %s", (message, expected) => {
    const payload = envelope(message);
    for (const error of [message, new Error(message), { message }, payload, new BackendErrorException(payload), JSON.stringify(payload)]) {
      expect(translateBackendError(t, error)).toBe(expected);
    }
  });

  it("uses the selected locale rather than forcing Chinese", () => {
    const message = "database connection is not open";
    expect(translateBackendError(translator("en"), envelope(message))).toBe("The database is not connected. Connect to it and try again.");
    expect(translateBackendError(translator("zh-TW"), envelope(message))).toBe("資料庫尚未連線，請先連線資料庫後再試。");
  });

  it.each([
    ["dial tcp [::1]:3306: connect: connection refused", "无法连接到 [::1]:3306：连接被拒绝，请检查服务是否启动及端口配置。"],
    ["dial tcp 127.0.0.1:5432: i/o timeout", "连接 127.0.0.1:5432 超时，请检查网络、地址与端口。"],
    ["read tcp 127.0.0.1:6000->127.0.0.1:5432: connection reset by peer", "与 127.0.0.1:6000->127.0.0.1:5432 的连接已中断，请重新连接后重试。"],
    ["dial tcp: lookup db.example.invalid on 127.0.0.1:53: no such host", "无法解析服务器地址 db.example.invalid，请检查主机名与 DNS 配置。"],
    ["not implemented by the Go database engine: custom import", "此功能暂未支持：custom import"],
  ])("keeps the diagnostic target in translated messages: %s", (message, expected) => {
    expect(translateBackendError(t, envelope(message))).toBe(expected);
  });

  it.each([
    ["Error 1045 (28000): Access denied for user 'demo'@'localhost' (using password: YES)", "数据库认证失败"],
    ['ERROR: password authentication failed for user "demo" (SQLSTATE 28P01)', "数据库认证失败"],
    ['failed to connect to `user=demo database=demo`: 127.0.0.1:5432 (localhost): server error: FATAL: password authentication failed for user "demo" (SQLSTATE 28P01)', "数据库认证失败"],
    ['ERROR: permission denied for table "订单" (SQLSTATE 42501)', "访问被拒绝"],
    ["Error 1064 (42000): You have an error in your SQL syntax near 'SELECT FROM' at line 2", "SQL 语法错误"],
    ["tls: failed to verify certificate: x509: certificate signed by unknown authority", "证书校验失败"],
  ])("explains driver failures without losing codes and original details: %s", (message, explanation) => {
    const payload = { ...envelope(message), errorPosition: { line: 2, column: 8, offset: 20 } };
    const result = translateBackendError(t, new BackendErrorException(payload));
    expect(result).toContain(explanation);
    expect(result).toContain(`原始信息：${message}`);
    expect(payload.errorPosition).toEqual({ line: 2, column: 8, offset: 20 });
  });

  it("keeps uncertain commits explicit rather than suggesting a blind retry", () => {
    const message = "transaction commit outcome is unknown; check the database before retrying: driver: bad connection";
    expect(translateBackendError(t, message)).toBe("无法确认事务是否提交成功，请先检查数据库状态，避免重试造成重复修改。\n\n原始信息：driver: bad connection");
  });

  it.each([
    'ERROR: column "database connection is not open" does not exist (SQLSTATE 42703)',
    'SQL: SELECT \'context deadline exceeded\', \'permission denied\'',
    "custom driver failure: dial tcp 127.0.0.1:5432: connect: connection refused",
    "constructor",
    "some driver specific failure",
  ])("leaves unknown diagnostics and SQL literals intact: %s", (message) => {
    expect(translateBackendError(t, message)).toBe(message);
  });

  it("does not add a duplicate generic backend summary for known errors", () => {
    const payload = envelope("database connection is not open");
    expect(translateBackendError(t, payload, "后端返回了错误。\n\ndatabase connection is not open")).toBe("数据库尚未连接，请先连接数据库后重试。");
  });
});
