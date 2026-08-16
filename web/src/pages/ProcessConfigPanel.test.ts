import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ErrorInfoSchema, ProcessSpecSchema } from "../gen/procmesh/v1/api_pb";
import { session } from "../lib/session";
import ProcessConfigPanel from "./ProcessConfigPanel.vue";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          processConfig: {
            conflictBanner: "409 Conflict — reload and retry",
            loading: "Loading…",
            config: {
              title: "Config",
              reload: "Reload",
              processId: "Process ID",
              latestRevision: "Latest Revision",
              readOnlyNote: "process_id and latest_revision are read-only.",
              specLabel: "ProcessSpec JSON",
              commentLabel: "Comment",
              save: "Save",
            },
            history: {
              title: "History",
              loading: "Loading history…",
              noRevisions: "No revisions",
              table: {
                select: "Select",
                revision: "Revision",
                operator: "Operator",
                time: "Time",
                comment: "Comment",
                rollback: "Rollback",
              },
              diff: {
                title: "Diff",
                loading: "Loading diff…",
                empty: "(empty)",
              },
              rollbackConfirm: "Rollback to revision {revision}? This writes a new revision.",
            },
          },
        },
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

function conflictError(): ConnectError {
  return new ConnectError("revision mismatch", Code.FailedPrecondition, undefined, [
    { desc: ErrorInfoSchema, value: { code: "CONFLICT", message: "revision mismatch" } },
  ]);
}

function sampleSpec() {
  return create(ProcessSpecSchema, {
    processId: "p1",
    name: "web",
    command: "sleep",
    latestRevision: 3n,
  });
}

type MountOpts = {
  updateConfig?: ReturnType<typeof vi.fn>;
  spec?: ReturnType<typeof sampleSpec>;
  queryClient?: QueryClient;
  revisions?: Array<{
    revision: bigint;
    operator: string;
    timestampUnixMs: bigint;
    comment: string;
    diff?: string;
  }>;
  diff?: string;
};

