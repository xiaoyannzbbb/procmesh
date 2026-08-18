import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { session } from "../lib/session";
import BackupPage from "./BackupPage.vue";

let i18n: typeof i18next;

const backupI18n = {
  title: "Backup",
  staleBanner: "Some backup sources are unreachable. This is not an empty catalog.",
  noBackups: "No backups",
  create: "Create backup",
  restore: "Restore",
  restoreConfirm: "Restore this snapshot?",
  owner: "Owner",
  expectedRevision: "Expected revision",
  sink: "Sink",
  delete: "Delete",
  loading: "Loading…",
  snapshotId: "Snapshot ID",
  node: "Node",
  processCount: "Processes",
  sha256: "SHA-256",
  freshness: "Freshness",
  lastUpdated: "Last updated",
  processIds: "Process IDs",
  processIdsPlaceholder: "Leave empty for all local processes",
  peerNodeIds: "Peer node IDs",
  peerNodeIdsPlaceholder: "One node ID per line",
  peerRequired: "Peer sink requires at least one node ID.",
  peerHint: "Specify extra peers with CLI --peer-node if they are not listed as ALIVE.",
  cancel: "Cancel",
  confirm: "Confirm restore",
  restoreConflict: "Restore conflict: {{detail}}",
  restoreFailed: "Restore failed: {{detail}}",
  restoreSuccess: "Restore completed",
  deleteConfirm: "Delete snapshot {{id}}?",
  process: "Process",
};

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          backup: backupI18n,
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
          actions: { cancel: "Cancel", confirm: "Confirm", delete: "Delete" },
        },
      },
    },
  });
});

const liveSnapshot = {
  snapshot: {
    snapshotId: "snap-live",
    clusterId: "c1",
    nodeId: "n1",
    createdUnixMs: BigInt(1_700_000_000_000),
    processIds: ["web"],
    revisionRanges: [{ processId: "web", minRevision: BigInt(1), maxRevision: BigInt(2) }],
    sha256: "abcdef0123456789deadbeef",
    sink: "fs",
    location: "/backup/fs/snap-live.json",
    sourceNodeId: "",
  },
  sourceNode: "n1",
  freshness: "LIVE",
  lastUpdatedUnixMs: BigInt(1_700_000_010_000),
};

const stalePlaceholder = {
  snapshot: undefined,
  sourceNode: "n2",
  freshness: "STALE",
  lastUpdatedUnixMs: BigInt(0),
};

const mounted: Array<{ unmount: () => void }> = [];

