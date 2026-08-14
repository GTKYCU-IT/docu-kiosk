export type AdminSessionState =
  | { status: "restoring" }
  | { status: "login"; submitting: boolean }
  | { status: "invalid-credentials"; submitting: boolean }
  | { status: "unavailable" }
  | { status: "authenticated" }
  | { status: "signing-out" }
  | { status: "logout-failed" };

export type AdminSessionMessage =
  | { type: "token-request"; tabId: string; requestId: string }
  | {
      type: "token";
      tabId: string;
      requestId: string;
      targetTabId: string | null;
      jwt: string;
    }
  | { type: "terminal"; tabId: string; requestId: string }
  | { type: "logout"; tabId: string; requestId: string };

export interface AdminSessionChannel {
  postMessage(message: AdminSessionMessage): void;
  addEventListener(
    type: "message",
    listener: (event: MessageEvent<unknown>) => void,
  ): void;
  removeEventListener(
    type: "message",
    listener: (event: MessageEvent<unknown>) => void,
  ): void;
  close(): void;
}

export interface AdminSessionLockManager {
  request<T>(name: string, callback: () => Promise<T> | T): Promise<T>;
}

export interface AdminSessionOptions {
  onChange: (state: AdminSessionState) => void;
  fetch?: typeof globalThis.fetch;
  channel?: AdminSessionChannel | null;
  locks?: AdminSessionLockManager | null;
  now?: () => number;
  peerWaitMs?: number;
  tabId?: string;
}

interface JwtResponse {
  jwt?: unknown;
}

interface PeerWaiter {
  resolve: (jwt: string | null) => void;
  timer: number;
  rejectedJwt: string | null;
}

const CHANNEL_NAME = "docu-kiosk-admin-session";
const REFRESH_LOCK_NAME = "docu-kiosk-admin-session-refresh";
const MINIMUM_TOKEN_LIFETIME_MS = 5_000;
const DEFAULT_PEER_WAIT_MS = 150;

export class AdminSessionController {
  private readonly onChange: (state: AdminSessionState) => void;
  private readonly fetch: typeof globalThis.fetch;
  private readonly channel: AdminSessionChannel | null;
  private readonly locks: AdminSessionLockManager | null;
  private readonly now: () => number;
  private readonly peerWaitMs: number;
  private readonly tabId: string;
  private readonly peerWaiters = new Map<string, PeerWaiter>();
  private state: AdminSessionState = { status: "restoring" };
  private jwt: string | null = null;
  private operation = 0;
  private closed = false;

  private readonly receiveMessage = (event: MessageEvent<unknown>): void => {
    const message = this.parseMessage(event.data);
    if (message === null || message.tabId === this.tabId || this.closed) return;

    if (message.type === "token-request") {
      // A signing-out tab must not serve its soon-revoked JWT to peers.
      if (
        this.state.status !== "signing-out" &&
        this.jwt !== null &&
        this.isFreshJwt(this.jwt)
      ) {
        this.postMessage({
          type: "token",
          tabId: this.tabId,
          requestId: message.requestId,
          targetTabId: message.tabId,
          jwt: this.jwt,
        });
      }
      return;
    }

    if (message.type === "token") {
      // Peer token broadcasts must not invalidate a pending logout.
      if (this.state.status === "signing-out") return;
      if (message.targetTabId !== null && message.targetTabId !== this.tabId) return;
      if (!this.isFreshJwt(message.jwt)) return;

      const waiter = this.peerWaiters.get(message.requestId);
      if (waiter !== undefined && waiter.rejectedJwt === message.jwt) return;
      if (message.targetTabId === null) ++this.operation;

      this.acceptToken(message.jwt, false);
      if (waiter !== undefined) {
        clearTimeout(waiter.timer);
        this.peerWaiters.delete(message.requestId);
        waiter.resolve(message.jwt);
      }
      return;
    }

    this.localTerminalTransition();
  };

