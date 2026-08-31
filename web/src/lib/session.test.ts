import { createClient } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CSRF_STORAGE_KEY } from "./csrf";
import { loadSession, selfUpdateHold, session, type Me } from "./session";

vi.mock("@connectrpc/connect", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@connectrpc/connect")>();
  return {
    ...actual,
    createClient: vi.fn(),
  };
});

const existing: Me = {
  userId: "u1",
  username: "admin",
  csrfToken: "csrf-hold",
  permissions: ["node.manage"],
};

beforeEach(() => {
  session.value = { ...existing };
  selfUpdateHold.value = false;
  sessionStorage.setItem(CSRF_STORAGE_KEY, existing.csrfToken);
});

afterEach(() => {
  session.value = null;
  selfUpdateHold.value = false;
  sessionStorage.removeItem(CSRF_STORAGE_KEY);
  vi.mocked(createClient).mockReset();
});

describe("loadSession", () => {
  it("does not clearSession while selfUpdateHold is true even if getMe fails", async () => {
    selfUpdateHold.value = true;
    vi.mocked(createClient).mockReturnValue({
      getMe: vi.fn().mockRejectedValue(new Error("offline")),
    } as never);

    const got = await loadSession();

    expect(got).toEqual(existing);
    expect(session.value).toEqual(existing);
    expect(sessionStorage.getItem(CSRF_STORAGE_KEY)).toBe(existing.csrfToken);
  });

  it("clears the session when getMe fails and hold is false", async () => {
    vi.mocked(createClient).mockReturnValue({
      getMe: vi.fn().mockRejectedValue(new Error("offline")),
    } as never);

    const got = await loadSession();

    expect(got).toBeNull();
    expect(session.value).toBeNull();
    expect(sessionStorage.getItem(CSRF_STORAGE_KEY)).toBeNull();
  });
});
