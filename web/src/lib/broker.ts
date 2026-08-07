export type BrokerStatus =
	| "connecting" // socket opening/opened; greeting not yet received
	| "unregistered" // socket closed before any greeting
	| "ready" // greeting received
	| "reconnecting" // authenticated socket lost; retrying
	| "signing"; // sign message received

export interface BrokerState {
	status: BrokerStatus;
	kioskName?: string; // present after a greeting was received
	signingUrl?: string; // present when status === "signing"
}

export interface BrokerSocket {
	onopen: (() => void) | null;
	onmessage: ((ev: MessageEvent) => void) | null;
	onclose: (() => void) | null;
	onerror: (() => void) | null;
	close(): void;
}

export interface BrokerOptions {
	url: string;
	onChange: (state: BrokerState) => void;
	reconnectDelayMs?: number; // default 3000
	createSocket?: (url: string) => BrokerSocket; // default: wraps new WebSocket(url)
}

// The DOM WebSocket fires its handlers with an Event argument and a `this`
// binding, so adapt it to the flat BrokerSocket shape instead of relying on
// structural compatibility. This keeps the DOM dependency in one place and
// leaves the rest of the module testable with a plain fake.
const defaultCreateSocket = (url: string): BrokerSocket => {
	const ws = new WebSocket(url);
	const socket: BrokerSocket = {
		onopen: null,
		onmessage: null,
		onclose: null,
		onerror: null,
		close: () => ws.close(),
	};
	ws.onopen = () => socket.onopen?.();
	ws.onmessage = (ev) => socket.onmessage?.(ev);
	ws.onclose = () => socket.onclose?.();
	ws.onerror = () => socket.onerror?.();
	return socket;
};

// The timer handle type differs between runtimes (DOM libs return `number`,
// @types/node returns `Timeout`), so capture it in a closure and expose only a
// named `clear()` operation.
interface ReconnectTimer {
	clear(): void;
}

export class BrokerConnection {
	private readonly url: string;
	private readonly onChange: (state: BrokerState) => void;
	private readonly reconnectDelayMs: number;
	private readonly createSocket: (url: string) => BrokerSocket;
	private socket: BrokerSocket | null = null;
	private status: BrokerStatus = "connecting";
	private kioskName: string | undefined;
	private signingUrl: string | undefined;
	private closed = false;
	private reconnectTimer: ReconnectTimer | null = null;

	constructor(options: BrokerOptions) {
		this.url = options.url;
		this.onChange = options.onChange;
		this.reconnectDelayMs = options.reconnectDelayMs ?? 3000;
		this.createSocket = options.createSocket ?? defaultCreateSocket;
		this.socket = this.createSocket(this.url);
		this.wire(this.socket);
	}

	private wire(socket: BrokerSocket): void {
		socket.onopen = () => {
			if (this.closed) return;
			// "connecting" persists until the greeting message arrives; nothing
			// to do here.
		};
		socket.onmessage = (ev) => {
			if (this.closed) return;
			this.handleMessage(ev);
		};
		socket.onclose = () => {
			if (this.closed) return;
			this.handleClose();
		};
		socket.onerror = () => {
			if (this.closed) return;
			console.warn("broker: socket error");
		};
	}

	private handleMessage(ev: MessageEvent): void {
		const rawData: string = ev.data;
		let parsed: unknown;
		try {
			parsed = JSON.parse(rawData);
		} catch {
			console.warn("broker: dropping malformed message", rawData);
			return;
		}
		if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
			console.warn("broker: dropping unknown message", parsed);
			return;
		}
		const record = parsed as Record<string, unknown>;
		if (record.type === "connected" && typeof record.name === "string") {
			this.handleGreeting(record.name);
			return;
		}
		if (record.type === "sign" && typeof record.url === "string") {
			this.handleSign(record.url);
			return;
		}
		console.warn("broker: dropping unknown message", record);
	}

	private handleGreeting(name: string): void {
		if (this.status === "signing") {
			// Reconnect during an active signing session: the iframe must stay
			// mounted, so refresh only the stored name and keep "signing".
			this.kioskName = name;
			return;
		}
		this.status = "ready";
		this.kioskName = name;
		this.signingUrl = undefined;
		this.onChange({ status: "ready", kioskName: name });
	}

	private handleSign(url: string): void {
		this.status = "signing";
		this.signingUrl = url;
		this.onChange({ status: "signing", kioskName: this.kioskName, signingUrl: url });
	}

	private handleClose(): void {
		if (this.status === "signing") {
			// Keep "signing" so the iframe stays mounted; reconnect in the
			// background without notifying the view.
			this.socket = null;
			this.scheduleReconnect();
			return;
		}
		if (this.status === "ready" || this.status === "reconnecting") {
			this.status = "reconnecting";
			this.socket = null;
			this.onChange({ status: "reconnecting", kioskName: this.kioskName });
			this.scheduleReconnect();
			return;
		}
		// status === "connecting": the broker rejects unregistered IPs at the
		// HTTP level, so retrying is pointless.
		this.status = "unregistered";
		this.socket = null;
		this.onChange({ status: "unregistered" });
	}

	private scheduleReconnect(): void {
		if (this.reconnectTimer !== null) return;
		const handle = globalThis.setTimeout(() => {
			this.reconnectTimer = null;
			if (this.closed) return;
			const socket = this.createSocket(this.url);
			this.socket = socket;
			this.wire(socket);
		}, this.reconnectDelayMs);
		this.reconnectTimer = { clear: () => globalThis.clearTimeout(handle) };
	}

	/** The view reports that the signing flow ended. */
	finishSigning(): void {
		if (this.closed) return;
		if (this.status !== "signing") return;
		this.signingUrl = undefined;
		if (this.socket !== null) {
			this.status = "ready";
			this.onChange({ status: "ready", kioskName: this.kioskName });
		} else {
			// The socket is down; the reconnect timer is already running.
			this.status = "reconnecting";
			this.onChange({ status: "reconnecting", kioskName: this.kioskName });
		}
	}

	/** Teardown: no further callbacks after this. */
	close(): void {
		if (this.closed) return;
		this.closed = true;
		this.reconnectTimer?.clear();
		this.reconnectTimer = null;
		const socket = this.socket;
		this.socket = null;
		socket?.close();
	}
}
