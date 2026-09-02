import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";
import { loadSession } from "./lib/session";
import { i18n } from "./lib/i18n";

const AppShell = () => import("./components/AppShell.vue");
const AuditPage = () => import("./pages/AuditPage.vue");
const LoginPage = () => import("./pages/LoginPage.vue");
const NodeDetailPage = () => import("./pages/NodeDetailPage.vue");
const NodesPage = () => import("./pages/NodesPage.vue");
const OverviewPage = () => import("./pages/OverviewPage.vue");
const ProcessCreatePage = () => import("./pages/ProcessCreatePage.vue");
const ProcessDetailPage = () => import("./pages/ProcessDetailPage.vue");
const ProcessesPage = () => import("./pages/ProcessesPage.vue");
const AlertsPage = () => import("./pages/AlertsPage.vue");
const BackupPage = () => import("./pages/BackupPage.vue");
const DisasterReplicaPage = () => import("./pages/DisasterReplicaPage.vue");
const BatchesPage = () => import("./pages/BatchesPage.vue");
const GroupsPage = () => import("./pages/GroupsPage.vue");
const RolesPage = () => import("./pages/RolesPage.vue");
const UsersPage = () => import("./pages/UsersPage.vue");
const ProfilePage = () => import("./pages/ProfilePage.vue");
const UpdatesPage = () => import("./pages/UpdatesPage.vue");

const routes: RouteRecordRaw[] = [
  { path: "/login", component: LoginPage, meta: { public: true } },
  {
    path: "/",
    component: AppShell,
    children: [
      { path: "", component: OverviewPage },
      { path: "nodes", component: NodesPage, meta: { i18nNamespaces: ['features'] } },
      { path: "nodes/:id", component: NodeDetailPage, meta: { i18nNamespaces: ['features'] } },
      { path: "processes", component: ProcessesPage, meta: { i18nNamespaces: ['features', 'process'] } },
      { path: "processes/new", component: ProcessCreatePage, meta: { i18nNamespaces: ['features', 'process'] } },
      { path: "processes/:idOrName", component: ProcessDetailPage, meta: { i18nNamespaces: ['features', 'process'] } },
      { path: "groups", component: GroupsPage, meta: { i18nNamespaces: ['features'] } },
      { path: "batches", component: BatchesPage, meta: { i18nNamespaces: ['features'] } },
      { path: "batches/:id", component: BatchesPage, meta: { i18nNamespaces: ['features'] } },
      { path: "alerts", component: AlertsPage, meta: { i18nNamespaces: ['features'] } },
      { path: "backup", component: BackupPage, meta: { i18nNamespaces: ['features'] } },
      { path: "disaster-replica", component: DisasterReplicaPage, meta: { i18nNamespaces: ['features'] } },
      { path: "updates", component: UpdatesPage, meta: { i18nNamespaces: ['features'] } },
      { path: "users", component: UsersPage, meta: { i18nNamespaces: ['features'] } },
      { path: "roles", component: RolesPage, meta: { i18nNamespaces: ['features'] } },
      { path: "audit", component: AuditPage, meta: { i18nNamespaces: ['features', 'audit'] } },
      { path: "profile", component: ProfilePage, meta: { i18nNamespaces: ['features'] } },
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

  const namespaces = to.meta.i18nNamespaces as string[] | undefined;
  if (namespaces) {
    await i18n.loadNamespaces(namespaces).catch(() => undefined);
  }
  return true;
});
