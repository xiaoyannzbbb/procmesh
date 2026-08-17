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
    expect(wrapper.find("svg").attributes("viewBox") ?? wrapper.find("svg").attributes("viewbox")).toBe(
      "0 0 600 160",
    );
  });
});
