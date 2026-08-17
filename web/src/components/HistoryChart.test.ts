import { mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import i18next from "i18next";
import I18NextVue from "i18next-vue";
import HistoryChart from "./HistoryChart.vue";

const STALE_COPY = "History unavailable (STALE). Live gossip summary is not a chart.";

async function setupI18n() {
  const i18n = i18next.createInstance();
  await i18n.init({
    lng: "en",
    fallbackLng: "en",
    resources: {
      en: {
        common: {
          status: { live: "LIVE", stale: "STALE", unknown: "UNKNOWN" },
          metricsHistory: {
            stale: STALE_COPY,
            empty: "No samples in this range",
            summary: "{{title}} last {{last}}, range {{min}} to {{max}}",
          },
        },
      },
    },
  });
  return i18n;
}

describe("HistoryChart", () => {
  it("renders STALE copy and no polyline", async () => {
    const i18n = await setupI18n();
    const wrapper = mount(HistoryChart, {
      props: {
        title: "CPU %",
        points: [
          { t: 0, v: 10 },
          { t: 60, v: 11 },
        ],
        stepSec: 60,
        stale: true,
      },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]],
      },
    });

    expect(wrapper.text()).toContain(STALE_COPY);
    expect(wrapper.findAll("polyline")).toHaveLength(0);

    const badge = wrapper.find(".freshness-stale");
    expect(badge.exists()).toBe(true);
    const badgeClass = badge.classes().join(" ").toLowerCase();
    expect(badgeClass).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
    const html = wrapper.html().toLowerCase();
    expect(html).not.toMatch(/green|#d1fae5|#10a37f|bg-green/);
  });

  it("renders two polylines when a minute is missing", async () => {
    const i18n = await setupI18n();
    const wrapper = mount(HistoryChart, {
      props: {
        title: "CPU %",
        points: [
          { t: 0, v: 10 },
          { t: 60, v: 11 },
          { t: 180, v: 12 },
        ],
        stepSec: 60,
        stale: false,
      },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]],
      },
    });

    expect(wrapper.findAll("polyline")).toHaveLength(2);
    expect(wrapper.findAll("path.area")).toHaveLength(2);
  });

  it("shows the last sample and zooms a low-percent series off the floor", async () => {
    const i18n = await setupI18n();
    const wrapper = mount(HistoryChart, {
      props: {
        title: "Memory",
        unit: "percent",
        kind: "memory",
        points: [
          { t: 0, v: 6.2 },
          { t: 60, v: 6.3 },
          { t: 120, v: 6.4 },
        ],
        stepSec: 60,
        stale: false,
      },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]],
      },
    });

    expect(wrapper.get(".chart-value").text()).toBe("6.4%");
    expect(wrapper.text()).toContain("6.2%");
    expect(wrapper.text()).not.toContain("100%");
    const svg = wrapper.get("svg");
    expect(svg.attributes("aria-label")).toContain("6.4%");
    const lastPoint = wrapper.get("polyline").attributes("points")?.trim().split(" ").at(-1);
    const lastY = Number(lastPoint?.split(",")[1]);
    expect(lastY).toBeLessThan(90);
  });

  it("shows a hover tooltip for the nearest sample", async () => {
    const i18n = await setupI18n();
    const wrapper = mount(HistoryChart, {
      props: {
        title: "CPU %",
        unit: "percent",
        kind: "cpu",
        points: [
          { t: 1_700_000_000, v: 10 },
          { t: 1_700_000_060, v: 11 },
        ],
        stepSec: 60,
        stale: false,
      },
      global: {
        plugins: [[I18NextVue, { i18next: i18n }]],
      },
    });

    const svg = wrapper.get("svg");
    Object.defineProperty(svg.element, "getBoundingClientRect", {
      value: () => ({ left: 0, top: 0, width: 200, height: 120, right: 200, bottom: 120 }),
    });
    await svg.trigger("pointermove", { clientX: 200, clientY: 20 });
    expect(wrapper.get("[data-testid=chart-tooltip]").text()).toContain("11%");
  });
});
