// A ConfigurationSource over HTTP (contract.ts): GET the endpoint for the Document, PATCH it with an Update for the
// committed Document. The owning module injects the endpoint (already naming its one resource) and the transport (its
// authentication, base address and timeouts); nothing in a response chooses another address, scope or resource.
import { ConfigurationError, type ConfigurationSource, type ConfigurationUpdate } from "./contract";

/** One request as the module's transport sends it; `body` is JSON already. */
export interface ConfigurationRequest {
  method: "GET" | "PATCH";
  body?: string;
  signal?: AbortSignal;
}

/** The transport's answer: its status and the parsed JSON body (undefined when there was none or it wasn't JSON). */
export interface ConfigurationResponse {
  status: number;
  body: unknown;
}

export type ConfigurationTransport = (endpoint: string, request: ConfigurationRequest) => Promise<ConfigurationResponse>;

/** A source bound to `endpoint` through `transport`. `key` defaults to the endpoint: another endpoint is another resource. */
export function httpConfigurationSource({ endpoint, transport, key = endpoint, subscribe }: {
  endpoint: string;
  transport: ConfigurationTransport;
  key?: string;
  subscribe?: ConfigurationSource["subscribe"];
}): ConfigurationSource {
  const send = async (request: ConfigurationRequest) => {
    const res = await transport(endpoint, request);
    if (res.status < 200 || res.status >= 300) throw ConfigurationError.fromBody(res.body, res.status);
    return res.body;
  };
  return {
    key,
    read: (signal) => send({ method: "GET", signal }),
    update: (update: ConfigurationUpdate, signal) => send({ method: "PATCH", body: JSON.stringify(update), signal }),
    ...(subscribe ? { subscribe } : {}),
  };
}
