import { useEffect, useState } from "react";

/** formatAbsoluteUTC renders an ISO timestamp as an explicit UTC string. */
export function formatAbsoluteUTC(iso: string): string {
  const d = new Date(iso);
  return d.toISOString().replace("T", " ").replace(/\.\d+Z$/, " UTC");
}

/** formatLocal renders a timestamp in the viewer's locale/timezone. */
export function formatLocal(iso: string): string {
  return new Date(iso).toLocaleString();
}

/** formatRelative renders a compact relative time ("3m ago", "in 2h"). */
export function formatRelative(iso: string): string {
  const then = new Date(iso).getTime();
  const now = Date.now();
  const diff = then - now;
  const abs = Math.abs(diff);
  const suffix = diff < 0 ? " ago" : "";
  const prefix = diff >= 0 ? "in " : "";
  const units: [number, string][] = [
    [86400000, "d"],
    [3600000, "h"],
    [60000, "m"],
    [1000, "s"],
  ];
  for (const [ms, label] of units) {
    if (abs >= ms) {
      return `${prefix}${Math.floor(abs / ms).toString()}${label}${suffix}`;
    }
  }
  return "just now";
}

export interface Countdown {
  days: number;
  hours: number;
  minutes: number;
  seconds: number;
  done: boolean;
}

/** useCountdown ticks once a second toward the target ISO time. */
export function useCountdown(targetIso: string | undefined): Countdown {
  const compute = (): Countdown => {
    if (!targetIso) return { days: 0, hours: 0, minutes: 0, seconds: 0, done: true };
    const diff = new Date(targetIso).getTime() - Date.now();
    if (diff <= 0) return { days: 0, hours: 0, minutes: 0, seconds: 0, done: true };
    return {
      days: Math.floor(diff / 86400000),
      hours: Math.floor((diff % 86400000) / 3600000),
      minutes: Math.floor((diff % 3600000) / 60000),
      seconds: Math.floor((diff % 60000) / 1000),
      done: false,
    };
  };
  const [state, setState] = useState<Countdown>(compute);
  useEffect(() => {
    setState(compute());
    const id = setInterval(() => {
      setState(compute());
    }, 1000);
    return () => {
      clearInterval(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [targetIso]);
  return state;
}
