import { onScopeDispose, ref, shallowRef, watch } from "vue";
import type { ProcedureExecutionRequest } from "@/composables/useSqlExecution";

/** Keep the invocation target until the dialog's close and execute events settle. */
export function useProcedureExecutionDialog() {
  const open = ref(false);
  const target = shallowRef<ProcedureExecutionRequest>();
  let resolvePending: ((sql: string | undefined) => void) | undefined;

  function settle(sql?: string) {
    const resolve = resolvePending;
    resolvePending = undefined;
    open.value = false;
    resolve?.(sql);
  }

  function request(invocation: ProcedureExecutionRequest): Promise<string | undefined> {
    settle();
    target.value = { ...invocation };
    open.value = true;
    return new Promise((resolve) => {
      resolvePending = resolve;
    });
  }

  function confirm(sql: string) {
    const value = sql.trim();
    if (value) settle(value);
  }

  // ProcedureExecutionDialog emits update:open(false) before execute. Let both
  // synchronous events finish before treating an unconfirmed close as cancel.
  watch(
    open,
    (isOpen) => {
      if (isOpen) return;
      const pending = resolvePending;
      queueMicrotask(() => {
        if (!open.value && resolvePending === pending) settle();
      });
    },
    { flush: "sync" },
  );
  onScopeDispose(() => settle());

  return { open, target, request, confirm, cancel: () => settle() };
}
