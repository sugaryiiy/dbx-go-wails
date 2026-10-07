// Wails' native asset response writer does not implement http.Flusher. Poll
// the same Go job state instead of opening an SSE connection that cannot flush.
export async function pollWailsProgress<T>(url: string, onProgress: (progress: T) => void, isTerminal: (progress: T) => boolean, signal?: AbortSignal): Promise<T> {
  while (true) {
    signal?.throwIfAborted();
    const response = await fetch(`${url}${url.includes("?") ? "&" : "?"}poll=1`, { signal });
    if (!response.ok) {
      let message = `Progress request failed (${response.status})`;
      try {
        const error = await response.json();
        message = error.message || error.error?.message || error.error || message;
      } catch {
        /* Preserve HTTP status when the error body is not JSON. */
      }
      throw new Error(String(message));
    }
    const progress = (await response.json()) as T;
    onProgress(progress);
    if (isTerminal(progress)) return progress;
    await new Promise<void>((resolve, reject) => {
      const abort = () => {
        clearTimeout(timer);
        signal?.removeEventListener("abort", abort);
        reject(signal?.reason ?? new Error("Cancelled"));
      };
      const timer = setTimeout(() => {
        signal?.removeEventListener("abort", abort);
        resolve();
      }, 250);
      signal?.addEventListener("abort", abort, { once: true });
      if (signal?.aborted) abort();
    });
  }
}
