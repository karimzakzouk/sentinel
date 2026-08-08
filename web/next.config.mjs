/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  images: { unoptimized: true },
  trailingSlash: true,
  // Strict TypeScript checking during build — surface type errors at build
  // time instead of silently shipping them.
  typescript: { ignoreBuildErrors: false },
  // Next 16 dropped the in-build `eslint` config key. Lint runs as a
  // separate `npm run lint` invocation (CI runs `npm run lint` in the web
  // job).
};

export default nextConfig;
