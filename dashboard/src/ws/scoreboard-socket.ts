import type { QueryClient } from "@tanstack/react-query";
import { queryKeys } from "../api/client";

interface WsMessage {
  type: "hello" | "scoreboard" | "event.phase";
  data: unknown;
}

/**
 * ScoreboardSocket is a single shared, ref-counted, reconnecting WebSocket that
 * pushes scoreboard snapshots straight into the TanStack Query cache. While
 * disconnected it falls back to polling GET /scoreboard (docs/v0.1/09-frontend.md).
 */
class ScoreboardSocket {
  private ws: WebSocket | null = null;
  private refs = 0;
  private backoff = 1000;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private closedByUs = false;
  private connectedListeners = new Set<(connected: boolean) => void>();

  constructor(private qc: QueryClient) {}

  onConnectedChange(fn: (connected: boolean) => void): () => void {
    this.connectedListeners.add(fn);
    return () => this.connectedListeners.delete(fn);
  }

  private setConnected(connected: boolean) {
    this.connectedListeners.forEach((fn) => {
      fn(connected);
    });
  }

  acquire(): () => void {
    this.refs++;
    if (this.refs === 1) this.connect();
    return () => { this.release(); };
  }

  private release() {
    this.refs--;
    if (this.refs <= 0) {
      this.refs = 0;
      this.closedByUs = true;
      this.ws?.close();
      this.ws = null;
      this.stopPolling();
      if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    }
  }

  private connect() {
    this.closedByUs = false;
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const url = `${proto}://${window.location.host}/api/v0/ws`;
    let ws: WebSocket;
    try {
      ws = new WebSocket(url);
    } catch {
      this.scheduleReconnect();
      return;
    }
    this.ws = ws;

    ws.onopen = () => {
      this.backoff = 1000;
      this.setConnected(true);
      this.stopPolling();
    };
    ws.onmessage = (ev: MessageEvent<string>) => {
      let msg: WsMessage;
      try {
        msg = JSON.parse(ev.data) as WsMessage;
      } catch {
        return;
      }
      if (msg.type === "scoreboard") {
        this.qc.setQueryData(queryKeys.scoreboard, msg.data);
        void this.qc.invalidateQueries({ queryKey: queryKeys.challenges });
      } else if (msg.type === "event.phase") {
        void this.qc.invalidateQueries({ queryKey: queryKeys.event });
        void this.qc.invalidateQueries({ queryKey: queryKeys.challenges });
      }
    };
    ws.onclose = () => {
      this.setConnected(false);
      if (!this.closedByUs) {
        this.startPolling();
        this.scheduleReconnect();
      }
    };
    ws.onerror = () => {
      ws.close();
    };
  }

  private scheduleReconnect() {
    if (this.refs <= 0) return;
    const jitter = Math.random() * 500;
    const delay = Math.min(this.backoff, 30000) + jitter;
    this.backoff = Math.min(this.backoff * 2, 30000);
    this.reconnectTimer = setTimeout(() => {
      if (this.refs > 0) this.connect();
    }, delay);
  }

  private startPolling() {
    if (this.pollTimer) return;
    this.pollTimer = setInterval(() => {
      void this.qc.invalidateQueries({ queryKey: queryKeys.scoreboard });
    }, 30000);
  }

  private stopPolling() {
    if (this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }
}

let singleton: ScoreboardSocket | null = null;

export function getScoreboardSocket(qc: QueryClient): ScoreboardSocket {
  singleton ??= new ScoreboardSocket(qc);
  return singleton;
}
