/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
  reactStrictMode: true,
  // Explicitly set the workspace root so Next.js doesn't get confused by
  // multiple bun.lock files in the parent directories.
  turbopack: {
    root: __dirname,
  },
  // The UI talks to the Sentinel API gateway. In production this is the
  // sentinel-api service in the same namespace. For local dev, override with
  // NEXT_PUBLIC_API_BASE_URL.
  env: {
    NEXT_PUBLIC_API_BASE_URL: process.env.NEXT_PUBLIC_API_BASE_URL || "http://sentinel-api:8000",
  },
};
module.exports = nextConfig;
