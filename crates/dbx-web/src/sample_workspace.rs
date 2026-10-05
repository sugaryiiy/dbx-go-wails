//! An editable local dataset for exploring the regular database workbench.
//! This is deliberately separate from the locked-down public demo deployment.

use std::io::ErrorKind;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use axum::extract::State;
use axum::Json;
use dbx_core::models::connection::{ConnectionConfig, DatabaseType};
use rusqlite::{Connection, OpenFlags};
use serde::Serialize;
use tokio::sync::Mutex;

use crate::error::AppError;
use crate::state::WebState;

const CONNECTION_ID: &str = "dbx-basic-workbench-sample";
const DATABASE_FILE: &str = "basic-workbench.sqlite";
// SQLite's application_id marks our file without adding a fifth user table.
const APPLICATION_ID: i32 = 0x44425853; // DBXS
const SAMPLE_VERSION: i32 = 1;

// Serialize first-time setup and registration, including requests from several
// browser windows. The stable connection ID and no-clobber file publication
// also prevent duplicate records or file replacement across processes.
static SETUP_LOCK: Mutex<()> = Mutex::const_new(());

// Open the tab directly on an executable statement: its initial caret is at
// position zero and the regular Run action executes the current statement.
const EXAMPLE_SQL: &str = r#"SELECT
    o.id AS order_id,
    c.name AS customer,
    c.city,
    p.name AS product,
    oi.quantity,
    oi.unit_price,
    ROUND(oi.quantity * oi.unit_price, 2) AS subtotal,
    o.status,
    o.ordered_at,
    o.note
FROM orders AS o
JOIN customers AS c ON c.id = o.customer_id
JOIN order_items AS oi ON oi.order_id = o.id
JOIN products AS p ON p.id = oi.product_id
ORDER BY o.id, oi.id;
"#;

const SEED_SQL: &str = r#"
CREATE TABLE customers (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT UNIQUE,
    city TEXT NOT NULL,
    note TEXT
);
CREATE TABLE products (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    category TEXT NOT NULL,
    price NUMERIC NOT NULL CHECK (price >= 0),
    stock INTEGER NOT NULL CHECK (stock >= 0)
);
CREATE TABLE orders (
    id INTEGER PRIMARY KEY,
    customer_id INTEGER NOT NULL REFERENCES customers(id),
    status TEXT NOT NULL CHECK (status IN ('待付款', '已付款', '已发货', '已取消')),
    ordered_at TEXT NOT NULL,
    note TEXT
);
CREATE TABLE order_items (
    id INTEGER PRIMARY KEY,
    order_id INTEGER NOT NULL REFERENCES orders(id),
    product_id INTEGER NOT NULL REFERENCES products(id),
    quantity INTEGER NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC NOT NULL CHECK (unit_price >= 0),
    UNIQUE (order_id, product_id)
);
CREATE INDEX idx_orders_customer ON orders(customer_id);
CREATE INDEX idx_order_items_product ON order_items(product_id);
INSERT INTO customers (id, name, email, city, note) VALUES
    (1, '林晓', 'lin.xiao@example.com', '上海', '工作日配送'),
    (2, '陈宇', 'chen.yu@example.com', '杭州', NULL),
    (3, '王宁', NULL, '深圳', '首次下单'),
    (4, '赵悦', 'zhao.yue@example.com', '北京', NULL);
INSERT INTO products (id, name, category, price, stock) VALUES
    (1, '无线键盘', '办公设备', 239.90, 36),
    (2, '笔记本支架', '办公设备', 89.50, 58),
    (3, '手冲咖啡豆', '食品', 68.80, 120),
    (4, '陶瓷马克杯', '生活用品', 39.90, 75),
    (5, '便签纸套装', '文具', 12.50, 200);
INSERT INTO orders (id, customer_id, status, ordered_at, note) VALUES
    (1001, 1, '已发货', '2026-09-28 09:30:00', '请开电子发票'),
    (1002, 2, '已付款', '2026-09-29 14:05:00', NULL),
    (1003, 3, '待付款', '2026-09-30 10:15:00', NULL),
    (1004, 1, '已付款', '2026-10-01 16:40:00', '与上次订单同一地址'),
    (1005, 4, '已取消', '2026-10-02 11:20:00', '客户取消');
