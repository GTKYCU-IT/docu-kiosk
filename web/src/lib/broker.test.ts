import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { BrokerConnection, type BrokerSocket, type BrokerState } from "./broker";

class FakeSocket implements BrokerSocket {
  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;

  open(): void {
    this.onopen?.();
  }

  message(data: string): void {
    this.onmessage?.(new MessageEvent("message", { data }));
  }

  closeFromServer(): void {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.();
  }

  error(): void {
    this.onerror?.();
  }

  close(): void {
    this.closed = true;
  }
}

function makeHarness() {
  const sockets: FakeSocket[] = [];
  const states: BrokerState[] = [];
  const onChange = (state: BrokerState) => {
    states.push(state);
  };
  const conn = new BrokerConnection({
    url: "ws://test",
    onChange,
    reconnectDelayMs: 1000,
    createSocket: () => {
      const s = new FakeSocket();
      sockets.push(s);
      return s;
    },
  });
  return { sockets, states, conn };
}

const greeting = (name: string) => JSON.stringify({ type: "connected", name });
const sign = (url: string) => JSON.stringify({ type: "sign", url });

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("BrokerConnection", () => {
  it("reports ready with the kiosk name from the wire on greeting", () => {
    const { sockets, states } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    expect(states).toEqual([{ status: "ready", kioskName: "lobby-1" }]);
  });

  it("reports signing with the kiosk name and signing url", () => {
    const { sockets, states } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].message(sign("https://sign.example/abc"));
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "signing", kioskName: "lobby-1", signingUrl: "https://sign.example/abc" },
    ]);
  });

  it("warns and drops malformed JSON without throwing", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { sockets, states } = makeHarness();
    expect(() => sockets[0].message("{not json")).not.toThrow();
    expect(warn).toHaveBeenCalled();
    expect(states).toEqual([]);
  });

  it("warns and ignores an unknown message type", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { sockets, states } = makeHarness();
    sockets[0].message(JSON.stringify({ type: "ping" }));
    expect(warn).toHaveBeenCalled();
    expect(states).toEqual([]);
  });

  it("warns and ignores a greeting with a non-string name", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { sockets, states } = makeHarness();
    sockets[0].message(JSON.stringify({ type: "connected", name: 5 }));
    expect(warn).toHaveBeenCalledWith("broker: dropping malformed connected message", {
      type: "connected",
      name: 5,
    });
    expect(states).toEqual([]);
  });

  it("warns and ignores a sign message without a url", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { sockets, states } = makeHarness();
    sockets[0].message(JSON.stringify({ type: "sign" }));
    expect(warn).toHaveBeenCalledWith("broker: dropping malformed sign message", {
      type: "sign",
    });
    expect(states).toEqual([]);
  });

  it("warns on socket error without notifying and still accepts a later greeting", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { sockets, states } = makeHarness();
    sockets[0].error();
    expect(warn).toHaveBeenCalledWith("broker: socket error");
    expect(states).toEqual([]);
    sockets[0].message(greeting("lobby-1"));
    expect(states).toEqual([{ status: "ready", kioskName: "lobby-1" }]);
  });

  it("retries a pre-greeting close without notifying and becomes ready on the reconnect greeting", () => {
    const { sockets, states } = makeHarness();
    sockets[0].closeFromServer();
    expect(states).toEqual([]);
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(2);
    sockets[1].message(greeting("lobby-1"));
    expect(states).toEqual([{ status: "ready", kioskName: "lobby-1" }]);
  });

  it("gives up as unregistered after exhausting the pre-greeting retry budget", () => {
    const { sockets, states } = makeHarness();
    sockets[0].closeFromServer();
    vi.advanceTimersByTime(1000);
    sockets[1].closeFromServer();
    vi.advanceTimersByTime(1000);
    sockets[2].closeFromServer();
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(4);
    sockets[3].closeFromServer();
    expect(states).toEqual([{ status: "unregistered" }]);
    expect(sockets).toHaveLength(4);
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(4);
  });

  it("reconnects after a ready socket closes and becomes ready again on greeting", () => {
    const { sockets, states } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].closeFromServer();
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "reconnecting", kioskName: "lobby-1" },
    ]);
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(2);
    sockets[1].message(greeting("lobby-1"));
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "reconnecting", kioskName: "lobby-1" },
      { status: "ready", kioskName: "lobby-1" },
    ]);
  });

  it("stays signing across a close and a reconnect greeting", () => {
    const { sockets, states } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].message(sign("https://sign.example/abc"));
    sockets[0].closeFromServer();
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "signing", kioskName: "lobby-1", signingUrl: "https://sign.example/abc" },
    ]);
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(2);
    sockets[1].message(greeting("lobby-1"));
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "signing", kioskName: "lobby-1", signingUrl: "https://sign.example/abc" },
    ]);
  });

  it("finishSigning with a live socket returns to ready", () => {
    const { sockets, states, conn } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].message(sign("https://sign.example/abc"));
    conn.finishSigning();
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "signing", kioskName: "lobby-1", signingUrl: "https://sign.example/abc" },
      { status: "ready", kioskName: "lobby-1" },
    ]);
  });

  it("finishSigning after the socket dropped reports reconnecting", () => {
    const { sockets, states, conn } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].message(sign("https://sign.example/abc"));
    sockets[0].closeFromServer();
    conn.finishSigning();
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "signing", kioskName: "lobby-1", signingUrl: "https://sign.example/abc" },
      { status: "reconnecting", kioskName: "lobby-1" },
    ]);
  });

  it("finishSigning reports reconnecting while the replacement socket is not yet greeted", () => {
    const { sockets, states, conn } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].message(sign("https://sign.example/abc"));
    sockets[0].closeFromServer();
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(2);
    conn.finishSigning();
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "signing", kioskName: "lobby-1", signingUrl: "https://sign.example/abc" },
      { status: "reconnecting", kioskName: "lobby-1" },
    ]);
  });

  it("finishSigning is a no-op while ready", () => {
    const { sockets, states, conn } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    conn.finishSigning();
    expect(states).toEqual([{ status: "ready", kioskName: "lobby-1" }]);
  });

  it("close tears down and ignores every subsequent event", () => {
    const { sockets, states, conn } = makeHarness();
    conn.close();
    expect(sockets[0].closed).toBe(true);
    sockets[0].closeFromServer();
    sockets[0].message(greeting("lobby-1"));
    vi.advanceTimersByTime(60_000);
    expect(states).toEqual([]);
    expect(sockets).toHaveLength(1);
  });

  it("keeps retrying across repeated failures", () => {
    const { sockets, states } = makeHarness();
    sockets[0].message(greeting("lobby-1"));
    sockets[0].closeFromServer();
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(2);
    sockets[1].closeFromServer();
    vi.advanceTimersByTime(1000);
    expect(sockets).toHaveLength(3);
    sockets[2].message(greeting("lobby-1"));
    expect(states).toEqual([
      { status: "ready", kioskName: "lobby-1" },
      { status: "reconnecting", kioskName: "lobby-1" },
      { status: "reconnecting", kioskName: "lobby-1" },
      { status: "ready", kioskName: "lobby-1" },
    ]);
  });
});
