import { describe, expect, it, vi, type Mock } from "vitest";
import {
  AdminSessionController,
  type AdminSessionChannel,
  type AdminSessionLockManager,
  type AdminSessionMessage,
  type AdminSessionOptions,
  type AdminSessionState,
} from "./session";
import { response, testJwt } from "./test-response";

const NOW = 2_000_000_000_000;

class TestChannelBus {
  readonly channels = new Set<TestChannel>();

  open(): TestChannel {
    const channel = new TestChannel(this);
    this.channels.add(channel);
    return channel;
  }

  publish(sender: TestChannel, message: AdminSessionMessage): void {
    for (const channel of this.channels) {
      if (channel !== sender) channel.deliver(message);
    }
  }
}

class TestChannel implements AdminSessionChannel {
  private readonly listeners = new Set<(event: MessageEvent<unknown>) => void>();
  private closed = false;

  constructor(private readonly bus: TestChannelBus) {}

  postMessage(message: AdminSessionMessage): void {
    if (!this.closed) this.bus.publish(this, message);
  }

  addEventListener(
    _type: "message",
    listener: (event: MessageEvent<unknown>) => void,
  ): void {
    this.listeners.add(listener);
  }

  removeEventListener(
    _type: "message",
    listener: (event: MessageEvent<unknown>) => void,
  ): void {
    this.listeners.delete(listener);
  }

  close(): void {
    this.closed = true;
    this.listeners.clear();
    this.bus.channels.delete(this);
  }

  deliver(message: AdminSessionMessage): void {
    if (this.closed) return;
    for (const listener of this.listeners) {
      listener({ data: message } as MessageEvent<unknown>);
    }
  }
}

class SerialLocks implements AdminSessionLockManager {
  readonly requestedNames: string[] = [];
  private successor: Promise<unknown> = Promise.resolve();

  request<T>(name: string, callback: () => Promise<T> | T): Promise<T> {
    this.requestedNames.push(name);
    const result = this.successor.then(() => callback());
    this.successor = result.catch(() => undefined);
    return result;
  }
}

type SessionOverrides = Omit<AdminSessionOptions, "fetch" | "onChange">;

function createSession(fetchMock: Mock, overrides: SessionOverrides = {}) {
  const states: AdminSessionState[] = [];
  const session = new AdminSessionController({
    fetch: fetchMock as unknown as typeof fetch,
    onChange: (state) => states.push(state),
    channel: null,
    locks: null,
    now: () => NOW,
    peerWaitMs: 0,
    ...overrides,
  });
  return { session, states };
}

describe("AdminSessionController refresh coordination", () => {
  it("reuses a fresh peer token without contacting refresh", async () => {
    const bus = new TestChannelBus();
    const ownerFetch = vi.fn().mockResolvedValue(response(200, { jwt: testJwt(NOW + 60_000) }));
    const peerFetch = vi.fn();
    const owner = createSession(ownerFetch, {
      channel: bus.open(),
      tabId: "owner",
    }).session;
    const peer = createSession(peerFetch, {
      channel: bus.open(),
      tabId: "peer",
    }).session;

    await owner.restore();
    await peer.restore();

    expect(peer.getAccessToken()).toBe(owner.getAccessToken());
    expect(peer.getState()).toEqual({ status: "authenticated" });
    expect(peerFetch).not.toHaveBeenCalled();
  });

  it("falls back to its own refresh when cross-tab APIs are unavailable", async () => {
    const token = testJwt(NOW + 60_000);
    const fetchMock = vi.fn().mockResolvedValue(response(200, { jwt: token }));
    const { session } = createSession(fetchMock);

    await session.restore();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(session.getAccessToken()).toBe(token);
  });

  it("allows only one concurrent refresh and reuses its successor token", async () => {
    const bus = new TestChannelBus();
    const locks = new SerialLocks();
    const token = testJwt(NOW + 60_000);
    const firstFetch = vi.fn().mockResolvedValue(response(200, { jwt: token }));
    const secondFetch = vi.fn().mockResolvedValue(response(200, { jwt: testJwt(NOW + 90_000) }));
    const first = createSession(firstFetch, {
      channel: bus.open(),
      locks,
      peerWaitMs: 0,
      tabId: "first",
    }).session;
    const second = createSession(secondFetch, {
      channel: bus.open(),
      locks,
      peerWaitMs: 0,
      tabId: "second",
    }).session;

    await Promise.all([first.restore(), second.restore()]);

    expect(firstFetch.mock.calls.length + secondFetch.mock.calls.length).toBe(1);
    expect(first.getAccessToken()).toBe(token);
    expect(second.getAccessToken()).toBe(token);
    expect(new Set(locks.requestedNames)).toEqual(
      new Set(["docu-kiosk-admin-session-refresh"]),
    );
  });

  it("requires expiration to be more than five seconds away", async () => {
    const boundaryToken = testJwt(NOW + 5_000);
    const freshBoundaryToken = testJwt(NOW + 5_001);
    const stale = createSession(
      vi.fn().mockResolvedValue(response(200, { jwt: boundaryToken })),
    ).session;
    const fresh = createSession(
      vi.fn().mockResolvedValue(response(200, { jwt: freshBoundaryToken })),
    ).session;

    await stale.restore();
    await fresh.restore();

    expect(stale.getState()).toEqual({ status: "unavailable" });
    expect(stale.getAccessToken()).toBeNull();
    expect(fresh.getState()).toEqual({ status: "authenticated" });
    expect(fresh.getAccessToken()).toBe(freshBoundaryToken);
  });

  it("does not rotate a token merely because time advances", async () => {
    let now = NOW;
    const fetchMock = vi.fn().mockResolvedValue(
      response(200, { jwt: testJwt(NOW + 60_000) }),
    );
    const { session } = createSession(fetchMock, { now: () => now });
    await session.restore();

    now += 56_000;
    await Promise.resolve();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(session.getState()).toEqual({ status: "authenticated" });
  });
});

