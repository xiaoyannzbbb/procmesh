import { mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { useClusterBackupClient, useReplicationClient } from "../lib/rpc";
import { session } from "../lib/session";
import { router } from "../router";
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
] as const;

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
        },
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

async function mountPage(permissions: string[] = ["replication.read"]) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions,
  };
  const wrapper = mount(DisasterReplicaPage, {
    global: {
      plugins: [[I18NextVue, { i18next: i18n }]],
    },
  });
  mounted.push(wrapper);
  await wrapper.vm.$nextTick();
  return wrapper;
}

afterEach(() => {
  session.value = null;
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
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
    const resolved = router.resolve("/disaster-replica");
    expect(resolved.matched.some((record) => record.path === "/disaster-replica")).toBe(true);
    expect(resolved.matched.some((record) => record.components?.default === DisasterReplicaPage)).toBe(true);
  });

  it("mounts a permission-gated shell when replication.read is present", async () => {
    const wrapper = await mountPage(["replication.read"]);
    expect(wrapper.text()).toContain("Disaster replica");
    expect(wrapper.attributes("data-permission")).toBe("granted");
    expect(wrapper.text()).toContain("No data available");
  });

  it("shows a denied shell without replication.read", async () => {
    const wrapper = await mountPage(["backup.read"]);
    expect(wrapper.text()).toContain("Disaster replica");
    expect(wrapper.attributes("data-permission")).toBe("denied");
    expect(wrapper.text()).not.toContain("No data available");
  });
});