INSERT INTO order_items (id, order_id, product_id, quantity, unit_price) VALUES
    (1, 1001, 1, 1, 239.90),
    (2, 1001, 2, 2, 89.50),
    (3, 1002, 3, 2, 68.80),
    (4, 1002, 4, 1, 39.90),
    (5, 1003, 2, 1, 89.50),
    (6, 1003, 5, 3, 12.50),
    (7, 1004, 3, 1, 68.80),
    (8, 1005, 4, 2, 39.90);
"#;

#[derive(Serialize)]
pub struct SampleWorkspaceResponse {
    pub connection: ConnectionConfig,
    pub sql: String,
}

/// POST /api/workspace/sample, without a request body. Register the connection
/// only after the complete sample file is available. Connecting and opening
/// the SQL tab then use the ordinary frontend connection/query workflow.
pub async fn open(State(state): State<Arc<WebState>>) -> Result<Json<SampleWorkspaceResponse>, AppError> {
    if state.demo_mode {
        return Err(AppError::forbidden("Creating an editable sample workspace is disabled in public demo mode"));
    }

    let _guard = SETUP_LOCK.lock().await;
    let data_dir = state.data_dir.clone();
    let path = tokio::task::spawn_blocking(move || ensure_sample_database(&data_dir))
        .await
        .map_err(|error| AppError::internal(format!("Sample workspace setup failed: {error}")))??;
    let host = path.to_str().ok_or_else(|| AppError::internal("The sample workspace path must contain valid UTF-8"))?;

    let saved = state.app.storage.load_connections().await.map_err(AppError::from)?;
    let existing = saved.iter().find(|config| config.id == CONNECTION_ID);
    if existing.is_some_and(|config| config.db_type != DatabaseType::Sqlite || config.host != host) {
        return Err(AppError::bad_request(
            "The sample connection points to another database. Remove that connection entry before reopening the sample.",
        ));
    }
    // Reuse an existing connection even if it was renamed/imported under a
    // different ID. Opening the sample must not reset connection preferences.
    let existing =
        existing.or_else(|| saved.iter().find(|config| config.db_type == DatabaseType::Sqlite && config.host == host));
    let connection = match existing {
        Some(config) => config.clone(),
        None => {
            // A plain SQLite file has no encryption key. `save_password: false`
            // means "ask for a password on connect" in the shared frontend;
            // keep the normal persisted-credential mode with an empty key.
            let config: ConnectionConfig = serde_json::from_value(serde_json::json!({
                "id": CONNECTION_ID,
                "name": "示例数据库",
                "note": "本地 SQLite 示例：客户、产品、订单和订单明细。可自由编辑，重新打开会保留修改。",
                "db_type": "sqlite",
                "host": host,
                "port": 0,
                "username": "",
                "password": "",
                "database": "main",
                "color": "#22c55e",
                "save_password": true
            }))
            .map_err(|error| AppError::internal(format!("Invalid sample connection configuration: {error}")))?;
            state.app.storage.save_connections(std::slice::from_ref(&config)).await.map_err(AppError::from)?;
            config
        }
    };
    state.app.configs.write().await.entry(connection.id.clone()).or_insert_with(|| connection.clone());
    Ok(Json(SampleWorkspaceResponse { connection, sql: EXAMPLE_SQL.to_string() }))
}

fn ensure_sample_database(data_dir: &Path) -> Result<PathBuf, AppError> {
    let data_dir = data_dir.canonicalize().map_err(sample_io_error)?;
    let samples_dir = data_dir.join("samples");
    match std::fs::create_dir(&samples_dir) {
        Ok(()) => {}
        Err(error) if error.kind() == ErrorKind::AlreadyExists => {}
        Err(error) => return Err(sample_io_error(error)),
    }
    let directory = std::fs::symlink_metadata(&samples_dir).map_err(sample_io_error)?;
    if !directory.is_dir() || directory.file_type().is_symlink() {
        return Err(AppError::bad_request("The sample workspace directory must be a regular directory"));
    }
    let path = samples_dir.join(DATABASE_FILE);
    if sample_file_exists(&path)? {
        validate_sample_file(&path)?;
        return Ok(path);
    }

    // Create the entire dataset in a private temporary file, then publish it
    // atomically without replacing an existing file. Interrupted/failed setup
    // cannot leave a half-initialized database at the public sample path.
    let temporary = tempfile::NamedTempFile::new_in(&samples_dir).map_err(sample_io_error)?;
    let mut database = Connection::open(temporary.path()).map_err(sample_sql_error)?;
    database.pragma_update(None, "foreign_keys", true).map_err(sample_sql_error)?;
    let transaction = database.transaction().map_err(sample_sql_error)?;
    transaction.pragma_update(None, "application_id", APPLICATION_ID).map_err(sample_sql_error)?;
    transaction.pragma_update(None, "user_version", SAMPLE_VERSION).map_err(sample_sql_error)?;
    transaction.execute_batch(SEED_SQL).map_err(sample_sql_error)?;
    transaction.commit().map_err(sample_sql_error)?;
    database.close().map_err(|(_, error)| sample_sql_error(error))?;
    temporary.as_file().sync_all().map_err(sample_io_error)?;
    match temporary.persist_noclobber(&path) {
        Ok(_) => {}
        Err(error) if error.error.kind() == ErrorKind::AlreadyExists => {
            validate_sample_file(&path)?;
        }
        Err(error) => return Err(sample_io_error(error.error)),
    }
    Ok(path)
}

