import { classify, LIVE } from "./freshness";

/** Version helpers aligned with Go internal/update.IsBehind / AnyLiveLinuxBehind. */

export type VersionMember = {
  state?: string;
  os?: string;
  agentVersion?: string;
  lastUpdatedUnixMs?: number;
};

/** Strip a single leading v/V (Go update.StripV). */
export function stripV(s: string): string {
  if (s.length > 0 && (s[0] === "v" || s[0] === "V")) {
    return s.slice(1);
  }
  return s;
}

function canonicalSemver(s: string): string {
  const trimmed = s.trim();
  if (!trimmed) {
    return "";
  }
  if (trimmed[0] === "v" || trimmed[0] === "V") {
    return "v" + trimmed.slice(1);
  }
  return "v" + trimmed;
}

/** Non-negative integer without leading zeros (except "0"), matching golang.org/x/mod/semver. */
function isNumNoLeadingZero(s: string): boolean {
  if (!s) {
    return false;
  }
  if (s === "0") {
    return true;
  }
  if (s[0] === "0") {
    return false;
  }
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 48 || c > 57) {
      return false;
    }
  }
  return true;
}

function isAlphanumHyphen(s: string): boolean {
  if (!s) {
    return false;
  }
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    const ok =
      (c >= 48 && c <= 57) ||
      (c >= 65 && c <= 90) ||
      (c >= 97 && c <= 122) ||
      c === 45;
    if (!ok) {
      return false;
    }
  }
  return true;
}

function isPrereleaseIdent(s: string): boolean {
  if (!isAlphanumHyphen(s)) {
    return false;
  }
  // Numeric identifiers must not have leading zeros.
  let allDigit = true;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 48 || c > 57) {
      allDigit = false;
      break;
    }
  }
  if (allDigit) {
    return isNumNoLeadingZero(s);
  }
  return true;
}

function isBuildIdent(s: string): boolean {
  return isAlphanumHyphen(s);
}

/**
 * golang.org/x/mod/semver.IsValid: vMAJOR[.MINOR[.PATCH[-PRERELEASE][+BUILD]]]
 */
export function isValidSemver(v: string): boolean {
  if (!v || v[0] !== "v" || v.length === 1) {
    return false;
  }
  let i = 1;
  const n = v.length;

  const readNum = (): boolean => {
    const start = i;
    while (i < n) {
      const c = v.charCodeAt(i);
      if (c < 48 || c > 57) {
        break;
      }
      i++;
    }
    if (i === start) {
      return false;
    }
    return isNumNoLeadingZero(v.slice(start, i));
  };

  if (!readNum()) {
    return false;
  }

  const readDotNum = (): boolean => {
    if (i >= n || v[i] !== ".") {
      return false;
    }
    i++;
    return readNum();
  };

  // optional .MINOR
  if (i < n && v[i] === ".") {
    if (!readDotNum()) {
      return false;
    }
    // optional .PATCH
    if (i < n && v[i] === ".") {
      if (!readDotNum()) {
        return false;
      }
    }
  }

  // optional -PRERELEASE
  if (i < n && v[i] === "-") {
    i++;
    let first = true;
    while (i < n && v[i] !== "+") {
      if (!first) {
        if (v[i] !== ".") {
          return false;
        }
        i++;
      }
      first = false;
      const start = i;
      while (i < n && v[i] !== "." && v[i] !== "+") {
        i++;
      }
      if (!isPrereleaseIdent(v.slice(start, i))) {
        return false;
      }
    }
    if (first) {
      return false;
    }
  }

  // optional +BUILD
  if (i < n && v[i] === "+") {
    i++;
    let first = true;
    while (i < n) {
      if (!first) {
        if (v[i] !== ".") {
          return false;
        }
        i++;
      }
      first = false;
      const start = i;
      while (i < n && v[i] !== ".") {
        i++;
      }
      if (!isBuildIdent(v.slice(start, i))) {
        return false;
      }
    }
    if (first) {
      return false;
    }
  }

  return i === n;
}

