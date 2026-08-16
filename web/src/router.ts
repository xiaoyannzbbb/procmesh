import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import AppShell from "./components/AppShell.vue";
import { loadSession } from "./lib/session";
import { i18n } from "./lib/i18n";
import AuditPage from "./pages/AuditPage.vue";
import LoginPage from "./pages/LoginPage.vue";
import NodeDetailPage from "./pages/NodeDetailPage.vue";
import NodesPage from "./pages/NodesPage.vue";
import OverviewPage from "./pages/OverviewPage.vue";
import ProcessDetailPage from "./pages/ProcessDetailPage.vue";
import ProcessesPage from "./pages/ProcessesPage.vue";
import RolesPage from "./pages/RolesPage.vue";
import UsersPage from "./pages/UsersPage.vue";

const routes: RouteRecordRaw[] = [
  { path: "/login", component: LoginPage, meta: { public: true } },
  {
    path: "/",
    component: AppShell,
    children: [
      { path: "", component: OverviewPage },
      { path: "nodes", component: NodesPage },
      { path: "nodes/:id", component: NodeDetailPage },
      { path: "processes", component: ProcessesPage, meta: { i18nNamespaces: ['process'] } },
      { path: "processes/:idOrName", component: ProcessDetailPage, meta: { i18nNamespaces: ['process'] } },
      { path: "users", component: UsersPage },
      { path: "roles", component: RolesPage },
      { path: "audit", component: AuditPage },
    ],
  },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});

router.beforeEach(async (to) => {
  const namespaces = to.meta.i18nNamespaces as string[] | undefined;
  if (namespaces) {
    await i18n.loadNamespaces(namespaces);
  }

  const me = await loadSession();
  if (!me && to.path !== "/login") {
    return { path: "/login", query: { next: to.fullPath } };
  }
  if (me && to.path === "/login") {
    return { path: "/" };
  }
  return true;
});
