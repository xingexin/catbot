import * as http from "node:http";
import * as https from "node:https";
import { lookup as dnsLookup } from "node:dns/promises";
import { isIP } from "node:net";
import ipaddr from "ipaddr.js";

export interface RequestOptions {
  signal: AbortSignal;
  timeoutMs: number;
  maxBytes: number;
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  maxRedirects?: number;
}

export interface WebResponse {
  url: string;
  status: number;
  headers: Record<string, string>;
  body: Buffer;
}

interface Address {
  address: string;
  family: number;
}
type Transport = (
  url: URL,
  options: http.RequestOptions,
  receive: (response: http.IncomingMessage) => void,
) => http.ClientRequest;
interface NetworkDependencies {
  resolve?: (hostname: string) => Promise<Address[]>;
  request?: Transport;
}

class WebRequestError extends Error {}

function fail(message: string): never {
  throw new WebRequestError(message);
}

export function isPublicAddress(address: string): boolean {
  try {
    const parsed = ipaddr.parse(address);
    if (parsed.range() !== "unicast") return false;
    // Only global IPv6 unicast: exclude translation, tunnelling and future/reserved ranges.
    return (
      parsed.kind() !== "ipv6" ||
      (parsed as ipaddr.IPv6).match(ipaddr.parse("2000::") as ipaddr.IPv6, 3)
    );
  } catch {
    return false;
  }
}

function targetURL(raw: string): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return fail("Invalid web URL");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:")
    fail("Only HTTP and HTTPS URLs are allowed");
  if (url.username || url.password)
    fail("Credentials in web URLs are not allowed");
  const hostname = url.hostname.toLowerCase().replace(/\.$/, "");
  if (
    hostname === "localhost" ||
    hostname.endsWith(".localhost") ||
    hostname.endsWith(".local")
  )
    fail("Only public web addresses are allowed");
  url.hash = "";
  return url;
}

function abortable<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise((resolve, reject) => {
    const abort = () => reject(new WebRequestError("Web request aborted"));
    if (signal.aborted) {
      abort();
      return;
    }
    signal.addEventListener("abort", abort, { once: true });
    promise
      .then(resolve, reject)
      .finally(() => signal.removeEventListener("abort", abort));
  });
}

function receiveResponse(
  url: URL,
  address: Address,
  options: RequestOptions,
  signal: AbortSignal,
  transport: Transport,
): Promise<WebResponse> {
  return new Promise((resolve, reject) => {
    let request: http.ClientRequest | undefined;
    let response: http.IncomingMessage | undefined;
    let finished = false;
    const cleanup = () => signal.removeEventListener("abort", abort);
    const error = (message: string) => {
      if (finished) return;
      finished = true;
      cleanup();
      reject(new WebRequestError(message));
      response?.destroy();
      request?.destroy();
    };
    const abort = () => error("Web request aborted");
    if (signal.aborted) {
      abort();
      return;
    }
    signal.addEventListener("abort", abort, { once: true });
    try {
      request = transport(
        url,
        {
          method: options.method ?? "GET",
          agent: false,
          signal,
          // Pin the validated DNS result. Node must never resolve the hostname again.
          lookup: ((
            _hostname: string,
            lookupOptions: { all?: boolean },
            callback: Function,
          ) => {
            if (lookupOptions?.all) callback(null, [address]);
            else callback(null, address.address, address.family);
          }) as http.RequestOptions["lookup"],
          headers: {
            "User-Agent": "catbot-web-search/1.0",
            ...options.headers,
            "Accept-Encoding": "identity",
          },
        },
        (incoming) => {
          response = incoming;
          if (finished || signal.aborted) {
            incoming.destroy();
            abort();
            return;
          }
          const headers: Record<string, string> = {};
          for (const [name, value] of Object.entries(incoming.headers)) {
            if (value !== undefined)
              headers[name.toLowerCase()] = Array.isArray(value)
                ? value.join(", ")
                : value;
          }
          const status = incoming.statusCode ?? 0;
          if ([301, 302, 303, 307, 308].includes(status)) {
            // Do not buffer redirect bodies; each next location is resolved and checked afresh.
            finished = true;
            cleanup();
            incoming.destroy();
            resolve({
              url: url.toString(),
              status,
              headers,
              body: Buffer.alloc(0),
            });
            return;
          }
          const encoding = headers["content-encoding"]?.trim().toLowerCase();
          if (encoding && encoding !== "identity") {
            error("Compressed web responses are not supported");
            return;
          }
          if (Number(headers["content-length"]) > options.maxBytes) {
            error("Web response exceeds the size limit");
            return;
          }
          const chunks: Buffer[] = [];
          let bytes = 0;
          incoming.on("data", (chunk: Buffer) => {
            if (finished) return;
            bytes += chunk.length;
            if (bytes > options.maxBytes) {
              error("Web response exceeds the size limit");
              return;
            }
            chunks.push(Buffer.from(chunk));
          });
          incoming.on("aborted", () => error("Web response was interrupted"));
          incoming.on("error", () => error("Web response failed"));
          incoming.on("end", () => {
            if (finished) return;
            finished = true;
            cleanup();
            resolve({
              url: url.toString(),
              status,
              headers,
              body: Buffer.concat(chunks),
            });
          });
        },
      );
      request.on("error", () => error("Web request failed"));
      request.end(options.body);
    } catch {
      error("Web request failed");
    }
  });
}

