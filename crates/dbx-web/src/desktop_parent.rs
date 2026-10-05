//! Optional lifetime binding for a desktop launcher that owns our stdin pipe.
//! Closing the parent's write end (including on a force quit) requests the
//! same bounded server shutdown as SIGTERM. Standalone deployments opt out.

use std::io::ErrorKind;

use tokio::io::{AsyncRead, AsyncReadExt};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

pub fn start_from_env(server_shutdown: CancellationToken, backup_stop: CancellationToken) {
    let enabled = stdin_close_enabled(std::env::var("DBX_EXIT_ON_STDIN_CLOSE").ok().as_deref());
    let _monitor = spawn_monitor(enabled, tokio::io::stdin, server_shutdown, backup_stop);
}

fn stdin_close_enabled(value: Option<&str>) -> bool {
    value.is_some_and(|value| matches!(value.trim(), "1" | "true"))
}

fn spawn_monitor<R, F>(
    enabled: bool,
    input: F,
    server_shutdown: CancellationToken,
    backup_stop: CancellationToken,
) -> Option<JoinHandle<()>>
where
    R: AsyncRead + Unpin + Send + 'static,
    F: FnOnce() -> R,
{
    if !enabled {
        return None;
    }
    let input = input();
    Some(tokio::spawn(monitor_input(input, server_shutdown, backup_stop)))
}

async fn monitor_input<R: AsyncRead + Unpin>(
    mut input: R,
    server_shutdown: CancellationToken,
    backup_stop: CancellationToken,
) {
    let mut buffer = [0u8; 1024];
    loop {
        let result = tokio::select! {
            biased;
            _ = server_shutdown.cancelled() => return,
            result = input.read(&mut buffer) => result,
        };
        match result {
            Ok(0) => {
                tracing::info!("Desktop parent input closed; shutting down");
                break;
            }
            Ok(_) => continue,
            Err(error) if error.kind() == ErrorKind::Interrupted => continue,
            Err(error) => {
                tracing::warn!("Desktop parent input failed: {error}; shutting down");
                break;
            }
        }
    }
    backup_stop.cancel();
    server_shutdown.cancel();
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;
    use tokio::io::AsyncWriteExt;

    #[test]
    fn lifecycle_binding_requires_an_explicit_switch() {
        assert!(stdin_close_enabled(Some("1")));
        assert!(stdin_close_enabled(Some(" true ")));
        for value in [None, Some(""), Some("0"), Some("false"), Some("enabled")] {
            assert!(!stdin_close_enabled(value));
        }
    }

    #[tokio::test]
    async fn disabled_monitor_does_not_open_or_read_stdin() {
        let server_shutdown = CancellationToken::new();
        let backup_stop = CancellationToken::new();
        let task = spawn_monitor(
            false,
            || -> tokio::io::Empty { panic!("an ordinary Web deployment must not consume stdin") },
            server_shutdown.clone(),
            backup_stop.clone(),
        );
        assert!(task.is_none());
        assert!(!server_shutdown.is_cancelled());
        assert!(!backup_stop.is_cancelled());
    }

    #[tokio::test]
    async fn parent_bytes_keep_running_and_eof_stops_server_and_backups() {
        let (reader, mut parent) = tokio::io::duplex(32);
        let server_shutdown = CancellationToken::new();
        let backup_stop = CancellationToken::new();
        let task = spawn_monitor(true, || reader, server_shutdown.clone(), backup_stop.clone()).unwrap();
        parent.write_all(b"parent still owns the pipe").await.unwrap();
        assert!(tokio::time::timeout(Duration::from_millis(20), server_shutdown.cancelled()).await.is_err());
        assert!(!backup_stop.is_cancelled());

        drop(parent);
        tokio::time::timeout(Duration::from_secs(1), task).await.unwrap().unwrap();
        assert!(server_shutdown.is_cancelled());
        assert!(backup_stop.is_cancelled());
    }

    #[tokio::test]
    async fn normal_shutdown_does_not_wait_for_parent_to_close_input() {
        let (reader, _parent) = tokio::io::duplex(32);
        let server_shutdown = CancellationToken::new();
        let backup_stop = CancellationToken::new();
        let task = spawn_monitor(true, || reader, server_shutdown.clone(), backup_stop).unwrap();
        server_shutdown.cancel();
        tokio::time::timeout(Duration::from_secs(1), task).await.unwrap().unwrap();
    }
}
