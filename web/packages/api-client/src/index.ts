/**
 * @grahita/api-client — kontrak HTTP bersama kedua portal.
 *
 * Aturan yang dikunci di sini (SystemDesign.md §6):
 *  - API selalu same-origin di /api/v1, sehingga tidak ada CORS di produksi.
 *  - Cookie sesi host-only: cukup `credentials: "same-origin"`.
 *  - Respons sukses berbentuk { data: ... }.
 *  - Respons gagal berbentuk { error: { code, message, fields? } }.
 *  - Request non-GET wajib membawa header X-CSRF-Token dari cookie grahita_csrf.
 */

/** Basis API; relatif supaya kedua host memakai API same-origin-nya sendiri. */
export const API_BASE = "/api/v1";

/** Nama cookie CSRF double-submit (bukan HttpOnly, dibaca JavaScript). */
export const CSRF_COOKIE = "grahita_csrf";

/** Nama header CSRF yang diperiksa backend. */
export const CSRF_HEADER = "X-CSRF-Token";

/** Bentuk payload error dari backend. */
export interface ApiErrorPayload {
  code: string;
  message: string;
  fields?: Record<string, string>;
}

/** Error API bertipe; `fields` dipakai untuk menempelkan pesan ke input. */
export class ApiClientError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: Record<string, string>;

  constructor(status: number, payload: ApiErrorPayload) {
    super(payload.message);
    this.name = "ApiClientError";
    this.status = status;
    this.code = payload.code;
    this.fields = payload.fields ?? {};
  }

  /** mapFields mengembalikan pesan per nama field form. */
  get mapFields(): Record<string, string> {
    return this.fields;
  }
}

/** Penanda tujuan request (dipakai untuk penanganan 401 terpusat). */
export interface RequestOptions {
  signal?: AbortSignal;
  /** Lewati header CSRF (mis. untuk endpoint yang memang dikecualikan). */
  skipCsrf?: boolean;
}

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface ApiClientConfig {
  base?: string;
  fetchImpl?: FetchLike;
  /** document dipakai membaca cookie CSRF; bisa diganti pada pengujian. */
  cookieReader?: (name: string) => string | null;
}

function bacaCookie(name: string): string | null {
  if (typeof document === "undefined") {
    return null;
  }
  const target = `${name}=`;
  for (const bagian of document.cookie.split(";")) {
    const bersih = bagian.trim();
    if (bersih.startsWith(target)) {
      return decodeURIComponent(bersih.slice(target.length));
    }
  }
  return null;
}

/** ApiClient membungkus fetch dengan kontrak di atas. */
export class ApiClient {
  private readonly base: string;
  private readonly fetchImpl: FetchLike;
  private readonly cookieReader: (name: string) => string | null;

  constructor(config: ApiClientConfig = {}) {
    this.base = config.base ?? API_BASE;
    this.fetchImpl = config.fetchImpl ?? ((input, init) => fetch(input, init));
    this.cookieReader = config.cookieReader ?? bacaCookie;
  }

  /** get mengambil data dari endpoint yang tidak mengubah state. */
  get<T>(path: string, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("GET", path, undefined, options);
  }

  /** post membuat sumber daya baru atau menjalankan aksi. */
  post<T>(path: string, body?: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("POST", path, body, options);
  }

  /** put mengganti isi sumber daya. */
  put<T>(path: string, body?: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("PUT", path, body, options);
  }

  /** delete menghapus sumber daya. */
  delete<T>(path: string, options: RequestOptions = {}): Promise<T> {
    return this.request<T>("DELETE", path, undefined, options);
  }

  private async request<T>(
    method: string,
    path: string,
    body: unknown,
    options: RequestOptions,
  ): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    const init: RequestInit = {
      method,
      headers,
      credentials: "same-origin",
      signal: options.signal,
    };

    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }

    if (method !== "GET" && method !== "HEAD" && !options.skipCsrf) {
      const token = this.cookieReader(CSRF_COOKIE);
      if (token) {
        headers[CSRF_HEADER] = token;
      }
    }

    const url = `${this.base}${path}`;
    const response = await this.fetchImpl(url, init);

    if (response.status === 204) {
      return undefined as T;
    }

    const teks = await response.text();
    let parsed: unknown = undefined;
    if (teks) {
      try {
        parsed = JSON.parse(teks);
      } catch {
        throw new ApiClientError(response.status, {
          code: "INVALID_RESPONSE",
          message: "Server mengirim respons yang tidak dikenali.",
        });
      }
    }

    if (!response.ok) {
      const payload = (parsed as { error?: ApiErrorPayload } | undefined)?.error;
      throw new ApiClientError(
        response.status,
        payload ?? {
          code: "UNKNOWN",
          message: `Permintaan gagal dengan status ${response.status}.`,
        },
      );
    }

    const envelope = parsed as { data?: T } | undefined;
    return (envelope?.data ?? (parsed as T)) as T;
  }
}

/** Klien default untuk pemakaian di komponen. */
export const api = new ApiClient();
