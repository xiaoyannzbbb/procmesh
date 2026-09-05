import { flushPromises, mount } from "@vue/test-utils";
import { Code, ConnectError } from "@connectrpc/connect";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ErrorInfoSchema } from "../gen/procmesh/v1/errors_pb";
import { session } from "../lib/session";
import type { NodeView } from "./clusterView";
import AddNodeDrawer from "./AddNodeDrawer.vue";

const translations = {
  nodes: {
    add: {
      title: "Add node",
      close: "Close",
      intro: "Run this command on the new node.",
      seed: "Seed node",
      selectSeed: "Select a seed node",
      noSeeds: "No eligible seed nodes are available.",
      nodesLoading: "Loading seed nodes...",
      nodesFailed: "Could not load seed nodes: {{detail}}",
      cachedWarning: "Refresh failed. Candidates may be outdated: {{detail}}",
      refresh: "Retry",
      freshnessWarning: "{{freshness}} data is not live.",
      duration: "Valid for",
      durationHint: "Choose a positive whole number.",
      unit: "Duration unit",
      units: { seconds: "seconds", minutes: "minutes", hours: "hours", days: "days" },
      uses: "Maximum uses",
      usesHint: "Choose a positive whole number.",
      invalidDuration: "Enter a positive whole duration within the supported range.",
      invalidUses: "Enter a positive whole number no greater than 2147483647.",
      generate: "Generate join command",
      regenerate: "Generate a new token",
      generating: "Generating...",
      permissionLost: "You no longer have permission to create join tokens. Code: {{code}}.",
      createFailed: "Could not create the join token: {{detail}}",
    createTimeout: "The request timed out and may have issued a valid token. Retrying creates another token, so an additional valid token may remain.",
      tokenId: "Token ID",
      expires: "Expires",
      remainingUses: "Remaining uses",
      secretWarning: "This token is shown only once. Store it securely before closing.",
      executeOnNewNode: "Run on the new node",
      commandLabel: "Join command",
      copy: "Copy command",
      copied: "Command copied",
      copyFailed: "Copy failed. Select the command and copy it manually.",
      customServerTitle: "Non-default new-node API",
      customServerCommand: "procmesh --server <NEW_AGENT_API> agent join --seed {{seed}} --token '<JOIN_TOKEN>'",
      customServerHint: "--server is the new node API; --seed is the existing cluster node API.",
      parametersChanged: "These settings differ from the issued token. The old token may still be valid; generate a new token to apply them.",
      seedInvalid: "The selected seed is no longer eligible. Select another seed before copying.",
      closeTitle: "Close and lose the token?",
      closeMessage: "The plaintext token cannot be viewed again, but the issued token remains valid.",
      closePendingMessage: "The request may still complete and its token cannot be recovered after closing.",
      closeConfirm: "Close drawer",
      cancel: "Keep open",
    },
  },
  status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
};

let i18n: typeof i18next;

function node(overrides: Partial<NodeView>): NodeView {
  return {
    nodeId: "n-a",
    hostname: "agent-a",
    state: "ALIVE",
    apiAddress: "10.0.0.11:18680",
    freshness: "LIVE",
    raftRole: "LEADER",
    raftRoleFreshness: "LIVE",
    agentVersion: "",
    bootId: "",
    rpcAddress: "",
    gossipAddress: "",
    labels: [],
    resources: {
      cpuPercent: 0,
      memoryPercent: 0,
      diskPercent: 0,
      historyWritesPaused: false,
      historyPausePercent: 0,
    },
    processCount: 0,
    processes: [],
    lastUpdatedUnixMs: Date.now(),
    lastUpdated: "now",
    disableRemoteCreate: false,
    disableRemoteUpdate: false,
    disableRemoteDelete: false,
    ...overrides,
  };
}

async function mountDrawer(options: {
  nodes?: NodeView[];
  createJoinToken?: ReturnType<typeof vi.fn>;
  canManage?: boolean;
  nodesError?: string;
}) {
  const createJoinToken = options.createJoinToken ?? vi.fn();
  const wrapper = mount(AddNodeDrawer, {
    attachTo: document.body,
    props: {
      open: true,
      nodes: options.nodes ?? [node({})],
      nodesLoading: false,
      nodesError: options.nodesError ?? "",
      canManage: options.canManage ?? true,
    },
    global: {
      plugins: [[I18NextVue, { i18next: i18n }]],
      provide: { nodeClient: { createJoinToken } },
    },
  });
  await flushPromises();
  return { wrapper, createJoinToken };
}

function bodyButton(text: string): HTMLButtonElement {
  const button = Array.from(document.body.querySelectorAll("button")).find(
    (candidate) => candidate.textContent?.trim() === text,
  );
  if (!(button instanceof HTMLButtonElement)) throw new Error(`Missing button: ${text}`);
  return button;
}

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({ lng: "en", resources: { en: { common: translations } } });
  session.value = {
    userId: "u-1",
    username: "admin",
    csrfToken: "csrf",
    permissions: ["node.manage"],
  };
});

afterEach(() => {
  document.body.innerHTML = "";
  document.body.style.overflow = "";
  session.value = null;
  vi.restoreAllMocks();
});

