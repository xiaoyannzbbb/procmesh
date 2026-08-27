import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { session } from "../lib/session";
import ProcessCreatePage from "./ProcessCreatePage.vue";

const Blank = defineComponent({ setup: () => () => h("div") });

let i18n: typeof i18next;
const mounted: Array<{ unmount: () => void }> = [];

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          processDetail: { back: "← Processes" },
          processConfig: {
            config: { specLabel: "YAML", commentLabel: "Comment", invalidYaml: "invalid" },
            editor: { modeLabel: "Mode", mode: { form: "Form", yaml: "YAML" } },
          },
          processes: {
            create: {
              title: "Create process",
              hint: "hint",
              submit: "Create",
              owners: "Owner nodes",
              ownersHint: "hint",
              ownerDisabled: "This node does not allow remote process creation",
              ownerUnknown: "unknown",
              needOwner: "need owner",
              noNodes: "no nodes",
              noPermission: "no permission",
            },
          },
        },
      },
    },
  });
});

afterEach(() => {
  session.value = null;
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
});

async function mountCreatePage(nodes: unknown[]) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: ["process.create"],
  };
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/processes", component: Blank },
      { path: "/processes/new", component: ProcessCreatePage },
      { path: "/processes/:idOrName", component: Blank },
    ],
  });
  await router.push("/processes/new");
  await router.isReady();
  const wrapper = mount(ProcessCreatePage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        router,
      ],
      provide: {
        nodeClient: { listNodes: vi.fn().mockResolvedValue({ nodes }) },
        processClient: { applyProcess: vi.fn() },
      },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

describe("ProcessCreatePage", () => {
  it("shows disabled owner nodes that reject remote create", async () => {
    const wrapper = await mountCreatePage([
      {
        nodeId: "n-allow",
        hostname: "allow-host",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        disableRemoteCreate: false,
      },
      {
        nodeId: "n-deny",
        hostname: "deny-host",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        disableRemoteCreate: true,
      },
    ]);
    const inputs = wrapper.findAll('.owner-row input[type="checkbox"]');
    expect(inputs).toHaveLength(2);
    expect((inputs[0].element as HTMLInputElement).disabled).toBe(false);
    expect((inputs[1].element as HTMLInputElement).disabled).toBe(true);
    expect(wrapper.text()).toContain("This node does not allow remote process creation");
  });
});
