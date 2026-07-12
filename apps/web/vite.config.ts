import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Dev server proxies the Connect RPC path prefix to a running Edge binary so
// `pnpm dev` works against localhost:4780 while the app is served from Vite.
// Production build emits a fully static, same-origin bundle into dist/ that the
// Edge binary embeds via go:embed.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/gantry.v1": {
        target: "http://localhost:4780",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: true,
  },
  test: {
    environment: "jsdom",
    globals: false,
    setupFiles: ["src/test-setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
