import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ProcessLogsPanel from "./ProcessLogsPanel.vue";

let i18n: typeof i18next;

beforeEach(async () => {
  i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {},
      },
    },
  });
});

const mounted: Array<{ unmount: () => void }> = [];

async function mountProcessLogsPanel(
  instances: string[] = [],
  extra: { redirectStderr?: boolean; logPathPending?: boolean } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const logClient = {
    tailLogs: vi.fn().mockResolvedValue({ lines: ["log line 1", "log line 2"] }),
    streamLogs: vi.fn().mockResolvedValue([]),
    downloadLogs: vi.fn().mockResolvedValue([]),
  };
  const wrapper = mount(ProcessLogsPanel, {
    props: {
      idOrName: "test-process",
      targetNodeId: "node1",
      instances,
      redirectStderr: extra.redirectStderr,
      logPathPending: extra.logPathPending,
    },
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
      ],
      provide: { logClient },
    },
  });
  mounted.push(wrapper);
  await flushPromises();
  await wrapper.vm.$nextTick();
  return wrapper;
}

afterEach(() => {
  while (mounted.length) {
    mounted.pop()?.unmount();
  }
});

describe("ProcessLogsPanel i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processLogs: {
        title: "Logs",
        stream: "Stream",
        instance: "Instance",
        allInstances: "All instances",
        stdout: "stdout",
        stderr: "stderr",
        tail: "Tail 100",
        streamButton: "Stream",
        download: "Download",
      },
    });

    const wrapper = await mountProcessLogsPanel(["inst1", "inst2"]);
    const text = wrapper.text();
    expect(text).toContain("Logs");
    expect(text).toContain("Stream");
    expect(text).toContain("Instance");
    expect(text).toContain("All instances");
    expect(text).toContain("Tail 100");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      processLogs: {
        title: "日志",
        stream: "流",
        instance: "实例",
        allInstances: "所有实例",
        stdout: "标准输出",
        stderr: "标准错误",
        tail: "查看最后100行",
        streamButton: "流式查看",
        download: "下载",
      },
    });

    const wrapper = await mountProcessLogsPanel(["inst1", "inst2"]);
    const text = wrapper.text();
    expect(text).toContain("日志");
    expect(text).toContain("流");
    expect(text).toContain("实例");
    expect(text).toContain("所有实例");
    expect(text).toContain("查看最后100行");
  });
});

describe("ProcessLogsPanel stderr merge hint", () => {
  async function addMergeCopy() {
    await i18n.addResourceBundle("en", "common", {
      processLogs: {
        title: "Logs",
        stream: "Stream",
        instance: "Instance",
        allInstances: "All instances",
        stdout: "stdout",
        stderr: "stderr",
        tail: "Tail 100",
        streamButton: "Stream",
        download: "Download",
        stderrMerged: "stderr is merged into stdout",
        stderrMergePending: "stderr will merge into stdout after restart",
      },
    }, true, true);
  }

  it("keeps the stderr tab clickable and shows the merged hint from latest spec", async () => {
    await addMergeCopy();
    const wrapper = await mountProcessLogsPanel(["inst1"], { redirectStderr: true, logPathPending: false });
    const stderr = wrapper.get("select").findAll("option").find((option) => option.element.value === "stderr");
    expect(stderr).toBeDefined();
    expect(stderr!.attributes("disabled")).toBeUndefined();

    await wrapper.get("select").setValue("stderr");
    await wrapper.vm.$nextTick();

    expect(wrapper.get("[role='status']").text()).toBe("stderr is merged into stdout");
  });

  it("shows a pending merge hint when latest spec redirects but the path is pending", async () => {
    await addMergeCopy();
    const wrapper = await mountProcessLogsPanel(["inst1"], { redirectStderr: true, logPathPending: true });
    await wrapper.get("select").setValue("stderr");
    await wrapper.vm.$nextTick();

    expect(wrapper.get("[role='status']").text()).toBe("stderr will merge into stdout after restart");
  });
});
