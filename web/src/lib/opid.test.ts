import { afterEach, describe, expect, it, vi } from "vitest";
import { newOperationId } from "./opid";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("newOperationId", () => {
  it("uses crypto.randomUUID when it is available", () => {
    const randomUUID = vi.fn(() => "123e4567-e89b-42d3-a456-426614174000");
    vi.stubGlobal("crypto", {
      randomUUID,
      getRandomValues: vi.fn(),
    });

    expect(newOperationId()).toBe("123e4567-e89b-42d3-a456-426614174000");
    expect(randomUUID).toHaveBeenCalledOnce();
  });

  it("generates a UUID v4 when randomUUID is unavailable", () => {
    vi.stubGlobal("crypto", {
      getRandomValues: (bytes: Uint8Array) => {
        bytes.set(Array.from({ length: 16 }, (_, index) => index));
        return bytes;
      },
    });

    expect(newOperationId()).toBe("00010203-0405-4607-8809-0a0b0c0d0e0f");
  });
});
