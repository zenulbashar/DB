import path from "node:path";
import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // The API client is shipped as TypeScript source (spec-first, ADR-012); Next
  // must transpile it rather than expect a prebuilt package.
  transpilePackages: ["@nimbusdb/api-client"],
  // Container deploys (ADR-020): self-contained server bundle. The traced root
  // is the repo root because @nimbusdb/api-client lives outside console/.
  output: "standalone",
  outputFileTracingRoot: path.join(process.cwd(), ".."),
};

export default nextConfig;
