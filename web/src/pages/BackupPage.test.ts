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
  snapshots: "Local snapshots",
  policies: "Cluster policies",
  runs: "Cluster runs",
  noPolicies: "No cluster backup policies",
  noRuns: "No cluster backup runs",
  createPolicy: "Create policy",
  editPolicy: "Edit policy",
  savePolicy: "Save policy",
  deletePolicy: "Delete policy",
  deletePolicyConfirm: "Delete policy {{name}}?",
  policyName: "Name",
  enabled: "Enabled",
  disabled: "Disabled",
  nextRun: "Next run",
  latestRun: "Latest run",
  retention: "Retention",
  retentionSummary: "keep last {{last}}, {{days}} days",
  retentionKeepLast: "Keep last",
  retentionKeepDays: "Keep days",
  retentionMaxBytes: "Max bytes",
  scheduleCron: "Cron",
  timezone: "Timezone",
  targetSet: "Target set",
  targetSelector: "Target selector",
  targetNodeIds: "Target IDs",
  targetNodeIdsPlaceholder: "One group or node ID per line",
  destinationProfile: "Destination profile",
  timeoutSeconds: "Timeout (seconds)",
  maxConcurrency: "Max concurrency",
  unavailablePolicy: "Unavailable policy",
  manualOnly: "Manual only",
  fsHostLossWarning: "Cluster FS does not protect against host loss.",
  partialWarning: "This run is PARTIAL: some Agents succeeded and others did not. This is not a successful backup.",
  retryFailed: "Retry failed Agents",
  startRun: "Start run",
  runId: "Run ID",
  targetCount: "Targets",
  successCount: "Success",
  failedCount: "Failed",
  unavailableCount: "Unavailable",
  started: "Started",
  finished: "Finished",
  destinationHealth: "Destination health",
  endpointHost: "Endpoint host",
  healthStatus: "Status",
  bytes: "Bytes",
  checksum: "Checksum",
  errorSummary: "Error",
  agentStatus: "Agent status",
  runDetail: "Run detail",
  policiesUnreachable: "Cluster backup policies are unreachable. This is not an empty catalog.",
  runsUnreachable: "Cluster backup runs are unreachable. This is not an empty catalog.",
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
          actions: { cancel: "Cancel", confirm: "Confirm", delete: "Delete", close: "Close", edit: "Edit", save: "Save" },
        },
      },
    },
  });
});

const clusterPolicy = {
  policyId: "pol-1",
  name: "nightly-fs",
  enabled: true,
  scheduleCron: "0 2 * * *",
  timezone: "UTC",
  targetSelector: "ALL_ADMITTED",
  targetNodeIds: [] as string[],
  sink: "fs",
  destinationProfile: "",
  retentionKeepLast: 7,
  retentionKeepDays: 30,
  retentionMaxBytes: 0n,
  timeoutSeconds: 60,
  maxConcurrency: 4,
  unavailablePolicy: "RECORD_AND_CONTINUE",
  revision: 1n,
};

const s3Policy = {
  ...clusterPolicy,
  policyId: "pol-s3",
  name: "nightly-s3",
  sink: "s3",
  destinationProfile: "primary",
};

const partialRunTasks = [
  {
    runId: "run-partial",
    taskId: "task-n1",
    nodeId: "n1",
    snapshotId: "snap-n1",
    sha256: "abc123def4567890",
    status: "SUCCEEDED",
    bytes: 1024n,
    errorCode: "",
    errorSummary: "",
    leaderTerm: 1n,
    updatedUnix: 1_700_000_015n,
  },
  {
    runId: "run-partial",
    taskId: "task-n2",
    nodeId: "n2",
    snapshotId: "",
    sha256: "",
    status: "FAILED",
    bytes: 0n,
    errorCode: "TIMEOUT",
    errorSummary: "agent timeout",
    leaderTerm: 1n,
    updatedUnix: 1_700_000_018n,
  },
];

