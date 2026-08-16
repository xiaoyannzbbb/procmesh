<script setup lang="ts">
import { computed, ref, onMounted } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  LayoutDashboard,
  Server,
  Layers,
  FolderTree,
  ListTodo,
  Users,
  ShieldCheck,
  FileSearch,
  LogOut,
  ChevronLeft,
  ChevronRight
} from "lucide-vue-next";
import { newOperationId } from "../lib/opid";
import { clearSession, session, useAuthClient } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import LanguageSwitcher from "./LanguageSwitcher.vue";

const route = useRoute();
const router = useRouter();
const client = useAuthClient();
const { t } = useI18n();

const COLLAPSED_KEY = "procmesh-sidebar-collapsed";
const isCollapsed = ref(false);

onMounted(() => {
  const stored = localStorage.getItem(COLLAPSED_KEY);
  if (stored !== null) {
    isCollapsed.value = stored === "true";
  }
});

function toggleCollapse() {
  isCollapsed.value = !isCollapsed.value;
  localStorage.setItem(COLLAPSED_KEY, String(isCollapsed.value));
}

const username = computed(() => session.value?.username ?? "");
const perms = computed(() => new Set(session.value?.permissions ?? []));

const navItems = computed(() => {
  const items = [
    { to: "/", label: t("nav.overview"), icon: LayoutDashboard },
    { to: "/nodes", label: t("nav.nodes"), icon: Server },
    { to: "/processes", label: t("nav.processes"), icon: Layers },
  ];
  if (perms.value.has("node.read")) {
    items.push({ to: "/groups", label: t("nav.groups"), icon: FolderTree });
  }
  if (perms.value.has("batch.execute")) {
    items.push({ to: "/batches", label: t("nav.batches"), icon: ListTodo });
  }
  if (perms.value.has("user.read")) {
    items.push({ to: "/users", label: t("nav.users"), icon: Users });
  }
  if (perms.value.has("role.read")) {
    items.push({ to: "/roles", label: t("nav.roles"), icon: ShieldCheck });
  }
  if (perms.value.has("audit.read")) {
    items.push({ to: "/audit", label: t("nav.audit"), icon: FileSearch });
  }
  return items;
});

function isActive(to: string): boolean {
  if (to === "/") {
    return route.path === "/";
  }
  return route.path === to || route.path.startsWith(to + "/");
}

async function onLogout(): Promise<void> {
  try {
    await client.logout({
      meta: {
        operationId: newOperationId(),
        operator: username.value,
      },
    });
  } finally {
    clearSession();
    await router.replace("/login");
  }
}
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar" :class="{ collapsed: isCollapsed }">
      <div class="sidebar-header">
        <div class="brand">
          <span class="brand-full">ProcMesh</span>
          <span class="brand-short">PM</span>
        </div>
        <button
          type="button"
          class="collapse-btn"
          @click="toggleCollapse"
          :aria-label="isCollapsed ? t('actions.expand') : t('actions.collapse')"
          :title="isCollapsed ? t('actions.expand') : t('actions.collapse')"
        >
          <ChevronLeft v-if="!isCollapsed" :size="20" />
          <ChevronRight v-else :size="20" />
        </button>
      </div>
      <nav class="nav">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          :class="{ active: isActive(item.to) }"
          :title="isCollapsed ? item.label : ''"
        >
          <component :is="item.icon" :size="20" class="nav-icon" />
          <span class="nav-label">{{ item.label }}</span>
        </RouterLink>
      </nav>
      <div class="sidebar-foot">
        <LanguageSwitcher :collapsed="isCollapsed" />
        <div class="user-info">
          <span class="username" :title="username">{{ username }}</span>
        </div>
        <button
          type="button"
          class="btn logout"
          @click="onLogout"
          :title="isCollapsed ? t('actions.logout') : ''"
        >
          <LogOut :size="18" />
          <span class="btn-label">{{ t("actions.logout") }}</span>
        </button>
      </div>
    </aside>
    <main class="content">
      <div class="content-inner">
        <RouterView />
      </div>
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  min-height: 100vh;
  background: var(--color-bg);
}

