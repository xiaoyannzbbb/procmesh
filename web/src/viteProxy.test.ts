// @vitest-environment node

import { createServer as createHTTPServer } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { createServer as createViteServer, type ProxyOptions } from "vite";
import viteConfig from "../vite.config";

describe("Vite development proxy", () => {
  const cleanup: Array<() => Promise<void>> = [];

  afterEach(async () => {
    for (const close of cleanup.splice(0).reverse()) {
      await close();
    }
  });

  it("forwards Connect RPC requests to the backend", async () => {
    const backend = createHTTPServer((request, response) => {
      response.setHeader("content-type", "application/json");
      response.end(JSON.stringify({ path: request.url }));
    });
    backend.listen(0, "127.0.0.1");
    await new Promise<void>((resolve) => backend.once("listening", resolve));
    cleanup.push(
      () => new Promise<void>((resolve) => backend.close(() => resolve())),
    );

    const backendAddress = backend.address();
    if (!backendAddress || typeof backendAddress === "string") {
      throw new Error("backend did not bind to a TCP port");
    }

    const configuredProxy = viteConfig.server?.proxy?.["/procmesh.v1"];
    if (!configuredProxy || typeof configuredProxy === "string") {
      throw new Error("missing /procmesh.v1 development proxy");
    }

    const proxy: ProxyOptions = {
      ...configuredProxy,
      target: `http://127.0.0.1:${backendAddress.port}`,
    };
    const vitePort = await availablePort();
    const vite = await createViteServer({
      ...viteConfig,
      configFile: false,
      server: {
        ...viteConfig.server,
        host: "127.0.0.1",
        port: vitePort,
        strictPort: true,
        proxy: { "/procmesh.v1": proxy },
      },
    });
    await vite.listen();
    cleanup.push(() => vite.close());

    const viteAddress = vite.httpServer?.address();
    if (!viteAddress || typeof viteAddress === "string") {
      throw new Error("Vite did not bind to a TCP port");
    }

    const response = await fetch(
      `http://127.0.0.1:${viteAddress.port}/procmesh.v1.AuthService/Login`,
    );

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({
      path: "/procmesh.v1.AuthService/Login",
    });
  });
});

async function availablePort(): Promise<number> {
  const server = createHTTPServer();
  server.listen(0, "127.0.0.1");
  await new Promise<void>((resolve) => server.once("listening", resolve));

  const address = server.address();
  if (!address || typeof address === "string") {
    throw new Error("port probe did not bind to a TCP port");
  }

  await new Promise<void>((resolve) => server.close(() => resolve()));
  return address.port;
}
