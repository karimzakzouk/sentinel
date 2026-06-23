import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Static export — embeds into the Go binary via go:embed
  output: "export",
  images: { unoptimized: true },
  // When embedded in the Go binary, the UI is served from the same origin
  // as the API (port 8000). No CORS needed.
  // In dev mode (npm run dev), the UI runs on port 3000 and calls the API
  // at http://localhost:8000 (set via NEXT_PUBLIC_API_URL env var).
};

export default nextConfig;
