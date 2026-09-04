export type DurationUnit = "seconds" | "minutes" | "hours" | "days";

const MAX_JOIN_TOKEN_TTL_SECONDS = 9_223_372_036n;
const INT32_MAX = 2_147_483_647n;
const UNIT_SECONDS: Record<DurationUnit, bigint> = {
  seconds: 1n,
  minutes: 60n,
  hours: 3_600n,
  days: 86_400n,
};

export function parseJoinTokenParameters(
  duration: string,
  unit: DurationUnit,
  uses: string,
): { ttlSeconds: bigint; uses: number } | null {
  if (!/^[0-9]+$/.test(duration) || !/^[0-9]+$/.test(uses)) {
    return null;
  }

  const ttlSeconds = BigInt(duration) * UNIT_SECONDS[unit];
  const usesValue = BigInt(uses);
  if (
    ttlSeconds <= 0n ||
    ttlSeconds > MAX_JOIN_TOKEN_TTL_SECONDS ||
    usesValue <= 0n ||
    usesValue > INT32_MAX
  ) {
    return null;
  }

  return { ttlSeconds, uses: Number(usesValue) };
}

function shellQuote(value: string, always = false): string {
  if (!always && /^[A-Za-z0-9_@%+=:,./-]+$/.test(value)) {
    return value;
  }
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

function formatFlagValue(flag: string, value: string, alwaysQuote = false): string {
  const separator = value.startsWith("-") ? "=" : " ";
  return `${flag}${separator}${shellQuote(value, alwaysQuote || value.startsWith("-"))}`;
}

export function buildJoinCommand(seedAddress: string, token: string): string {
  return `procmesh agent join ${formatFlagValue("--seed", seedAddress)} ${formatFlagValue("--token", token, true)}`;
}

export function buildCustomServerJoinTemplate(seedAddress: string): string {
  return `procmesh --server <NEW_AGENT_API> agent join ${formatFlagValue("--seed", seedAddress)} --token '<JOIN_TOKEN>'`;
}
