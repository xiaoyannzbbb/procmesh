<script setup lang="ts">
import { computed, ref, onMounted, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import {
  LayoutDashboard,
  Server,
  Layers,
  FolderTree,
  ListTodo,
  Bell,
  Archive,
  Copy,
  Users,
  ShieldCheck,
  FileSearch,
  LogOut,
  ChevronLeft,
  ChevronRight,
  Menu,
  X,
} from "lucide-vue-next";
import { newOperationId } from "../lib/opid";
import { clearSession, session, useAuthClient } from "../lib/session";
import { useI18n } from "../lib/useI18n";
import LanguageSwitcher from "./LanguageSwitcher.vue";
import BrandMark from "./BrandMark.vue";

const route = useRoute();
const router = useRouter();
const client = useAuthClient();
const { t } = useI18n();

const COLLAPSED_KEY = "procmesh-sidebar-collapsed";
const isCollapsed = ref(false);
const isMobileNavOpen = ref(false);
const sidebarTooltip = ref<{ label: string; left: number; top: number } | null>(null);

onMounted(() => {
  const stored = localStorage.getItem(COLLAPSED_KEY);
  if (stored !== null) {
    isCollapsed.value = stored === "true";
  }
});

function toggleCollapse() {
  isCollapsed.value = !isCollapsed.value;
  localStorage.setItem(COLLAPSED_KEY, String(isCollapsed.value));
  if (!isCollapsed.value) {
    hideSidebarTooltip();
  }
}

function toggleMobileNav(): void {
  isMobileNavOpen.value = !isMobileNavOpen.value;
}

function closeMobileNav(): void {
  isMobileNavOpen.value = false;
}

function showSidebarTooltip(event: MouseEvent | FocusEvent, label: string): void {
  if (!isCollapsed.value || window.innerWidth <= 768) {
    return;
  }

  const target = event.currentTarget;
  if (!(target instanceof HTMLElement)) {
    return;
  }

  const itemRect = target.getBoundingClientRect();
  const sidebarRect = target.closest(".sidebar")?.getBoundingClientRect();
  const centerY = itemRect.top + itemRect.height / 2;
  sidebarTooltip.value = {
    label,
    left: Math.max(sidebarRect?.right ?? itemRect.right, itemRect.right) + 8,
    top: Math.min(Math.max(centerY, 24), window.innerHeight - 24),
  };
}

function hideSidebarTooltip(): void {
  sidebarTooltip.value = null;
}

watch(
  () => route.fullPath,
  () => closeMobileNav(),
);

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
  if (perms.value.has("alert.read")) {
    items.push({ to: "/alerts", label: t("nav.alerts"), icon: Bell });
  }
  if (perms.value.has("backup.read")) {
    items.push({ to: "/backup", label: t("nav.backup"), icon: Archive });
  }
  if (perms.value.has("replication.read")) {
    items.push({ to: "/disaster-replica", label: t("nav.disasterReplica"), icon: Copy });
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
    <div v-if="isMobileNavOpen" class="mobile-nav-scrim" aria-hidden="true" @click="closeMobileNav" />
    <aside class="sidebar" :class="{ collapsed: isCollapsed, 'mobile-open': isMobileNavOpen }">
      <div class="sidebar-header">
        <div class="brand">
          <BrandMark :size="isCollapsed ? 32 : 28" />
          <span class="brand-full">{{ t("app.name") }}</span>
        </div>
        <button
          type="button"
          class="collapse-btn"
          @click="toggleCollapse"
          :aria-label="isCollapsed ? t('actions.expand') : t('actions.collapse')"
          @mouseenter="showSidebarTooltip($event, t('actions.expand'))"
          @mouseleave="hideSidebarTooltip"
          @focus="showSidebarTooltip($event, t('actions.expand'))"
          @blur="hideSidebarTooltip"
        >
          <ChevronLeft v-if="!isCollapsed" :size="20" />
          <ChevronRight v-else :size="20" />
        </button>
      </div>
      <nav class="nav" @scroll.passive="hideSidebarTooltip">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          :class="{ active: isActive(item.to) }"
          :aria-label="isCollapsed ? item.label : undefined"
          @mouseenter="showSidebarTooltip($event, item.label)"
          @mouseleave="hideSidebarTooltip"
          @focus="showSidebarTooltip($event, item.label)"
          @blur="hideSidebarTooltip"
          @click="closeMobileNav(); hideSidebarTooltip()"
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
          :aria-label="isCollapsed ? t('actions.logout') : undefined"
          @mouseenter="showSidebarTooltip($event, t('actions.logout'))"
          @mouseleave="hideSidebarTooltip"
          @focus="showSidebarTooltip($event, t('actions.logout'))"
          @blur="hideSidebarTooltip"
          @click="hideSidebarTooltip(); onLogout()"
        >
          <LogOut :size="18" />
          <span class="btn-label">{{ t("actions.logout") }}</span>
        </button>
      </div>
    </aside>
    <Teleport to="body">
      <div
        v-if="sidebarTooltip"
        id="sidebar-tooltip"
        class="sidebar-tooltip"
        role="tooltip"
        :style="{ left: `${sidebarTooltip.left}px`, top: `${sidebarTooltip.top}px` }"
      >
        {{ sidebarTooltip.label }}
      </div>
    </Teleport>
    <main class="content">
      <header class="mobile-topbar">
        <button
          type="button"
          class="mobile-menu-btn"
          :aria-label="isMobileNavOpen ? t('actions.close') : t('actions.menu')"
          :aria-expanded="isMobileNavOpen"
          @click="toggleMobileNav"
        >
          <X v-if="isMobileNavOpen" :size="22" aria-hidden="true" />
          <Menu v-else :size="22" aria-hidden="true" />
        </button>
        <span class="mobile-brand">
          <BrandMark :size="24" />
          {{ t("app.name") }}
        </span>
      </header>
      <div class="content-inner">
        <RouterView />
      </div>
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  height: 100dvh;
  overflow: hidden;
  background: var(--color-bg);
}

.mobile-topbar,
.mobile-nav-scrim {
  display: none;
}

.sidebar {
  width: var(--sidebar-width);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  min-height: 0;
  border-right: 1px solid var(--color-border);
  background: var(--color-sidebar);
  transition: width 0.3s cubic-bezier(0.4, 0, 0.2, 1), transform 0.25s cubic-bezier(0.4, 0, 0.2, 1);
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

.collapsed .sidebar-header {
  position: relative;
  justify-content: center;
  padding: 1rem 0.75rem;
  gap: 0;
}

.brand {
  display: flex;
  align-items: center;
  gap: 0.625rem;
  min-width: 0;
  font-size: 1.125rem;
  font-weight: 650;
  letter-spacing: -0.01em;
  white-space: nowrap;
  overflow: hidden;
}

.collapsed .brand {
  justify-content: center;
  overflow: visible;
}

.collapsed .brand-full {
  display: none;
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
  min-width: 44px;
  min-height: 44px;
}

.collapse-btn:hover {
  background: color-mix(in srgb, var(--color-text) 8%, transparent);
  color: var(--color-text);
}

.collapse-btn:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}

.collapsed .collapse-btn {
  position: absolute;
  top: 50%;
  right: 0;
  z-index: 10;
  border: none;
  border-radius: 50%;
  background: transparent;
  transform: translate(50%, -50%);
}

.collapsed .collapse-btn::before {
  position: absolute;
  inset: 4px;
  content: "";
  border: 1px solid var(--color-border);
  border-radius: 50%;
  background: var(--color-card);
  box-shadow: var(--shadow-sm);
  transition: background 0.2s, border-color 0.2s;
}

.collapsed .collapse-btn :deep(svg) {
  position: relative;
  z-index: 1;
}

.collapsed .collapse-btn:hover {
  background: transparent;
}

.collapsed .collapse-btn:hover::before {
  background: color-mix(in srgb, var(--color-text) 8%, var(--color-card));
  border-color: color-mix(in srgb, var(--color-text) 18%, var(--color-border));
}

.nav {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.75rem;
  flex: 1;
  overflow-x: hidden;
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
  min-height: 44px;
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
  display: none;
}

.sidebar-tooltip {
  position: fixed;
  z-index: 1100;
  padding: 0.375rem 0.625rem;
  border-radius: 6px;
  background: var(--color-text);
  box-shadow: var(--shadow-md);
  color: var(--color-sidebar);
  font-size: 0.8125rem;
  font-weight: 500;
  line-height: 1.25;
  white-space: nowrap;
  pointer-events: none;
  transform: translateY(-50%);
}

.sidebar-tooltip::before {
  position: absolute;
  top: 50%;
  left: -4px;
  width: 8px;
  height: 8px;
  content: "";
  background: var(--color-text);
  transform: translateY(-50%) rotate(45deg);
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
  min-height: 0;
  overflow-y: auto;
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
  .mobile-topbar {
    position: sticky;
    top: 0;
    z-index: 900;
    display: flex;
    align-items: center;
    gap: 0.75rem;
    min-height: 3.5rem;
    padding: 0 1rem;
    border-bottom: 1px solid var(--color-border);
    background: color-mix(in srgb, var(--color-bg) 94%, transparent);
    backdrop-filter: blur(12px);
  }

  .mobile-brand {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 1rem;
    font-weight: 700;
  }

  .mobile-menu-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 2.75rem;
    height: 2.75rem;
    padding: 0;
    border: 1px solid var(--color-border);
    border-radius: 8px;
    background: var(--color-card);
    color: var(--color-text);
    cursor: pointer;
  }

  .mobile-menu-btn:focus-visible {
    outline: 2px solid var(--color-accent);
    outline-offset: 2px;
  }

  .mobile-nav-scrim {
    position: fixed;
    inset: 0;
    z-index: 950;
    display: block;
    background: rgba(13, 13, 13, 0.42);
  }

  .sidebar,
  .sidebar.collapsed {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 0;
    z-index: 1000;
    width: min(82vw, 18rem);
    transform: translateX(-100%);
    box-shadow: 0 0 24px rgba(0, 0, 0, 0.16);
  }

  .sidebar.mobile-open {
    transform: translateX(0);
  }

  .sidebar .collapse-btn {
    display: none;
  }

  .sidebar .brand-full,
  .sidebar.collapsed .brand-full {
    display: inline;
  }

  .sidebar.collapsed .brand {
    justify-content: flex-start;
    overflow: hidden;
  }

  .sidebar.collapsed .sidebar-header {
    flex-direction: row;
    justify-content: space-between;
    padding: 1rem 1.25rem;
    gap: 0.5rem;
  }

  .sidebar.collapsed .nav-link {
    justify-content: flex-start;
    padding: 0.75rem;
  }

  .sidebar.collapsed .nav-label {
    display: inline;
    position: static;
    margin-left: 0;
    padding: 0;
    background: transparent;
    color: inherit;
    border-radius: 0;
    opacity: 1;
    pointer-events: auto;
  }

  .sidebar.collapsed .sidebar-foot {
    padding: 1rem 1.25rem;
    align-items: stretch;
  }

  .sidebar.collapsed .username {
    width: auto;
    opacity: 1;
  }

  .sidebar.collapsed .btn-label {
    display: inline;
  }

  .content {
    width: 100%;
    margin-left: 0;
  }

  .content-inner {
    padding: 1rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .sidebar,
  .mobile-topbar,
  .mobile-nav-scrim {
    transition: none;
  }
}
</style>
