import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { ErrorInfoSchema } from "../gen/procmesh/v1/api_pb";
import { useClusterBackupClient, useReplicationClient } from "../lib/rpc";
import { session } from "../lib/session";
import { router as appRouter } from "../router";
import DisasterReplicaPage from "./DisasterReplicaPage.vue";

let i18n: typeof i18next;

const clusterBackupMethods = [
  "createPolicy",
  "updatePolicy",
  "deletePolicy",
  "listPolicies",
  "validatePolicy",
  "startRun",
  "getRun",
  "listRuns",
  "retryFailedTasks",
  "getDestinationHealth",
] as const;

const replicationMethods = [
  "getTopology",
  "generatePolicyDraft",
  "applyPolicyDraft",
  "listPolicies",
  "getPolicy",
  "updatePolicy",
  "deletePolicy",
  "startRun",
  "getRun",
  "listRuns",
  "retryFailedRoutes",
  "verifyReplica",
  "listRecoverableSnapshots",
  "prepareRecoverableSnapshotRestore",
  "restoreRecoverableSnapshot",
] as const;

const replicaI18n = {
  overview: "Overview",
  config: "Replica config",
  runs: "Runs and recovery",
  recovery: "Recoverable snapshots",
  routeCount: "Routes",
  healthyCount: "Healthy",
  lagCount: "Lag",
  failedCount: "Failed",
  lastSuccess: "Last successful replication",
  recoverableCount: "Recoverable snapshots",
  generate: "Generate cluster replica config",
  preview: "Preview replica config",
  apply: "Apply draft",
  replaceCurrent: "Replace current configuration",
  replaceHint: "Existing routes will not be overwritten unless you choose replace.",
  topologyChanged:
    "The cluster topology changed while you were editing. The preview was refreshed; review it and confirm replacement again.",
  generationRules: "Generation rules",
  routeTable: "Route table",
  warnings: "Failure-domain warnings",
  costEstimate: "Cost estimate",
  inboundLoad: "Inbound load",
  n1Warning: "Single-node cluster: no replica target is available.",
  offlineWarning: "Admitted node {{id}} is offline and remains in topology.",
  policyRevision: "Policy revision",
  replicaFactor: "Replica factor",
  trigger: "Trigger",
  schedule: "Schedule",
  scheduleCron: "Cron",
  timezone: "Timezone",
  timezoneHint: "Cron and day-based retention use this timezone. New policies default to this browser.",
  timezoneSuggested: "Suggested",
  timezoneAll: "All timezones",
  timezoneBrowser: "This browser · {{zone}}",
  nextRun: "Next run",
  manualOnly: "Manual only",
  scheduleDisabled: "Schedule disabled",
  nextRunHint:
    "Applying the policy does not back up immediately. The next cron fire will. Use Start run to back up now.",
  retention: "Retention",
  retentionSummary: "keep last {{last}}, {{days}} days",
  concurrency: "Concurrency",
  topologyConstraints: "Topology constraints",
  source: "Source",
  editSource: "Source node",
  editTarget: "Target node {{index}}",
  targets: "Targets",
  health: "Health",
  lag: "Lag",
  freshness: "Freshness",
  retryFailed: "Retry failed routes",
  verify: "Verify checksum",
  restoreOwner: "Restore on Owner",
  restore: "Restore",
  restoreConfirm: "Restore recoverable snapshot",
  restoreWarning: "The selected snapshot will replace process specs on the Owner using CAS.",
  snapshot: "Snapshot",
  storageNodes: "Stored on",
  storageNode: "Restore source",
  automaticStorage: "Automatic",
  ownerCopy: "Owner copy",
  peerCopy: "Peer copy",
  process: "Process",
  snapshotRevision: "Snapshot revision",
  expectedRevision: "Expected revision",
  currentMissing: "Not currently present",
  prepareRestoreFailed: "Cannot prepare restore: {{detail}}",
  ownerUnavailable: "Owner is unavailable. Restore cannot run until the Owner is reachable.",
  confirmRestore: "Confirm restore",
  restoreSuccess: "Restore completed",
  restoreConflict: "Restore conflict: {{detail}}",
  restoreFailed: "Restore failed: {{detail}}",
  restoredFrom: "Restored from {{node}}",
  owner: "Owner",
  snapshotId: "Snapshot ID",
  checksum: "Checksum",
  noRoutes: "No replica routes",
  noRuns: "No replication runs",
  noSnapshots: "No recoverable snapshots",
  policiesUnreachable: "Replica policies are unreachable. This is not an empty catalog.",
  runsUnreachable: "Replication runs are unreachable. This is not an empty catalog.",
  snapshotsUnreachable: "Recoverable snapshots are unreachable. This is not an empty catalog.",
  topologyUnreachable: "Replica topology is unreachable. This is not an empty catalog.",
  staleBanner: "Some replica sources are unreachable. This is not an empty catalog.",
  partialWarning: "This run is PARTIAL: some routes succeeded and others did not. This is not a successful replication.",
  loading: "Loading…",
  startRun: "Capture and replicate now",
  primaryRunId: "Primary backup run ID",
  runId: "Run ID",
  runDetail: "Run detail",
  status: "Status",
  bytes: "Bytes",
  errorSummary: "Error",
  applied: "Applied policy revision {{revision}}",
  cancel: "Cancel",
  node: "Node",
  alive: "Alive",
  admitted: "Admitted",
  host: "Host",
  rack: "Rack",
  zone: "Zone",
  none: "None",
  restoreHint: "Peer copies cannot be applied directly. Restore on the source Owner.",
  verifyValid: "Replica checksum matches",
  verifyInvalid: "Replica checksum mismatch",
  sourceSelector: "Source selector",
  to: "to",
};

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          nav: { disasterReplica: "Disaster replica" },
          common: { noData: "No data available" },
          replica: replicaI18n,
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
          actions: { cancel: "Cancel", confirm: "Confirm", close: "Close" },
        },
      },
    },
  });
});

const topologyNodes = [
  {
    nodeId: "n1",
    hostname: "agent-one",
    host: "host-1",
    rack: "r1",
    zone: "z1",
    capacityWeight: 1,
    admitted: true,
    alive: true,
  },
  {
    nodeId: "n2",
    hostname: "agent-two",
    host: "host-2",
    rack: "r2",
    zone: "z1",
    capacityWeight: 1,
    admitted: true,
    alive: false,
  },
  {
    nodeId: "n3",
    hostname: "agent-three",
    host: "host-3",
    rack: "r1",
    zone: "z2",
    capacityWeight: 1,
    admitted: true,
    alive: true,
  },
];