describe("AdminSessionController protected requests", () => {
  it("sends a bearer token and refreshes then retries once after a 401", async () => {
    const bus = new TestChannelBus();
    const firstToken = testJwt(NOW + 60_000);
    const successorToken = testJwt(NOW + 120_000);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: firstToken }))
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(200, { jwt: successorToken }))
      .mockResolvedValueOnce(response(200, { ok: true }));
    const ownerChannel = bus.open();
    // Record the owner's outbound broadcasts so the refresh successor's
    // browser-session epoch is observable.
    const posted: AdminSessionMessage[] = [];
    const { session } = createSession(fetchMock, {
      channel: {
        postMessage: (message) => {
          posted.push(message);
          ownerChannel.postMessage(message);
        },
        addEventListener: (type, listener) =>
          ownerChannel.addEventListener(type, listener),
        removeEventListener: (type, listener) =>
          ownerChannel.removeEventListener(type, listener),
        close: () => ownerChannel.close(),
      },
      tabId: "owner",
    });
    await session.restore();

    const result = await session.protectedFetch("/admin/documents", {
      method: "DELETE",
    });

    expect(result.status).toBe(200);
    const firstRequest = fetchMock.mock.calls[1][0] as Request;
    const retryRequest = fetchMock.mock.calls[3][0] as Request;
    expect(firstRequest.method).toBe("DELETE");
    expect(firstRequest.headers.get("Authorization")).toBe(`Bearer ${firstToken}`);
    expect(retryRequest.headers.get("Authorization")).toBe(
      `Bearer ${successorToken}`,
    );
    expect(fetchMock).toHaveBeenCalledTimes(4);

    // The coordinated refresh keeps the successor in the same browser-session
    // epoch: the restore and refresh broadcasts must carry identical epochs.
    const tokenBroadcasts = posted.filter(
      (message): message is Extract<AdminSessionMessage, { type: "token" }> =>
        message.type === "token",
    );
    expect(tokenBroadcasts).toHaveLength(2);
    const [restoreBroadcast, refreshBroadcast] = tokenBroadcasts;
    expect(restoreBroadcast.jwt).toBe(firstToken);
    expect(refreshBroadcast.jwt).toBe(successorToken);
    expect(refreshBroadcast.epoch).toBe(restoreBroadcast.epoch);
    expect(refreshBroadcast.epoch).not.toBeNull();
  });

  it("propagates a refresh 401 as terminal loss without retrying", async () => {
    const bus = new TestChannelBus();
    const token = testJwt(NOW + 60_000);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: token }))
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(401));
    const owner = createSession(fetchMock, {
      channel: bus.open(),
      tabId: "owner",
    }).session;
    const peer = createSession(vi.fn(), {
      channel: bus.open(),
      tabId: "peer",
    }).session;
    await owner.restore();

    const result = await owner.protectedFetch("/admin/documents");

    expect(result.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(owner.getState()).toEqual({ status: "login", submitting: false });
    expect(peer.getState()).toEqual({ status: "login", submitting: false });
  });

  it("propagates a retry 401 as terminal loss and never loops", async () => {
    const bus = new TestChannelBus();
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: testJwt(NOW + 60_000) }))
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(200, { jwt: testJwt(NOW + 120_000) }))
      .mockResolvedValueOnce(response(401));
    const owner = createSession(fetchMock, {
      channel: bus.open(),
      tabId: "owner",
    }).session;
    const peer = createSession(vi.fn(), {
      channel: bus.open(),
      tabId: "peer",
    }).session;
    await owner.restore();

    const result = await owner.protectedFetch("/admin/documents");

    expect(result.status).toBe(401);
    expect(fetchMock).toHaveBeenCalledTimes(4);
    expect(owner.getAccessToken()).toBeNull();
    expect(peer.getState()).toEqual({ status: "login", submitting: false });
  });
});

