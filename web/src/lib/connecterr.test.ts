import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { ErrorInfoSchema } from "../gen/procmesh/v1/errors_pb";
import { appCode, isConflict } from "./connecterr";

function conflictError(): ConnectError {
  return new ConnectError("revision mismatch", Code.FailedPrecondition, undefined, [
    { desc: ErrorInfoSchema, value: { code: "CONFLICT", message: "revision mismatch" } },
  ]);
}

describe("connecterr", () => {
  it("isConflict is true for ErrorInfo CONFLICT", () => {
    const err = conflictError();
    expect(appCode(err)).toBe("CONFLICT");
    expect(isConflict(err)).toBe(true);
  });

  it("isConflict is false without CONFLICT detail", () => {
    expect(isConflict(new Error("boom"))).toBe(false);
    expect(appCode(new ConnectError("denied", Code.PermissionDenied))).toBe("");
  });
});
