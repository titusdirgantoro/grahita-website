import { describe, expect, it, vi } from "vitest";

import { ApiClient, ApiClientError, CSRF_COOKIE, CSRF_HEADER } from "./index";

/** respon membuat Response JSON; 204 tidak boleh membawa body. */
function respon(body: string | null, status = 200): Response {
  return new Response(status === 204 ? null : body, {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type FetchImpl = (input: string, init?: RequestInit) => Promise<Response>;

/** mockFetch mengetik argumen fetch tiruan supaya mock.calls bisa dibaca. */
function mockFetch(impl: FetchImpl) {
  return vi.fn((input: string, init?: RequestInit) => impl(input, init));
}

describe("ApiClient", () => {
  it("membuka amplop { data } pada respons sukses", async () => {
    const fetchImpl = mockFetch(async () => respon(JSON.stringify({ data: { status: "ok" } })));
    const client = new ApiClient({ fetchImpl });

    const hasil = await client.get<{ status: string }>("/healthz");
    expect(hasil.status).toBe("ok");
    expect(fetchImpl).toHaveBeenCalledOnce();
  });

  it("memakai API same-origin /api/v1 secara default", async () => {
    const fetchImpl = mockFetch(async () => respon(JSON.stringify({ data: [] })));
    const client = new ApiClient({ fetchImpl });

    await client.get("/catalog");

    expect(fetchImpl.mock.calls[0]?.[0]).toBe("/api/v1/catalog");
    expect(fetchImpl.mock.calls[0]?.[1]?.credentials).toBe("same-origin");
  });

  it("menerjemahkan { error: { code, message, fields } } menjadi ApiClientError", async () => {
    const fetchImpl = mockFetch(async () =>
      respon(
        JSON.stringify({
          error: {
            code: "VALIDATION_FAILED",
            message: "Data tidak valid.",
            fields: { email: "format email tidak valid" },
          },
        }),
        400,
      ),
    );
    const client = new ApiClient({ fetchImpl });

    const err = await client.post("/auth/login", {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiClientError);
    const apiErr = err as ApiClientError;
    expect(apiErr.status).toBe(400);
    expect(apiErr.code).toBe("VALIDATION_FAILED");
    expect(apiErr.fields.email).toBe("format email tidak valid");
    expect(apiErr.mapFields).toEqual({ email: "format email tidak valid" });
  });

  it("mengirim header CSRF untuk request non-GET", async () => {
    const fetchImpl = mockFetch(async () => respon(null, 204));
    const client = new ApiClient({
      fetchImpl,
      cookieReader: (name) => (name === CSRF_COOKIE ? "token-uji" : null),
    });

    await client.post("/auth/logout");

    const headers = fetchImpl.mock.calls[0]?.[1]?.headers as Record<string, string>;
    expect(headers[CSRF_HEADER]).toBe("token-uji");
  });

  it("tidak mengirim header CSRF untuk GET", async () => {
    const fetchImpl = mockFetch(async () => respon(JSON.stringify({ data: {} })));
    const client = new ApiClient({
      fetchImpl,
      cookieReader: () => "token-uji",
    });

    await client.get("/auth/me");

    const headers = fetchImpl.mock.calls[0]?.[1]?.headers as Record<string, string>;
    expect(headers[CSRF_HEADER]).toBeUndefined();
  });

  it("mengembalikan undefined untuk 204", async () => {
    const fetchImpl = mockFetch(async () => respon(null, 204));
    const client = new ApiClient({ fetchImpl });

    await expect(client.delete("/orders/1")).resolves.toBeUndefined();
  });

  it("menandai respons yang bukan JSON", async () => {
    const fetchImpl = mockFetch(
      async () => new Response("<html>gagal</html>", { status: 502 }),
    );
    const client = new ApiClient({ fetchImpl });

    const err = (await client.get("/healthz").catch((e: unknown) => e)) as ApiClientError;
    expect(err.code).toBe("INVALID_RESPONSE");
    expect(err.status).toBe(502);
  });
});
