#!/usr/bin/env python3
"""Phase 1 load harness — python3 / stdlib-only runner.

PLAN_PHASE3_PROJECTS.md G1 / Phase3_Stages.md 0.2. Same scenario, env vars and
pass/fail gate as load.js, but runnable now without installing k6 (matches
evaluation/phase1/smoke/run-smoke.sh's toolchain). Use for a smaller local
shake-out (LOAD_VUS=20) before the real k6 run on a multi-node cluster (0.4).

Per virtual learner, LOAD_ITERATIONS times:
  dev-login -> pick published L1 lab -> create attempt -> provision -> connect
  -> LOAD_CMDS_PER_ATTEMPT file writes -> check -> submit -> teardown

Exit code is non-zero if any doc §13.1 threshold is missed, so it drops
straight into CI or a pre-flight check.
"""
from __future__ import annotations

import json
import os
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field

BASE = os.environ.get("LOAD_BASE_URL", "http://localhost:3001").rstrip("/")
ORCH_METRICS = os.environ.get("LOAD_ORCH_METRICS_URL", "http://localhost:9090").rstrip("/")
VUS = int(os.environ.get("LOAD_VUS", "200"))
ITERATIONS = int(os.environ.get("LOAD_ITERATIONS", "3"))
CMDS = int(os.environ.get("LOAD_CMDS_PER_ATTEMPT", "20"))
LAB_SLUG = os.environ.get("LOAD_LAB_SLUG", "lab.linux.navigate-filesystem")
RAMP_S = float(os.environ.get("LOAD_RAMP", "30").rstrip("s") or "30")
THINK_MS = int(os.environ.get("LOAD_THINK_MS", "250"))
HTTP_TIMEOUT = float(os.environ.get("LOAD_HTTP_TIMEOUT", "60"))
PREMINTED_TOKENS_FILE = os.environ.get("LOAD_PREMINTED_TOKENS", "")

_PREMINTED: list = []
if PREMINTED_TOKENS_FILE:
    with open(PREMINTED_TOKENS_FILE, encoding="utf-8") as _fh:
        _PREMINTED = [ln.strip() for ln in _fh if ln.strip()]
    if not _PREMINTED:
        print(f"LOAD_PREMINTED_TOKENS={PREMINTED_TOKENS_FILE} is empty", file=sys.stderr)
        sys.exit(2)

# doc §13.1 thresholds
T_PROVISION_SUCCESS = 0.99
T_SUBMIT_SUCCESS = 0.99
T_DESTROY_SUCCESS = 1.0
T_READY_P95_MS = 20_000


@dataclass
class Stats:
    lock: threading.Lock = field(default_factory=threading.Lock)
    provision_attempts: int = 0
    provision_ok: int = 0
    provision_ms: list = field(default_factory=list)
    connect_attempts: int = 0
    connect_ok: int = 0
    submit_attempts: int = 0
    submit_ok: int = 0
    destroy_attempts: int = 0
    destroy_ok: int = 0
    cmd_writes: int = 0
    labs_completed: int = 0
    errors: list = field(default_factory=list)

    def add(self, **kw):
        with self.lock:
            for k, v in kw.items():
                cur = getattr(self, k)
                if isinstance(cur, list):
                    cur.append(v)
                else:
                    setattr(self, k, cur + v)


S = Stats()


