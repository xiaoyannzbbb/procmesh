const FALLBACK_TIMEZONES = [
  "UTC",
  "Africa/Cairo",
  "Africa/Johannesburg",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/New_York",
  "America/Sao_Paulo",
  "Asia/Dubai",
  "Asia/Hong_Kong",
  "Asia/Kolkata",
  "Asia/Seoul",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Europe/Berlin",
  "Europe/London",
  "Europe/Moscow",
  "Europe/Paris",
  "Pacific/Auckland",
];

const SUGGESTED_TIMEZONES = [
  "UTC",
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Asia/Singapore",
  "Asia/Hong_Kong",
  "Europe/London",
  "Europe/Paris",
  "America/New_York",
  "America/Los_Angeles",
  "America/Chicago",
  "Australia/Sydney",
];

function unique(values: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const value of values) {
    const zone = value.trim();
    if (!zone || seen.has(zone)) {
      continue;
    }
    seen.add(zone);
    out.push(zone);
  }
  return out;
}

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function listTimezones(): string[] {
  const supported = (Intl as { supportedValuesOf?: (key: string) => string[] }).supportedValuesOf;
  let zones = FALLBACK_TIMEZONES;
  if (typeof supported === "function") {
    try {
      const resolved = supported.call(Intl, "timeZone");
      if (Array.isArray(resolved) && resolved.length) {
        zones = resolved;
      }
    } catch {
      // fall through to the curated list
    }
  }
  return unique(["UTC", ...zones]);
}

export function timezoneLabel(zone: string, at = new Date()): string {
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "shortOffset",
    }).formatToParts(at);
    const offset = parts.find((part) => part.type === "timeZoneName")?.value;
    return offset ? `${zone} (${offset})` : zone;
  } catch {
    return zone;
  }
}

export function timezonePickerOptions(current?: string): {
  browser: string;
  suggested: string[];
  remaining: string[];
} {
  const browser = browserTimezone();
  const available = unique([browser, current ?? "", ...listTimezones()]);
  const known = new Set(available);
  const suggested = SUGGESTED_TIMEZONES.filter((zone) => zone !== browser && known.has(zone));
  const promoted = new Set([browser, ...suggested]);
  const remaining = available.filter((zone) => !promoted.has(zone));
  return { browser, suggested, remaining };
}