async function mountBackup(
  opts: {
    permissions?: string[];
    entries?: unknown[];
    restoreResults?: { processId: string; status: string; newRevision?: bigint; error?: string }[];
    listNodes?: unknown[];
    listNodesError?: Error;
    getProcess?: ReturnType<typeof vi.fn>;
  } = {},
) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: opts.permissions ?? ["backup.read", "backup.manage"],
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const entries = opts.entries ?? [liveSnapshot, stalePlaceholder];
  const backupClient = {
    listBackups: vi.fn().mockResolvedValue({ entries }),
    createBackup: vi.fn().mockResolvedValue({ snapshot: liveSnapshot.snapshot }),
    deleteBackup: vi.fn().mockResolvedValue({}),
    restoreBackup: vi.fn().mockResolvedValue({
      results: opts.restoreResults ?? [
        { processId: "web", status: "SUCCESS", newRevision: BigInt(3), error: "" },
      ],
    }),
    getBackup: vi.fn(),
  };
  const nodeClient = {
    listNodes: opts.listNodesError
      ? vi.fn().mockRejectedValue(opts.listNodesError)
      : vi.fn().mockResolvedValue({
          nodes: opts.listNodes ?? [
            { nodeId: "n1", state: "ALIVE" },
            { nodeId: "n2", state: "FAILED" },
            { nodeId: "n3", state: "ALIVE" },
          ],
        }),
    getNode: vi.fn(),
    removeNode: vi.fn(),
  };
  const processClient = {
    listProcesses: vi.fn(),
    getProcess:
      opts.getProcess ??
      vi.fn().mockResolvedValue({
        process: { spec: { latestRevision: 2n } },
      }),
    startProcess: vi.fn(),
    stopProcess: vi.fn(),
    restartProcess: vi.fn(),
    killProcess: vi.fn(),
  };
  const wrapper = mount(BackupPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { backupClient, nodeClient, processClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, backupClient, nodeClient, processClient, queryClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("BackupPage", () => {
  it("shows STALE badge and banner, not empty catalog, for LIVE snapshot plus STALE placeholder", async () => {
    const { wrapper } = await mountBackup();
    const text = wrapper.text();
    expect(text).toContain("Some backup sources are unreachable. This is not an empty catalog.");
    expect(wrapper.find(".freshness-stale").exists()).toBe(true);
    expect(text).not.toContain("No backups");
    const staleRow = wrapper.get('[data-freshness="STALE"]');
    const html = staleRow.html().toLowerCase();
    const style = (staleRow.attributes("style") ?? "").toLowerCase();
    expect(html).not.toMatch(/#d1fae5|rgb\(209,\s*250,\s*229\)/);
    expect(style).not.toMatch(/#d1fae5|rgb\(209,\s*250,\s*229\)/);
    const el = staleRow.element as HTMLElement;
    const bg = (getComputedStyle(el).backgroundColor || el.style.backgroundColor).toLowerCase();
    expect(bg).not.toMatch(/rgb\(209,\s*250,\s*229\)|#d1fae5/);
  });

  it("hides create, restore, and delete without backup.manage", async () => {
    const { wrapper } = await mountBackup({ permissions: ["backup.read"] });
    expect(wrapper.find('[data-action="create"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="restore"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="delete"]').exists()).toBe(false);
    expect(wrapper.find("form.create-backup").exists()).toBe(false);
  });

  it("shows Owner text and expected revision input in restore dialog", async () => {
    const { wrapper } = await mountBackup();
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const dialog = wrapper.get('[data-restore-dialog]');
    expect(dialog.text()).toContain("Owner");
    expect(dialog.text()).toContain("n1");
    const revision = wrapper.get('input[name="expectedRevision"]');
    expect((revision.element as HTMLInputElement).value).toBe("2");
    const confirm = wrapper.get('[data-action="confirm-restore"]');
    expect((confirm.element as HTMLButtonElement).disabled).toBe(false);
    await revision.setValue("");
    await wrapper.vm.$nextTick();
    expect((wrapper.get('[data-action="confirm-restore"]').element as HTMLButtonElement).disabled).toBe(true);
  });

  it("prefills expectedRevision from live Owner latestRevision, not snapshot maxRevision", async () => {
    const snapshot = {
      snapshot: {
        snapshotId: "snap-old",
        clusterId: "c1",
        nodeId: "n1",
        createdUnixMs: BigInt(1_700_000_000_000),
        processIds: ["web"],
        revisionRanges: [{ processId: "web", minRevision: BigInt(1), maxRevision: BigInt(1) }],
        sha256: "abcdef0123456789deadbeef",
        sink: "fs",
        location: "/backup/fs/snap-old.json",
        sourceNodeId: "",
      },
      sourceNode: "n1",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(1_700_000_010_000),
    };
    const getProcess = vi.fn().mockResolvedValue({
      process: { spec: { latestRevision: 2n } },
    });
    const { wrapper, processClient } = await mountBackup({
      entries: [snapshot],
      getProcess,
    });
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(processClient.getProcess).toHaveBeenCalled();
    const revision = wrapper.get('input[name="expectedRevision"]');
    expect((revision.element as HTMLInputElement).value).toBe("2");
    expect(wrapper.get('[data-restore-owner]').text()).toContain("n1");
  });

  it("hides Restore and Delete for S3 extras without a local index", async () => {
    const s3Extra = {
      snapshot: {
        snapshotId: "snap-s3-extra",
        clusterId: "c1",
        nodeId: "n1",
        createdUnixMs: BigInt(1_700_000_000_000),
        processIds: ["web"],
        revisionRanges: [{ processId: "web", minRevision: BigInt(1), maxRevision: BigInt(1) }],
        sha256: "abcdef0123456789deadbeef",
        sink: "s3",
        location: "s3://bucket/snap-s3-extra.json",
        sourceNodeId: "",
      },
      sourceNode: "s3",
      freshness: "LIVE",
      lastUpdatedUnixMs: BigInt(1_700_000_010_000),
    };
    const { wrapper } = await mountBackup({ entries: [s3Extra] });
    expect(wrapper.text()).toContain("snap-s3-extra");
    expect(wrapper.find('[data-action="restore"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="delete"]').exists()).toBe(false);
  });

  it("lists backups with includeS3 and ALIVE peer ids", async () => {
    const { backupClient } = await mountBackup();
    expect(backupClient.listBackups).toHaveBeenCalled();
    const arg = backupClient.listBackups.mock.calls[0][0] as {
      includeS3?: boolean;
      peerNodeIds?: string[];
    };
    expect(arg.includeS3).toBe(true);
    expect(arg.peerNodeIds).toEqual(["n1", "n3"]);
  });

  it("create backup sends operationId", async () => {
    const { wrapper, backupClient } = await mountBackup();
    await wrapper.get("form.create-backup").trigger("submit");
    await flushPromises();
    expect(backupClient.createBackup).toHaveBeenCalled();
    const arg = backupClient.createBackup.mock.calls[0][0] as {
      meta?: { operationId?: string; operator?: string };
      sink?: string;
    };
    expect(arg.meta?.operationId).toBeTruthy();
    expect(arg.meta?.operator).toBe("admin");
    expect(arg.sink).toBe("fs");
  });

  it("shows error, not success, when restore returns CONFLICT", async () => {
    const { wrapper } = await mountBackup({
      restoreResults: [{ processId: "web", status: "CONFLICT", newRevision: BigInt(0), error: "revision mismatch" }],
    });
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-action="confirm-restore"]').trigger("click");
    await flushPromises();
    const text = wrapper.text();
    expect(text).toContain("Restore conflict");
    expect(text).not.toContain("Restore completed");
    expect(wrapper.find('[role="alert"]').exists()).toBe(true);
  });
});