describe("AdminSessionController cross-tab lifecycle", () => {
  it("shares a successful login token", async () => {
    const bus = new TestChannelBus();
    const token = testJwt(NOW + 60_000);
    const ownerFetch = vi
      .fn()
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(200, { jwt: token }));
    const owner = createSession(ownerFetch, {
      channel: bus.open(),
      tabId: "owner",
    }).session;
    const peer = createSession(vi.fn(), {
      channel: bus.open(),
      tabId: "peer",
    }).session;
    await owner.restore();

    await owner.login("administrator", "secret phrase");

    expect(owner.getAccessToken()).toBe(token);
    expect(peer.getAccessToken()).toBe(token);
    expect(peer.getState()).toEqual({ status: "authenticated" });
  });

  it("broadcasts logout only after a confirmed 204", async () => {
    const bus = new TestChannelBus();
    const token = testJwt(NOW + 60_000);
    const ownerFetch = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: token }))
      .mockResolvedValueOnce(response(500))
      .mockResolvedValueOnce(response(204));
    const owner = createSession(ownerFetch, {
      channel: bus.open(),
      tabId: "owner",
    }).session;
    const peer = createSession(vi.fn(), {
      channel: bus.open(),
      tabId: "peer",
    }).session;
    await owner.restore();

    await owner.logout();
    expect(owner.getState()).toEqual({ status: "logout-failed" });
    expect(peer.getAccessToken()).toBe(token);

    await owner.logout();
    expect(owner.getAccessToken()).toBeNull();
    expect(peer.getAccessToken()).toBeNull();
    expect(peer.getState()).toEqual({ status: "login", submitting: false });
  });

  it("keeps a signing-out tab from accepting peer tokens or serving its JWT", async () => {
    const bus = new TestChannelBus();
    const token = testJwt(NOW + 60_000);
    let resolveLogout!: (value: Response | PromiseLike<Response>) => void;
    const logoutPromise = new Promise<Response>((res) => {
      resolveLogout = res;
    });
    const ownerChannel = bus.open();
    // Record the owner's outbound messages so "serves no token" is observable.
    const posted: AdminSessionMessage[] = [];
    const owner = createSession(
      vi
        .fn()
        .mockResolvedValueOnce(response(200, { jwt: token }))
        .mockReturnValueOnce(logoutPromise),
      {
        channel: {
          postMessage: (message) => {
            posted.push(message);
            ownerChannel.postMessage(message);
          },
          addEventListener: (type, listener) =>
            ownerChannel.addEventListener(type, listener),
          removeEventListener: (type, listener) =>
            ownerChannel.removeEventListener(type, listener),
          close: () => ownerChannel.close(),
        },
        tabId: "owner",
      },
    ).session;
    const peer = createSession(vi.fn(), {
      channel: bus.open(),
      tabId: "peer",
    }).session;
    await owner.restore();
    expect(peer.getState()).toEqual({ status: "authenticated" });

    // The owner's restore broadcast pins the current browser-session epoch;
    // a refresh successor must carry the same epoch, not a later login's.
    const ownerTokenBroadcast = posted.find(
      (message): message is Extract<AdminSessionMessage, { type: "token" }> =>
        message.type === "token",
    );
    expect(ownerTokenBroadcast?.type).toBe("token");
    const sessionEpoch = ownerTokenBroadcast!.epoch;

    // The logout request is deferred, leaving the initiating tab signing-out.
    const signingOut = owner.logout();
    expect(owner.getState()).toEqual({ status: "signing-out" });

    // A peer refresh broadcasts its successor token in the same session
    // epoch while the logout is pending: the signing-out tab must not accept
    // it or invalidate its logout, but the ordinary peer adopts it.
    const successorToken = testJwt(NOW + 120_000);
    bus.open().postMessage({
      type: "token",
      tabId: "intruder",
      requestId: "intruder-broadcast",
      targetTabId: null,
      jwt: successorToken,
      epoch: sessionEpoch,
    });
    expect(owner.getState()).toEqual({ status: "signing-out" });
    expect(owner.getAccessToken()).toBe(token);
    expect(peer.getAccessToken()).toBe(successorToken);

    // A token request must go unanswered: the signing-out JWT is soon revoked.
    bus.open().postMessage({
      type: "token-request",
      tabId: "intruder",
      requestId: "intruder-request",
    });
    expect(
      posted.some(
        (message) =>
          message.type === "token" && message.requestId === "intruder-request",
      ),
    ).toBe(false);

    // The confirmed 204 still completes the logout for both tabs.
    resolveLogout(response(204));
    await signingOut;

    expect(owner.getState()).toEqual({ status: "login", submitting: false });
    expect(owner.getAccessToken()).toBeNull();
    expect(peer.getState()).toEqual({ status: "login", submitting: false });
    expect(peer.getAccessToken()).toBeNull();
    expect(posted.some((message) => message.type === "logout")).toBe(true);
  });

  it("close cancels a late refresh and detaches cross-tab messages", async () => {
    const bus = new TestChannelBus();
    let resolve!: (value: Response | PromiseLike<Response>) => void;
    const promise = new Promise<Response>((res) => {
      resolve = res;
    });
    const fetchMock = vi.fn().mockReturnValue(promise);
    const { session } = createSession(fetchMock, {
      channel: bus.open(),
      tabId: "closing",
    });
    const restoring = session.restore();

    session.close();
    bus.open().postMessage({
      type: "terminal",
      tabId: "peer",
      requestId: "terminal-after-close",
      epoch: null,
    });
    resolve(response(200, { jwt: testJwt(NOW + 60_000) }));
    await restoring;

    expect(session.getAccessToken()).toBeNull();
    expect(session.getState()).toEqual({ status: "restoring" });
    await expect(session.protectedFetch("/admin/documents")).rejects.toThrow(
      "closed",
    );
  });
});

