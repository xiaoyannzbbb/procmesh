import type { Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { getCsrf } from "./csrf";

const csrfInterceptor: Interceptor = (next) => async (req) => {
  if (req.url.includes("/procmesh.v1.AuthService/Login")) {
    return next(req);
  }
  const token = getCsrf();
  if (token) {
    req.header.set("X-CSRF-Token", token);
  }
  return next(req);
};

export const transport = createConnectTransport({
  baseUrl: "",
  fetch: (input, init) => fetch(input, { ...init, credentials: "same-origin" }),
  interceptors: [csrfInterceptor],
});
