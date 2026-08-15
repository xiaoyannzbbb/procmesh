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

async function mountPanel(updateConfig = vi.fn().mockRejectedValue(conflictError())) {
  session.value = {
    userId: "u1",
    username: "admin",
    csrfToken: "csrf",
    permissions: ["process.config.update", "process.config.read"],
  };
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const spec = sampleSpec();
  const configClient = {
    getConfig: vi.fn().mockResolvedValue({ spec }),
    updateConfig,
    history: vi.fn().mockResolvedValue({ revisions: [] }),
    diff: vi.fn().mockResolvedValue({ diff: "" }),
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
  return { wrapper, updateConfig, configClient };
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
});