const replicaPolicy = {
  policyId: "rep-1",
  name: "cluster-replica",
  enabled: true,
  sourceSelector: "ALL_ADMITTED",
  sourceIds: [] as string[],
  replicaFactor: 1,
  routes: [
    { sourceNodeId: "n1", targetNodeIds: ["n3"], warnings: [] as string[] },
    { sourceNodeId: "n2", targetNodeIds: ["n1"], warnings: ["admitted-node-offline:n2"] },
    { sourceNodeId: "n3", targetNodeIds: ["n1"], warnings: [] as string[] },
  ],
  trigger: "MANUAL",
  primaryPolicyIds: [] as string[],
  scheduleCron: "",
  timezone: "UTC",
  retentionKeepLast: 7,
  retentionKeepDays: 30,
  retentionMaxBytes: 0n,
  maxConcurrency: 2,
  verifyAfterCopy: true,
  bandwidthLimit: 0n,
  topologyConstraints: { minZoneDiversity: "1" } as Record<string, string>,
  revision: 2n,
};

const replicaTasks = [
  {
    taskId: "task-ok",
    runId: "run-partial",
    sourceNodeId: "n1",
    targetNodeIds: ["n3"],
    snapshotId: "snap-n1",
    sha256: "abc123def4567890",
    status: "SUCCEEDED",
    bytes: 2048n,
    errorCode: "",
    errorSummary: "",
    startedAt: 1_700_000_000n,
    finishedAt: 1_700_000_050n,
  },
  {
    taskId: "task-fail",
    runId: "run-partial",
    sourceNodeId: "n2",
    targetNodeIds: ["n1"],
    snapshotId: "snap-n2",
    sha256: "def456abc1237890",
    status: "FAILED",
    bytes: 0n,
    errorCode: "CHECKSUM",
    errorSummary: "checksum mismatch",
    startedAt: 1_700_000_000n,
    finishedAt: 1_700_000_040n,
  },
  {
    taskId: "task-lag",
    runId: "run-partial",
    sourceNodeId: "n3",
    targetNodeIds: ["n1"],
    snapshotId: "",
    sha256: "",
    status: "UNAVAILABLE",
    bytes: 0n,
    errorCode: "UNAVAILABLE",
    errorSummary: "target offline",
    startedAt: 0n,
    finishedAt: 0n,
  },
];

const replicaRun = {
  runId: "run-partial",
  policyId: "rep-1",
  policyRevision: 2n,
  status: "PARTIAL",
  tasks: replicaTasks,
  startedAt: 1_700_000_000n,
  finishedAt: 1_700_000_050n,
};

const policyDraft = {
  name: "cluster-replica",
  enabled: true,
  sourceSelector: "ALL_ADMITTED",
  sourceIds: [] as string[],
  replicaFactor: 1,
  routes: [
    { sourceNodeId: "n1", targetNodeIds: ["n3"], warnings: [] as string[] },
    { sourceNodeId: "n3", targetNodeIds: ["n1"], warnings: [] as string[] },
  ],
  trigger: "MANUAL",
  primaryPolicyIds: [] as string[],
  scheduleCron: "",
  timezone: "UTC",
  retentionKeepLast: 7,
  retentionKeepDays: 30,
  retentionMaxBytes: 0n,
  maxConcurrency: 2,
  verifyAfterCopy: true,
  bandwidthLimit: 0n,
  topologyConstraints: { minZoneDiversity: "1" } as Record<string, string>,
  draftRevision: 7n,
  draftHash: "draft-hash-7",
  globalWarnings: ["admitted-node-offline:n2"],
  inboundLoad: { n1: 1, n3: 1 } as Record<string, number>,
  topologyHealth: "DEGRADED",
};

const n1Draft = {
  ...policyDraft,
  routes: [] as typeof policyDraft.routes,
  globalWarnings: ["single-node-no-replica"],
  inboundLoad: {} as Record<string, number>,
  topologyHealth: "DEGRADED",
};

const recoverableSnapshot = {
  snapshotId: "snap-n1",
  clusterId: "c1",
  sourceNodeId: "n1",
  sha256: "abc123def4567890",
  createdAt: 1_700_000_000n,
  processCount: 1,
  processIds: ["web"],
  storageNodeIds: ["n1", "n2"],
  freshness: "LIVE",
  lastUpdatedUnixMs: 1_700_000_000_000n,
};

const mounted: Array<{ unmount: () => void }> = [];

async function mountPage(
  opts: {
    permissions?: string[];
    topology?: unknown[];
    policies?: unknown[];
    runs?: unknown[];
    run?: unknown;
    snapshots?: unknown[];
    inventoryStatuses?: unknown[];
    draft?: unknown;
    topologyError?: Error;
    policiesError?: Error;
    runsError?: Error;
    snapshotsError?: Error;
    verifyResult?: { valid: boolean };
    verifyError?: Error;
    prepareRestore?: unknown;
    prepareRestoreError?: Error;
    restoreResults?: unknown[];
  } = {},
) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: opts.permissions ?? ["replication.read", "replication.manage", "backup.manage"],
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const topology = opts.topology ?? topologyNodes;
  const policies = opts.policies ?? [replicaPolicy];
  const runs = opts.runs ?? [{ ...replicaRun, tasks: replicaTasks }];
  const run = opts.run ?? replicaRun;
  const snapshots = opts.snapshots ?? [recoverableSnapshot];
  const inventoryStatuses = opts.inventoryStatuses ?? [
    { nodeId: "n1", freshness: "LIVE", lastUpdatedUnixMs: 1_700_000_000_000n },
    { nodeId: "n2", freshness: "LIVE", lastUpdatedUnixMs: 1_700_000_000_000n },
    { nodeId: "n3", freshness: "LIVE", lastUpdatedUnixMs: 1_700_000_000_000n },
  ];
  const draft = opts.draft ?? policyDraft;
  const replicationClient = {
    getTopology: opts.topologyError
      ? vi.fn().mockRejectedValue(opts.topologyError)
      : vi.fn().mockResolvedValue({ nodes: topology, clusterId: "c1" }),
    generatePolicyDraft: vi.fn().mockResolvedValue({ draft }),
    applyPolicyDraft: vi.fn().mockResolvedValue({ policyId: "rep-1", revision: 3n }),
    listPolicies: opts.policiesError
      ? vi.fn().mockRejectedValue(opts.policiesError)
      : vi.fn().mockResolvedValue({ policies }),
    getPolicy: vi.fn().mockResolvedValue({ policy: replicaPolicy }),
    updatePolicy: vi.fn().mockResolvedValue({ policyId: "rep-1", revision: 3n }),
    deletePolicy: vi.fn().mockResolvedValue({ deleted: true }),
    startRun: vi.fn().mockResolvedValue({ runId: "run-2", policyId: "rep-1", policyRevision: 2n, startedAt: 1n }),
    listRuns: opts.runsError
      ? vi.fn().mockRejectedValue(opts.runsError)
      : vi.fn().mockResolvedValue({ runs }),
    getRun: vi.fn().mockResolvedValue({ run }),
    retryFailedRoutes: vi.fn().mockResolvedValue({ retriedCount: 1 }),
    verifyReplica: opts.verifyError
      ? vi.fn().mockRejectedValue(opts.verifyError)
      : vi.fn().mockResolvedValue({
          valid: true,
          sha256: recoverableSnapshot.sha256,
          processCount: 1,
          processIds: ["web"],
          errors: [],
          ...opts.verifyResult,
        }),
    listRecoverableSnapshots: opts.snapshotsError
      ? vi.fn().mockRejectedValue(opts.snapshotsError)
      : vi.fn().mockResolvedValue({ snapshots, inventoryStatuses }),
    prepareRecoverableSnapshotRestore: opts.prepareRestoreError
      ? vi.fn().mockRejectedValue(opts.prepareRestoreError)
      : vi.fn().mockResolvedValue(
          opts.prepareRestore ?? {
            snapshot: recoverableSnapshot,
            candidates: [
              {
                processId: "web",
                processName: "web",
                snapshotRevision: 7n,
                currentRevision: 11n,
                currentExists: true,
              },
            ],
            selectedStorageNodeId: "n1",
            ownerCopy: true,
          },
        ),
    restoreRecoverableSnapshot: vi.fn().mockResolvedValue({
      results: opts.restoreResults ?? [
        { processId: "web", status: "SUCCESS", newRevision: 12n, error: "" },
      ],
      restoredFromNodeId: "n1",
      fetchedFromPeer: false,
    }),
  };
  const memoryRouter = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/disaster-replica", component: DisasterReplicaPage },
      { path: "/backup", component: defineComponent({ setup: () => () => h("div") }) },
    ],
  });
  await memoryRouter.push("/disaster-replica");
  await memoryRouter.isReady();
  const wrapper = mount(DisasterReplicaPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        memoryRouter,
      ],
      provide: { replicationClient },
      stubs: { Teleport: true },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, replicationClient, queryClient, memoryRouter };
}

