// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
import { render, fireEvent, screen, cleanup } from "@testing-library/svelte";
import { tick } from "svelte";
import App from "./App.svelte";
import Register from "./lib/components/Register.svelte";
import { PROBLEM_ALREADY_REGISTERED } from "./lib/registration";

/**
 * Minimal WebSocket fake standing in for the browser WebSocket the broker's
 * default socket factory wraps. App mounts a real BrokerConnection, so this
 * global is what the broker connects through.
 */
class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  url: string;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }

  open(): void {
    this.onopen?.(new Event("open"));
  }

  message(data: string): void {
    this.onmessage?.(new MessageEvent("message", { data }));
  }

  /** The broker sees a server-side close: fires onclose and marks closed. */
  closeFromServer(): void {
    if (this.closed) return;
    this.closed = true;
    this.onclose?.(new CloseEvent("close"));
  }

  close(): void {
    this.closed = true;
  }
}

// The broker never names a socket, so tests address sockets positionally in
// creation order; this is the one currently driving the session.
function lastSocket() {
  return FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
}

function jsonResponse(status: number, body: unknown, contentType: string) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (name: string) => (name === "content-type" ? contentType : null) },
    text: () => Promise.resolve(JSON.stringify(body)),
    json: () => Promise.resolve(body),
  };
}

let fetchMock: Mock;

beforeEach(() => {
  vi.useFakeTimers();
  FakeWebSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("matchMedia", vi.fn(() => ({
    matches: false,
    addListener: vi.fn(),
    removeListener: vi.fn(),
  })));
  fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url === "/api/version") {
      return Promise.resolve(
        jsonResponse(200, { version: "test" }, "application/json"),
      );
    }
    return Promise.reject(new Error(`unexpected fetch: ${url}`));
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("App kiosk recovery flow", () => {
  it("recovers a lost 204 through Register, a fresh socket, and the greeting name", async () => {
    const { container } = render(App);

    // Pre-greeting exhaustion: 3 bounded retries then the view exposes
    // registration. Each retry opens a fresh socket after the reconnect
    // delay; the last close gives up as unregistered.
    for (let i = 0; i < 3; i += 1) {
      lastSocket().closeFromServer();
      await vi.advanceTimersByTimeAsync(3000);
    }
    lastSocket().closeFromServer();
    await tick();

    // Card.Title renders a div (data-slot="card-title"), not a heading
    // element, so locate the register screen by its title text.
    expect(screen.getByText("Register Kiosk")).toBeTruthy();

    // Submit the name; the server answers 409 already-registered (the
    // original 204 was lost). The App must reopen the broker session.
    await fireEvent.input(
      screen.getByPlaceholderText("e.g. Branch Office Kiosk 1"),
      { target: { value: "Front Desk Lobby" } },
    );
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/kiosks") {
        return Promise.resolve(
          jsonResponse(
            409,
            {
              type: PROBLEM_ALREADY_REGISTERED,
              title: "Kiosk already registered",
              status: 409,
            },
            "application/problem+json",
          ),
        );
      }
      if (url === "/api/version") {
        return Promise.resolve(
          jsonResponse(200, { version: "test" }, "application/json"),
        );
      }
      return Promise.reject(new Error(`unexpected fetch: ${url}`));
    });
    const socketsBeforeReopen = FakeWebSocket.instances.length;
    await fireEvent.submit(container.querySelector("form")!);
    // Drain fetch -> classify -> onAlreadyRegistered -> reopen.
    await vi.advanceTimersByTimeAsync(0);
    await tick();

    // A fresh socket replaced the exhausted one, and it is the one the
    // authoritative greeting arrives on.
    expect(FakeWebSocket.instances.length).toBe(socketsBeforeReopen + 1);
    const fresh = lastSocket();
    expect(fresh.closed).toBe(false);

    // The greeting on the fresh socket carries the authoritative kiosk name.
    fresh.message(JSON.stringify({ type: "connected", name: "Front Desk Lobby" }));
    await tick();

    expect(screen.getByText("Ready for member")).toBeTruthy();
    expect(screen.getByText("Front Desk Lobby")).toBeTruthy();
    // Every exhausted socket was torn down; nothing is left open.
    expect(
      FakeWebSocket.instances
        .slice(0, socketsBeforeReopen)
        .every((s) => s.closed),
    ).toBe(true);
  });
});

describe("Register completion callbacks", () => {
  it("fires onRegistered exactly once on 204 and never onAlreadyRegistered", async () => {
    vi.useRealTimers();
    const onRegistered = vi.fn();
    const onAlreadyRegistered = vi.fn();
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url === "/api/kiosks") {
        return Promise.resolve({
          ok: true,
          status: 204,
          headers: { get: () => null },
          text: () => Promise.resolve(""),
        });
      }
      return Promise.reject(new Error(`unexpected fetch: ${url}`));
    });

    const { container } = render(Register, {
      props: { onRegistered, onAlreadyRegistered },
    });

    await fireEvent.input(
      screen.getByPlaceholderText("e.g. Branch Office Kiosk 1"),
      { target: { value: "Branch Kiosk 1" } },
    );
    await fireEvent.submit(container.querySelector("form")!);

    await vi.waitFor(() => {
      expect(onRegistered).toHaveBeenCalledTimes(1);
    });
    expect(onAlreadyRegistered).not.toHaveBeenCalled();
  });
});
