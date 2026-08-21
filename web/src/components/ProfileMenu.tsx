'use client';

import { useState, useRef, useEffect } from 'react';

/**
 * Phase 1 has no auth/session layer yet (see lib/demo-context.ts's own
 * stub-identity comment). This component is the auth swap-in point for
 * the top-right profile control: once a real session exists, replace
 * `learner` below with the actual signed-in user (name/email from the
 * session) and wire `onLogout` to the real sign-out call. Until then it
 * honestly shows "Not signed in" rather than fabricating a fake name --
 * the icon and dropdown structure are real, only the identity data is
 * stubbed.
 */
interface Learner {
  name: string;
  email: string;
}

const CURRENT_LEARNER: Learner | null = null; // swap-in point: real session data replaces this

export function ProfileMenu() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    function onClickOutside(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener('mousedown', onClickOutside);
    return () => document.removeEventListener('mousedown', onClickOutside);
  }, []);

  const initials = CURRENT_LEARNER
    ? CURRENT_LEARNER.name
        .split(' ')
        .map((p) => p[0])
        .slice(0, 2)
        .join('')
        .toUpperCase()
    : '?';

  return (
    <div className="profile-menu" ref={ref}>
      <button
        type="button"
        className="profile-avatar"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="true"
        aria-expanded={open}
        aria-label="Account menu"
      >
        {initials}
      </button>

      {open && (
        <div className="profile-dropdown" role="menu">
          {CURRENT_LEARNER ? (
            <>
              <div className="profile-dropdown-header">
                <p className="profile-dropdown-name">{CURRENT_LEARNER.name}</p>
                <p className="profile-dropdown-email">{CURRENT_LEARNER.email}</p>
              </div>
              <button
                type="button"
                className="profile-dropdown-action"
                onClick={() => {
                  // swap-in point: call the real sign-out endpoint here
                }}
              >
                Log out
              </button>
            </>
          ) : (
            <div className="profile-dropdown-header">
              <p className="profile-dropdown-name">Not signed in</p>
              <p className="profile-dropdown-email">Learner authentication isn&apos;t built yet</p>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
