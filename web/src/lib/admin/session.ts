export type AdminSessionState =
  | { status: "restoring" }
  | { status: "login"; submitting: boolean }
  | { status: "invalid-credentials"; submitting: boolean }
  | { status: "unavailable" }
  | { status: "authenticated" }
  | { status: "signing-out" }
  | { status: "logout-failed" };

export interface AdminSessionOptions {
  onChange: (state: AdminSessionState) => void;
  fetch?: typeof globalThis.fetch;
}

interface JwtResponse {
  jwt?: unknown;
}

export class AdminSessionController {
  private readonly onChange: (state: AdminSessionState) => void;
  private readonly fetch: typeof globalThis.fetch;
  private state: AdminSessionState = { status: "restoring" };
  private jwt: string | null = null;
  private operation = 0;

  constructor(options: AdminSessionOptions) {
    this.onChange = options.onChange;
    this.fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
  }

  getState(): AdminSessionState {
    return this.state;
  }

  getAccessToken(): string | null {
    return this.jwt;
  }

  async restore(): Promise<void> {
    const operation = ++this.operation;
    this.clearProtectedState();
    this.update({ status: "restoring" });

    try {
      const response = await this.fetch("/refresh", {
        method: "POST",
        credentials: "same-origin",
      });
      if (!this.isCurrent(operation)) return;

      if (response.status === 200) {
        const jwt = await this.readJwt(response);
        if (!this.isCurrent(operation)) return;
        if (jwt !== null) {
          this.jwt = jwt;
          this.update({ status: "authenticated" });
          return;
        }
        this.update({ status: "unavailable" });
        return;
      }

      if (response.status === 401) {
        this.update({ status: "login", submitting: false });
        return;
      }

      this.update({ status: "unavailable" });
    } catch {
      if (this.isCurrent(operation)) {
        this.update({ status: "unavailable" });
      }
    }
  }

  retry(): Promise<void> {
    return this.restore();
  }

  async login(username: string, password: string): Promise<void> {
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
        if (jwt !== null) {
          this.jwt = jwt;
          this.update({ status: "authenticated" });
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
    if (this.jwt === null) return;
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

  terminalAuthenticationLoss(): void {
    ++this.operation;
    this.clearProtectedState();
    this.update({ status: "login", submitting: false });
  }

  private async readJwt(response: Response): Promise<string | null> {
    try {
      const body = (await response.json()) as JwtResponse;
      return typeof body.jwt === "string" && body.jwt.length > 0 ? body.jwt : null;
    } catch {
      return null;
    }
  }

  private clearProtectedState(): void {
    this.jwt = null;
  }

  private isCurrent(operation: number): boolean {
    return operation === this.operation;
  }

  private update(state: AdminSessionState): void {
    this.state = state;
    this.onChange(state);
  }
}
