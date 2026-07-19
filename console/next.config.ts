import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // The API client is shipped as TypeScript source (spec-first, ADR-012); Next
  // must transpile it rather than expect a prebuilt package.
  transpilePackages: ["@nimbusdb/api-client"],
};

export default nextConfig;