type Parsed = {
  major: number;
  minor: number;
  patch: number;
  prerelease: string[]; // empty = release
};

function parseCanonical(v: string): Parsed | null {
  if (!isValidSemver(v)) {
    return null;
  }
  // strip leading v
  let rest = v.slice(1);
  let buildIdx = rest.indexOf("+");
  if (buildIdx >= 0) {
    rest = rest.slice(0, buildIdx);
  }
  let preIdx = rest.indexOf("-");
  let core = rest;
  let pre = "";
  if (preIdx >= 0) {
    core = rest.slice(0, preIdx);
    pre = rest.slice(preIdx + 1);
  }
  const parts = core.split(".");
  const major = Number(parts[0] ?? 0);
  const minor = Number(parts[1] ?? 0);
  const patch = Number(parts[2] ?? 0);
  const prerelease = pre ? pre.split(".") : [];
  return { major, minor, patch, prerelease };
}

function comparePreIdent(a: string, b: string): number {
  const aNum = /^\d+$/.test(a);
  const bNum = /^\d+$/.test(b);
  if (aNum && bNum) {
    const an = Number(a);
    const bn = Number(b);
    return an === bn ? 0 : an < bn ? -1 : 1;
  }
  if (aNum) {
    return -1;
  }
  if (bNum) {
    return 1;
  }
  return a === b ? 0 : a < b ? -1 : 1;
}

/** Compare two canonical semver strings; both must be valid. */
export function compareSemver(a: string, b: string): number {
  const pa = parseCanonical(a);
  const pb = parseCanonical(b);
  if (!pa || !pb) {
    // Invalid versions: match Go — invalid < valid; invalids equal.
    if (!pa && !pb) {
      return 0;
    }
    return !pa ? -1 : 1;
  }
  if (pa.major !== pb.major) {
    return pa.major < pb.major ? -1 : 1;
  }
  if (pa.minor !== pb.minor) {
    return pa.minor < pb.minor ? -1 : 1;
  }
  if (pa.patch !== pb.patch) {
    return pa.patch < pb.patch ? -1 : 1;
  }
  const aPre = pa.prerelease;
  const bPre = pb.prerelease;
  if (aPre.length === 0 && bPre.length === 0) {
    return 0;
  }
  if (aPre.length === 0) {
    return 1;
  }
  if (bPre.length === 0) {
    return -1;
  }
  const n = Math.max(aPre.length, bPre.length);
  for (let i = 0; i < n; i++) {
    if (i >= aPre.length) {
      return -1;
    }
    if (i >= bPre.length) {
      return 1;
    }
    const c = comparePreIdent(aPre[i]!, bPre[i]!);
    if (c !== 0) {
      return c;
    }
  }
  return 0;
}

/**
 * Reports whether current should be treated as behind latest.
 * Semver tags (after stripping leading v) use semver order; otherwise inequality
 * of the stripped strings means "different"/behind. current >= latest → false.
 */
export function isBehind(current: string, latest: string): boolean {
  const cur = current.trim();
  const lat = latest.trim();
  if (cur === "" && lat === "") {
    return false;
  }
  const cv = canonicalSemver(cur);
  const lv = canonicalSemver(lat);
  if (isValidSemver(cv) && isValidSemver(lv)) {
    return compareSemver(cv, lv) < 0;
  }
  return stripV(cur) !== stripV(lat);
}

/**
 * True if any freshness-LIVE member with os==="linux" is behind latestTag.
 * LIVE is ALIVE and lastUpdated within MAX_AGE_MS. Empty os is not linux.
 */
export function anyLiveLinuxBehind(
  members: readonly VersionMember[],
  latestTag: string,
  nowMs: number,
): boolean {
  for (const m of members) {
    if (classify(nowMs, Number(m.lastUpdatedUnixMs ?? 0), m.state ?? "") !== LIVE) {
      continue;
    }
    if (m.os !== "linux") {
      continue;
    }
    if (isBehind(m.agentVersion ?? "", latestTag)) {
      return true;
    }
  }
  return false;
}