describe("AdminSessionController state transitions", () => {
  it("keeps restoration retryable after a transient failure", async () => {
    const token = testJwt(NOW + 60_000);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(503))
      .mockResolvedValueOnce(response(200, { jwt: token }));
    const { session, states } = createSession(fetchMock);

    await session.restore();
    expect(session.getState()).toEqual({ status: "unavailable" });

    await session.retry();
    expect(states.at(-2)).toEqual({ status: "restoring" });
    expect(session.getState()).toEqual({ status: "authenticated" });
    expect(session.getAccessToken()).toBe(token);
  });

  it("uses the generic invalid-credential state for rejected login", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(401));
    const { session } = createSession(fetchMock);
    await session.restore();

    await session.login("administrator", "wrong");

    expect(session.getState()).toEqual({
      status: "invalid-credentials",
      submitting: false,
    });
    expect(session.getAccessToken()).toBeNull();
  });

  it("retains the active token when logout fails", async () => {
    const token = testJwt(NOW + 60_000);
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: token }))
      .mockRejectedValueOnce(new Error("offline"));
    const { session, states } = createSession(fetchMock);
    await session.restore();

    const logout = session.logout();
    expect(states.at(-1)).toEqual({ status: "signing-out" });
    await logout;

    expect(session.getState()).toEqual({ status: "logout-failed" });
    expect(session.getAccessToken()).toBe(token);
  });

  it("ignores a late refresh result after explicit terminal loss", async () => {
    let resolve!: (value: Response | PromiseLike<Response>) => void;
    const promise = new Promise<Response>((res) => {
      resolve = res;
    });
    const fetchMock = vi.fn().mockReturnValue(promise);
    const { session } = createSession(fetchMock);
    const restoring = session.restore();

    session.terminalAuthenticationLoss();
    resolve(response(200, { jwt: testJwt(NOW + 60_000) }));
    await restoring;

    expect(session.getState()).toEqual({ status: "login", submitting: false });
    expect(session.getAccessToken()).toBeNull();
  });
});

