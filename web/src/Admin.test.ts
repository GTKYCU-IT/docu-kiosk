// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/svelte";
import Admin from "./Admin.svelte";
import { AdminSessionController } from "$lib/admin/session";
import { response, testJwt } from "$lib/admin/test-response";

let fetchMock: Mock;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("Admin restoration", () => {
  it("shows only the neutral restoration state while refresh is unresolved", () => {
    fetchMock.mockReturnValue(new Promise<Response>(() => {}));

    render(Admin);

    expect(screen.getByRole("status").textContent?.trim()).toBe(
      "Restoring administrator session",
    );
    expect(screen.queryByText("Administrator session active")).toBeNull();
    expect(screen.queryByText("Administrator sign in")).toBeNull();
  });

  it("shows a clean sign-in form after refresh returns 401", async () => {
    fetchMock.mockResolvedValue(response(401));

    render(Admin);

    expect(
      await screen.findByRole("heading", { name: "Administrator sign in" }),
    ).toBeTruthy();
    expect(screen.getByLabelText("Username")).toBeTruthy();
    expect((screen.getByLabelText("Password") as HTMLInputElement).type).toBe(
      "password",
    );
    expect(screen.queryByText("Invalid username or password")).toBeNull();
  });

  it("shows Broker unavailable after restoration failure and retries through neutral restoration", async () => {
    let resolveRetry!: (value: Response) => void;
    fetchMock
      .mockResolvedValueOnce(response(503))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveRetry = resolve;
          }),
      );

    render(Admin);
    expect(
      await screen.findByRole("heading", { name: "Broker unavailable" }),
    ).toBeTruthy();

    await fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(screen.getByRole("status").textContent?.trim()).toBe(
      "Restoring administrator session",
    );
    expect(screen.queryByText("Broker unavailable")).toBeNull();

    const restoredJwt = testJwt();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    resolveRetry(response(200, { jwt: restoredJwt }));
    expect(
      await screen.findByRole("heading", {
        name: "Administrator session active",
      }),
    ).toBeTruthy();
    expect(document.body.textContent).not.toContain(restoredJwt);
  });
});

describe("Admin login", () => {
  it("shows one generic error and clears the password after invalid credentials", async () => {
    fetchMock
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(401));
    render(Admin);
    await screen.findByRole("heading", { name: "Administrator sign in" });

    await fireEvent.input(screen.getByLabelText("Username"), {
      target: { value: "administrator" },
    });
    await fireEvent.input(screen.getByLabelText("Password"), {
      target: { value: "not the password" },
    });
    await fireEvent.submit(
      screen.getByRole("button", { name: "Sign in" }).closest("form")!,
    );

    expect(
      (await screen.findByText("Invalid username or password")).getAttribute(
        "role",
      ),
    ).toBe("alert");
    expect(screen.getAllByText("Invalid username or password")).toHaveLength(1);
    expect((screen.getByLabelText("Password") as HTMLInputElement).value).toBe(
      "",
    );
    expect(document.body.textContent).not.toContain("not the password");
  });

  it("replaces the form in place after successful login without rendering the JWT", async () => {
    const loginJwt = testJwt();
    fetchMock
      .mockResolvedValueOnce(response(401))
      .mockResolvedValueOnce(response(200, { jwt: loginJwt }));
    render(Admin);
    await screen.findByRole("heading", { name: "Administrator sign in" });

    await fireEvent.input(screen.getByLabelText("Username"), {
      target: { value: "administrator" },
    });
    await fireEvent.input(screen.getByLabelText("Password"), {
      target: { value: "secret phrase" },
    });
    await fireEvent.submit(
      screen.getByRole("button", { name: "Sign in" }).closest("form")!,
    );

    expect(
      await screen.findByRole("heading", {
        name: "Administrator session active",
      }),
    ).toBeTruthy();
    expect(screen.queryByLabelText("Password")).toBeNull();
    expect(document.body.textContent).not.toContain(loginJwt);
    expect(document.body.textContent).not.toContain("secret phrase");
  });
});

describe("Admin logout", () => {
  it("keeps the authenticated surface visible and disables sign out while it is in flight", async () => {
    let resolveLogout!: (value: Response) => void;
    fetchMock
      .mockResolvedValueOnce(response(200, { jwt: testJwt() }))
      .mockImplementationOnce(
        () =>
          new Promise<Response>((resolve) => {
            resolveLogout = resolve;
          }),
      );
    render(Admin);
    await screen.findByRole("heading", {
      name: "Administrator session active",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(
      screen.getByRole("heading", { name: "Administrator session active" }),
    ).toBeTruthy();
    expect(
      (screen.getByRole("button", { name: "Signing out…" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    expect(screen.queryByText("Administrator sign in")).toBeNull();

    resolveLogout(response(500));
    expect(
      await screen.findByText(
        "Sign out failed. Your administrator session is still active.",
      ),
    ).toBeTruthy();
  });

  it.each([
    ["a server failure", () => Promise.resolve(response(500))],
    ["an unexpected non-204 response", () => Promise.resolve(response(202))],
    ["a network failure", () => Promise.reject(new Error("offline"))],
  ])("keeps the authenticated view and reports failure after %s", async (_name, outcome) => {
    fetchMock
      .mockResolvedValueOnce(response(200, { jwt: testJwt() }))
      .mockImplementationOnce(outcome);
    render(Admin);
    await screen.findByRole("heading", {
      name: "Administrator session active",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(
      (
        await screen.findByText(
          "Sign out failed. Your administrator session is still active.",
        )
      ).getAttribute("role"),
    ).toBe("alert");
    expect(
      screen.getByRole("heading", { name: "Administrator session active" }),
    ).toBeTruthy();
    expect(screen.queryByText("Administrator sign in")).toBeNull();
  });

  it("clears the authenticated view in place only after logout returns 204", async () => {
    fetchMock
      .mockResolvedValueOnce(response(200, { jwt: testJwt() }))
      .mockResolvedValueOnce(response(204));
    render(Admin);
    await screen.findByRole("heading", {
      name: "Administrator session active",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(
      await screen.findByRole("heading", { name: "Administrator sign in" }),
    ).toBeTruthy();
    expect(screen.queryByText("Administrator session active")).toBeNull();
    expect(fetchMock).toHaveBeenLastCalledWith("/logout", {
      method: "POST",
      credentials: "same-origin",
    });
  });

  it("returns to clean sign in when logout reports terminal authentication loss", async () => {
    fetchMock
      .mockResolvedValueOnce(response(200, { jwt: testJwt() }))
      .mockResolvedValueOnce(response(401));
    render(Admin);
    await screen.findByRole("heading", {
      name: "Administrator session active",
    });

    await fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(
      await screen.findByRole("heading", { name: "Administrator sign in" }),
    ).toBeTruthy();
    expect(screen.queryByText("Administrator session active")).toBeNull();
    expect(
      screen.queryByText(
        "Sign out failed. Your administrator session is still active.",
      ),
    ).toBeNull();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  });
});

describe("Admin teardown", () => {
  it("closes the session controller when the page unmounts", async () => {
    const close = vi.spyOn(AdminSessionController.prototype, "close");

    fetchMock.mockResolvedValue(response(200, { jwt: testJwt() }));
    const { unmount } = render(Admin);
    await screen.findByRole("heading", {
      name: "Administrator session active",
    });

    unmount();

    expect(close).toHaveBeenCalled();
  });
});
