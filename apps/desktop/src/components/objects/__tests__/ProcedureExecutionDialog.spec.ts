// @vitest-environment happy-dom

import { createApp, h, nextTick, reactive } from "vue";
import { createI18n } from "vue-i18n";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { RoutineParameter } from "@/lib/table/routineExecutionSql";
import ProcedureExecutionDialog from "../ProcedureExecutionDialog.vue";

const mocks = vi.hoisted(() => ({ loadRoutineParameters: vi.fn() }));
vi.mock("@/lib/table/routineParameters", () => mocks);

const cleanups: Array<() => void> = [];
afterEach(() => {
  for (const cleanup of cleanups.splice(0)) cleanup();
  mocks.loadRoutineParameters.mockReset();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function button(label: string) {
  const element = [...document.querySelectorAll<HTMLButtonElement>("[role='dialog'] button")].find((candidate) => candidate.textContent?.trim() === label);
  expect(element, `Button ${label}`).toBeDefined();
  return element!;
}

function valueInput(name: string) {
  const input = document.querySelector<HTMLInputElement>(`[data-routine-parameter='${name}'] input[data-slot='input']`);
  expect(input).not.toBeNull();
  return input!;
}

async function setValue(input: HTMLInputElement | HTMLTextAreaElement, value: string) {
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  await nextTick();
}

async function checkParameter(name: string, mode: "NULL" | "DEFAULT", checked = true) {
  const input = document.querySelector<HTMLInputElement>(`[data-routine-parameter='${name}'] input[aria-label='${name} ${mode}']`)!;
  input.checked = checked;
  input.dispatchEvent(new Event("change", { bubbles: true }));
  await nextTick();
}

function preview() {
  return document.querySelector<HTMLTextAreaElement>("[role='dialog'] textarea")!;
}

async function mount(databaseType: "mysql" | "postgres" = "mysql") {
  const execute = vi.fn();
  const openSql = vi.fn();
  const state = reactive({ open: true, connectionId: "mysql-connection", database: "mes", databaseType, schema: "mes", routineName: "master_p9_dq" });
  const host = document.createElement("div");
  document.body.append(host);
  const app = createApp({
    render: () => h(ProcedureExecutionDialog, { ...state, "onUpdate:open": (value: boolean) => (state.open = value), onExecute: execute, onOpenSql: openSql }),
  });
  app.use(
    createI18n({
      legacy: false,
      locale: "en",
      missingWarn: false,
      fallbackWarn: false,
      messages: {
        en: {
          dangerDialog: { cancel: "Cancel" },
          contextMenu: {
            confirmExecuteProcedureTitle: "Execute procedure",
            confirmExecuteProcedureMessage: "Execute {name}",
            executeProcedure: "Execute",
            openInSqlEditor: "Open SQL",
            resetSqlPreview: "Reset SQL",
            loadingProcedureParameters: "Loading parameters",
            procedureParametersUnavailable: "Edit SQL to provide arguments manually",
            parameterNull: "NULL",
            parameterDefault: "DEFAULT",
            procedureParameterValuesRequired: "Fill parameters or select NULL / DEFAULT: {names}",
            procedureEmptyStringHint: "Use '' in SQL preview for an empty string",
          },
        },
      },
    }),
  );
  app.mount(host);
  cleanups.push(() => {
    app.unmount();
    host.remove();
  });
  await vi.waitFor(() => expect(document.querySelector("[role='dialog']")).not.toBeNull());
  await nextTick();
  return { execute, openSql, state };
}

describe("ProcedureExecutionDialog parameter execution", () => {
  it("blocks both actions until metadata arrives, including dispatched clicks on disabled buttons", async () => {
    const pending = deferred<RoutineParameter[]>();
    mocks.loadRoutineParameters.mockReturnValueOnce(pending.promise);
    const { execute, openSql } = await mount();

    expect(button("Execute").disabled).toBe(true);
    expect(button("Open SQL").disabled).toBe(true);
    expect(preview().disabled).toBe(true);
    button("Execute").dispatchEvent(new MouseEvent("click", { bubbles: true }));
    button("Open SQL").dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(execute).not.toHaveBeenCalled();
    expect(openSql).not.toHaveBeenCalled();

    pending.resolve([]);
    await vi.waitFor(() => expect(button("Execute").disabled).toBe(false));
    expect(button("Open SQL").disabled).toBe(false);
    button("Execute").click();
    expect(execute).toHaveBeenCalledExactlyOnceWith("CALL `master_p9_dq`();");
  });

  it("requires IN and INOUT values, accepts a date, and binds OUT without asking for input", async () => {
    mocks.loadRoutineParameters.mockResolvedValueOnce([
      { name: "SDATE", dataType: "date", mode: "IN", ordinal: 1 },
      { name: "p_count", dataType: "int", mode: "INOUT", ordinal: 2 },
      { name: "p_status", dataType: "varchar(20)", mode: "OUT", ordinal: 3 },
    ]);
    const { execute, state } = await mount();
    await vi.waitFor(() => expect(valueInput("SDATE")).toBeDefined());

    expect(document.querySelector("[role='alert']")?.textContent).toContain("SDATE, p_count");
    expect(valueInput("p_status").disabled).toBe(true);
    expect(valueInput("SDATE").getAttribute("aria-invalid")).toBe("true");
    button("Execute").dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(execute).not.toHaveBeenCalled();

    await setValue(valueInput("SDATE"), "2026-10-13");
    expect(button("Execute").disabled).toBe(true);
    await setValue(valueInput("p_count"), "3");
    expect(button("Execute").disabled).toBe(false);
    expect(document.querySelector("[role='alert']")).toBeNull();
    const executeButton = button("Execute");
    executeButton.click();
    expect(execute).toHaveBeenCalledExactlyOnceWith(["SET @dbx_output_2 = 3;", "SET @dbx_output_3 = NULL;", "CALL `master_p9_dq`('2026-10-13', @dbx_output_2, @dbx_output_3);", "SELECT @dbx_output_2 AS `p_count`, @dbx_output_3 AS `p_status`;"].join("\n"));
    expect(state.open).toBe(false);
    executeButton.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(execute).toHaveBeenCalledOnce();
  });

  it("does not treat whitespace as a value and requires an explicit NULL selection", async () => {
    mocks.loadRoutineParameters.mockResolvedValueOnce([{ name: "SDATE", dataType: "date", mode: "IN", ordinal: 1 }]);
    const { execute } = await mount();
    await vi.waitFor(() => expect(valueInput("SDATE")).toBeDefined());
    await setValue(valueInput("SDATE"), "   ");
    expect(button("Execute").disabled).toBe(true);
    await checkParameter("SDATE", "NULL");
    expect(button("Execute").disabled).toBe(false);
    button("Execute").click();
    expect(execute).toHaveBeenCalledExactlyOnceWith("CALL `master_p9_dq`(NULL);");
  });

  it("accepts a declared default without requiring a value, and validates again if it is unchecked", async () => {
    mocks.loadRoutineParameters.mockResolvedValueOnce([{ name: "p_scope", dataType: "text", mode: "IN", ordinal: 1, hasDefault: true }]);
    const { openSql, execute } = await mount("postgres");
    await vi.waitFor(() => expect(valueInput("p_scope")).toBeDefined());
    expect(button("Execute").disabled).toBe(false);
    await checkParameter("p_scope", "DEFAULT", false);
    expect(button("Execute").disabled).toBe(true);
    await checkParameter("p_scope", "DEFAULT");
    button("Open SQL").click();
    expect(openSql).toHaveBeenCalledExactlyOnceWith('CALL "mes"."master_p9_dq"();');
    expect(execute).not.toHaveBeenCalled();
  });

  it("requires a real manual SQL change after metadata failure and preserves an explicit empty string", async () => {
    mocks.loadRoutineParameters.mockRejectedValueOnce(new Error("metadata access denied"));
    const { execute } = await mount();
    await vi.waitFor(() => expect(document.querySelector("[role='dialog']")?.textContent).toContain("Edit SQL to provide arguments manually"));
    expect(button("Execute").disabled).toBe(true);
    expect(button("Open SQL").disabled).toBe(true);
    await setValue(preview(), "  CALL `master_p9_dq`();  ");
    expect(button("Execute").disabled).toBe(true);
    button("Execute").dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(execute).not.toHaveBeenCalled();
    await setValue(preview(), "CALL `master_p9_dq`('');");
    expect(button("Execute").disabled).toBe(false);
    button("Execute").click();
    expect(execute).toHaveBeenCalledExactlyOnceWith("CALL `master_p9_dq`('');");
  });

  it("returns to parameter validation after resetting a manually edited preview", async () => {
    mocks.loadRoutineParameters.mockResolvedValueOnce([{ name: "p_text", dataType: "text", mode: "IN", ordinal: 1 }]);
    const { execute } = await mount();
    await vi.waitFor(() => expect(valueInput("p_text")).toBeDefined());
    expect(button("Execute").disabled).toBe(true);
    await setValue(preview(), "CALL `master_p9_dq`('');");
    expect(button("Execute").disabled).toBe(false);
    button("Reset SQL").click();
    await nextTick();
    expect(button("Execute").disabled).toBe(true);
    expect(document.querySelector("[role='alert']")?.textContent).toContain("p_text");
    expect(execute).not.toHaveBeenCalled();
  });

  it("keeps whitespace-only preview edits generated when parameters change", async () => {
    mocks.loadRoutineParameters.mockResolvedValueOnce([
      { name: "SDATE", dataType: "date", mode: "IN", ordinal: 1 },
      { name: "p_text", dataType: "text", mode: "IN", ordinal: 2 },
    ]);
    const { execute } = await mount();
    await vi.waitFor(() => expect(valueInput("SDATE")).toBeDefined());
    await setValue(preview(), `  ${preview().value}  `);
    expect(button("Execute").disabled).toBe(true);

    await setValue(valueInput("SDATE"), "2026-10-13");
    expect(preview().value).toBe("CALL `master_p9_dq`('2026-10-13', NULL);");
    expect(button("Execute").disabled).toBe(true);
    expect(document.querySelector("[role='alert']")?.textContent).toContain("p_text");
    button("Execute").dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(execute).not.toHaveBeenCalled();

    await setValue(valueInput("p_text"), "ready");
    expect(preview().value).toBe("CALL `master_p9_dq`('2026-10-13', 'ready');");
    expect(button("Execute").disabled).toBe(false);
    button("Execute").click();
    expect(execute).toHaveBeenCalledExactlyOnceWith("CALL `master_p9_dq`('2026-10-13', 'ready');");
  });

  it("discards closed-dialog metadata when reopened and never executes on cancel", async () => {
    const old = deferred<RoutineParameter[]>();
    const current = deferred<RoutineParameter[]>();
    mocks.loadRoutineParameters.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    const { execute, openSql, state } = await mount();
    button("Cancel").click();
    await nextTick();
    expect(state.open).toBe(false);
    state.open = true;
    await nextTick();
    await vi.waitFor(() => expect(mocks.loadRoutineParameters).toHaveBeenCalledTimes(2));
    old.resolve([{ name: "old_parameter", dataType: "int", mode: "IN", ordinal: 1 }]);
    await old.promise;
    await nextTick();
    expect(document.querySelector("[data-routine-parameter='old_parameter']")).toBeNull();
    expect(button("Execute").disabled).toBe(true);
    current.resolve([{ name: "SDATE", dataType: "date", mode: "IN", ordinal: 1 }]);
    await vi.waitFor(() => expect(valueInput("SDATE")).toBeDefined());
    expect(execute).not.toHaveBeenCalled();
    expect(openSql).not.toHaveBeenCalled();
  });

  it("invalidates the old target when the connection and routine change", async () => {
    const old = deferred<RoutineParameter[]>();
    const current = deferred<RoutineParameter[]>();
    mocks.loadRoutineParameters.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise);
    const { execute, state } = await mount();
    state.connectionId = "new-connection";
    state.database = "reporting";
    state.schema = "reporting";
    state.routineName = "refresh_report";
    await nextTick();
    await vi.waitFor(() => expect(mocks.loadRoutineParameters).toHaveBeenCalledTimes(2));
    expect(mocks.loadRoutineParameters.mock.calls[1][0]).toMatchObject({ connectionId: "new-connection", database: "reporting", schema: "reporting", routineName: "refresh_report" });
    current.resolve([]);
    await vi.waitFor(() => expect(button("Execute").disabled).toBe(false));
    old.reject(new Error("old target error"));
    await old.promise.catch(() => undefined);
    await nextTick();
    expect(preview().value).toBe("CALL `refresh_report`();");
    expect(button("Execute").disabled).toBe(false);
    expect(document.querySelector("[role='dialog']")?.textContent).not.toContain("Edit SQL to provide arguments manually");
    button("Execute").click();
    expect(execute).toHaveBeenCalledExactlyOnceWith("CALL `refresh_report`();");
  });

  it("clears a manual SQL draft before loading a newly selected routine", async () => {
    const pending = deferred<RoutineParameter[]>();
    mocks.loadRoutineParameters.mockResolvedValueOnce([{ name: "p_text", dataType: "text", mode: "IN", ordinal: 1 }]).mockReturnValueOnce(pending.promise);
    const { execute, state } = await mount();
    await vi.waitFor(() => expect(valueInput("p_text")).toBeDefined());
    await setValue(preview(), "CALL `master_p9_dq`('');");
    expect(button("Execute").disabled).toBe(false);
    const oldExecuteButton = button("Execute");

    state.routineName = "refresh_report";
    await nextTick();
    expect(button("Execute").disabled).toBe(true);
    oldExecuteButton.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(execute).not.toHaveBeenCalled();
    expect(preview().value).toBe("CALL `refresh_report`();");
    pending.resolve([{ name: "p_report_id", dataType: "int", mode: "IN", ordinal: 1 }]);
    await vi.waitFor(() => expect(valueInput("p_report_id")).toBeDefined());
    expect(button("Execute").disabled).toBe(true);
    await setValue(valueInput("p_report_id"), "7");
    button("Execute").click();
    expect(execute).toHaveBeenCalledExactlyOnceWith("CALL `refresh_report`(7);");
  });
});