fn sample_file_exists(path: &Path) -> Result<bool, AppError> {
    match std::fs::symlink_metadata(path) {
        Ok(metadata) if metadata.is_file() && !metadata.file_type().is_symlink() => Ok(true),
        Ok(_) => Err(AppError::bad_request("The sample database path must be a regular file")),
        Err(error) if error.kind() == ErrorKind::NotFound => Ok(false),
        Err(error) => Err(sample_io_error(error)),
    }
}

fn validate_sample_file(path: &Path) -> Result<(), AppError> {
    sample_file_exists(path)?;
    let database = Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY).map_err(sample_sql_error)?;
    let application_id: i32 =
        database.pragma_query_value(None, "application_id", |row| row.get(0)).map_err(sample_sql_error)?;
    let version: i32 = database.pragma_query_value(None, "user_version", |row| row.get(0)).map_err(sample_sql_error)?;
    if application_id != APPLICATION_ID || version != SAMPLE_VERSION {
        return Err(AppError::bad_request(
            "The existing file is not a supported DBX sample database. It has been left unchanged.",
        ));
    }
    Ok(())
}

fn sample_io_error(error: std::io::Error) -> AppError {
    AppError::internal(format!("Cannot prepare the sample workspace file: {error}"))
}

fn sample_sql_error(error: rusqlite::Error) -> AppError {
    AppError::internal(format!("Cannot prepare the sample SQLite database: {error}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::StatusCode;
    use dbx_core::connection::AppState;

    async fn test_state() -> (Arc<WebState>, tempfile::TempDir) {
        let directory = tempfile::tempdir().unwrap();
        let storage = dbx_core::persistence::test_storage::open(&directory.path().join("storage.db")).await.unwrap();
        let app = Arc::new(AppState::new_with_plugin_dir(storage, directory.path().join("plugins")));
        (Arc::new(WebState::for_tests(app, directory.path().to_path_buf())), directory)
    }

    #[tokio::test]
    async fn sample_has_related_editable_data_and_executable_example() {
        let (state, _directory) = test_state().await;
        let Json(sample) = open(State(state.clone())).await.unwrap();
        assert_eq!(sample.connection.name, "示例数据库");
        assert_eq!(sample.connection.db_type, DatabaseType::Sqlite);
        assert_eq!(sample.connection.database.as_deref(), Some("main"));
        assert!(!sample.connection.read_only);
        let database = Connection::open(&sample.connection.host).unwrap();
        let mut query = database.prepare(&sample.sql).unwrap();
        let rows = query
            .query_map([], |row| Ok((row.get::<_, String>(1)?, row.get::<_, f64>(6)?)))
            .unwrap()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        assert_eq!(rows.len(), 8);
        assert_eq!(rows[0].0, "林晓");
        assert!((rows[0].1 - 239.9).abs() < 0.0001);
        let nulls: i32 =
            database.query_row("SELECT COUNT(*) FROM customers WHERE email IS NULL", [], |row| row.get(0)).unwrap();
        assert_eq!(nulls, 1);
        let foreign_key_errors: i32 =
            database.query_row("SELECT COUNT(*) FROM pragma_foreign_key_check", [], |row| row.get(0)).unwrap();
        assert_eq!(foreign_key_errors, 0);
        database.execute("UPDATE products SET stock = stock + 1 WHERE id = 1", []).unwrap();
        assert_eq!(state.app.storage.load_connections().await.unwrap().len(), 1);
    }

    #[tokio::test]
    async fn reopening_preserves_edits_deletions_and_connection_preferences() {
        let (state, _directory) = test_state().await;
        let Json(first) = open(State(state.clone())).await.unwrap();
        let database = Connection::open(&first.connection.host).unwrap();
        database.execute("UPDATE customers SET name = '已修改客户' WHERE id = 1", []).unwrap();
        database.execute("DELETE FROM order_items WHERE id = 8", []).unwrap();
        let mut renamed = first.connection.clone();
        renamed.name = "我的练习数据库".to_string();
        renamed.save_password = false;
        state.app.storage.save_connections(std::slice::from_ref(&renamed)).await.unwrap();

        let Json(second) = open(State(state.clone())).await.unwrap();
        assert_eq!(first.connection.id, second.connection.id);
        assert_eq!(second.connection.name, renamed.name);
        assert!(!second.connection.save_password, "reopening must preserve edited credential preferences");
        let name: String = database.query_row("SELECT name FROM customers WHERE id = 1", [], |row| row.get(0)).unwrap();
        assert_eq!(name, "已修改客户");
        let count: i32 = database.query_row("SELECT COUNT(*) FROM order_items", [], |row| row.get(0)).unwrap();
        assert_eq!(count, 7);
        assert_eq!(state.app.storage.load_connections().await.unwrap().len(), 1);
    }

    #[tokio::test]
    async fn saved_plain_sample_connects_without_requesting_an_encryption_key() {
        use axum::http::HeaderMap;
        use dbx_core::connection::PoolKind;

        use crate::routes::connection::{connect_db, ConnectRequest};

        let (state, _directory) = test_state().await;
        let Json(sample) = open(State(state.clone())).await.unwrap();
        // This is the frontend's ordinary no-prompt credential mode. A false
        // value would ask users to enter a key for this unencrypted database.
        assert!(sample.connection.save_password);
        assert!(sample.connection.password.is_empty());
        let saved = state.app.storage.load_connections().await.unwrap().remove(0);
        assert!(saved.save_password);
        assert!(saved.password.is_empty());

        // Exercise the persisted configuration and normal Web connection path,
        // not the rusqlite handle used to initialize the sample file.
        state.app.configs.write().await.clear();
        let Json(connection_id) = connect_db(
            State(state.clone()),
            HeaderMap::new(),
            Json(ConnectRequest { config: saved, client_attempt: None }),
        )
        .await
        .unwrap();
        let pool = match state.app.pool_handle(&connection_id).await.unwrap() {
            PoolKind::Sqlite(pool) => pool,
            _ => panic!("the sample must use the regular SQLite pool"),
        };
        let result = dbx_core::db::sqlite::execute_query(&pool, &sample.sql).await.unwrap();
        assert_eq!(result.rows.len(), 8);
        assert_eq!(result.rows[0][1], serde_json::json!("林晓"));
        drop(pool);
        state.app.remove_connection_pools(&connection_id).await;
    }

    #[tokio::test]
    async fn concurrent_requests_create_one_connection_and_preserve_other_connections() {
        let (state, directory) = test_state().await;
        let unrelated: ConnectionConfig = serde_json::from_value(serde_json::json!({
            "id": "existing", "name": "Existing", "db_type": "sqlite",
            "host": directory.path().join("existing.sqlite").to_str().unwrap(),
            "port": 0, "username": "", "password": "", "database": "main"
        }))
        .unwrap();
        state.app.storage.save_connections(std::slice::from_ref(&unrelated)).await.unwrap();
        let mut jobs = Vec::new();
        for _ in 0..6 {
            let state = state.clone();
            jobs.push(tokio::spawn(async move { open(State(state)).await.unwrap().0.connection.id }));
        }
        for job in jobs {
            assert_eq!(job.await.unwrap(), CONNECTION_ID);
        }
        let saved = state.app.storage.load_connections().await.unwrap();
        assert_eq!(saved.len(), 2);
        assert!(saved.iter().any(|config| config == &unrelated));
    }

    #[tokio::test]
    async fn preexisting_unmarked_file_is_not_modified_or_registered() {
        let (state, directory) = test_state().await;
        let samples = directory.path().join("samples");
        std::fs::create_dir(&samples).unwrap();
        let path = samples.join(DATABASE_FILE);
        let database = Connection::open(&path).unwrap();
        database
            .execute_batch("CREATE TABLE valuable_data(value TEXT); INSERT INTO valuable_data VALUES ('keep');")
            .unwrap();
        database.close().unwrap();
        let original = std::fs::read(&path).unwrap();
        let error = match open(State(state.clone())).await {
            Err(error) => error,
            Ok(_) => panic!("must refuse to overwrite an unrelated file"),
        };
        assert_eq!(error.status, StatusCode::BAD_REQUEST);
        assert_eq!(std::fs::read(&path).unwrap(), original);
        assert!(state.app.storage.load_connections().await.unwrap().is_empty());
    }

    #[tokio::test]
    async fn failed_file_creation_does_not_register_a_connection() {
        let (state, directory) = test_state().await;
        std::fs::write(directory.path().join("samples"), "not a directory").unwrap();
        assert!(open(State(state.clone())).await.is_err());
        assert!(state.app.storage.load_connections().await.unwrap().is_empty());
        assert!(state.app.configs.read().await.is_empty());
    }

    #[tokio::test]
    async fn public_demo_cannot_create_an_editable_sample() {
        let (mut state, directory) = test_state().await;
        Arc::get_mut(&mut state).unwrap().demo_mode = true;
        let error = match open(State(state.clone())).await {
            Err(error) => error,
            Ok(_) => panic!("public demo must reject sample creation"),
        };
        assert_eq!(error.status, StatusCode::FORBIDDEN);
        assert!(!directory.path().join("samples").exists());
        assert!(state.app.storage.load_connections().await.unwrap().is_empty());
    }

    #[tokio::test]
    async fn changed_connection_target_is_not_overwritten() {
        let (state, directory) = test_state().await;
        let Json(sample) = open(State(state.clone())).await.unwrap();
        let mut changed = sample.connection;
        changed.host = directory.path().join("another.sqlite").to_str().unwrap().to_string();
        state.app.storage.save_connections(std::slice::from_ref(&changed)).await.unwrap();
        assert!(open(State(state.clone())).await.is_err());
        let saved = state.app.storage.load_connections().await.unwrap();
        assert_eq!(saved.len(), 1);
        assert_eq!(saved[0].host, changed.host);
    }

    #[tokio::test]
    async fn http_endpoint_requires_login_and_completed_migration() {
        use std::sync::atomic::Ordering;

        use axum::{middleware, routing::post, Router};

        let (state, directory) = test_state().await;
        *state.password_hash.write().await = Some("configured-password-hash".to_string());
        state.sessions.write().await.insert("sample-test-session".to_string());
        state.migration_ready.store(false, Ordering::Release);
        let api = Router::new()
            .route("/workspace/sample", post(open))
            .layer(middleware::from_fn_with_state(state.clone(), crate::migration_gate))
            .layer(middleware::from_fn_with_state(state.clone(), crate::demo::demo_mode_gate))
            .layer(middleware::from_fn_with_state(state.clone(), crate::auth::auth_middleware))
            .with_state(state.clone());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/api/workspace/sample", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            axum::serve(listener, Router::new().nest("/api", api)).await.unwrap();
        });
        let client = reqwest::Client::new();
        let response = client.post(&url).send().await.unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
        let response = client.post(&url).header("cookie", "dbx_session=sample-test-session").send().await.unwrap();
        assert_eq!(response.status(), StatusCode::LOCKED);
        assert!(!directory.path().join("samples").exists());

        state.migration_ready.store(true, Ordering::Release);
        let response = client.post(&url).header("cookie", "dbx_session=sample-test-session").send().await.unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        let payload: serde_json::Value = response.json().await.unwrap();
        assert_eq!(payload["connection"]["id"], CONNECTION_ID);
        assert_eq!(payload["connection"]["db_type"], "sqlite");
        assert_eq!(payload["sql"], EXAMPLE_SQL);
        server.abort();
    }

    #[cfg(unix)]
    #[tokio::test]
    async fn symbolic_links_are_not_followed_for_sample_files_or_directory() {
        let (state, directory) = test_state().await;
        let elsewhere = tempfile::tempdir().unwrap();
        let samples = directory.path().join("samples");
        std::os::unix::fs::symlink(elsewhere.path(), &samples).unwrap();
        assert!(open(State(state.clone())).await.is_err());
        assert!(!elsewhere.path().join(DATABASE_FILE).exists());
        std::fs::remove_file(&samples).unwrap();
        std::fs::create_dir(&samples).unwrap();
        let target = elsewhere.path().join("keep.sqlite");
        std::fs::write(&target, "untouched").unwrap();
        std::os::unix::fs::symlink(&target, samples.join(DATABASE_FILE)).unwrap();
        assert!(open(State(state.clone())).await.is_err());
        assert_eq!(std::fs::read_to_string(&target).unwrap(), "untouched");
        assert!(state.app.storage.load_connections().await.unwrap().is_empty());
    }
}
