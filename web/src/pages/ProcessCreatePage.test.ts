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
            editor: {
              modeLabel: "Mode",
              mode: { form: "Form", yaml: "YAML" },
              errorSummary: "Fix the following fields",
              validation: { invalidName: "invalid name", required: "required" },
            },
          },
          status: { live: "Live", stale: "Stale", unknown: "Unknown" },
          actions: { cancel: "Cancel" },
          processes: {
            eyebrow: "Cluster processes",
            create: {
              title: "Create process",
              hint: "hint",
              back: "Processes",
              spec: "Process spec",
              submit: "Create",
              submitBusy: "Creating…",
              owners: "Owner nodes",
              ownersHint: "hint",
              ownersSelected: "{{count}} selected",
              ownersLoading: "Loading nodes…",
              ownersError: "Could not load nodes: {{detail}}",
              ownerDisabled: "This node does not allow remote process creation",
              ownerUnknown: "unknown",
              ownerNodeId: "Node ID",
              ownerProcesses: "{{count}} processes",
              needOwner: "need owner",
              noNodes: "no nodes",
              noPermission: "no permission",
              commentHint: "Optional note",
              yamlHint: "Invalid YAML cannot switch back",
              errorTitle: "Could not create the process",
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

  it("renders owner cards with freshness and a labeled create form", async () => {
    const wrapper = await mountCreatePage([
      {
        nodeId: "n-allow",
        hostname: "allow-host",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        disableRemoteCreate: false,
      },
    ]);

    expect(wrapper.find("form.create-form").exists()).toBe(true);
    expect(wrapper.find(".back").attributes("href")).toBe("/processes");
    expect(wrapper.get("#process-create-owners").text()).toContain("allow-host");
    expect(wrapper.get("#process-create-owners").text()).toContain("Live");
    expect(wrapper.find("#process-create-comment").exists()).toBe(true);
    expect(wrapper.find("label[for='process-create-comment']").exists()).toBe(true);

    const modeButtons = wrapper.findAll(".editor-mode-button");
    expect(modeButtons).toHaveLength(2);
    expect(modeButtons[0].attributes("aria-pressed")).toBe("true");
    expect(modeButtons[1].attributes("aria-pressed")).toBe("false");
    expect(wrapper.find("#process-config-ownerAgentId").exists()).toBe(false);
    expect(wrapper.find("#process-config-processId").exists()).toBe(false);
    expect(wrapper.find("#process-config-latestRevision").exists()).toBe(false);
    expect(wrapper.find("#process-config-name").exists()).toBe(true);
  });

  it("shows YAML validation errors after submit", async () => {
    const wrapper = await mountCreatePage([
      {
        nodeId: "n-allow",
        hostname: "allow-host",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        disableRemoteCreate: false,
      },
    ]);
    await wrapper.findAll(".editor-mode-button")[1].trigger("click");
    await wrapper.get("form.create-form").trigger("submit");
    expect(wrapper.get("[data-error-summary]").text()).toContain("Fix the following fields");
  });

  it("lets an incomplete form switch into YAML and back without errors", async () => {
    const wrapper = await mountCreatePage([
      {
        nodeId: "n-allow",
        hostname: "allow-host",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        disableRemoteCreate: false,
      },
    ]);
    const [formButton, yamlButton] = wrapper.findAll(".editor-mode-button");
    await yamlButton.trigger("click");
    const yaml = (wrapper.get("#process-create-yaml").element as HTMLTextAreaElement).value;
    expect(yaml.trim()).not.toBe("{}");
    expect(yaml).toContain("name:");
    expect(yaml).toContain("command:");
    expect(yaml).toContain("working_directory:");
    expect(yaml).toContain("restart:");
    expect(yaml).toContain("health:");
    expect(yaml).toContain("log:");
    expect(yaml).toContain("resources:");
    expect(yaml).toContain("dependencies:");
    expect(yaml).not.toContain("owner_agent_id:");
    expect(yaml).not.toContain("process_id:");
    expect(wrapper.find("[data-error-summary]").exists()).toBe(false);

    await formButton.trigger("click");
    expect(wrapper.find("#process-create-yaml").exists()).toBe(false);
    expect(wrapper.find("#process-config-name").exists()).toBe(true);
    expect(wrapper.find("[data-error-summary]").exists()).toBe(false);
    expect(formButton.attributes("aria-pressed")).toBe("true");
  });

  it("marks a selected owner card", async () => {
    const wrapper = await mountCreatePage([
      {
        nodeId: "n-allow",
        hostname: "allow-host",
        state: "ALIVE",
        lastUpdatedUnixMs: Date.now(),
        disableRemoteCreate: false,
      },
    ]);
    const row = wrapper.get(".owner-row");
    expect(row.classes()).toContain("selected");
    await row.get('input[type="checkbox"]').setValue(false);
    expect(row.classes()).not.toContain("selected");
  });
});
