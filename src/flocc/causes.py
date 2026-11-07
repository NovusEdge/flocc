"""Why a run stopped, and who acts on it: the service (transient), cardinal (agent), the owner,
or nobody until the repo's base is fixed (repo)."""

TRANSIENT, AGENT, OWNER, REPO = "transient", "agent", "owner", "repo"
TYPES = frozenset({TRANSIENT, AGENT, OWNER, REPO})

CAUSES = {
    # A second failure, after the service's one retry, is the owner's (supervisor._block).
    "publish-failed": TRANSIENT,
    # supervise already relaunched the session once, so this is its second death.
    "crash": AGENT,
    "idle": AGENT,
    "timed-out": AGENT,
    # The same failure twice with fix rounds left; at the last round it is check-cap.
    "check-failed": AGENT,
    "no-changes": AGENT,
    # cardinal resumes it only when the issue text settles every question.
    "needs-owner": OWNER,
    "check-cap": OWNER,
    # The check outran check_timeout_s; a fixer cannot make a slow check fast, so the owner raises
    # the limit or splits the check.
    "check-timeout": OWNER,
    "tests-touched": OWNER,
    "no-tests": OWNER,
    "branch-moved": OWNER,
    "rebase-conflict": OWNER,
    "base-broken": REPO,
}

RESUMABLE = frozenset({c for c, t in CAUSES.items() if t == AGENT} | {"needs-owner"})


def type_of(cause: str) -> str:
    try:
        return CAUSES[cause]
    except KeyError:
        raise ValueError(f"unknown stop cause {cause!r}") from None
