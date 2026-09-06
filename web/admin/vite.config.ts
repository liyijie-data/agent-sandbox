import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/console/",
  plugins: [react()],
  build: {
    outDir: "../../internal/console/dist",
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    proxy: {
      "/console/api": {
        target: process.env.VITE_CONSOLE_API_URL || "http://127.0.0.1:13081",
        changeOrigin: true,
      },
    },
  },
});
