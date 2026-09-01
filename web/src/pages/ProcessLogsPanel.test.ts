import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
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
  extra: {
    redirectStderr?: boolean;
    logPathPending?: boolean;
    logClient?: {
      tailLogs?: ReturnType<typeof vi.fn>;
      streamLogs?: ReturnType<typeof vi.fn>;
      downloadLogs?: ReturnType<typeof vi.fn>;
    };
  } = {},
) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const logClient = {
    tailLogs: extra.logClient?.tailLogs ?? vi.fn().mockResolvedValue({ lines: ["log line 1", "log line 2"] }),
    streamLogs: extra.logClient?.streamLogs ?? vi.fn().mockResolvedValue([]),
    downloadLogs: extra.logClient?.downloadLogs ?? vi.fn().mockResolvedValue([]),
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

function createChunkStream() {
  const pending: Array<{ data: Uint8Array; eof: boolean }> = [];
  let notify: (() => void) | null = null;
  let finished = false;

  async function* iterate() {
    while (!finished || pending.length > 0) {
      if (pending.length === 0) {
        await new Promise<void>((resolve) => {
          notify = resolve;
        });
        continue;
      }
      yield pending.shift()!;
    }
  }

  return {
    iterate,
    push(text: string) {
      pending.push({ data: new TextEncoder().encode(text), eof: false });
      notify?.();
      notify = null;
    },
    end() {
      finished = true;
      notify?.();
      notify = null;
    },
  };
}

function stubLogWindow(
  el: HTMLElement,
  metrics: { clientHeight: number; scrollHeight: number; scrollTop: number },
) {
  let scrollTop = metrics.scrollTop;
  let scrollHeight = metrics.scrollHeight;
  Object.defineProperty(el, "clientHeight", {
    configurable: true,
    get: () => metrics.clientHeight,
  });
  Object.defineProperty(el, "scrollHeight", {
    configurable: true,
    get: () => scrollHeight,
  });
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => scrollTop,
    set: (value: number) => {
      scrollTop = Number(value);
    },
  });
  return {
    get scrollTop() {
      return scrollTop;
    },
    set scrollTop(value: number) {
      scrollTop = value;
    },
    setScrollHeight(value: number) {
      scrollHeight = value;
    },
  };
}

async function addStreamCopy() {
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
      stop: "Stop",
      download: "Download",
    },
  }, true, true);
}

describe("ProcessLogsPanel stream follow", () => {
  it("scrolls to the latest line when the window is already at the bottom", async () => {
    await addStreamCopy();
    const stream = createChunkStream();
    const wrapper = await mountProcessLogsPanel(["inst1"], {
      logClient: { streamLogs: vi.fn().mockReturnValue(stream.iterate()) },
    });
    const windowEl = wrapper.get(".log-window").element as HTMLElement;
    const stub = stubLogWindow(windowEl, { clientHeight: 100, scrollHeight: 400, scrollTop: 300 });

    await wrapper.findAll("button").find((btn) => btn.text() === "Stream")!.trigger("click");
    await wrapper.vm.$nextTick();

    stream.push("new log line\n");
    stub.setScrollHeight(520);
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(stub.scrollTop).toBe(520);
    stream.end();
  });

  it("does not steal the scroll position when the user has scrolled up", async () => {
    await addStreamCopy();
    const stream = createChunkStream();
    const wrapper = await mountProcessLogsPanel(["inst1"], {
      logClient: { streamLogs: vi.fn().mockReturnValue(stream.iterate()) },
    });
    const windowEl = wrapper.get(".log-window").element as HTMLElement;
    const stub = stubLogWindow(windowEl, { clientHeight: 100, scrollHeight: 400, scrollTop: 300 });

    await wrapper.findAll("button").find((btn) => btn.text() === "Stream")!.trigger("click");
    await wrapper.vm.$nextTick();

    stub.scrollTop = 40;
    await wrapper.get(".log-window").trigger("scroll");

    stream.push("new log line\n");
    stub.setScrollHeight(520);
    await flushPromises();
    await wrapper.vm.$nextTick();

    expect(stub.scrollTop).toBe(40);
    stream.end();
  });
});

describe("ProcessLogsPanel layout", () => {
  it("lets the log window grow with remaining page height instead of capping at 28rem", () => {
    const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "ProcessLogsPanel.vue"), "utf8");
    expect(src).not.toMatch(/max-height:\s*28rem/);
    expect(src).toMatch(/\.log-window[\s\S]*flex:\s*1/);
  });
});