.sidebar {
  width: var(--sidebar-width);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  border-right: 1px solid var(--color-border);
  background: var(--color-sidebar);
  transition: width 0.3s cubic-bezier(0.4, 0, 0.2, 1);
}

.sidebar.collapsed {
  width: 72px;
}

.sidebar-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1rem 1.25rem;
  border-bottom: 1px solid var(--color-border);
  gap: 0.5rem;
}

.brand {
  font-size: 1.125rem;
  font-weight: 650;
  letter-spacing: -0.01em;
  white-space: nowrap;
  overflow: hidden;
}

.brand-short {
  display: none;
}

.collapsed .brand-full {
  display: none;
}

.collapsed .brand-short {
  display: inline;
}

.collapse-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 0.375rem;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--color-muted);
  cursor: pointer;
  transition: all 0.2s;
  flex-shrink: 0;
}

.collapse-btn:hover {
  background: color-mix(in srgb, var(--color-text) 8%, transparent);
  color: var(--color-text);
}

.collapse-btn:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.nav {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.75rem;
  flex: 1;
  overflow-y: auto;
}

.nav-link {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  border-radius: 8px;
  padding: 0.625rem 0.75rem;
  color: var(--color-text);
  text-decoration: none;
  font-size: 0.9rem;
  font-weight: 500;
  transition: all 0.2s;
  white-space: nowrap;
  position: relative;
}

.nav-link:hover {
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
}

.nav-link:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.nav-link.active {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}

.nav-link.active:hover {
  background: color-mix(in srgb, var(--color-accent) 18%, transparent);
}

.nav-icon {
  flex-shrink: 0;
}

.nav-label {
  overflow: hidden;
  text-overflow: ellipsis;
}

.collapsed .nav-link {
  justify-content: center;
  padding: 0.625rem;
}

.collapsed .nav-label {
  position: absolute;
  left: 100%;
  margin-left: 0.75rem;
  padding: 0.375rem 0.75rem;
  background: var(--color-text);
  color: var(--color-sidebar);
  border-radius: 6px;
  font-size: 0.875rem;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.2s;
  z-index: 100;
}

.collapsed .nav-link:hover .nav-label,
.collapsed .nav-link:focus-visible .nav-label {
  opacity: 1;
}

.sidebar-foot {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  padding: 1rem 1.25rem;
  border-top: 1px solid var(--color-border);
}

.collapsed .sidebar-foot {
  padding: 1rem 0.75rem;
  align-items: center;
}

.user-info {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.username {
  font-size: 0.875rem;
  color: var(--color-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}

.collapsed .username {
  width: 0;
  opacity: 0;
}

.logout {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  width: 100%;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-card);
  color: var(--color-text);
  padding: 0.5rem 0.875rem;
  font-size: 0.875rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.logout:hover {
  background: color-mix(in srgb, var(--color-text) 6%, transparent);
  border-color: var(--color-text);
}

.logout:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.collapsed .btn-label {
  display: none;
}

.content {
  flex: 1;
  min-width: 0;
}

.content-inner {
  max-width: var(--content-max);
  margin: 0 auto;
  padding: 1.5rem;
}

@media (min-width: 1024px) {
  .content-inner {
    padding: 2rem 3rem;
  }
}

@media (min-width: 1280px) {
  .content-inner {
    padding: 2rem 4rem;
  }
}

@media (max-width: 768px) {
  .sidebar {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 0;
    z-index: 1000;
    box-shadow: 2px 0 8px rgba(0, 0, 0, 0.1);
  }

  .sidebar.collapsed {
    width: 72px;
  }

  .content {
    margin-left: var(--sidebar-width);
  }

  .sidebar.collapsed ~ .content {
    margin-left: 72px;
  }
}
</style>