async function mountPanel(opts: MountOpts | ReturnType<typeof vi.fn> = {}) {
  const resolved = typeof opts === "function" ? { updateConfig: opts } : opts;
  const updateConfig = resolved.updateConfig ?? vi.fn().mockRejectedValue(conflictError());
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: ["process.config.update", "process.config.read"],
  };
  const queryClient =
    resolved.queryClient ??
    new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
  const spec = resolved.spec ?? sampleSpec();
  const configClient = {
    getConfig: vi.fn().mockResolvedValue({ spec }),
    updateConfig,
    history: vi.fn().mockResolvedValue({ revisions: resolved.revisions ?? [] }),
    diff: vi.fn().mockResolvedValue({ diff: resolved.diff ?? "" }),
    rollback: vi.fn(),
  };
  const wrapper = mount(ProcessConfigPanel, {
    props: { idOrName: "web", targetNodeId: "n1" },
    global: {
      plugins: [[VueQueryPlugin, { queryClient }], [I18NextVue, { i18next: i18n }]],
      provide: { configClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return { wrapper, updateConfig, configClient, queryClient };
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
  session.value = null;
});

describe("ProcessConfigPanel", () => {
  it("shows 409 Conflict when UpdateConfig throws CONFLICT", async () => {
    const { wrapper, updateConfig } = await mountPanel();
    await wrapper.get("form.config-form").trigger("submit");
    await flushPromises();
    expect(updateConfig).toHaveBeenCalledTimes(1);
    const [req] = updateConfig.mock.calls[0];
    expect(req.expectedRevision).toBe(3n);
    expect(req.meta?.operationId).toBeTruthy();
    expect(wrapper.text()).toContain("409 Conflict");
    expect(updateConfig).toHaveBeenCalledTimes(1);
  });

  it("shows diff text after selecting two revisions", async () => {
    const { wrapper, configClient } = await mountPanel({
      updateConfig: vi.fn().mockResolvedValue({ spec: sampleSpec() }),
      revisions: [
        { revision: 1n, operator: "ada", timestampUnixMs: 1n, comment: "first" },
        { revision: 2n, operator: "bob", timestampUnixMs: 2n, comment: "second" },
      ],
      diff: "--- old\n+++ new",
    });
    const boxes = wrapper.findAll('input[type="checkbox"]');
    expect(boxes.length).toBe(2);
    await boxes[0].setValue(true);
    await boxes[1].setValue(true);
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(configClient.diff).toHaveBeenCalled();
    expect(wrapper.text()).not.toContain("Loading diff…");
    expect(wrapper.text()).toContain("--- old");
    expect(wrapper.text()).toContain("+++ new");
  });

  it("does not overwrite textarea or expected_revision on refetch while editing or after 409", async () => {
    const { wrapper, updateConfig, configClient, queryClient } = await mountPanel();
    const textarea = wrapper.get("textarea");
    const edited = textarea.element.value.replace('"name": "web"', '"name": "api"');
    await textarea.setValue(edited);

    const newer = create(ProcessSpecSchema, {
      processId: "p1",
      name: "other",
      command: "sleep",
      latestRevision: 4n,
    });
    configClient.getConfig.mockResolvedValue({ spec: newer });
    await queryClient.invalidateQueries({ queryKey: ["process-config"] });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.get("textarea").element.value).toBe(edited);

    await wrapper.get("form.config-form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("409 Conflict");

    await queryClient.invalidateQueries({ queryKey: ["process-config"] });
    await flushPromises();
    await wrapper.vm.$nextTick();
    expect(wrapper.get("textarea").element.value).toBe(edited);
    expect(wrapper.text()).toContain("409 Conflict");

    await wrapper.get("form.config-form").trigger("submit");
    await flushPromises();
    expect(updateConfig).toHaveBeenCalledTimes(2);
    expect(updateConfig.mock.calls[0][0].expectedRevision).toBe(3n);
    expect(updateConfig.mock.calls[1][0].expectedRevision).toBe(3n);
  });

  it("remount after save uses new latest as expected_revision", async () => {
    const saved = create(ProcessSpecSchema, {
      processId: "p1",
      name: "api",
      command: "sleep",
      latestRevision: 4n,
    });
    const updateConfig = vi.fn().mockResolvedValue({ spec: saved });
    const first = await mountPanel({ updateConfig });
    const edited = first.wrapper.get("textarea").element.value.replace('"name": "web"', '"name": "api"');
    await first.wrapper.get("textarea").setValue(edited);
    await first.wrapper.get("form.config-form").trigger("submit");
    await flushPromises();
    expect(first.wrapper.get("textarea").element.value).toContain('"name": "api"');
    expect(first.wrapper.text()).toContain("Latest Revision4");

    first.wrapper.unmount();
    const idx = mounted.indexOf(first.wrapper);
    if (idx >= 0) {
      mounted.splice(idx, 1);
    }

    const second = await mountPanel({
      queryClient: first.queryClient,
      updateConfig,
      spec: sampleSpec(),
    });
    expect(second.wrapper.get("textarea").element.value).toContain('"name": "api"');
    expect(second.wrapper.text()).toContain("Latest Revision4");
    await second.wrapper.get("form.config-form").trigger("submit");
    await flushPromises();
    expect(updateConfig.mock.calls.at(-1)?.[0].expectedRevision).toBe(4n);
  });
});

describe("ProcessConfigPanel i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processConfig: {
        loading: "Loading…",
        config: {
          title: "Config",
          reload: "Reload",
          processId: "Process ID",
          latestRevision: "Latest Revision",
          save: "Save",
        },
        history: {
          title: "History",
          loading: "Loading history…",
          table: {
            select: "Select",
            revision: "Revision",
            operator: "Operator",
            time: "Time",
            comment: "Comment",
            rollback: "Rollback",
          },
        },
      },
    });

    const { wrapper } = await mountPanel();
    const text = wrapper.text();
    expect(text).toContain("Config");
    expect(text).toContain("Reload");
    expect(text).toContain("Process ID");
    expect(text).toContain("Latest Revision");
    expect(text).toContain("History");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      processConfig: {
        loading: "加载中…",
        config: {
          title: "配置",
          reload: "重新加载",
          processId: "进程ID",
          latestRevision: "最新版本",
          save: "保存",
        },
        history: {
          title: "历史",
          loading: "加载历史中…",
          table: {
            select: "选择",
            revision: "版本",
            operator: "操作者",
            time: "时间",
            comment: "备注",
            rollback: "回滚",
          },
        },
      },
    });

    const { wrapper } = await mountPanel();
    const text = wrapper.text();
    expect(text).toContain("配置");
    expect(text).toContain("重新加载");
    expect(text).toContain("进程ID");
    expect(text).toContain("最新版本");
    expect(text).toContain("历史");
  });
});