def _req(method: str, url: str, token: str | None = None, body: dict | None = None):
    data = None
    headers = {"content-type": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
    if token:
        headers["authorization"] = f"Bearer {token}"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, (json.loads(raw) if raw else {})
        except Exception:
            return e.code, {}
    except Exception as e:  # noqa: BLE001 — record and keep the VU alive
        return 0, {"__error__": str(e)}


def dev_login() -> str | None:
    st, body = _req("POST", f"{BASE}/v1/auth/dev-login", body={})
    if 200 <= st < 300 and body.get("token"):
        return body["token"]
    S.add(errors=f"dev-login {st} {body}")
    return None


_avid_cache: dict = {}
_avid_lock = threading.Lock()


def resolve_lab_version_id(token: str) -> str | None:
    with _avid_lock:
        if "id" in _avid_cache:
            return _avid_cache["id"]
    st, body = _req("GET", f"{BASE}/v1/practice/activities", token=token)
    if st != 200 or not isinstance(body, list):
        S.add(errors=f"catalog {st}")
        return None
    hit = next((a for a in body if a.get("slug") == LAB_SLUG), None)
    if not hit:
        S.add(errors=f"lab {LAB_SLUG} not in catalog")
        return None
    with _avid_lock:
        _avid_cache["id"] = hit["activity_version_id"]
    return _avid_cache["id"]


def run_one_lab(token: str, avid: str, vu: int, it: int) -> None:
    st, body = _req(
        "POST", f"{BASE}/v1/practice/attempts", token=token, body={"activity_version_id": avid}
    )
    S.add(provision_attempts=1)
    if not (200 <= st < 300 and body.get("id")):
        S.add(provision_ok=0, errors=f"create {st} {body}")
        return
    attempt_id = body["id"]

    t0 = time.monotonic()
    st, body = _req("POST", f"{BASE}/v1/practice/attempts/{attempt_id}/provision", token=token)
    dt_ms = (time.monotonic() - t0) * 1000.0
    ready = 200 <= st < 300 and body.get("status") == "READY"
    S.add(provision_ok=1 if ready else 0)
    if ready:
        S.add(provision_ms=dt_ms)
    else:
        S.add(errors=f"provision {st} {body.get('status')}")
        teardown(token, attempt_id)
        return

    S.add(connect_attempts=1)
    st, body = _req("POST", f"{BASE}/v1/practice/attempts/{attempt_id}/connect", token=token)
    ws = body.get("terminalWsUrl", "") if isinstance(body, dict) else ""
    S.add(connect_ok=1 if (200 <= st < 300 and "/terminal?session=" in ws) else 0)

    # start (CREATED/READY -> IN_PROGRESS) — submit is rejected without it
    _req("POST", f"{BASE}/v1/practice/attempts/{attempt_id}/start", token=token)

    for i in range(CMDS):
        path = f"loadtest/step-{i}.txt"
        st, _ = _req(
            "POST",
            f"{BASE}/v1/practice/attempts/{attempt_id}/files/content?path={path}",
            token=token,
            body={"content": f"vu={vu} iter={it} step={i} ts={time.time()}\n"},
        )
        if 200 <= st < 300:
            S.add(cmd_writes=1)
        if THINK_MS and i % 5 == 4:
            time.sleep(THINK_MS / 1000.0)

    _req("POST", f"{BASE}/v1/practice/attempts/{attempt_id}/check", token=token)

    # submit IS the teardown trigger — submit() scores the attempt and (with
    # the real orchestrator) requests environment Destroy → ENV_DESTROYED.
    # There is no separate learner-facing destroy route; check-orphans.sh is
    # the backstop proof nothing leaked.
    S.add(submit_attempts=1, destroy_attempts=1)
    st, _ = _req("POST", f"{BASE}/v1/practice/attempts/{attempt_id}/submit", token=token)
    ok = 200 <= st < 300
    S.add(submit_ok=1 if ok else 0, destroy_ok=1 if ok else 0)
    if ok:
        S.add(labs_completed=1)
    else:
        teardown(token, attempt_id)


def teardown(token: str, attempt_id: str) -> None:
    """Provision-failed / submit-failed path only: no submit teardown ran, so
    nudge practice-core to abandon the attempt if a route exists; else the
    TTL/idle reaper reclaims it and check-orphans.sh is the assertion. A 404
    is expected, not a failure."""
    _req("DELETE", f"{BASE}/v1/practice/attempts/{attempt_id}/environment", token=token)


def vu_token(vu: int) -> str | None:
    """A preminted token for this VU if provided, else a fresh dev-login."""
    if _PREMINTED:
        return _PREMINTED[vu % len(_PREMINTED)]
    return dev_login()


def vu_thread(vu: int) -> None:
    time.sleep((vu / max(VUS, 1)) * RAMP_S)  # ramp-in
    for it in range(ITERATIONS):
        token = vu_token(vu)
        if not token:
            continue
        avid = resolve_lab_version_id(token)
        if not avid:
            continue
        run_one_lab(token, avid, vu, it)
        if THINK_MS:
            time.sleep(THINK_MS / 1000.0)


def orchestrator_provision_count() -> str:
    try:
        with urllib.request.urlopen(f"{ORCH_METRICS}/metrics", timeout=10) as resp:
            for line in resp.read().decode().splitlines():
                if line.startswith("orchestrator_provision_duration_seconds_count"):
                    return line
        return "orchestrator provision histogram not yet populated"
    except Exception as e:  # noqa: BLE001
        return f"orchestrator /metrics not reachable: {e}"


def main() -> int:
    print(
        f"Phase 1 load: VUS={VUS} ITER={ITERATIONS} CMDS/attempt={CMDS} lab={LAB_SLUG}\n"
        f"target: {BASE}  ramp: {RAMP_S:.0f}s"
    )
    start = time.time()
    threads = [threading.Thread(target=vu_thread, args=(i,), daemon=True) for i in range(VUS)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    dur = time.time() - start

    prov_rate = S.provision_ok / S.provision_attempts if S.provision_attempts else 0.0
    sub_rate = S.submit_ok / S.submit_attempts if S.submit_attempts else 0.0
    des_rate = S.destroy_ok / S.destroy_attempts if S.destroy_attempts else 0.0
    conn_rate = S.connect_ok / S.connect_attempts if S.connect_attempts else 0.0
    p95 = (
        statistics.quantiles(S.provision_ms, n=20)[18]
        if len(S.provision_ms) >= 20
        else (max(S.provision_ms) if S.provision_ms else 0.0)
    )

    print("\n=== Phase 1 load run summary ===")
    print(f"duration:            {dur:.1f}s")
    print(f"labs completed:      {S.labs_completed} (target ≥ {VUS * ITERATIONS} = VUS×ITER)")
    print(f"workspace writes:    {S.cmd_writes} (target {VUS * ITERATIONS * CMDS})")
    print(f"provision success:   {prov_rate:.4f}  (threshold ≥ {T_PROVISION_SUCCESS})")
    print(f"time-to-ready p95:   {p95:.0f} ms   (threshold ≤ {T_READY_P95_MS} ms)")
    print(f"connect success:     {conn_rate:.4f}")
    print(f"submit success:      {sub_rate:.4f}  (threshold ≥ {T_SUBMIT_SUCCESS})")
    print(f"teardown success:    {des_rate:.4f}  (threshold ≥ {T_DESTROY_SUCCESS})")
    print(f"{orchestrator_provision_count()}")
    if S.errors:
        seen = {}
        for e in S.errors:
            seen[e] = seen.get(e, 0) + 1
        print("\nerror sample (top 10 by count):")
        for e, n in sorted(seen.items(), key=lambda kv: -kv[1])[:10]:
            print(f"  {n:5d}  {e}")

    failures = []
    if prov_rate < T_PROVISION_SUCCESS:
        failures.append("provision success below 99%")
    if p95 > T_READY_P95_MS:
        failures.append("time-to-ready p95 above 20s")
    if sub_rate < T_SUBMIT_SUCCESS:
        failures.append("submit success below 99%")
    if des_rate < T_DESTROY_SUCCESS:
        failures.append("teardown success below 100%")

    print(
        "\nNext: run evaluation/phase1/load/check-orphans.sh now and again in 1h,"
        "\nthen fill evaluation/phase1/results/loadtest-<date>.md."
    )
    if failures:
        print("\nFAIL: " + "; ".join(failures))
        return 1
    print("\nPASS: all in-script thresholds met (orphan gate still to verify separately)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
