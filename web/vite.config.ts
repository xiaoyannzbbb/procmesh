import { defineConfig } from "vitest/config";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath } from "url";
import { dirname } from "path";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  base: "/",
  build: {
    outDir: "../internal/web/dist",
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks: {
          "i18n-core": [
            "i18next",
            "i18next-vue",
            "i18next-browser-languagedetector",
          ],
          "i18n-backend": ["i18next-http-backend"],
          "vue-core": ["vue", "vue-router"],
          connect: ["@connectrpc/connect", "@connectrpc/connect-web"],
        },
      },
    },
    chunkSizeWarningLimit: 600,
  },
  server: {
    port: 5173,
    strictPort: true,
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.ts"],
  },
});
