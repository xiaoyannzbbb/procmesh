import { describe, expect, it } from "vitest";
import { browserTimezone, listTimezones, timezoneLabel, timezonePickerOptions } from "./timezones";

describe("timezones", () => {
  it("lists available IANA zones including UTC", () => {
    const zones = listTimezones();
    expect(zones).toContain("UTC");
    expect(zones.length).toBeGreaterThan(1);
  });

  it("keeps browser, suggested, remaining, and current choices unique", () => {
    const current = "US/Eastern";
    const options = timezonePickerOptions(current);
    const choices = [options.browser, ...options.suggested, ...options.remaining];

    expect(choices).toContain(browserTimezone());
    expect(choices).toContain("UTC");
    expect(choices).toContain(current);
    expect(new Set(choices).size).toBe(choices.length);
  });

  it("labels zones with a UTC offset when Intl can resolve them", () => {
    const label = timezoneLabel("UTC");
    expect(label).toContain("UTC");
  });
});
