import { defineComponent, h } from "vue";
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import AppShell from "./components/AppShell.vue";
import { loadSession } from "./lib/session";
import LoginPage from "./pages/LoginPage.vue";

const PlaceholderPage = defineComponent({
  name: "PlaceholderPage",
  setup() {
    return () => h("div");
  },
});

const routes: RouteRecordRaw[] = [
  { path: "/login", component: LoginPage, meta: { public: true } },
  {
    path: "/",
    component: AppShell,
    children: [
      { path: "", component: PlaceholderPage },
      { path: "nodes", component: PlaceholderPage },
      { path: "nodes/:id", component: PlaceholderPage },
      { path: "processes", component: PlaceholderPage },
      { path: "processes/:idOrName", component: PlaceholderPage },
      { path: "users", component: PlaceholderPage },
      { path: "roles", component: PlaceholderPage },
      { path: "audit", component: PlaceholderPage },
    ],
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});

router.beforeEach(async (to) => {
  const me = await loadSession();
  if (!me && to.path !== "/login") {
    return { path: "/login", query: { next: to.fullPath } };
  }
  if (me && to.path === "/login") {
    return { path: "/" };
  }
  return true;
});
