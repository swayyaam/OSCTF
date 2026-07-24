import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { getScoreboardSocket } from "./scoreboard-socket";

/**
 * useLiveScoreboard opens the shared scoreboard socket for the lifetime of the
 * calling component and reports whether it is currently connected (else the
 * caller shows a "reconnecting, polling" hint).
 */
export function useLiveScoreboard(): boolean {
  const qc = useQueryClient();
  const [connected, setConnected] = useState(false);
  useEffect(() => {
    const socket = getScoreboardSocket(qc);
    const off = socket.onConnectedChange(setConnected);
    const release = socket.acquire();
    return () => {
      off();
      release();
    };
  }, [qc]);
  return connected;
}