afterEach(() => {
  session.value = null;
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  document.body.style.overflow = "";
});

describe("cluster backup and replication clients", () => {
  it("injects generated ClusterBackupService and DisasterReplicationService clients", () => {
    const clusterBackupClient = Object.fromEntries(clusterBackupMethods.map((name) => [name, vi.fn()]));
    const replicationClient = Object.fromEntries(replicationMethods.map((name) => [name, vi.fn()]));
    const Probe = defineComponent({
      setup() {
        const backup = useClusterBackupClient();
        const replica = useReplicationClient();
        return () =>
          h("div", {
            "data-backup": backup === clusterBackupClient ? "ok" : "no",
            "data-replica": replica === replicationClient ? "ok" : "no",
            "data-backup-methods": clusterBackupMethods.every((name) => typeof backup[name] === "function")
              ? "ok"
              : "no",
            "data-replica-methods": replicationMethods.every((name) => typeof replica[name] === "function")
              ? "ok"
              : "no",
          });
      },
    });
    const wrapper = mount(Probe, {
      global: { provide: { clusterBackupClient, replicationClient } },
    });
    mounted.push(wrapper);
    expect(wrapper.attributes("data-backup")).toBe("ok");
    expect(wrapper.attributes("data-replica")).toBe("ok");
    expect(wrapper.attributes("data-backup-methods")).toBe("ok");
    expect(wrapper.attributes("data-replica-methods")).toBe("ok");
  });
});

