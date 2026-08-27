'use client';

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import Editor from '@monaco-editor/react';
import { api } from '@/lib/api-client';
import { toUserFacingError } from '@/lib/error-message';
import { Button } from '@/components/ui/Button';

/**
 * Doc §8.5's Monaco editor pane -- "client-side, file-API-backed" per
 * the doc's own note next to ConnectResult.editorUrl (always "" -- there
 * is no server-rendered editor to point a URL at). The file API is
 * practice-core's /files and /files/content routes
 * (workspace-file.service.ts), backed by the orchestrator's ExecShell
 * RPC running inside the same workspace pod the terminal execs into --
 * editing a file here and then running it in WorkspaceTerminal operates
 * on the exact same filesystem.
 */
export function WorkspaceEditor({ attemptId }: { attemptId: string }) {
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [pendingContent, setPendingContent] = useState<string | undefined>(undefined);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const queryClient = useQueryClient();

  const { data: fileContent, isLoading: isLoadingContent } = useQuery({
    queryKey: ['workspace-file', attemptId, selectedPath],
    queryFn: () => api.readWorkspaceFile(attemptId, selectedPath!),
    enabled: !!selectedPath,
  });

  function selectFile(path: string) {
    if (path === selectedPath) return;
    setSelectedPath(path);
    setDirty(false);
    setPendingContent(undefined);
    setSaveError(null);
  }

  async function save() {
    if (!selectedPath || pendingContent === undefined) return;
    setSaving(true);
    setSaveError(null);
    try {
      await api.writeWorkspaceFile(attemptId, selectedPath, pendingContent);
      setDirty(false);
      queryClient.invalidateQueries({ queryKey: ['workspace-file', attemptId, selectedPath] });
    } catch (err) {
      setSaveError(`Save failed: ${toUserFacingError(err).headline}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex h-full overflow-hidden rounded-xl border border-[var(--border)]">
      <div className="w-56 shrink-0 overflow-y-auto border-r border-[var(--border)] bg-[var(--surface)] p-2">
        <FileTree attemptId={attemptId} dir="." depth={0} selectedPath={selectedPath} onSelect={selectFile} />
      </div>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center justify-between border-b border-[var(--border)] bg-[var(--inset)] px-3 py-1.5 text-xs">
          <span className="truncate font-mono text-[var(--ink-muted)]">
            {selectedPath ?? 'Select a file to edit'}
          </span>
          {selectedPath && (
            <Button onClick={save} disabled={!dirty || saving} size="sm">
              {saving ? 'Saving…' : dirty ? 'Save' : 'Saved'}
            </Button>
          )}
        </div>

        {saveError && (
          <div className="border-b border-[var(--danger)] bg-[var(--danger-soft)] px-3 py-1 text-xs text-[var(--danger)]">
            {saveError}
          </div>
        )}

        <div className="min-h-0 flex-1">
          {!selectedPath && (
            <div className="flex h-full items-center justify-center text-sm text-[var(--ink-soft)]">
              No file open
            </div>
          )}
          {selectedPath && isLoadingContent && (
            <div className="flex h-full items-center justify-center text-sm text-[var(--ink-muted)]">
              Loading…
            </div>
          )}
          {selectedPath && !isLoadingContent && fileContent && (
            <Editor
              key={selectedPath}
              height="100%"
              language={languageForPath(selectedPath)}
              defaultValue={fileContent.content}
              theme="vs-dark"
              onChange={(value) => {
                setPendingContent(value ?? '');
                setDirty(true);
              }}
              options={{ minimap: { enabled: false }, fontSize: 13, automaticLayout: true }}
            />
          )}
        </div>
      </div>
    </div>
  );
}

function FileTree({
  attemptId,
  dir,
  depth,
  selectedPath,
  onSelect,
}: {
  attemptId: string;
  dir: string;
  depth: number;
  selectedPath: string | null;
  onSelect: (path: string) => void;
}) {
  const { data: entries, isLoading } = useQuery({
    queryKey: ['workspace-files', attemptId, dir],
    queryFn: () => api.listWorkspaceFiles(attemptId, dir),
  });

  if (isLoading) {
    return depth === 0 ? <p className="p-2 text-xs text-[var(--ink-soft)]">Loading files…</p> : null;
  }
  if (!entries || entries.length === 0) {
    return depth === 0 ? <p className="p-2 text-xs text-[var(--ink-soft)]">No files yet</p> : null;
  }

  return (
    <div style={{ paddingLeft: depth > 0 ? 12 : 0 }}>
      {entries.map((entry) =>
        entry.type === 'dir' ? (
          <DirNode key={entry.path} attemptId={attemptId} entry={entry} depth={depth} selectedPath={selectedPath} onSelect={onSelect} />
        ) : (
          <button
            key={entry.path}
            onClick={() => onSelect(entry.path)}
            className={`block w-full truncate rounded px-2 py-1 text-left text-xs hover:bg-[var(--inset)] ${
              selectedPath === entry.path ? 'bg-[var(--accent-soft)] text-[var(--accent-deep)]' : 'text-[var(--ink)]'
            }`}
          >
            {entry.path.split('/').pop()}
          </button>
        ),
      )}
    </div>
  );
}

function DirNode({
  attemptId,
  entry,
  depth,
  selectedPath,
  onSelect,
}: {
  attemptId: string;
  entry: { path: string };
  depth: number;
  selectedPath: string | null;
  onSelect: (path: string) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  return (
    <div>
      <button
        onClick={() => setExpanded((e) => !e)}
        className="flex w-full items-center gap-1 truncate rounded px-2 py-1 text-left text-xs text-[var(--ink-muted)] hover:bg-[var(--inset)]"
      >
        <span className="w-3 shrink-0">{expanded ? '▾' : '▸'}</span>
        {entry.path.split('/').pop()}
      </button>
      {expanded && (
        <FileTree attemptId={attemptId} dir={entry.path} depth={depth + 1} selectedPath={selectedPath} onSelect={onSelect} />
      )}
    </div>
  );
}

const EXTENSION_LANGUAGE: Record<string, string> = {
  sh: 'shell',
  bash: 'shell',
  py: 'python',
  js: 'javascript',
  ts: 'typescript',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  md: 'markdown',
  go: 'go',
  tf: 'hcl',
  Dockerfile: 'dockerfile',
};

function languageForPath(path: string): string {
  const name = path.split('/').pop() ?? '';
  if (name === 'Dockerfile') return 'dockerfile';
  const ext = name.split('.').pop() ?? '';
  return EXTENSION_LANGUAGE[ext] ?? 'plaintext';
}
