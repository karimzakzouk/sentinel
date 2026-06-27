/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  images: { unoptimized: true },
  trailingSlash: true,
  // v3.18.0: H23 — re-enabled TypeScript checking during build.
  // Was: ignoreBuildErrors=true, which silently shipped typos to production.
  // Fix any type errors that surface instead of suppressing them.
  // Note: Next.js 16 removed the `eslint` config key — lint is now a separate
  // `next lint` invocation. We run it via `npm run lint` in CI instead.
  typescript: { ignoreBuildErrors: false },
};

export default nextConfig;
