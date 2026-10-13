import assert from "node:assert/strict";
import { EventEmitter } from "node:events";
import { PassThrough } from "node:stream";
import test from "node:test";
import type * as http from "node:http";
import { createSafeRequester, isPublicAddress } from "../src/network.js";

const publicDNS = async () => [{ address: "93.184.216.34", family: 4 }];
const options = () => ({
  signal: new AbortController().signal,
  timeoutMs: 1000,
  maxBytes: 1024,
});
type Reply = {
  status?: number;
  headers?: Record<string, string>;
  chunks?: string[];
};
function mockTransport(
  replies: Reply[],
  onRequest?: (url: URL, opts: http.RequestOptions) => void,
) {
  const responses: PassThrough[] = [];
  let calls = 0;
  const request = (
    url: URL,
    opts: http.RequestOptions,
    receive: (res: http.IncomingMessage) => void,
  ) => {
    calls++;
    onRequest?.(url, opts);
    const reply = replies.shift() ?? {};
    const req = new EventEmitter() as http.ClientRequest;
    req.destroy = () => req;
    req.end = (() => {
      queueMicrotask(() => {
        const response = new PassThrough();
        responses.push(response);
        Object.assign(response, {
          statusCode: reply.status ?? 200,
          headers: reply.headers ?? {},
        });
        receive(response as unknown as http.IncomingMessage);
        for (const chunk of reply.chunks ?? ["hello"])
          response.write(Buffer.from(chunk));
        response.end();
      });
      return req;
    }) as http.ClientRequest["end"];
    return req;
  };
  return { request, responses, calls: () => calls };
}

test("public address guard rejects private, reserved, mapped and tunnel IPv6 ranges", () => {
  for (const address of [
    "127.0.0.1",
    "0.0.0.0",
    "10.1.2.3",
    "172.16.0.1",
    "192.168.1.1",
    "169.254.169.254",
    "100.64.0.1",
    "192.0.2.1",
    "198.18.0.1",
    "224.0.0.1",
    "255.255.255.255",
    "::",
    "::1",
    "::ffff:127.0.0.1",
    "::ffff:8.8.8.8",
    "fc00::1",
    "fe80::1",
    "ff02::1",
    "2001:db8::1",
    "2002:7f00:1::",
    "64:ff9b::7f00:1",
    "garbage",
  ]) {
    assert.equal(isPublicAddress(address), false, address);
  }
  for (const address of ["8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"])
    assert.equal(isPublicAddress(address), true, address);
});

test("URL guard rejects unsafe schemes, credentials, normalized literal addresses and local names", async () => {
  const transport = mockTransport([]);
  const request = createSafeRequester({
    resolve: publicDNS,
    request: transport.request,
  });
  for (const url of [
    "file:///etc/passwd",
    "ftp://example.com",
    "https://secret@example.com",
    "http://localhost",
    "http://service.local",
    "http://2130706433",
    "http://0177.0.0.1",
    "http://[::ffff:127.0.0.1]",
    "http://[::1]",
  ]) {
    await assert.rejects(request(url, options()), /allowed/);
  }
  assert.equal(transport.calls(), 0);
});

test("rejects any private DNS answer before starting a request", async () => {
  const transport = mockTransport([]);
  const request = createSafeRequester({
    resolve: async () => [
      { address: "8.8.8.8", family: 4 },
      { address: "127.0.0.1", family: 4 },
    ],
    request: transport.request,
  });
  await assert.rejects(request("https://example.com", options()), /public/);
  assert.equal(transport.calls(), 0);
});

test("pins validated DNS to the actual socket lookup and does not resolve again", async () => {
  let resolutions = 0;
  let checked = false;
  const transport = mockTransport([{}], (_url, opts) => {
    assert.equal(opts.agent, false);
    const lookup = opts.lookup as Function;
    lookup(
      "example.com",
      {},
      (error: unknown, address: string, family: number) => {
        assert.equal(error, null);
        assert.equal(address, "93.184.216.34");
        assert.equal(family, 4);
        checked = true;
      },
    );
    lookup(
      "example.com",
      { all: true },
      (error: unknown, addresses: unknown[]) => {
        assert.equal(error, null);
        assert.deepEqual(addresses, [{ address: "93.184.216.34", family: 4 }]);
      },
    );
  });
  const request = createSafeRequester({
    resolve: async () => {
      resolutions++;
      return publicDNS();
    },
    request: transport.request,
  });
  assert.equal(
    (await request("https://example.com/path#fragment", options())).url,
    "https://example.com/path",
  );
  assert.equal(resolutions, 1);
  assert.equal(checked, true);
});

