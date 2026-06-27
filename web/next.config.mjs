/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  images: { unoptimized: true },
  trailingSlash: true,
  // v3.18.0: H23 — re-enabled TypeScript + ESLint checking during build.
  // Was: ignoreBuildErrors=true + ignoreDuringBuilds=true, which silently
  // shipped typos (e.g. useFoo() undefined) to production. Fix any type
  // errors that surface instead of suppressing them.
  typescript: { ignoreBuildErrors: false },
  eslint: { ignoreDuringBuilds: false },
};

export default nextConfig;