describe("AdminSessionController browser-session epoch", () => {
  it("ignores a stale old-epoch logout after a newer login", async () => {
    const bus = new TestChannelBus();
    const firstToken = testJwt(NOW + 60_000);
    const secondToken = testJwt(NOW + 120_000);
    const ownerFetch = vi
      .fn()
      .mockResolvedValueOnce(response(200, { jwt: firstToken }))
      .mockResolvedValueOnce(response(204))
      .mockResolvedValueOnce(response(200, { jwt: secondToken }));
    const ownerChannel = bus.open();
    const posted: AdminSessionMessage[] = [];
    const owner = createSession(ownerFetch, {
      channel: {
        postMessage: (message) => {
          posted.push(message);
          ownerChannel.postMessage(message);
        },
        addEventListener: (type, listener) =>
          ownerChannel.addEventListener(type, listener),
        removeEventListener: (type, listener) =>
          ownerChannel.removeEventListener(type, listener),
        close: () => ownerChannel.close(),
      },
      tabId: "owner",
    }).session;
    const peer = createSession(vi.fn(), {
      channel: bus.open(),
      tabId: "peer",
    }).session;

    await owner.restore();
    await owner.logout();
    await owner.login("administrator", "secret phrase");

    // The logout broadcast carries the epoch it ended, and the new login
    // rotates to a fresh epoch.
    const firstEpoch = posted.find((m) => m.type === "token")?.epoch ?? null;
    const logoutEpoch = posted.find((m) => m.type === "logout")?.epoch ?? null;
    const secondEpoch =
      posted.filter((m) => m.type === "token").at(-1)?.epoch ?? null;
    expect(logoutEpoch).toBe(firstEpoch);
    expect(secondEpoch).not.toBe(firstEpoch);

    // A logout and terminal from the superseded epoch must not terminate
    // tabs that have since logged in again.
    bus.open().postMessage({
      type: "logout",
      tabId: "stale-tab",
      requestId: "stale-logout",
      epoch: firstEpoch,
    });
    bus.open().postMessage({
      type: "terminal",
      tabId: "stale-tab",
      requestId: "stale-terminal",
      epoch: firstEpoch,
    });

    expect(owner.getState()).toEqual({ status: "authenticated" });
    expect(owner.getAccessToken()).toBe(secondToken);
    expect(peer.getState()).toEqual({ status: "authenticated" });
    expect(peer.getAccessToken()).toBe(secondToken);
  });

  it.each(["terminal", "logout"])(
    "%s during restoration followed by a late targeted token leaves login state",
    async (messageType) => {
      const bus = new TestChannelBus();
      const ownerChannel = bus.open();
      const posted: AdminSessionMessage[] = [];
      const owner = createSession(vi.fn(), {
        channel: {
          postMessage: (message) => {
            posted.push(message);
            ownerChannel.postMessage(message);
          },
          addEventListener: (type, listener) =>
            ownerChannel.addEventListener(type, listener),
          removeEventListener: (type, listener) =>
            ownerChannel.removeEventListener(type, listener),
          close: () => ownerChannel.close(),
        },
        tabId: "owner",
        peerWaitMs: 10_000,
      }).session;

      const restoring = owner.restore();
      const requestId =
        posted.find((m) => m.type === "token-request")?.requestId ?? "missing";
      expect(owner.getState()).toEqual({ status: "restoring" });

      // Loss arrives while this tab still has no epoch: the restoration
      // must stop and every pending waiter must be cancelled.
      bus.open().postMessage({
        type: messageType as "terminal" | "logout",
        tabId: "peer",
        requestId: `${messageType}-during-restore`,
        epoch: "stale-epoch",
      });
      expect(owner.getState()).toEqual({ status: "login", submitting: false });
      expect(owner.getAccessToken()).toBeNull();

      // The peer's response for the cancelled request must not resurrect
      // the terminated session.
      bus.open().postMessage({
        type: "token",
        tabId: "peer",
        requestId,
        targetTabId: "owner",
        jwt: testJwt(NOW + 60_000),
        epoch: "stale-epoch",
      });
      await restoring;

      expect(owner.getState()).toEqual({ status: "login", submitting: false });
      expect(owner.getAccessToken()).toBeNull();
    },
  );

  it("answers a reloaded tab's token request with an epoch only, so its refresh-401 terminal reaches still-open peers", async () => {
    const bus = new TestChannelBus();
    let now = NOW;
    const token = testJwt(NOW + 60_000);
    const peerFetch = vi.fn().mockResolvedValue(response(200, { jwt: token }));
    const peerChannel = bus.open();
    // Record the peer's outbound messages so the epoch-only response is
    // observable at the wire level.
    const peerPosted: AdminSessionMessage[] = [];
    const peer = createSession(peerFetch, {
      channel: {
        postMessage: (message) => {
          peerPosted.push(message);
          peerChannel.postMessage(message);
        },
        addEventListener: (type, listener) =>
          peerChannel.addEventListener(type, listener),
        removeEventListener: (type, listener) =>
          peerChannel.removeEventListener(type, listener),
        close: () => peerChannel.close(),
      },
      tabId: "peer",
      now: () => now,
    }).session;
    await peer.restore();

    // The peer's own restore broadcast pins the shared browser-session epoch.
    const peerEpoch =
      peerPosted.find(
        (message): message is Extract<AdminSessionMessage, { type: "token" }> =>
          message.type === "token",
      )?.epoch ?? null;
    expect(peerEpoch).not.toBeNull();

    // Age the shared JWT inside the five-second freshness cutoff so the
    // still-open peer must refuse to serve it to a reloaded tab.
    now += 56_000;

    // A reloaded tab starts with no epoch and no JWT of its own; its restore
    // must get only the session epoch, never the stale credential.
    const reloaderChannel = bus.open();
    const reloaderPosted: AdminSessionMessage[] = [];
    const reloaderFetch = vi.fn().mockResolvedValue(response(401));
    const { session: reloader, states: reloaderStates } = createSession(
      reloaderFetch,
      {
        channel: {
          postMessage: (message) => {
            reloaderPosted.push(message);
            reloaderChannel.postMessage(message);
          },
          addEventListener: (type, listener) =>
            reloaderChannel.addEventListener(type, listener),
          removeEventListener: (type, listener) =>
            reloaderChannel.removeEventListener(type, listener),
          close: () => reloaderChannel.close(),
        },
        tabId: "reloader",
        now: () => now,
      },
    );
    await reloader.restore();

    // The peer's responses carry only the epoch, never a jwt field, and the
    // stale JWT is never served to the reloaded tab.
    const epochResponses = peerPosted.filter(
      (message): message is Extract<AdminSessionMessage, { type: "epoch" }> =>
        message.type === "epoch",
    );
    expect(epochResponses.length).toBeGreaterThan(0);
    for (const epochResponse of epochResponses) {
      expect(epochResponse.epoch).toBe(peerEpoch);
      expect(epochResponse.targetTabId).toBe("reloader");
      expect(epochResponse).not.toHaveProperty("jwt");
    }
    expect(
      peerPosted.some(
        (message) =>
          message.type === "token" && message.targetTabId === "reloader",
      ),
    ).toBe(false);

    // The credential-free response must not authenticate the reloaded tab:
    // it goes straight to its own refresh, whose 401 is terminal.
    expect(reloaderStates.some((state) => state.status === "authenticated")).toBe(
      false,
    );
    expect(reloaderFetch).toHaveBeenCalledTimes(1);
    expect(reloader.getState()).toEqual({ status: "login", submitting: false });
    expect(reloader.getAccessToken()).toBeNull();

    // The terminal broadcast carries the learned epoch, so the still-open
    // peer in that same epoch terminates instead of ignoring it.
    const terminalBroadcast = reloaderPosted.find(
      (message): message is Extract<AdminSessionMessage, { type: "terminal" }> =>
        message.type === "terminal",
    );
    expect(terminalBroadcast?.epoch).toBe(peerEpoch);
    expect(peer.getState()).toEqual({ status: "login", submitting: false });
    expect(peer.getAccessToken()).toBeNull();
    expect(peerFetch).toHaveBeenCalledTimes(1);
  });
});