describe("DisasterReplicaPage", () => {
  it("registers the /disaster-replica route", () => {
    const resolved = appRouter.resolve("/disaster-replica");
    expect(resolved.matched.some((record) => record.path === "/disaster-replica")).toBe(true);
    expect(resolved.matched.some((record) => record.components?.default === DisasterReplicaPage)).toBe(true);
  });

  it("mounts a permission-gated workflow when replication.read is present", async () => {
    const { wrapper } = await mountPage({ permissions: ["replication.read"] });
    expect(wrapper.text()).toContain("Disaster replica");
    expect(wrapper.attributes("data-permission")).toBe("granted");
    expect(wrapper.find('[data-section="overview"]').exists()).toBe(true);
    expect(wrapper.find('[data-section="config"]').exists()).toBe(true);
    expect(wrapper.find('[data-section="runs"]').exists()).toBe(true);
  });

  it("shows a denied shell without replication.read", async () => {
    const { wrapper, replicationClient } = await mountPage({ permissions: ["backup.read"] });
    expect(wrapper.text()).toContain("Disaster replica");
    expect(wrapper.attributes("data-permission")).toBe("denied");
    expect(wrapper.find('[data-section="overview"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="generate"]').exists()).toBe(false);
    expect(replicationClient.getTopology).not.toHaveBeenCalled();
  });

  it("shows overview route counts, healthy/lag/failed, last success, and recoverable snapshots", async () => {
    const { wrapper } = await mountPage();
    const overview = wrapper.get('[data-section="overview"]');
    expect(overview.get("[data-route-count]").text()).toContain("3");
    expect(overview.get("[data-healthy-count]").text()).toContain("1");
    expect(overview.get("[data-lag-count]").text()).toContain("1");
    expect(overview.get("[data-failed-count]").text()).toContain("1");
    expect(overview.get("[data-recoverable-count]").text()).toContain("1");
    expect(overview.get("[data-last-success]").text()).not.toBe("—");
  });

  it("shows recovery Owner, storage nodes, and inventory freshness", async () => {
    const { wrapper } = await mountPage({
      inventoryStatuses: [
        { nodeId: "n1", freshness: "LIVE", lastUpdatedUnixMs: 1_700_000_000_000n },
        { nodeId: "n2", freshness: "STALE", lastUpdatedUnixMs: 1_700_000_000_000n },
        { nodeId: "n3", freshness: "UNKNOWN", errorCode: "UNAVAILABLE" },
      ],
      snapshots: [{ ...recoverableSnapshot, freshness: "STALE" }],
    });
    const recovery = wrapper.get('[data-section="recovery"]');
    expect(recovery.get("[data-snapshot-owner]").text()).toBe("agent-one");
    expect(recovery.get("[data-snapshot-storage]").text()).toContain("agent-one");
    expect(recovery.get("[data-snapshot-storage]").text()).toContain("agent-two");
    expect(recovery.get("[data-snapshot-freshness]").text()).toMatch(/stale/i);
    expect(recovery.get(".section-header .freshness-badge").text()).toMatch(/unknown/i);
    expect(wrapper.text()).toContain("Some replica sources are unreachable");
  });

  it("keeps offline admitted nodes in topology as warnings, not exclusions", async () => {
    const { wrapper } = await mountPage();
    const config = wrapper.get('[data-section="config"]');
    expect(config.text()).toContain("n2");
    expect(config.get('[data-node-id="n2"]').attributes("data-alive")).toBe("false");
    expect(config.get('[data-node-id="n2"]').attributes("data-admitted")).toBe("true");
    expect(config.get("[data-offline-warning]").text()).toContain("n2");
    expect(config.get("[data-offline-warning]").text()).toMatch(/offline|topology/i);
  });

  it("shows node names in the topology node column", async () => {
    const { wrapper } = await mountPage();
    const config = wrapper.get('[data-section="config"]');
    expect(config.get('[data-node-id="n1"]').get("[data-node-name]").text()).toBe("agent-one");
    expect(config.get('[data-node-id="n2"]').get("[data-node-name]").text()).toBe("agent-two");
    expect(config.get('[data-node-id="n3"]').get("[data-node-name]").text()).toBe("agent-three");
  });

  it("falls back to node id when hostname is missing", async () => {
    const { wrapper } = await mountPage({
      topology: [{ ...topologyNodes[0], hostname: "", host: "" }],
    });
    expect(wrapper.get('[data-node-id="n1"]').get("[data-node-name]").text()).toBe("n1");
  });

  it("opens a generate preview with rules, routes, warnings, and inbound load", async () => {
    const { wrapper, replicationClient } = await mountPage();
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(replicationClient.generatePolicyDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        sourceSelector: "ALL_ADMITTED",
        replicaFactor: 1,
      }),
    );
    const preview = wrapper.get("[data-preview-dialog]");
    expect(preview.text()).toContain("Generation rules");
    expect(preview.text()).toContain("ALL_ADMITTED");
    expect(preview.text()).toContain("Inbound load");
    expect(preview.text()).toContain("n2");
    expect(preview.find('input[name="draftHash"]').exists()).toBe(false);
    expect(preview.find('input[name="draftRevision"]').exists()).toBe(false);
    const routes = preview.get("[data-preview-routes]");
    expect(routes.element.closest(".card")).toBeNull();
    expect(routes.get('[data-route-source="0"]').exists()).toBe(true);
    expect(routes.get('[data-route-target="0-0"]').exists()).toBe(true);
    expect(routes.get('[data-route-source="1"]').exists()).toBe(true);
    const sourceSelect = routes.get('[data-route-source="0"]').element as HTMLSelectElement;
    const targetSelect = routes.get('[data-route-target="0-0"]').element as HTMLSelectElement;
    expect(sourceSelect.value).toBe("n1");
    expect(targetSelect.value).toBe("n3");
    expect(preview.get("[data-preview-header]").exists()).toBe(true);
    expect(preview.get("[data-preview-body]").exists()).toBe(true);
    expect(preview.get("[data-preview-footer]").exists()).toBe(true);
    expect(preview.get("[data-preview-footer]").get('[data-action="apply-draft"]').exists()).toBe(true);
  });

  it("lets the operator change preview source and target nodes before apply", async () => {
    const { wrapper, replicationClient } = await mountPage({
      policies: [{ ...replicaPolicy, routes: [] }],
    });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const preview = wrapper.get("[data-preview-dialog]");
    await preview.get('[data-route-source="0"]').setValue("n2");
    await preview.get('[data-route-target="0-0"]').setValue("n1");
    await wrapper.vm.$nextTick();
    expect((preview.get('[data-route-source="0"]').element as HTMLSelectElement).value).toBe("n2");
    expect((preview.get('[data-route-target="0-0"]').element as HTMLSelectElement).value).toBe("n1");
    await wrapper.get('[data-action="apply-draft"]').trigger("click");
    await flushPromises();
    expect(replicationClient.applyPolicyDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        draftRevision: 7n,
        draftHash: "draft-hash-7",
        draft: expect.objectContaining({
          draftRevision: 7n,
          draftHash: "draft-hash-7",
          routes: expect.arrayContaining([
            expect.objectContaining({ sourceNodeId: "n2", targetNodeIds: ["n1"] }),
          ]),
        }),
      }),
    );
  });

  it("shows the N=1 warning in the generate preview", async () => {
    const { wrapper } = await mountPage({
      topology: [topologyNodes[0]],
      policies: [],
      draft: n1Draft,
    });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const preview = wrapper.get("[data-preview-dialog]");
    expect(preview.get("[data-n1-warning]").text()).toMatch(/single-node|no replica/i);
    expect(preview.get("[data-preview-routes]").text()).toMatch(/no replica routes/i);
  });

  it("applies the server draft revision and hash, then shows the policy revision", async () => {
    const { wrapper, replicationClient } = await mountPage({
      policies: [{ ...replicaPolicy, routes: [] }],
    });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-action="apply-draft"]').trigger("click");
    await flushPromises();
    expect(replicationClient.applyPolicyDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String), operator: "admin" }),
        policyId: "rep-1",
        expectedRevision: 2n,
        draftRevision: 7n,
        draftHash: "draft-hash-7",
        draft: expect.objectContaining({ draftRevision: 7n, draftHash: "draft-hash-7" }),
      }),
    );
    expect(wrapper.get("[data-policy-revision]").text()).toContain("3");
  });

  it("applies a first-time draft with expectedRevision -1 and a generated policyId", async () => {
    const { wrapper, replicationClient } = await mountPage({ policies: [] });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-action="apply-draft"]').trigger("click");
    await flushPromises();
    expect(replicationClient.applyPolicyDraft).toHaveBeenCalledTimes(1);
    const arg = replicationClient.applyPolicyDraft.mock.calls[0][0] as {
      policyId?: string;
      expectedRevision?: bigint;
    };
    expect(arg.expectedRevision).toBe(-1n);
    expect(arg.policyId).toEqual(expect.any(String));
    expect(arg.policyId).toBeTruthy();
    expect(arg.policyId).not.toBe("rep-1");
  });

  it("does not silently overwrite existing routes without an explicit replace choice", async () => {
    const { wrapper, replicationClient } = await mountPage();
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const apply = wrapper.get('[data-action="apply-draft"]');
    expect((apply.element as HTMLButtonElement).disabled).toBe(true);
    expect(wrapper.get("[data-replace-current]").text()).toMatch(/replace/i);
    await wrapper.get('input[name="replaceCurrent"]').setValue(true);
    await wrapper.vm.$nextTick();
    expect((wrapper.get('[data-action="apply-draft"]').element as HTMLButtonElement).disabled).toBe(false);
    await wrapper.get('[data-action="apply-draft"]').trigger("click");
    await flushPromises();
    expect(replicationClient.applyPolicyDraft).toHaveBeenCalledTimes(1);
    expect(replicationClient.applyPolicyDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        policyId: "rep-1",
        expectedRevision: 2n,
        draftRevision: 7n,
        draftHash: "draft-hash-7",
      }),
    );
  });

  it("lists runs, shows per-route detail, and retries failed routes with operationId", async () => {
    const { wrapper, replicationClient } = await mountPage();
    const runs = wrapper.get('[data-section="runs"]');
    expect(runs.text()).toContain("run-partial");
    expect(runs.get('[data-status="PARTIAL"]').exists()).toBe(true);
    await wrapper.get('[data-run-id="run-partial"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(replicationClient.getRun).toHaveBeenCalledWith({ runId: "run-partial" });
    const detail = wrapper.get("[data-run-detail]");
    expect(detail.text()).toContain("agent-one");
    expect(detail.text()).toContain("agent-two");
    expect(detail.text()).toContain("checksum mismatch");
    await wrapper.get('[data-action="retry-failed"]').trigger("click");
    await flushPromises();
    expect(replicationClient.retryFailedRoutes).toHaveBeenCalledWith(
      expect.objectContaining({
        meta: expect.objectContaining({ operationId: expect.any(String) }),
        runId: "run-partial",
      }),
    );
  });

  it("renders PARTIAL and failed routes as warnings, not success green", async () => {
    const { wrapper } = await mountPage();
    const badge = wrapper.get('[data-status="PARTIAL"]');
    expect(badge.classes()).not.toContain("status-success");
    const style = (badge.attributes("style") ?? "").toLowerCase();
    expect(style).not.toMatch(/#d1fae5|#065f46|rgb\(209,\s*250,\s*229\)/);
    expect(style).toMatch(/#fef3c7|#92400e|rgb\(254,\s*243,\s*199\)/);
    expect(wrapper.get("[data-partial-warning]").text()).toContain("PARTIAL");
    await wrapper.get('[data-run-id="run-partial"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const failed = wrapper.get('[data-route-status="FAILED"]');
    expect(failed.classes()).not.toContain("status-success");
    const failedStyle = (failed.attributes("style") ?? "").toLowerCase();
    expect(failedStyle).not.toMatch(/#d1fae5|#065f46/);
  });

  it("shows a success toast after verifying a matching replica checksum", async () => {
    const { wrapper, replicationClient } = await mountPage();
    const recovery = wrapper.get('[data-section="recovery"]');
    expect(recovery.text()).toContain("snap-n1");
    expect(recovery.get("[data-snapshot-owner]").text()).toBe("agent-one");
    await wrapper.get('[data-action="verify"]').trigger("click");
    await flushPromises();
    expect(replicationClient.verifyReplica).toHaveBeenCalledWith(
      expect.objectContaining({ sourceNodeId: "n1", snapshotId: "snap-n1" }),
    );
    expect(wrapper.get(".toast-success").text()).toMatch(/checksum matches/i);
    expect(wrapper.find("p.notice").exists()).toBe(false);
  });

  it("shows an error toast when the replica checksum does not match", async () => {
    const { wrapper } = await mountPage({ verifyResult: { valid: false } });
    await wrapper.get('[data-action="verify"]').trigger("click");
    await flushPromises();
    expect(wrapper.get(".toast-error").text()).toMatch(/checksum mismatch/i);
    expect(wrapper.find("p.notice").exists()).toBe(false);
  });

  it("shows an error toast when checksum verification fails", async () => {
    const { wrapper } = await mountPage({ verifyError: new Error("verify unavailable") });
    await wrapper.get('[data-action="verify"]').trigger("click");
    await flushPromises();
    expect(wrapper.get(".toast-error").text()).toContain("verify unavailable");
    expect(wrapper.find("p.error").exists()).toBe(false);
  });

  it("opens restore on the replica page with the clicked snapshot selected", async () => {
    const otherSnapshot = {
      ...recoverableSnapshot,
      snapshotId: "snap-n1-newer",
      sha256: "9999999999999999",
      createdAt: 1_700_000_100n,
      storageNodeIds: ["n2", "n3"],
    };
    const otherOwnerSnapshot = {
      ...recoverableSnapshot,
      snapshotId: "snap-n3",
      sourceNodeId: "n3",
    };
    const { wrapper, replicationClient, memoryRouter } = await mountPage({
      snapshots: [recoverableSnapshot, otherSnapshot, otherOwnerSnapshot],
    });
    const recovery = wrapper.get('[data-section="recovery"]');
    expect(recovery.get("[data-snapshot-owner]").text()).toBe("agent-one");
    const restoreButtons = recovery.findAll('[data-action="restore"]');
    await restoreButtons[0].trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(memoryRouter.currentRoute.value.path).toBe("/disaster-replica");
    expect(replicationClient.prepareRecoverableSnapshotRestore).toHaveBeenCalledWith({
      sourceNodeId: "n1",
      snapshotId: "snap-n1",
      sha256: "abc123def4567890",
      storageNodeId: "",
    });
    const dialog = wrapper.get("[data-restore-dialog]");
    expect(dialog.attributes("role")).toBe("dialog");
    expect((dialog.get('[data-field="restore-snapshot"]').element as HTMLSelectElement).value).toBe("snap-n1");
    expect(dialog.get('[data-field="restore-snapshot"]').findAll("option").map((option) => option.attributes("value"))).toEqual([
      "snap-n1",
      "snap-n1-newer",
    ]);
    expect(dialog.text()).toContain("agent-one");
    expect(dialog.text()).toContain("abc123def4567890");
    expect(dialog.text()).toContain("LIVE");
    expect(dialog.text()).toContain("agent-two");
    expect(dialog.text()).toContain("web");
    expect((dialog.get('input[name="expectedRevision"]').element as HTMLInputElement).value).toBe("11");
  });

  it("restores on the Owner with CAS targets and shows per-process results in the dialog", async () => {
    const { wrapper, replicationClient, memoryRouter } = await mountPage();
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    await wrapper.get('[data-action="confirm-restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(replicationClient.restoreRecoverableSnapshot).toHaveBeenCalledWith({
      meta: expect.objectContaining({ operationId: expect.any(String), operator: "admin" }),
      sourceNodeId: "n1",
      snapshotId: "snap-n1",
      sha256: "abc123def4567890",
      storageNodeId: "n1",
      targets: [{ processId: "web", expectedRevision: 11n }],
    });
    expect(memoryRouter.currentRoute.value.path).toBe("/disaster-replica");
    const dialog = wrapper.get("[data-restore-dialog]");
    expect(dialog.get('[data-restore-result="web"]').text()).toContain("SUCCESS");
    expect(dialog.text()).toContain("agent-one");
    expect(wrapper.text()).toContain("Restore completed");
  });

  it("requires backup.manage for restore independently from replication.manage", async () => {
    const { wrapper } = await mountPage({
      permissions: ["replication.read", "replication.manage"],
    });
    expect(wrapper.find('[data-action="verify"]').exists()).toBe(true);
    expect(wrapper.find('[data-action="restore"]').exists()).toBe(false);
  });

  it("keeps CAS conflicts visible and does not report restore success", async () => {
    const { wrapper } = await mountPage({
      restoreResults: [
        { processId: "web", status: "CONFLICT", newRevision: 11n, error: "revision mismatch" },
      ],
    });
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.get('[data-action="confirm-restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(wrapper.get('[data-restore-result="web"]').text()).toContain("CONFLICT");
    expect(wrapper.get('[data-restore-result="web"]').text()).toContain("revision mismatch");
    expect(wrapper.text()).toContain("Restore conflict: web: revision mismatch");
    expect(wrapper.text()).not.toContain("Restore completed");
  });

  it("keeps the operator on the replica page when the Owner is unavailable", async () => {
    const { wrapper, replicationClient, memoryRouter } = await mountPage({
      prepareRestoreError: new ConnectError("UNAVAILABLE", Code.Unavailable, undefined, [
        {
          desc: ErrorInfoSchema,
          value: { code: "UNAVAILABLE", message: "UNAVAILABLE: owner unreachable" },
        },
      ]),
    });
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(replicationClient.prepareRecoverableSnapshotRestore).toHaveBeenCalledOnce();
    expect(memoryRouter.currentRoute.value.path).toBe("/disaster-replica");
    const dialog = wrapper.get("[data-restore-dialog]");
    expect(dialog.get('[role="alert"]').text()).toContain(
      "Owner is unavailable. Restore cannot run until the Owner is reachable.",
    );
    expect((dialog.get('[data-action="confirm-restore"]').element as HTMLButtonElement).disabled).toBe(true);
    expect(replicationClient.restoreRecoverableSnapshot).not.toHaveBeenCalled();
  });

  it("does not misreport another UNAVAILABLE prepare failure as an unreachable Owner", async () => {
    const { wrapper } = await mountPage({
      prepareRestoreError: new ConnectError("replica storage unavailable", Code.Unavailable),
    });
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    const alert = wrapper.get("[data-restore-dialog]").get('[role="alert"]');
    expect(alert.text()).toContain("Cannot prepare restore: UNAVAILABLE");
    expect(alert.text()).not.toContain("Owner is unavailable");
  });

  it("closes restore with Escape and restores body scrolling", async () => {
    document.body.style.overflow = "auto";
    const { wrapper } = await mountPage();
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(wrapper.find("[data-restore-dialog]").exists()).toBe(true);
    expect(document.body.style.overflow).toBe("hidden");
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await wrapper.vm.$nextTick();

    expect(wrapper.find("[data-restore-dialog]").exists()).toBe(false);
    expect(document.body.style.overflow).toBe("auto");
  });

  it("keeps restore open on Escape while prepare or restore is running", async () => {
    let resolvePrepare!: (value: unknown) => void;
    const pendingPrepare = new Promise((resolve) => {
      resolvePrepare = resolve;
    });
    const { wrapper, replicationClient } = await mountPage();
    replicationClient.prepareRecoverableSnapshotRestore.mockReturnValueOnce(pendingPrepare);

    await wrapper.get('[data-action="restore"]').trigger("click");
    await wrapper.vm.$nextTick();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await wrapper.vm.$nextTick();
    expect(wrapper.find("[data-restore-dialog]").exists()).toBe(true);
    expect(document.body.style.overflow).toBe("hidden");

    resolvePrepare({
      snapshot: recoverableSnapshot,
      candidates: [
        {
          processId: "web",
          processName: "web",
          snapshotRevision: 7n,
          currentRevision: 11n,
          currentExists: true,
        },
      ],
      selectedStorageNodeId: "n1",
      ownerCopy: true,
    });
    await flushPromises();

    let resolveRestore!: (value: unknown) => void;
    const pendingRestore = new Promise((resolve) => {
      resolveRestore = resolve;
    });
    replicationClient.restoreRecoverableSnapshot.mockReturnValueOnce(pendingRestore);
    await wrapper.get('[data-action="confirm-restore"]').trigger("click");
    await wrapper.vm.$nextTick();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await wrapper.vm.$nextTick();
    expect(wrapper.find("[data-restore-dialog]").exists()).toBe(true);
    expect(document.body.style.overflow).toBe("hidden");

    resolveRestore({
      results: [{ processId: "web", status: "SUCCESS", newRevision: 12n, error: "" }],
      restoredFromNodeId: "n1",
      fetchedFromPeer: false,
    });
    await flushPromises();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await wrapper.vm.$nextTick();
    expect(wrapper.find("[data-restore-dialog]").exists()).toBe(false);
  });

  it("prepares again when the operator selects a different storage node", async () => {
    const { wrapper, replicationClient } = await mountPage();
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    replicationClient.prepareRecoverableSnapshotRestore.mockResolvedValue({
      snapshot: recoverableSnapshot,
      candidates: [
        {
          processId: "web",
          processName: "web",
          snapshotRevision: 7n,
          currentRevision: 12n,
          currentExists: true,
        },
      ],
      selectedStorageNodeId: "n2",
      ownerCopy: false,
    });
    await wrapper.get('[data-field="restore-storage"]').setValue("n2");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(replicationClient.prepareRecoverableSnapshotRestore).toHaveBeenLastCalledWith({
      sourceNodeId: "n1",
      snapshotId: "snap-n1",
      sha256: "abc123def4567890",
      storageNodeId: "n2",
    });
    expect((wrapper.get('[data-field="restore-storage"]').element as HTMLSelectElement).value).toBe("n2");
    expect((wrapper.get('input[name="expectedRevision"]').element as HTMLInputElement).value).toBe("12");
  });

  it("uses create CAS revision zero when a snapshot process is missing on the Owner", async () => {
    const { wrapper, replicationClient } = await mountPage({
      prepareRestore: {
        snapshot: recoverableSnapshot,
        candidates: [
          {
            processId: "worker",
            processName: "worker",
            snapshotRevision: 4n,
            currentExists: false,
          },
        ],
        selectedStorageNodeId: "n2",
        ownerCopy: false,
      },
    });
    await wrapper.get('[data-action="restore"]').trigger("click");
    await flushPromises();
    expect(wrapper.get("[data-restore-dialog]").text()).toContain("Not currently present");

    await wrapper.get('[data-action="confirm-restore"]').trigger("click");
    await flushPromises();
    expect(replicationClient.restoreRecoverableSnapshot).toHaveBeenCalledWith(
      expect.objectContaining({
        targets: [{ processId: "worker", expectedRevision: 0n }],
      }),
    );
  });

  it("does not present topology, policy, run, or snapshot errors as an empty catalog", async () => {
    const { wrapper } = await mountPage({
      topologyError: new Error("leader unavailable"),
      policiesError: new Error("leader unavailable"),
      runsError: new Error("leader unavailable"),
      snapshotsError: new Error("leader unavailable"),
    });
    expect(wrapper.get('[data-section="config"]').text()).toContain(
      "Replica topology is unreachable. This is not an empty catalog.",
    );
    expect(wrapper.get('[data-section="config"]').text()).not.toContain("No replica routes");
    expect(wrapper.get('[data-section="config"]').text()).toContain(
      "Replica policies are unreachable. This is not an empty catalog.",
    );
    const overview = wrapper.get('[data-section="overview"]');
    expect(overview.find(".freshness-unknown").exists()).toBe(true);
    expect(overview.find(".freshness-live").exists()).toBe(false);
    expect(overview.find("[data-route-count]").exists()).toBe(false);
    expect(overview.text()).toMatch(/not an empty catalog/i);
    expect(wrapper.get('[data-section="runs"]').text()).toContain(
      "Replication runs are unreachable. This is not an empty catalog.",
    );
    expect(wrapper.get('[data-section="runs"]').text()).not.toContain("No replication runs");
    expect(wrapper.get('[data-section="recovery"]').text()).toContain(
      "Recoverable snapshots are unreachable. This is not an empty catalog.",
    );
    expect(wrapper.get('[data-section="recovery"]').text()).not.toContain("No recoverable snapshots");
    expect(wrapper.find(".freshness-unknown, .freshness-stale").exists()).toBe(true);
  });

  it("does not present a policy query error as an empty route catalog when topology is available", async () => {
    const { wrapper } = await mountPage({
      policiesError: new Error("leader unavailable"),
    });
    const config = wrapper.get('[data-section="config"]');
    expect(config.text()).toContain("n1");
    expect(config.text()).toContain("Replica policies are unreachable. This is not an empty catalog.");
    expect(config.text()).not.toContain("No replica routes");
    expect(config.find(".freshness-unknown, .freshness-stale").exists()).toBe(true);
  });

  it("keeps last successful replication after a later failed run", async () => {
    const olderSuccess = {
      ...replicaRun,
      runId: "run-ok",
      status: "SUCCEEDED",
      startedAt: 1_700_000_000n,
      finishedAt: 1_700_000_050n,
      tasks: [
        {
          ...replicaTasks[0],
          runId: "run-ok",
          status: "SUCCEEDED",
          finishedAt: 1_700_000_050n,
        },
      ],
    };
    const laterFailed = {
      ...replicaRun,
      runId: "run-fail",
      status: "FAILED",
      startedAt: 1_700_000_100n,
      finishedAt: 1_700_000_120n,
      tasks: [
        {
          ...replicaTasks[1],
          runId: "run-fail",
          sourceNodeId: "n1",
          status: "FAILED",
          finishedAt: 1_700_000_120n,
        },
      ],
    };
    const { wrapper } = await mountPage({ runs: [laterFailed, olderSuccess] });
    expect(wrapper.get("[data-last-success]").text()).toContain(new Date(1_700_000_050 * 1000).toISOString());
    expect(wrapper.get("[data-last-success]").text()).not.toBe("—");
  });

  it("keeps last successful topology, routes, and snapshots when a later fetch fails", async () => {
    const { wrapper, replicationClient, queryClient } = await mountPage();
    expect(wrapper.get('[data-section="config"]').text()).toContain("n1");
    expect(wrapper.get('[data-section="runs"]').text()).toContain("run-partial");
    expect(wrapper.get('[data-section="recovery"]').text()).toContain("snap-n1");
    replicationClient.getTopology.mockRejectedValue(new Error("leader unavailable"));
    replicationClient.listPolicies.mockRejectedValue(new Error("leader unavailable"));
    replicationClient.listRuns.mockRejectedValue(new Error("leader unavailable"));
    replicationClient.listRecoverableSnapshots.mockRejectedValue(new Error("leader unavailable"));
    await queryClient.invalidateQueries({ queryKey: ["replica-topology"] });
    await queryClient.invalidateQueries({ queryKey: ["replica-policies"] });
    await queryClient.invalidateQueries({ queryKey: ["replica-runs"] });
    await queryClient.invalidateQueries({ queryKey: ["replica-snapshots"] });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.get('[data-section="config"]').text()).toContain("n1");
    expect(wrapper.get('[data-section="runs"]').text()).toContain("run-partial");
    expect(wrapper.get('[data-section="recovery"]').text()).toContain("snap-n1");
    expect(wrapper.text()).not.toContain("No replica routes");
    expect(wrapper.find(".freshness-stale").exists()).toBe(true);
  });

  it("hides generate, apply, retry, and verify without replication.manage", async () => {
    const { wrapper } = await mountPage({ permissions: ["replication.read"] });
    expect(wrapper.find('[data-action="generate"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="apply-draft"]').exists()).toBe(false);
    expect(wrapper.find('[data-action="verify"]').exists()).toBe(false);
    expect(wrapper.get('[data-section="overview"]').exists()).toBe(true);
    expect(wrapper.get('[data-section="runs"]').text()).toContain("run-partial");
    await wrapper.get('[data-run-id="run-partial"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.get("[data-run-detail]").exists()).toBe(true);
    expect(wrapper.find('[data-action="retry-failed"]').exists()).toBe(false);
  });

  it("keeps the preview dialog usable on small viewports", async () => {
    const { wrapper } = await mountPage({ policies: [{ ...replicaPolicy, routes: [] }] });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const dialog = wrapper.get("[data-preview-dialog]");
    expect(dialog.attributes("role")).toBe("dialog");
    expect(dialog.attributes("data-responsive")).toBe("true");
    expect(dialog.classes().join(" ")).toMatch(/preview-panel/);
    const style = (dialog.attributes("style") ?? "").toLowerCase();
    expect(style).toMatch(/min\(100%/);
    expect(style).toMatch(/max-height:\s*90vh/);
    expect(dialog.get("[data-preview-body]").classes().join(" ")).toMatch(/preview-body/);
    expect(dialog.get("[data-preview-routes]").exists()).toBe(true);
  });

  it("prefills cron and timezone when generating a draft", async () => {
    const { wrapper, replicationClient } = await mountPage();
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(replicationClient.generatePolicyDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        scheduleCron: "0 2 * * *",
        timezone: expect.any(String),
        enabled: true,
      }),
    );
    expect(wrapper.get('[data-field="schedule-cron"]').exists()).toBe(true);
    const timezone = wrapper.get('[data-field="timezone"]');
    expect(timezone.element.tagName).toBe("SELECT");
    expect((timezone.element as HTMLSelectElement).value).toBeTruthy();
    expect(timezone.text()).toMatch(/This browser|UTC|Asia\//);
  });

  it("lets the operator pick a timezone from IANA options", async () => {
    const { wrapper } = await mountPage({
      draft: { ...policyDraft, scheduleCron: "0 2 * * *", timezone: "Asia/Shanghai" },
    });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const timezone = wrapper.get("[data-preview-dialog]").get('[data-field="timezone"]');
    expect((timezone.element as HTMLSelectElement).value).toBe("Asia/Shanghai");
    const optionValues = timezone.findAll("option").map((option) => option.attributes("value"));
    expect(new Set(optionValues).size).toBe(optionValues.length);
    await timezone.setValue("UTC");
    await wrapper.vm.$nextTick();
    expect((timezone.element as HTMLSelectElement).value).toBe("UTC");
    expect(wrapper.get("[data-preview-dialog]").get("[data-next-run]").text()).toContain("UTC");
  });

  it("refreshes the draft hash after timezone edits before replacing the current policy", async () => {
    const refreshedDraft = {
      ...policyDraft,
      scheduleCron: "0 2 * * *",
      timezone: "Asia/Shanghai",
      draftHash: "draft-hash-shanghai",
    };
    const { wrapper, replicationClient } = await mountPage({
      draft: { ...policyDraft, scheduleCron: "0 2 * * *", timezone: "UTC" },
    });
    replicationClient.generatePolicyDraft
      .mockResolvedValueOnce({
        draft: { ...policyDraft, scheduleCron: "0 2 * * *", timezone: "UTC" },
      })
      .mockResolvedValueOnce({ draft: refreshedDraft });

    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const preview = wrapper.get("[data-preview-dialog]");
    await preview.get('[data-field="timezone"]').setValue("Asia/Shanghai");
    await preview.get('[data-route-source="0"]').setValue("n2");
    await preview.get('[data-route-target="0-0"]').setValue("n1");
    await wrapper.get('input[name="replaceCurrent"]').setValue(true);
    await wrapper.get('[data-action="apply-draft"]').trigger("click");
    await flushPromises();

    expect(replicationClient.generatePolicyDraft).toHaveBeenCalledTimes(2);
    expect(replicationClient.generatePolicyDraft).toHaveBeenLastCalledWith(
      expect.objectContaining({
        scheduleCron: "0 2 * * *",
        timezone: "Asia/Shanghai",
      }),
    );
    expect(replicationClient.applyPolicyDraft).toHaveBeenCalledWith(
      expect.objectContaining({
        expectedRevision: 2n,
        draftRevision: 7n,
        draftHash: "draft-hash-shanghai",
        draft: expect.objectContaining({
          timezone: "Asia/Shanghai",
          draftHash: "draft-hash-shanghai",
          routes: expect.arrayContaining([
            expect.objectContaining({ sourceNodeId: "n2", targetNodeIds: ["n1"] }),
          ]),
        }),
      }),
    );
  });

  it("requires another review when topology changes while refreshing an edited draft", async () => {
    const refreshedDraft = {
      ...policyDraft,
      scheduleCron: "0 2 * * *",
      timezone: "Asia/Shanghai",
      routes: [{ sourceNodeId: "n2", targetNodeIds: ["n3"], warnings: [] as string[] }],
      draftRevision: 8n,
      draftHash: "draft-hash-topology-8",
    };
    const { wrapper, replicationClient } = await mountPage({
      draft: { ...policyDraft, scheduleCron: "0 2 * * *", timezone: "UTC" },
    });
    replicationClient.generatePolicyDraft
      .mockResolvedValueOnce({
        draft: { ...policyDraft, scheduleCron: "0 2 * * *", timezone: "UTC" },
      })
      .mockResolvedValueOnce({ draft: refreshedDraft });

    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-field="timezone"]').setValue("Asia/Shanghai");
    await wrapper.get('input[name="replaceCurrent"]').setValue(true);
    await wrapper.get('[data-action="apply-draft"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(replicationClient.generatePolicyDraft).toHaveBeenCalledTimes(2);
    expect(replicationClient.applyPolicyDraft).not.toHaveBeenCalled();
    expect(wrapper.get("[data-preview-dialog]").text()).toContain("n2");
    expect(wrapper.text()).toMatch(/topology changed/i);
    expect((wrapper.get('input[name="replaceCurrent"]').element as HTMLInputElement).checked).toBe(false);
  });

  it("keeps timezone editable without cron because retention still uses it", async () => {
    const { wrapper } = await mountPage({
      draft: { ...policyDraft, scheduleCron: "0 2 * * *", timezone: "Asia/Shanghai" },
    });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const preview = wrapper.get("[data-preview-dialog]");
    await preview.get('[data-field="schedule-cron"]').setValue("");
    await flushPromises();
    await wrapper.vm.$nextTick();
    const updatedPreview = wrapper.get("[data-preview-dialog]");
    expect(updatedPreview.get("[data-next-run]").text()).toMatch(/manual/i);
    expect(updatedPreview.get("[data-timezone-hint]").text()).toMatch(/retention/i);
    expect((updatedPreview.get('[data-field="timezone"]').element as HTMLSelectElement).disabled).toBe(false);
  });

  it("shows manual-only when cron is cleared in preview", async () => {
    const { wrapper } = await mountPage({
      draft: { ...policyDraft, scheduleCron: "0 2 * * *" },
    });
    await wrapper.get('[data-action="generate"]').trigger("click");
    await flushPromises();
    await wrapper.vm.$nextTick();
    await wrapper.get('[data-field="schedule-cron"]').setValue("");
    await wrapper.vm.$nextTick();
    expect(wrapper.get("[data-preview-dialog]").get("[data-next-run]").text()).toMatch(/manual/i);
  });

  it("starts a run without a primary backup run id", async () => {
    const { wrapper, replicationClient } = await mountPage();
    expect(wrapper.find('input[name="primaryRunId"]').exists()).toBe(false);
    await wrapper.get('[data-action="start-run"]').trigger("click");
    await flushPromises();
    expect(replicationClient.startRun).toHaveBeenCalledWith(
      expect.objectContaining({ policyId: "rep-1" }),
    );
    const arg = replicationClient.startRun.mock.calls[0][0] as { primaryRunId?: string };
    expect(arg.primaryRunId ?? "").toBe("");
  });

  it("shows schedule disabled instead of next run when policy is disabled", async () => {
    const { wrapper } = await mountPage({
      policies: [{ ...replicaPolicy, enabled: false, scheduleCron: "0 2 * * *" }],
    });
    expect(wrapper.get("[data-next-run]").text()).toMatch(/disabled/i);
    expect(wrapper.get("[data-next-run]").text()).not.toContain("0 2 * * *");
  });
});
