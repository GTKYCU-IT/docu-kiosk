import { describe, expect, it, vi, type Mock } from "vitest";
import {
  AdminSessionController,
  type AdminSessionState,
} from "./session";
import { response } from "./test-response";

function createSession(fetchMock: Mock) {
  const states: AdminSessionState[] = [];
  const session = new AdminSessionController({
    fetch: fetchMock as unknown as typeof fetch,
    onChange: (state) => states.push(state),
  });
  return { session, states };
}

describe("AdminSessionController restoration", () => {
  it("keeps protected state absent until a successful refresh authenticates", async () => {
    let resolveRefresh!: (value: Response) => void;
    const fetchMock = vi.fn(
      () => new Promise<Response>((resolve) => {
        resolveRefresh = resolve;
      }),
    );
    const { session, states } = createSession(fetchMock);

    const restoring = session.restore();
    expect(session.getState()).toEqual({ status: "restoring" });
    expect(session.getAccessToken()).toBeNull();
    expect(states).toEqual([{ status: "restoring" }]);

    resolveRefresh(response(200, { jwt: "access-token" }));
    await restoring;

    expect(fetchMock).toHaveBeenCalledWith("/refresh", {
      method: "POST",
      credentials: "same-origin",
    });
    expect(session.getState()).toEqual({ status: "authenticated" });
    expect(session.getAccessToken()).toBe("access-token");
  });

  it("treats a 401 refresh as a clean login without an invalid-credential state", async () => {
    const fetchMock = vi.fn().mockResolvedValue(response(401));
    const { session, states } = createSession(fetchMock);

    await session.restore();

    expect(states).toEqual([
      { status: "restoring" },
      { status: "login", submitting: false },
    ]);
  });

  it.each([
    ["a server failure", () => Promise.resolve(response(503))],
    ["a network failure", () => Promise.reject(new Error("offline"))],
  ])("makes restoration retryable after %s", async (_name, outcome) => {
    const fetchMock = vi.fn(outcome);
    const { session } = createSession(fetchMock);

    await session.restore();

    expect(session.getState()).toEqual({ status: "unavailable" });
    expect(session.getAccessToken()).toBeNull();
  });

  it("returns through neutral restoration when retrying", async () => {
    let resolveRetry!: (value: Response) => void;
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(500))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveRetry = resolve;
          }),
      );
    const { session, states } = createSession(fetchMock);
    await session.restore();

    const retrying = session.retry();
    expect(session.getState()).toEqual({ status: "restoring" });
    expect(states.at(-1)).toEqual({ status: "restoring" });

    resolveRetry(response(200, { jwt: "restored-token" }));
    await retrying;
    expect(session.getState()).toEqual({ status: "authenticated" });
  });
});

describe("AdminSessionController login", () => {
  it("authenticates with a JWT held only by the controller", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(200, { jwt: "login-token" }));
    const { session } = createSession(fetchMock);
    await session.restore();

    await session.login("administrator", "secret phrase");

    expect(fetchMock).toHaveBeenLastCalledWith("/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        username: "administrator",
        password: "secret phrase",
      }),
    });
    expect(session.getState()).toEqual({ status: "authenticated" });
    expect(session.getAccessToken()).toBe("login-token");
    expect(JSON.stringify(session.getState())).not.toContain("login-token");
    expect(JSON.stringify(session.getState())).not.toContain("secret phrase");
  });

  it.each([
    ["rejected credentials", () => Promise.resolve(response(401))],
    ["an unavailable Broker", () => Promise.reject(new Error("offline"))],
  ])("reports one generic invalid-credential state for %s", async (_name, outcome) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(401))
      .mockImplementationOnce(outcome);
    const { session } = createSession(fetchMock);
    await session.restore();

    await session.login("administrator", "wrong");

    expect(session.getState()).toEqual({
      status: "invalid-credentials",
      submitting: false,
    });
    expect(session.getAccessToken()).toBeNull();
  });
});

describe("AdminSessionController sign out and terminal loss", () => {
  it("represents an in-flight sign out as its own session state", async () => {
    let resolveLogout!: (value: Response) => void;
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: "active-token" }))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveLogout = resolve;
          }),
      );
    const { session, states } = createSession(fetchMock);
    await session.restore();

    const logout = session.logout();

    expect(session.getState()).toEqual({ status: "signing-out" });
    expect(states.at(-1)).toEqual({ status: "signing-out" });
    expect(session.getAccessToken()).toBe("active-token");

    resolveLogout(response(500));
    await logout;
    expect(session.getState()).toEqual({ status: "logout-failed" });
  });

  it.each([
    ["a non-success response", () => Promise.resolve(response(500))],
    ["an unexpected non-204 success", () => Promise.resolve(response(202))],
    ["a network failure", () => Promise.reject(new Error("offline"))],
  ])("keeps the active token and protected state after %s", async (_name, outcome) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: "active-token" }))
      .mockImplementationOnce(outcome);
    const { session } = createSession(fetchMock);
    await session.restore();

    await session.logout();

    expect(session.getState()).toEqual({ status: "logout-failed" });
    expect(session.getAccessToken()).toBe("active-token");
  });

  it("clears protected state in place only after a confirmed 204", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: "active-token" }))
      .mockResolvedValueOnce(response(204));
    const { session } = createSession(fetchMock);
    await session.restore();

    await session.logout();

    expect(fetchMock).toHaveBeenLastCalledWith("/logout", {
      method: "POST",
      credentials: "same-origin",
    });
    expect(session.getState()).toEqual({ status: "login", submitting: false });
    expect(session.getAccessToken()).toBeNull();
  });

  it("treats a logout 401 as terminal authentication loss", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: "active-token" }))
      .mockResolvedValueOnce(response(401));
    const { session, states } = createSession(fetchMock);
    await session.restore();

    await session.logout();

    expect(states.slice(-2)).toEqual([
      { status: "signing-out" },
      { status: "login", submitting: false },
    ]);
    expect(session.getState()).toEqual({ status: "login", submitting: false });
    expect(session.getAccessToken()).toBeNull();
  });

  it("terminal authentication loss clears state and ignores a late request result", async () => {
    let resolveRefresh!: (value: Response) => void;
    const fetchMock = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          resolveRefresh = resolve;
        }),
    );
    const { session } = createSession(fetchMock);
    const restoring = session.restore();

    session.terminalAuthenticationLoss();
    expect(session.getState()).toEqual({ status: "login", submitting: false });
    expect(session.getAccessToken()).toBeNull();

    resolveRefresh(response(200, { jwt: "late-token" }));
    await restoring;
    expect(session.getState()).toEqual({ status: "login", submitting: false });
    expect(session.getAccessToken()).toBeNull();
  });
});
