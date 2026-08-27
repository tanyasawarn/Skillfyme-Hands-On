'use client';

import { useEffect, useRef, useState } from 'react';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebglAddon } from '@xterm/addon-webgl';
import '@xterm/xterm/css/xterm.css';
import { api } from '@/lib/api-client';
import { WORKSPACE_BG, WORKSPACE_FG } from '@/lib/workspace-theme';

type ConnectionState = 'connecting' | 'open' | 'reconnecting' | 'closed';

const RECONNECT_DELAYS_MS = [500, 1000, 2000, 4000, 8000];

/**
 * Doc §5.4 / §8.5 terminal pane: xterm.js against the Environment
 * Orchestrator's WS Gateway (orchestrator/internal/wsgateway). The
 * gateway speaks raw binary frames -- stdin bytes in, PTY output bytes
 * out, no JSON envelope -- confirmed against
 * orchestrator/internal/sessionbroker/broker.go's wsStdinReader/tappedWriter.
 *
 * Reconnection: the server side (internal/sessionbroker's ptySession,
 * session_registry.go) now keeps the PTY alive for 120s after socket
 * loss and replays scrollback on reattach -- opening a fresh
 * session_token via connect() and a new WebSocket resumes the *same*
 * shell process (env vars, cwd, running jobs all survive) as long as the
 * new connection lands within that grace window, and the server replays
 * everything that happened while disconnected as the very first message.
 * This client-side reconnect loop is what makes that resume actually
 * happen from the browser's perspective; nothing here needs to reconcile
 * scrollback itself, since the server sends it before any new output.
 */
export function WorkspaceTerminal({ attemptId }: { attemptId: string }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const attemptRef = useRef(0);
  const unmountedRef = useRef(false);

  const [state, setState] = useState<ConnectionState>('connecting');
  const [reconnectCount, setReconnectCount] = useState(0);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    unmountedRef.current = false;

    const term = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      theme: { background: WORKSPACE_BG, foreground: WORKSPACE_FG },
      scrollback: 5000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    termRef.current = term;
    fitRef.current = fit;

    const focusTerm = () => term.focus();

    if (containerRef.current) {
      term.open(containerRef.current);
      // WebGL renderer per M1.12 spec; falls back to the default canvas
      // renderer if the browser/context doesn't support it (headless CI,
      // some older GPUs) rather than crashing the whole terminal.
      try {
        term.loadAddon(new WebglAddon());
      } catch {
        // canvas renderer remains active -- fine, just slower on huge scrollback.
      }
      fit.fit();
      // xterm only captures keystrokes when its internal hidden textarea
      // has DOM focus -- term.open() alone doesn't focus it, so without
      // this the cursor blinks but every keypress is silently swallowed
      // by the page instead of reaching the PTY.
      term.focus();
      containerRef.current.addEventListener('mousedown', focusTerm);
    }

    const resizeObserver = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        // container not laid out yet -- next resize event will retry.
      }
    });
    if (containerRef.current) resizeObserver.observe(containerRef.current);

    const onData = term.onData((data) => {
      const ws = wsRef.current;
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(data);
      }
    });

    connect(attemptRef.current);

    return () => {
      unmountedRef.current = true;
      attemptRef.current += 1; // invalidate any in-flight connect()/reconnect chain
      resizeObserver.disconnect();
      containerRef.current?.removeEventListener('mousedown', focusTerm);
      onData.dispose();
      wsRef.current?.close();
      term.dispose();
    };

    async function connect(myAttempt: number) {
      if (unmountedRef.current || myAttempt !== attemptRef.current) return;
      setState((prev) => (prev === 'connecting' ? 'connecting' : 'reconnecting'));
      setError(null);

      try {
        const info = await api.connectAttempt(attemptId);
        if (unmountedRef.current || myAttempt !== attemptRef.current) return;

        const ws = new WebSocket(info.terminalWsUrl);
        ws.binaryType = 'arraybuffer';
        wsRef.current = ws;

        ws.onopen = () => {
          if (unmountedRef.current || myAttempt !== attemptRef.current) return;
          setState('open');
          setReconnectCount(0);
        };

        ws.onmessage = (event) => {
          if (unmountedRef.current || myAttempt !== attemptRef.current) return;
          const data = event.data;
          if (typeof data === 'string') {
            term.write(data);
          } else {
            term.write(new Uint8Array(data as ArrayBuffer));
          }
        };

        ws.onclose = () => {
          if (unmountedRef.current || myAttempt !== attemptRef.current) return;
          scheduleReconnect(myAttempt);
        };

        ws.onerror = () => {
          // onclose fires right after onerror for a browser WebSocket --
          // scheduleReconnect happens there, not here, to avoid double-scheduling.
        };
      } catch (err) {
        if (unmountedRef.current || myAttempt !== attemptRef.current) return;
        setError(err instanceof Error ? err.message : 'failed to connect');
        scheduleReconnect(myAttempt);
      }
    }

    function scheduleReconnect(myAttempt: number) {
      if (unmountedRef.current || myAttempt !== attemptRef.current) return;
      setState('reconnecting');
      setReconnectCount((c) => {
        const next = c + 1;
        const delay = RECONNECT_DELAYS_MS[Math.min(next - 1, RECONNECT_DELAYS_MS.length - 1)];
        window.setTimeout(() => connect(myAttempt), delay);
        return next;
      });
    }
  }, [attemptId]);

  return (
    <div className="flex h-full flex-col overflow-hidden rounded-xl border border-[var(--border)]">
      <ConnectionBanner state={state} reconnectCount={reconnectCount} error={error} />
      <div ref={containerRef} className="min-h-0 flex-1 bg-[var(--workspace-bg)] p-2" />
    </div>
  );
}

function ConnectionBanner({
  state,
  reconnectCount,
  error,
}: {
  state: ConnectionState;
  reconnectCount: number;
  error: string | null;
}) {
  if (state === 'open') return null;

  if (state === 'connecting') {
    return (
      <div className="border-b border-[var(--border-strong)] bg-[var(--inset)] px-3 py-1.5 text-xs text-[var(--ink-muted)]">
        Connecting to workspace…
      </div>
    );
  }

  return (
    <div className="border-b border-[var(--warning)] bg-[var(--warning-soft)] px-3 py-1.5 text-xs text-[var(--warning)]">
      Connection lost — reconnecting (attempt {reconnectCount})…{' '}
      <span className="opacity-80">Your session and scrollback will resume once reconnected.</span>
      {error && <span className="ml-2 opacity-70">({error})</span>}
    </div>
  );
}