describe("AddNodeDrawer", () => {
  it("distinguishes a node loading failure from an empty eligible list", async () => {
    const { wrapper } = await mountDrawer({ nodes: [], nodesError: "UNAVAILABLE" });

    expect(document.body.textContent).toContain("Could not load seed nodes: UNAVAILABLE");
    expect(document.body.textContent).not.toContain("No eligible seed nodes are available.");
    bodyButton("Retry").click();
    expect(wrapper.emitted("refresh")).toHaveLength(1);
  });

  it("marks cached candidates as potentially outdated after a refresh failure", async () => {
    await mountDrawer({ nodes: [node({})], nodesError: "TIMEOUT" });

    expect(document.body.textContent).toContain("Candidates may be outdated: TIMEOUT");
    expect(document.body.querySelectorAll("select[name=seed] option")).toHaveLength(2);
  });

  it("only lists ALIVE nodes with API addresses and preserves stale labels", async () => {
    await mountDrawer({
      nodes: [
        node({ nodeId: "live", hostname: "live", freshness: "LIVE" }),
        node({ nodeId: "stale", hostname: "stale", apiAddress: "10.0.0.12:18680", freshness: "STALE" }),
        node({ nodeId: "failed", hostname: "failed", state: "FAILED" }),
        node({ nodeId: "missing", hostname: "missing", apiAddress: "" }),
      ],
    });

    const options = Array.from(document.body.querySelectorAll("select[name=seed] option"));
    expect(options).toHaveLength(3);
    expect(options.map((option) => option.textContent)).toEqual([
      "Select a seed node",
      expect.stringContaining("live"),
      expect.stringContaining("stale"),
    ]);
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "stale";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    expect(document.body.textContent).toContain("STALE data is not live.");
  });

  it("submits one request with normalized values and an operation ID", async () => {
    let resolve!: (value: unknown) => void;
    const createJoinToken = vi.fn().mockImplementation(
      () => new Promise((done) => { resolve = done; }),
    );
    await mountDrawer({ createJoinToken });

    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    const generate = bodyButton("Generate join command");
    generate.click();
    generate.click();
    await flushPromises();

    expect(createJoinToken).toHaveBeenCalledTimes(1);
    expect(createJoinToken).toHaveBeenCalledWith({
      meta: { operationId: expect.any(String), operator: "admin" },
      ttlSeconds: 3600n,
      uses: 1,
    });
    resolve({ tokenId: "jt-1", token: "pmj_example", expiresUnix: 1_800_000_000n, uses: 1 });
    await flushPromises();
    expect(document.body.textContent).toContain("jt-1");
  });

  it("uses a new operation ID and warns when retrying an uncertain timeout", async () => {
    const timeout = new ConnectError("join token result unknown", Code.DeadlineExceeded, undefined, [
      {
        desc: ErrorInfoSchema,
        value: { code: "TIMEOUT", message: "TIMEOUT: join token result unknown" },
      },
    ]);
    const createJoinToken = vi
      .fn()
      .mockRejectedValueOnce(timeout)
      .mockResolvedValueOnce({ tokenId: "jt-1", token: "pmj_example", expiresUnix: 1_800_000_000n, uses: 1 });
    await mountDrawer({ createJoinToken });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();

    bodyButton("Generate join command").click();
    await flushPromises();
    expect(document.body.textContent).toContain("The request timed out and may have issued a valid token");
    bodyButton("Generate join command").click();
    await flushPromises();

    expect(createJoinToken).toHaveBeenCalledTimes(2);
    expect(createJoinToken.mock.calls[1][0].meta.operationId).not.toBe(
      createJoinToken.mock.calls[0][0].meta.operationId,
    );
  });

  it("shows a whitelist-safe stable code when permission is revoked", async () => {
    const createJoinToken = vi
      .fn()
      .mockRejectedValue(new ConnectError("private policy evaluation detail", Code.PermissionDenied));
    await mountDrawer({ createJoinToken });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();

    bodyButton("Generate join command").click();
    await flushPromises();

    expect(document.body.textContent).toContain("Code: DENIED");
    expect(document.body.textContent).not.toContain("private policy evaluation detail");
  });

  it("updates only the command when the seed changes after creation", async () => {
    const createJoinToken = vi.fn().mockResolvedValue({
      tokenId: "jt-1",
      token: "pmj_example",
      expiresUnix: 1_800_000_000n,
      uses: 2,
    });
    await mountDrawer({
      createJoinToken,
      nodes: [node({}), node({ nodeId: "n-b", hostname: "agent-b", apiAddress: "10.0.0.12:18680" })],
    });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();
    expect(document.body.querySelector("code")?.textContent).toContain("10.0.0.11:18680");

    seed.value = "n-b";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    expect(document.body.querySelector("code")?.textContent).toContain("10.0.0.12:18680");
    expect(createJoinToken).toHaveBeenCalledTimes(1);
  });

  it("does not submit invalid values and associates errors with fields", async () => {
    const { createJoinToken } = await mountDrawer({});
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    const uses = document.body.querySelector<HTMLInputElement>("input[name=uses]")!;
    uses.value = "1.5";
    uses.dispatchEvent(new Event("input", { bubbles: true }));
    document.body.querySelector<HTMLFormElement>("form")!.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    );
    await flushPromises();

    expect(createJoinToken).not.toHaveBeenCalled();
    expect(uses.getAttribute("aria-invalid")).toBe("true");
    expect(uses.getAttribute("aria-describedby")).toBeTruthy();
  });

  it("keeps the command selectable and announces a clipboard failure", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    await mountDrawer({
      createJoinToken: vi.fn().mockResolvedValue({
        tokenId: "jt-1",
        token: "pmj_example",
        expiresUnix: 1_800_000_000n,
        uses: 1,
      }),
    });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();
    bodyButton("Copy command").click();
    await flushPromises();

    expect(document.body.textContent).toContain("Copy failed");
    expect(document.body.querySelector("code")?.textContent).toContain("pmj_example");
  });

  it("copies the complete current command exactly", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    await mountDrawer({
      createJoinToken: vi.fn().mockResolvedValue({
        tokenId: "jt-1",
        token: "pmj_example",
        expiresUnix: 1_800_000_000n,
        uses: 1,
      }),
    });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();
    bodyButton("Copy command").click();
    await flushPromises();

    expect(writeText).toHaveBeenCalledWith(
      "procmesh agent join --seed 10.0.0.11:18680 --token 'pmj_example'",
    );
    expect(document.body.textContent).toContain("Command copied");
  });

  it("uses a new operation ID for an explicit regeneration", async () => {
    const createJoinToken = vi
      .fn()
      .mockResolvedValueOnce({ tokenId: "jt-1", token: "pmj_one", expiresUnix: 1_800_000_000n, uses: 1 })
      .mockResolvedValueOnce({ tokenId: "jt-2", token: "pmj_two", expiresUnix: 1_800_000_100n, uses: 2 });
    await mountDrawer({ createJoinToken });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();

    const uses = document.body.querySelector<HTMLInputElement>("input[name=uses]")!;
    uses.value = "2";
    uses.dispatchEvent(new Event("input", { bubbles: true }));
    await flushPromises();
    expect(document.body.textContent).toContain("old token may still be valid");
    bodyButton("Generate a new token").click();
    await flushPromises();

    expect(createJoinToken).toHaveBeenCalledTimes(2);
    expect(createJoinToken.mock.calls[1][0].meta.operationId).not.toBe(
      createJoinToken.mock.calls[0][0].meta.operationId,
    );
    expect(createJoinToken.mock.calls[1][0].uses).toBe(2);
  });

  it("blocks copying when the selected seed loses eligibility", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const { wrapper } = await mountDrawer({
      createJoinToken: vi.fn().mockResolvedValue({
        tokenId: "jt-1",
        token: "pmj_example",
        expiresUnix: 1_800_000_000n,
        uses: 1,
      }),
    });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();

    await wrapper.setProps({ nodes: [node({ state: "FAILED" })] });
    await flushPromises();
    expect(document.body.textContent).toContain("no longer eligible");
    const copy = bodyButton("Copy command");
    expect(copy.disabled).toBe(true);
    copy.click();
    expect(writeText).not.toHaveBeenCalled();
  });

  it("requires confirmation before clearing a generated plaintext token", async () => {
    const { wrapper } = await mountDrawer({
      createJoinToken: vi.fn().mockResolvedValue({
        tokenId: "jt-1",
        token: "pmj_example",
        expiresUnix: 1_800_000_000n,
        uses: 1,
      }),
    });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();
    document.body.querySelector<HTMLButtonElement>(".drawer-close")!.click();
    await flushPromises();

    expect(document.body.textContent).toContain("Close and lose the token?");
    expect(wrapper.emitted("close")).toBeUndefined();
    bodyButton("Close drawer").click();
    await flushPromises();
    expect(wrapper.emitted("close")).toHaveLength(1);
    expect(document.body.textContent).not.toContain("pmj_example");
  });

  it("ignores a token response from a closed drawer lifecycle", async () => {
    let resolve!: (value: unknown) => void;
    const createJoinToken = vi.fn().mockImplementation(
      () => new Promise((done) => { resolve = done; }),
    );
    const { wrapper } = await mountDrawer({ createJoinToken });
    const seed = document.body.querySelector<HTMLSelectElement>("select[name=seed]")!;
    seed.value = "n-a";
    seed.dispatchEvent(new Event("change", { bubbles: true }));
    await flushPromises();
    bodyButton("Generate join command").click();
    await flushPromises();

    document.body.querySelector<HTMLButtonElement>(".drawer-close")!.click();
    await flushPromises();
    expect(document.body.textContent).toContain("The request may still complete");
    bodyButton("Close drawer").click();
    await wrapper.setProps({ open: false });
    resolve({ tokenId: "jt-late", token: "pmj_late", expiresUnix: 1_800_000_000n, uses: 1 });
    await flushPromises();

    await wrapper.setProps({ open: true });
    await flushPromises();
    expect(document.body.textContent).not.toContain("jt-late");
    expect(document.body.textContent).not.toContain("pmj_late");
  });
});
