import { effectScope, nextTick, type EffectScope } from "vue";
import { afterEach, describe, expect, it } from "vitest";
import { useProcedureExecutionDialog } from "../useProcedureExecutionDialog";
import type { ProcedureExecutionRequest } from "../useSqlExecution";

const scopes: EffectScope[] = [];
const target: ProcedureExecutionRequest = {
  tabId: "source-tab",
  connectionId: "mes-connection",
  database: "mes",
  databaseType: "mysql",
  routineName: "master_p9_dg",
};

function dialog() {
  const scope = effectScope();
  scopes.push(scope);
  return scope.run(useProcedureExecutionDialog)!;
}

afterEach(() => {
  for (const scope of scopes.splice(0)) scope.stop();
});

describe("useProcedureExecutionDialog", () => {
  it("captures the invocation target independently of later changes", async () => {
    const state = dialog();
    const input = { ...target };
    const execution = state.request(input);
    input.database = "another_database";
    expect(state.target.value?.database).toBe("mes");
    expect(state.open.value).toBe(true);
    state.confirm(" CALL master_p9_dg('2026-10-13'); ");
    await expect(execution).resolves.toBe("CALL master_p9_dg('2026-10-13');");
  });

  it("accepts confirmation after the dialog emits its synchronous close event", async () => {
    const state = dialog();
    const execution = state.request(target);
    state.open.value = false;
    state.confirm("CALL master_p9_dg('2026-10-13');");
    await nextTick();
    await expect(execution).resolves.toBe("CALL master_p9_dg('2026-10-13');");
  });

  it("cancels an unconfirmed close without dispatching a call", async () => {
    const state = dialog();
    const execution = state.request(target);
    state.open.value = false;
    await nextTick();
    await expect(execution).resolves.toBeUndefined();
  });

  it("cancels an earlier request when another procedure replaces it", async () => {
    const state = dialog();
    const earlier = state.request(target);
    const latest = state.request({ ...target, routineName: "another_procedure" });
    await expect(earlier).resolves.toBeUndefined();
    await nextTick();
    expect(state.target.value?.routineName).toBe("another_procedure");
    expect(state.open.value).toBe(true);
    state.confirm("CALL another_procedure();");
    await expect(latest).resolves.toBe("CALL another_procedure();");
  });

  it("cancels pending execution when its host is disposed", async () => {
    const state = dialog();
    const execution = state.request(target);
    scopes[0].stop();
    await expect(execution).resolves.toBeUndefined();
  });
});
