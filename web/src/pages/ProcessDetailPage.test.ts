import { VueQueryPlugin, QueryClient } from "@tanstack/vue-query";
import { flushPromises, mount } from "@vue/test-utils";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRouter, createMemoryHistory } from "vue-router";
import ProcessDetailPage from "./ProcessDetailPage.vue";

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

async function mountProcessDetailPage(process: unknown = null, nodes: unknown[] = []) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const processClient = {
    getProcess: vi.fn().mockResolvedValue({ process }),
    startProcess: vi.fn().mockResolvedValue({}),
    stopProcess: vi.fn().mockResolvedValue({}),
    restartProcess: vi.fn().mockResolvedValue({}),
    killProcess: vi.fn().mockResolvedValue({}),
  };
  const nodeClient = {
    listNodes: vi.fn().mockResolvedValue({ nodes }),
  };
  const metricsClient = {
    getProcessMetrics: vi.fn().mockResolvedValue({}),
  };

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/processes/:idOrName",
        component: ProcessDetailPage,
      },
    ],
  });

  await router.push("/processes/test-process?node=node1");
  await router.isReady();

  const wrapper = mount(ProcessDetailPage, {
    global: {
      plugins: [
        [VueQueryPlugin, { queryClient }],
        [I18NextVue, { i18next: i18n }],
        router,
      ],
      provide: { processClient, nodeClient, metricsClient },
      stubs: {
        RouterLink: {
          template: '<a><slot /></a>',
        },
        ProcessConfigPanel: {
          template: '<div>ProcessConfigPanel</div>',
        },
        ProcessLogsPanel: {
          template: '<div>ProcessLogsPanel</div>',
        },
      },
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

describe("ProcessDetailPage i18n", () => {
  it("should render in English", async () => {
    await i18n.changeLanguage("en");
    await i18n.addResourceBundle("en", "common", {
      processDetail: {
        back: "← Processes",
        loading: "Loading…",
        actions: {
          start: "Start",
          stop: "Stop",
          restart: "Restart",
          forceStop: "Force Stop",
        },
        tabs: {
          overview: "Overview",
          config: "Config",
          logs: "Logs",
        },
        process: {
          title: "Process",
          name: "Name",
          processId: "Process ID",
        },
        instances: {
          title: "Instances",
        },
      },
    });

    const wrapper = await mountProcessDetailPage();
    const text = wrapper.text();
    expect(text).toContain("← Processes");
    expect(text).toContain("Start");
    expect(text).toContain("Stop");
    expect(text).toContain("Restart");
  });

  it("should render in Chinese", async () => {
    await i18n.changeLanguage("zh");
    await i18n.addResourceBundle("zh", "common", {
      processDetail: {
        back: "← 进程",
        loading: "加载中…",
        actions: {
          start: "启动",
          stop: "停止",
          restart: "重启",
          forceStop: "强制停止",
        },
        tabs: {
          overview: "概览",
          config: "配置",
          logs: "日志",
        },
        process: {
          title: "进程",
          name: "名称",
          processId: "进程ID",
        },
        instances: {
          title: "实例",
        },
      },
    });

    const wrapper = await mountProcessDetailPage();
    const text = wrapper.text();
    expect(text).toContain("← 进程");
    expect(text).toContain("启动");
    expect(text).toContain("停止");
    expect(text).toContain("重启");
  });
});