test("redirect destinations are independently checked and private redirects never connect", async () => {
  const transport = mockTransport([
    {
      status: 302,
      headers: { location: "http://169.254.169.254/latest/meta-data" },
    },
  ]);
  const request = createSafeRequester({
    resolve: publicDNS,
    request: transport.request,
  });
  await assert.rejects(request("https://example.com", options()), /public/);
  assert.equal(transport.calls(), 1);
  assert.equal(transport.responses[0].destroyed, true);
});

test("public redirects preserve bounds and never forward caller secrets", async () => {
  const seen: http.RequestOptions[] = [];
  const transport = mockTransport(
    [
      { status: 302, headers: { location: "/final" } },
      { headers: { "content-type": "text/plain" }, chunks: ["done"] },
    ],
    (_url, opts) => seen.push(opts),
  );
  const request = createSafeRequester({
    resolve: publicDNS,
    request: transport.request,
  });
  const result = await request("https://example.com/start", {
    ...options(),
    headers: { Authorization: "secret", "X-Subscription-Token": "secret" },
  });
  assert.equal(result.url, "https://example.com/final");
  assert.equal(result.body.toString(), "done");
  assert.equal(
    (seen[1].headers as Record<string, string>).Authorization,
    undefined,
  );
  assert.equal(
    (seen[1].headers as Record<string, string>)["X-Subscription-Token"],
    undefined,
  );
});

test("zero redirect policy and redirect loops fail with bounded requests", async () => {
  for (const limit of [0, 3]) {
    const transport = mockTransport(
      Array.from({ length: 4 }, () => ({
        status: 302,
        headers: { location: "/again" },
      })),
    );
    const request = createSafeRequester({
      resolve: publicDNS,
      request: transport.request,
    });
    await assert.rejects(
      request("https://example.com", { ...options(), maxRedirects: limit }),
      /redirect limit/,
    );
    assert.equal(transport.calls(), limit + 1);
  }
});

test("response caps apply to declared and streamed bytes and compressed responses are rejected", async () => {
  for (const reply of [
    { headers: { "content-length": "1025" } },
    { chunks: ["a".repeat(600), "b".repeat(600)] },
    { headers: { "content-encoding": "gzip" } },
  ]) {
    const transport = mockTransport([reply]);
    const request = createSafeRequester({
      resolve: publicDNS,
      request: transport.request,
    });
    await assert.rejects(
      request("https://example.com", options()),
      /size limit|Compressed/,
    );
    assert.equal(transport.responses[0].destroyed, true);
  }
});

test("one deadline covers DNS and cancels before any socket is created", async () => {
  const transport = mockTransport([]);
  const request = createSafeRequester({
    resolve: () => new Promise(() => {}),
    request: transport.request,
  });
  await assert.rejects(
    request("https://example.com", { ...options(), timeoutMs: 15 }),
    /timed out/,
  );
  assert.equal(transport.calls(), 0);
});

test("caller cancellation destroys an in-flight request and suppresses private error details", async () => {
  const controller = new AbortController();
  let destroyed = false;
  const request = createSafeRequester({
    resolve: publicDNS,
    request: () => {
      const req = new EventEmitter() as http.ClientRequest;
      req.end = (() => {
        queueMicrotask(() => controller.abort("secret"));
        return req;
      }) as http.ClientRequest["end"];
      req.destroy = () => {
        destroyed = true;
        return req;
      };
      return req;
    },
  });
  await assert.rejects(
    request("https://example.com", { ...options(), signal: controller.signal }),
    /cancelled/,
  );
  assert.equal(destroyed, true);
  const failedDNS = createSafeRequester({
    resolve: async () => {
      throw new Error("secret internal DNS address");
    },
  });
  await assert.rejects(
    failedDNS("https://example.com", options()),
    (error) => error instanceof Error && error.message === "Web request failed",
  );
});
