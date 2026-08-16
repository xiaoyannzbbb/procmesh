import { describe, it, expect } from "vitest";
import { i18nWithMethods } from "./i18n";

describe("i18n performance", () => {
  it("should translate keys quickly", () => {
    const start = performance.now();

    for (let i = 0; i < 1000; i++) {
      i18nWithMethods.t("app.name");
      i18nWithMethods.t("actions.start");
    }

    const elapsed = performance.now() - start;
    expect(elapsed).toBeLessThan(500);
  });

  it("should handle namespace operations without error", () => {
    expect(() => {
      i18nWithMethods.unloadNamespaces(["audit"]);
      i18nWithMethods.loadNamespaces(["common"]);
    }).not.toThrow();
  });
});
