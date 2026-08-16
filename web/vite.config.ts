import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { previewApiPlugin } from "./vite-preview-api";

export default defineConfig({
  plugins: [react(), previewApiPlugin()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 1100,
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ["echarts"],
          "react-vendor": ["react", "react-dom", "react-router-dom"],
          icons: ["@fluentui/react-icons"],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:7575",
    },
  },
});
