import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ErrorInfoSchema, ProcessSpecSchema } from "../gen/procmesh/v1/api_pb";
import { session } from "../lib/session";
import ProcessConfigPanel from "./ProcessConfigPanel.vue";

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
  const queryClient = new QueryClient({
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
      plugins: [[VueQueryPlugin, { queryClient }]],
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
});