const partialRun = {
  runId: "run-partial",
  policyId: "pol-1",
  policyRevision: 1n,
  targetNodeIds: ["n1", "n2"],
  status: "PARTIAL",
  success: 1,
  failed: 1,
  unavailable: 0,
  timeout: 0,
  createdUnix: 1_700_000_000n,
  startedUnix: 1_700_000_010n,
  finishedUnix: 1_700_000_020n,
  tasks: [] as typeof partialRunTasks,
};

const destinationHealth = {
  sink: "s3",
  destinationProfile: "primary",
  status: "OK",
  errorSummary: "",
  checkedUnix: 1_700_000_000n,
  endpointHost: "s3.example.com",
  nodeId: "n1",
};

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
    policies?: unknown[];
    runs?: unknown[];
    run?: unknown;
    health?: unknown;
    policiesError?: Error;
    runsError?: Error;
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
  const policies = opts.policies ?? [clusterPolicy];
  const runs = opts.runs ?? [{ ...partialRun, tasks: [] }];
  const run = opts.run ?? { ...partialRun, tasks: partialRunTasks };
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
  const clusterBackupClient = {
    listPolicies: opts.policiesError
      ? vi.fn().mockRejectedValue(opts.policiesError)
      : vi.fn().mockResolvedValue({ policies }),
    createPolicy: vi.fn().mockResolvedValue({ policy: clusterPolicy }),
    updatePolicy: vi.fn().mockResolvedValue({ policy: clusterPolicy }),
    deletePolicy: vi.fn().mockResolvedValue({}),
    validatePolicy: vi.fn().mockResolvedValue({ valid: true, errors: [] }),
    listRuns: opts.runsError
      ? vi.fn().mockRejectedValue(opts.runsError)
      : vi.fn().mockResolvedValue({ runs }),
    getRun: vi.fn().mockResolvedValue({ run }),
    startRun: vi.fn().mockResolvedValue({ run }),
    retryFailedTasks: vi.fn().mockResolvedValue({ run }),
    getDestinationHealth: vi.fn().mockResolvedValue({
      health: opts.health ?? destinationHealth,
    }),
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
      provide: { backupClient, clusterBackupClient, nodeClient, processClient },
      stubs: { Teleport: true },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, backupClient, clusterBackupClient, nodeClient, processClient, queryClient };
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

  it("does not offer peer as a primary create-backup sink", async () => {
    const { wrapper } = await mountBackup();
    const sinks = wrapper
      .findAll("form.create-backup select[name='sink'] option")
      .map((option) => (option.element as HTMLOptionElement).value);
    expect(sinks).toEqual(["fs", "s3"]);
    expect(sinks).not.toContain("peer");
    expect(wrapper.find('textarea[name="peerNodeIds"]').exists()).toBe(false);
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

  it("lists cluster policies with enabled, sink, next run, latest status, and retention", async () => {
    const { wrapper } = await mountBackup();
    const policies = wrapper.get('[data-section="policies"]');
    const text = policies.text();
    expect(text).toContain("nightly-fs");
    expect(text).toContain("Enabled");
    expect(text).toContain("fs");
    expect(text).toContain("0 2 * * *");
    expect(text).toContain("PARTIAL");
    expect(text).toContain("keep last 7, 30 days");
  });

  it("warns that cluster FS does not protect against host loss", async () => {
    const { wrapper } = await mountBackup();
    expect(wrapper.get("[data-fs-warning]").text()).toContain(
      "Cluster FS does not protect against host loss.",
    );
  });

  it("creates a policy with ALL_ADMITTED, FS/S3-only sink, cron, timezone, and retention", async () => {
    const { wrapper, clusterBackupClient } = await mountBackup({ policies: [] });
    await wrapper.get('[data-action="create-policy"]').trigger("click");
    await wrapper.vm.$nextTick();
    const form = wrapper.get("form.policy-form");
    expect((form.get('select[name="targetSelector"]').element as HTMLSelectElement).value).toBe(
      "ALL_ADMITTED",
    );
    const sinks = form
      .findAll('select[name="policySink"] option')
      .map((option) => (option.element as HTMLOptionElement).value);
    expect(sinks).toEqual(["fs", "s3"]);
    expect(sinks).not.toContain("peer");
    expect(form.find('input[name="accessKey"]').exists()).toBe(false);
    expect(form.find('input[name="secretKey"]').exists()).toBe(false);
    await form.get('input[name="policyName"]').setValue("nightly");
    await form.get('input[name="scheduleCron"]').setValue("0 2 * * *");
    await form.get('input[name="timezone"]').setValue("UTC");
    await form.get('input[name="retentionKeepLast"]').setValue("7");
    await form.get('input[name="retentionKeepDays"]').setValue("30");
    await form.trigger("submit");
    await flushPromises();
    expect(clusterBackupClient.createPolicy).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String), operator: "admin" }),
        policy: expect.objectContaining({
          name: "nightly",
          targetSelector: "ALL_ADMITTED",
          sink: "fs",
          scheduleCron: "0 2 * * *",
          timezone: "UTC",
          retentionKeepLast: 7,
          retentionKeepDays: 30,
        }),
      }),
    );
  });

  it("edits and deletes a policy with operationId", async () => {
    const { wrapper, clusterBackupClient } = await mountBackup();
    await wrapper.get('[data-action="edit-policy"]').trigger("click");
    await wrapper.vm.$nextTick();
    const form = wrapper.get("form.policy-form");
    await form.get('input[name="policyName"]').setValue("nightly-fs-2");
    await form.trigger("submit");
    await flushPromises();
    expect(clusterBackupClient.updatePolicy).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String) }),
        policy: expect.objectContaining({ policyId: "pol-1", name: "nightly-fs-2" }),
      }),
    );

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    await wrapper.get('[data-action="delete-policy"]').trigger("click");
    await flushPromises();
    expect(clusterBackupClient.deletePolicy).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String) }),
        policyId: "pol-1",
      }),
    );
    confirm.mockRestore();
  });

  it("shows destination health profile, endpoint host, and status without secrets", async () => {
    const { wrapper, clusterBackupClient } = await mountBackup({ policies: [s3Policy] });
    expect(clusterBackupClient.getDestinationHealth).toHaveBeenCalledWith(
      expect.objectContaining({ sink: "s3", destinationProfile: "primary" }),
    );
    const health = wrapper.get('[data-section="destination-health"]');
    expect(health.text()).toContain("primary");
    expect(health.text()).toContain("s3.example.com");
    expect(health.text()).toContain("OK");
    expect(health.text().toLowerCase()).not.toMatch(/secret|access.?key|akia/i);
  });

  it("starts a manual run with operationId", async () => {
    const { wrapper, clusterBackupClient } = await mountBackup();
    await wrapper.get('[data-action="start-run"]').trigger("click");
    await flushPromises();
    expect(clusterBackupClient.startRun).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String), operator: "admin" }),
        policyId: "pol-1",
      }),
    );
  });

  it("lists runs with counts and times, and shows per-Agent detail", async () => {
    const { wrapper, clusterBackupClient } = await mountBackup();
    const runs = wrapper.get('[data-section="runs"]');
    expect(runs.text()).toContain("run-partial");
    expect(runs.text()).toContain("2");
    expect(runs.text()).toContain("1");
    await wrapper.get('[data-run-id="run-partial"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(clusterBackupClient.getRun).toHaveBeenCalledWith({ runId: "run-partial" });
    const detail = wrapper.get("[data-run-detail]");
    expect(detail.text()).toContain("n1");
    expect(detail.text()).toContain("n2");
    expect(detail.text()).toContain("snap-n1");
    expect(detail.text()).toContain("abc123de");
    expect(detail.text()).toContain("1024");
    expect(detail.text()).toContain("agent timeout");
    expect(detail.text()).toContain("SUCCEEDED");
    expect(detail.text()).toContain("FAILED");
  });

  it("renders PARTIAL as a warning, not success, and retries only failed Agents", async () => {
    const { wrapper, clusterBackupClient } = await mountBackup();
    const badge = wrapper.get('[data-status="PARTIAL"]');
    expect(badge.classes()).not.toContain("status-success");
    const style = (badge.attributes("style") ?? "").toLowerCase();
    expect(style).not.toMatch(/#d1fae5|#065f46|rgb\(209,\s*250,\s*229\)/);
    expect(style).toMatch(/#fef3c7|#92400e|rgb\(254,\s*243,\s*199\)/);
    expect(wrapper.get("[data-partial-warning]").text()).toContain("PARTIAL");
    expect(wrapper.get("[data-partial-warning]").text()).toContain("not a successful");
    await wrapper.get('[data-run-id="run-partial"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.find('[data-action="retry-failed"]').exists()).toBe(true);
    await wrapper.get('[data-action="retry-failed"]').trigger("click");
    await flushPromises();
    expect(clusterBackupClient.retryFailedTasks).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String) }),
        runId: "run-partial",
      }),
    );
  });

  it("hides policy and run mutations without backup.manage", async () => {
    const { wrapper } = await mountBackup({ permissions: ["backup.read"] });
    expect(wrapper.find('[data-action="create-policy"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="edit-policy"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="delete-policy"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="start-run"]').exists()).toBe(false);
    expect(wrapper.get('[data-section="policies"]').text()).toContain("nightly-fs");
    expect(wrapper.get('[data-section="runs"]').text()).toContain("run-partial");
    await wrapper.get('[data-run-id="run-partial"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.get("[data-run-detail]").exists()).toBe(true);
    expect(wrapper.find('[data-action="retry-failed"]').exists()).toBe(false);
  });

  it("pins latest run by createdUnix, not list order", async () => {
    const older = {
      ...partialRun,
      runId: "run-old",
      status: "SUCCEEDED",
      createdUnix: 1_700_000_000n,
      success: 2,
      failed: 0,
    };
    const newer = {
      ...partialRun,
      runId: "run-new",
      status: "PARTIAL",
      createdUnix: 1_700_000_100n,
    };
    const { wrapper } = await mountBackup({ runs: [older, newer] });
    expect(wrapper.get("[data-latest-run]").text()).toBe("PARTIAL");
    expect(wrapper.get("[data-latest-run]").text()).not.toBe("SUCCEEDED");
  });

  it("does not present policy or run query errors as an empty catalog", async () => {
    const { wrapper } = await mountBackup({
      policiesError: new Error("leader unavailable"),
      runsError: new Error("leader unavailable"),
    });
    const policies = wrapper.get('[data-section="policies"]');
    const runs = wrapper.get('[data-section="runs"]');
    expect(policies.text()).toContain("Cluster backup policies are unreachable. This is not an empty catalog.");
    expect(runs.text()).toContain("Cluster backup runs are unreachable. This is not an empty catalog.");
    expect(policies.text()).not.toContain("No cluster backup policies");
    expect(runs.text()).not.toContain("No cluster backup runs");
    expect(policies.find(".freshness-unknown, .freshness-stale").exists()).toBe(true);
    expect(runs.find(".freshness-unknown, .freshness-stale").exists()).toBe(true);
  });

  it("keeps last successful policy and run rows when a later fetch fails", async () => {
    const { wrapper, clusterBackupClient, queryClient } = await mountBackup();
    expect(wrapper.get('[data-section="policies"]').text()).toContain("nightly-fs");
    expect(wrapper.get('[data-section="runs"]').text()).toContain("run-partial");
    clusterBackupClient.listPolicies.mockRejectedValue(new Error("leader unavailable"));
    clusterBackupClient.listRuns.mockRejectedValue(new Error("leader unavailable"));
    await queryClient.invalidateQueries({ queryKey: ["cluster-backup-policies"] });
    await queryClient.invalidateQueries({ queryKey: ["cluster-backup-runs"] });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-section="policies"]').text()).toContain("nightly-fs");
    expect(wrapper.get('[data-section="runs"]').text()).toContain("run-partial");
    expect(wrapper.text()).not.toContain("No cluster backup policies");
    expect(wrapper.text()).not.toContain("No cluster backup runs");
  });
});
