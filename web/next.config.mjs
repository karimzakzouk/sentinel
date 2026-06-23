/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  images: { unoptimized: true },
  trailingSlash: true,
  // Disable turbopack for Docker builds — it's less stable in CI
  // and the webpack builder produces more reliable static exports.
};

export default nextConfig;