  constructor(options: AdminSessionOptions) {
    this.onChange = options.onChange;
    this.fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.now = options.now ?? Date.now;
    this.peerWaitMs = Math.max(0, options.peerWaitMs ?? DEFAULT_PEER_WAIT_MS);
    this.tabId = options.tabId ?? this.createId();
    this.channel =
      options.channel === undefined ? this.createChannel() : options.channel;
    this.locks = options.locks === undefined ? this.getBrowserLocks() : options.locks;
    this.channel?.addEventListener("message", this.receiveMessage);
  }

  getState(): AdminSessionState {
    return this.state;
  }

  getAccessToken(): string | null {
    return this.jwt;
  }

  async restore(): Promise<void> {
    if (this.closed) return;

    const operation = ++this.operation;
    this.clearProtectedState();
    this.update({ status: "restoring" });

    const peerJwt = await this.requestPeerToken(null);
    if (!this.isCurrent(operation)) return;
    if (peerJwt !== null) {
      this.acceptToken(peerJwt, false);
      return;
    }

    await this.refreshWithCoordination(operation, null);
  }

  retry(): Promise<void> {
    return this.restore();
  }

  async login(username: string, password: string): Promise<void> {
    if (this.closed) return;
    if (this.state.status !== "login" && this.state.status !== "invalid-credentials") {
      return;
    }

    const operation = ++this.operation;
    this.update({ status: "login", submitting: true });

    try {
      const response = await this.fetch("/login", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      if (!this.isCurrent(operation)) return;

      if (response.status === 200) {
        const jwt = await this.readJwt(response);
        if (!this.isCurrent(operation)) return;
        if (jwt !== null && this.isFreshJwt(jwt)) {
          this.acceptToken(jwt, true);
          return;
        }
      }

      this.clearProtectedState();
      this.update({ status: "invalid-credentials", submitting: false });
    } catch {
      if (!this.isCurrent(operation)) return;
      this.clearProtectedState();
      this.update({ status: "invalid-credentials", submitting: false });
    }
  }

  async logout(): Promise<void> {
    if (this.closed || this.jwt === null) return;
    if (this.state.status !== "authenticated" && this.state.status !== "logout-failed") {
      return;
    }

    const operation = ++this.operation;
    this.update({ status: "signing-out" });

    try {
      const response = await this.fetch("/logout", {
        method: "POST",
        credentials: "same-origin",
      });
      if (!this.isCurrent(operation)) return;

      if (response.status === 204) {
        this.clearProtectedState();
        this.update({ status: "login", submitting: false });
        this.postMessage({
          type: "logout",
          tabId: this.tabId,
          requestId: this.createId(),
        });
        return;
      }
      if (response.status === 401) {
        this.terminalAuthenticationLoss();
        return;
      }

      this.update({ status: "logout-failed" });
    } catch {
      if (this.isCurrent(operation)) {
        this.update({ status: "logout-failed" });
      }
    }
  }

  async protectedFetch(
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> {
    if (this.closed) throw new Error("The admin session is closed");

    if (this.jwt === null || !this.isFreshJwt(this.jwt)) {
      const operation = ++this.operation;
      this.clearProtectedState();
      await this.refreshWithCoordination(operation, null);
    }

    if (this.closed) throw new Error("The admin session is closed");
    if (this.jwt === null || !this.isFreshJwt(this.jwt)) {
      throw new Error("Unable to acquire an admin access token");
    }

    const request = new Request(this.resolveTarget(input), init);
    const rejectedJwt = this.jwt;
    const response = await this.fetch(this.withBearer(request, rejectedJwt));
    if (this.closed || response.status !== 401) return response;

    const operation = ++this.operation;
    if (this.jwt === rejectedJwt) this.clearProtectedState();
    await this.refreshWithCoordination(operation, rejectedJwt);

    if (this.closed) throw new Error("The admin session is closed");
    if (this.jwt === null || !this.isFreshJwt(this.jwt) || this.jwt === rejectedJwt) {
      return response;
    }

    const retry = await this.fetch(this.withBearer(request, this.jwt));
    if (retry.status === 401) this.terminalAuthenticationLoss();
    return retry;
  }

  terminalAuthenticationLoss(): void {
    if (this.closed) return;

    // Local-origin loss: this tab's own fetch failed, so peers must hear about it.
    this.localTerminalTransition();
    this.postMessage({
      type: "terminal",
      tabId: this.tabId,
      requestId: this.createId(),
    });
  }

  close(): void {
    if (this.closed) return;

    this.closed = true;
    ++this.operation;
    this.clearProtectedState();
    for (const waiter of this.peerWaiters.values()) {
      clearTimeout(waiter.timer);
      waiter.resolve(null);
    }
    this.peerWaiters.clear();
    this.channel?.removeEventListener("message", this.receiveMessage);
    this.channel?.close();
  }

  private async refreshWithCoordination(
    operation: number,
    rejectedJwt: string | null,
  ): Promise<void> {
    if (!this.isCurrent(operation)) return;

    const refresh = async (): Promise<void> => {
      if (!this.isCurrent(operation)) return;
      if (
        this.jwt !== null &&
        this.jwt !== rejectedJwt &&
        this.isFreshJwt(this.jwt)
      ) {
        return;
      }

      const peerJwt = await this.requestPeerToken(rejectedJwt);
      if (!this.isCurrent(operation)) return;
      if (peerJwt !== null) {
        this.acceptToken(peerJwt, false);
        return;
      }

      await this.refreshFromServer(operation);
    };

    if (this.locks === null) {
      await refresh();
      return;
    }

    try {
      await this.locks.request(REFRESH_LOCK_NAME, refresh);
    } catch {
      if (this.isCurrent(operation)) await refresh();
    }
  }

  private async refreshFromServer(operation: number): Promise<void> {
    try {
      const response = await this.fetch("/refresh", {
        method: "POST",
        credentials: "same-origin",
      });
      if (!this.isCurrent(operation)) return;

      if (response.status === 200) {
        const jwt = await this.readJwt(response);
        if (!this.isCurrent(operation)) return;
        if (jwt !== null && this.isFreshJwt(jwt)) {
          this.acceptToken(jwt, true);
          return;
        }
        this.clearProtectedState();
        this.update({ status: "unavailable" });
        return;
      }

      if (response.status === 401) {
        this.terminalAuthenticationLoss();
        return;
      }

      this.clearProtectedState();
      this.update({ status: "unavailable" });
    } catch {
      if (this.isCurrent(operation)) {
        this.clearProtectedState();
        this.update({ status: "unavailable" });
      }
    }
  }

  private requestPeerToken(rejectedJwt: string | null): Promise<string | null> {
    if (this.channel === null || this.closed) return Promise.resolve(null);

    const requestId = this.createId();
    let resolve: (jwt: string | null) => void = () => {};
    const promise = new Promise<string | null>((resolvePromise) => {
      resolve = resolvePromise;
    });
    const timer = globalThis.setTimeout(() => {
      this.peerWaiters.delete(requestId);
      resolve(null);
    }, this.peerWaitMs);
    this.peerWaiters.set(requestId, { resolve, timer, rejectedJwt });
    this.postMessage({ type: "token-request", tabId: this.tabId, requestId });
    return promise;
  }

  private acceptToken(jwt: string, broadcast: boolean): void {
    if (this.closed || !this.isFreshJwt(jwt)) return;

    this.jwt = jwt;
    this.update({ status: "authenticated" });
    if (broadcast) {
      this.postMessage({
        type: "token",
        tabId: this.tabId,
        requestId: this.createId(),
        targetTabId: null,
        jwt,
      });
    }
  }

  private resolveTarget(input: RequestInfo | URL): RequestInfo | URL {
    if (typeof input !== "string") return input;
    return new URL(input, this.sameOriginBase());
  }

  private sameOriginBase(): string {
    try {
      const href = globalThis.location?.href;
      if (typeof href === "string" && href.length > 0) return href;
    } catch {
      // No location in non-browser runtimes; fall back to a harmless
      // same-origin base so relative targets still construct.
    }
    return "http://localhost/";
  }

  private withBearer(request: Request, jwt: string): Request {
    const headers = new Headers(request.headers);
    headers.set("Authorization", `Bearer ${jwt}`);
    return new Request(request.clone(), { headers });
  }

  private async readJwt(response: Response): Promise<string | null> {
    try {
      const body = (await response.json()) as JwtResponse;
      return typeof body.jwt === "string" && body.jwt.length > 0 ? body.jwt : null;
    } catch {
      return null;
    }
  }

  private isFreshJwt(jwt: string): boolean {
    try {
      const parts = jwt.split(".");
      if (parts.length !== 3 || parts[1].length === 0) return false;
      const normalized = parts[1].replace(/-/g, "+").replace(/_/g, "/");
      const padding = (4 - (normalized.length % 4)) % 4;
      const payload = JSON.parse(
        globalThis.atob(normalized + "=".repeat(padding)),
      ) as { exp?: unknown };
      return (
        typeof payload.exp === "number" &&
        Number.isFinite(payload.exp) &&
        payload.exp * 1_000 > this.now() + MINIMUM_TOKEN_LIFETIME_MS
      );
    } catch {
      return false;
    }
  }

  private parseMessage(value: unknown): AdminSessionMessage | null {
    if (typeof value !== "object" || value === null) return null;
    const message = value as Record<string, unknown>;
    if (typeof message.tabId !== "string" || typeof message.requestId !== "string") {
      return null;
    }

    if (message.type === "token-request") {
      return {
        type: "token-request",
        tabId: message.tabId,
        requestId: message.requestId,
      };
    }
    if (message.type === "terminal" || message.type === "logout") {
      return {
        type: message.type,
        tabId: message.tabId,
        requestId: message.requestId,
      };
    }
    if (
      message.type === "token" &&
      typeof message.jwt === "string" &&
      (message.targetTabId === null || typeof message.targetTabId === "string")
    ) {
      return {
        type: "token",
        tabId: message.tabId,
        requestId: message.requestId,
        targetTabId: message.targetTabId,
        jwt: message.jwt,
      };
    }
    return null;
  }

  private postMessage(message: AdminSessionMessage): void {
    if (this.closed) return;
    try {
      this.channel?.postMessage(message);
    } catch {
      // A closed or unavailable channel degrades to single-tab coordination.
    }
  }

  private createChannel(): AdminSessionChannel | null {
    try {
      if (typeof globalThis.BroadcastChannel !== "function") return null;
      return new globalThis.BroadcastChannel(CHANNEL_NAME);
    } catch {
      return null;
    }
  }

  private getBrowserLocks(): AdminSessionLockManager | null {
    try {
      return (globalThis.navigator?.locks as AdminSessionLockManager | undefined) ?? null;
    } catch {
      return null;
    }
  }

  private createId(): string {
    try {
      if (typeof globalThis.crypto?.randomUUID === "function") {
        return globalThis.crypto.randomUUID();
      }
    } catch {
      // Fall through to an in-memory identifier when crypto is unavailable.
    }
    return `${this.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  }

  private clearProtectedState(): void {
    this.jwt = null;
  }

  // Terminal loss observed by this tab (own fetch or a peer terminal/logout
  // message): cancel the in-flight operation, drop the JWT, and show login.
  // Broadcasting is the caller's choice so peer-derived messages stay local.
  private localTerminalTransition(): void {
    ++this.operation;
    this.clearProtectedState();
    this.update({ status: "login", submitting: false });
  }

  private isCurrent(operation: number): boolean {
    return !this.closed && operation === this.operation;
  }

  private update(state: AdminSessionState): void {
    if (this.closed) return;
    this.state = state;
    this.onChange(state);
  }
}
