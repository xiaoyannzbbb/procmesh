import { QueryClient, VueQueryPlugin } from "@tanstack/vue-query";
import { createApp } from "vue";
import App from "./App.vue";
import { router } from "./router";
import { i18n, i18nReady, I18NextVue } from "./lib/i18n";
import "./style.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
    },
  },
});

async function bootstrap(): Promise<void> {
  try {
    await i18nReady;
  } catch {
    console.error("Failed to initialize translations");
  }

  createApp(App)
    .use(router)
    .use(VueQueryPlugin, { queryClient })
    .use(I18NextVue, { i18next: i18n })
    .mount("#app");
}

void bootstrap();
