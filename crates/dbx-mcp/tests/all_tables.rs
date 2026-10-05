use std::sync::Arc;

use dbx_core::{models::connection::ConnectionConfig, storage::Storage};
use dbx_mcp::{DbxMcpServer, LocalBackend, McpScope};
use rmcp::{model::CallToolRequestParams, ServiceExt};
use serde_json::json;

#[tokio::test]
async fn lists_every_sqlite_table_over_mcp_with_read_only_permissions() {
    let directory = tempfile::tempdir().unwrap();
    let database_path = directory.path().join("tables.sqlite");
    let pool = dbx_core::db::sqlite::connect_path_create_if_missing(database_path.to_str().unwrap()).await.unwrap();
    for index in 0..125 {
        dbx_core::db::sqlite::execute_query(&pool, &format!("CREATE TABLE table_{index:03}(id INTEGER)"))
            .await
            .unwrap();
    }
    dbx_core::db::sqlite::execute_query(&pool, "CREATE VIEW all_ids AS SELECT id FROM table_000").await.unwrap();
    drop(pool);

    let storage_path = directory.path().join("dbx.db");
    let storage = Storage::open(&storage_path).await.unwrap();
    let connection: ConnectionConfig = serde_json::from_value(json!({
        "id": "all-tables", "name": "All tables", "db_type": "sqlite", "host": database_path,
        "port": 0, "username": "", "password": "", "database": "main", "read_only": true
    }))
    .unwrap();
    storage.save_connections(&[connection]).await.unwrap();
    let backend = Arc::new(LocalBackend::open(&storage_path).await.unwrap());
    let server = DbxMcpServer::with_runtime_options(backend, McpScope::default(), false);
    let (server_transport, client_transport) = tokio::io::duplex(16 * 1024);
    let server_task = tokio::spawn(async move { server.serve(server_transport).await });
    let client = ().serve(client_transport).await.unwrap();

    let tools = client.peer().list_tools(None).await.unwrap();
    let tool = tools.tools.iter().find(|tool| tool.name == "dbx_list_tables").unwrap();
    assert_eq!(tool.input_schema["properties"]["all_schemas"]["type"], "boolean");

    // Both the new all-schema request and existing requests must remain uncapped.
    for all_schemas in [false, true] {
        let mut arguments = json!({"connection_id": "all-tables", "database": "main"});
        if all_schemas {
            arguments["all_schemas"] = json!(true);
        }
        let result = client
            .peer()
            .call_tool(
                CallToolRequestParams::new("dbx_list_tables").with_arguments(arguments.as_object().unwrap().clone()),
            )
            .await
            .unwrap();
        assert_ne!(result.is_error, Some(true), "{result:?}");
        let content = result.structured_content.unwrap();
        assert_eq!(content["total"], 126);
        let tables = content["tables"].as_array().unwrap();
        assert_eq!(tables.len(), 126);
        assert!(tables.iter().any(|table| table["name"] == "table_124"));
        assert!(tables.iter().any(|table| table["name"] == "all_ids" && table["table_type"] == "VIEW"));
        assert!(result.content[0].as_text().unwrap().text.contains("table_124"));
    }
    client.cancel().await.unwrap();
    server_task.await.unwrap().unwrap().cancel().await.unwrap();
}
