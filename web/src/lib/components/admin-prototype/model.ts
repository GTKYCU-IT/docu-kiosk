export type KioskStatus = "ready" | "signing" | "offline";
export type DirectoryState = "ready" | "loading" | "empty" | "error";

export interface PrototypeKiosk {
  id: string;
  name: string;
  ip: string;
  status: KioskStatus;
  lastSeen: string;
}

export const fixtureKiosks: PrototypeKiosk[] = [
  {
    id: "kiosk-north-lobby",
    name: "North Lobby",
    ip: "10.24.8.41",
    status: "ready",
    lastSeen: "Connected now",
  },
  {
    id: "kiosk-drive-up",
    name: "Drive-Up",
    ip: "10.24.8.57",
    status: "signing",
    lastSeen: "Signing now",
  },
  {
    id: "kiosk-loan-office",
    name: "Loan Office",
    ip: "10.24.8.63",
    status: "offline",
    lastSeen: "Last connected 18 min ago",
  },
  {
    id: "kiosk-community-room",
    name: "Community Room",
    ip: "10.24.8.79",
    status: "offline",
    lastSeen: "Last connected yesterday at 4:42 PM",
  },
];

export function cloneFixtures(): PrototypeKiosk[] {
  return fixtureKiosks.map((kiosk) => ({ ...kiosk }));
}

export function statusLabel(status: KioskStatus): string {
  if (status === "signing") return "Online · Signing";
  if (status === "ready") return "Online · Ready";
  return "Offline";
}

export interface VariantProps {
  kiosks: PrototypeKiosk[];
  renameKiosk: (id: string, name: string) => string | null;
  deleteKiosk: (id: string) => string | null;
}
