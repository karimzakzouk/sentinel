import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The build lands where the Go binary embeds it. In development, /api goes to
// a Sentinel reached through `kubectl -n sentinel port-forward deploy/sentinel 8090`.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/ui/dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    proxy: { "/api": "http://localhost:8090" },
  },
});
