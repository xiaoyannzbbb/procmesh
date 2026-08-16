<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import { newOperationId } from "../lib/opid";
import { clearSession, session, useAuthClient } from "../lib/session";
import { useI18n } from "../lib/useI18n";

const route = useRoute();
const router = useRouter();
const client = useAuthClient();
const { t } = useI18n();

const username = computed(() => session.value?.username ?? "");
const perms = computed(() => new Set(session.value?.permissions ?? []));

const navItems = computed(() => {
  const items = [
    { to: "/", label: t("common:nav.overview") },
    { to: "/nodes", label: t("common:nav.nodes") },
    { to: "/processes", label: t("common:nav.processes") },
  ];
  if (perms.value.has("user.read")) {
    items.push({ to: "/users", label: t("common:nav.users") });
  }
  if (perms.value.has("role.read")) {
    items.push({ to: "/roles", label: t("common:nav.roles") });
  }
  if (perms.value.has("audit.read")) {
    items.push({ to: "/audit", label: t("common:nav.audit") });
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
    <aside class="sidebar">
      <div class="brand">ProcMesh</div>
      <nav class="nav">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          :class="{ active: isActive(item.to) }"
        >
          {{ item.label }}
        </RouterLink>
      </nav>
      <div class="sidebar-foot">
        <div class="username">{{ username }}</div>
        <button type="button" class="btn logout" @click="onLogout">{{ t("common:actions.logout") }}</button>
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
}
.brand {
  padding: 1.25rem 1.25rem 0.75rem;
  font-size: 1.05rem;
  font-weight: 650;
}
.nav {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  padding: 0.5rem 0.75rem;
  flex: 1;
}
.nav-link {
  display: block;
  border-radius: 8px;
  padding: 0.5rem 0.75rem;
  color: var(--color-text);
  text-decoration: none;
  font-size: 0.9rem;
}
.nav-link.active {
  background: color-mix(in srgb, var(--color-accent) 12%, transparent);
  color: var(--color-accent);
  font-weight: 600;
}
.sidebar-foot {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding: 1rem 1.25rem 1.25rem;
  border-top: 1px solid var(--color-border);
}
.username {
  font-size: 0.875rem;
  color: var(--color-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
</style>