/** Dependency injection is for transport tests; production always uses public DNS and pinned sockets. */
export function createSafeRequester(dependencies: NetworkDependencies = {}) {
  const resolve =
    dependencies.resolve ??
    ((hostname: string) => dnsLookup(hostname, { all: true }));
  const transport: Transport =
    dependencies.request ??
    ((url, options, receive) =>
      (url.protocol === "https:" ? https : http).request(
        url,
        options,
        receive,
      ));
  return async (
    rawURL: string,
    options: RequestOptions,
  ): Promise<WebResponse> => {
    if (
      !Number.isInteger(options.timeoutMs) ||
      options.timeoutMs < 1 ||
      options.timeoutMs > 60_000
    )
      fail("Invalid web request timeout");
    if (
      !Number.isInteger(options.maxBytes) ||
      options.maxBytes < 1 ||
      options.maxBytes > 2 * 1024 * 1024
    )
      fail("Invalid web response size limit");
    const maxRedirects = options.maxRedirects ?? 3;
    if (!Number.isInteger(maxRedirects) || maxRedirects < 0 || maxRedirects > 3)
      fail("Invalid redirect limit");
    const controller = new AbortController();
    const cancel = () => controller.abort();
    options.signal.addEventListener("abort", cancel, { once: true });
    const timer = setTimeout(cancel, options.timeoutMs);
    if (options.signal.aborted) controller.abort();
    try {
      let url = targetURL(rawURL);
      let current = { ...options };
      for (let hop = 0; ; hop++) {
        if (controller.signal.aborted) fail("Web request aborted");
        const hostname = url.hostname.replace(/^\[|\]$/g, "");
        const literalFamily = isIP(hostname);
        const addresses = literalFamily
          ? [{ address: hostname, family: literalFamily }]
          : await abortable(resolve(hostname), controller.signal);
        if (
          !addresses.length ||
          addresses.some((address) => !isPublicAddress(address.address))
        )
          fail("Only public web addresses are allowed");
        const result = await receiveResponse(
          url,
          addresses[0],
          current,
          controller.signal,
          transport,
        );
        if (![301, 302, 303, 307, 308].includes(result.status)) return result;
        if (hop >= maxRedirects) fail("Web redirect limit exceeded");
        if (!result.headers.location) fail("Web redirect has no destination");
        let next: URL;
        try {
          next = targetURL(new URL(result.headers.location, url).toString());
        } catch (error) {
          if (error instanceof WebRequestError) throw error;
          return fail("Invalid web redirect");
        }
        // Redirected reads never receive API keys or caller-supplied credential headers.
        current = { ...current, headers: undefined };
        if (
          result.status === 303 ||
          ((result.status === 301 || result.status === 302) &&
            current.method === "POST")
        ) {
          current = { ...current, method: "GET", body: undefined };
        }
        url = next;
      }
    } catch (error) {
      if (controller.signal.aborted)
        fail(
          options.signal.aborted
            ? "Web request cancelled"
            : "Web request timed out",
        );
      if (error instanceof WebRequestError) throw error;
      return fail("Web request failed");
    } finally {
      clearTimeout(timer);
      options.signal.removeEventListener("abort", cancel);
    }
  };
}

export const safeRequest = createSafeRequester();
