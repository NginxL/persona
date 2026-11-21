"""Real-user GitHub reviews on the exact current head (opt-in watcher policy).

When a repository enables ``respect_human_reviews``, a review submitted by a
real user can replace the watcher's own semantic review for that exact head.
The rules are deliberately narrow:

* only the current head counts -- a review on an older commit never carries to
  a new head, so every pushed commit is re-evaluated from scratch;
* only real user accounts count -- bot and GitHub App reviews, missing users
  and the watcher's own authenticated identity never qualify as human;
* dismissed and pending reviews are neutral rather than approval or a block;
* the latest effective review per reviewer wins, so an approval followed by a
  change request on the same head blocks.

The watcher's other gates (verification, privacy scan, linked issue,
mergeability and exact-head matching) are unaffected and stay mandatory.
"""
from pr_watch_common import SHA, WatchError, bounded, gh_json, run

APPROVED = "APPROVED"
CHANGES_REQUESTED = "CHANGES_REQUESTED"
COMMENTED = "COMMENTED"
DISMISSED = "DISMISSED"
PENDING = "PENDING"
EFFECTIVE_STATES = (APPROVED, CHANGES_REQUESTED, COMMENTED)
NEUTRAL_STATES = (DISMISSED, PENDING)

DECISION_APPROVED = "approved"
DECISION_CHANGES_REQUESTED = "changes_requested"
DECISION_COMMENTED = "commented"
DECISION_NONE = "none"

# A blocking human decision outranks an approval; an approval outranks a note.
_STATE_PRECEDENCE = {CHANGES_REQUESTED: 3, APPROVED: 2, COMMENTED: 1}
_DECISION_BY_STATE = {CHANGES_REQUESTED: DECISION_CHANGES_REQUESTED,
                      APPROVED: DECISION_APPROVED,
                      COMMENTED: DECISION_COMMENTED}


def fetch_reviews(repo, number):
    """Read every PR review through bounded REST pages."""
    data = gh_json(["api", "repos/%s/pulls/%s/reviews?per_page=100" % (repo, number),
                    "--paginate"], timeout=90)
    if not isinstance(data, list):
        raise WatchError("PR reviews endpoint did not return an array")
    return data


def authenticated_login():
    """The login whose reviews are the watcher's own automated publications.

    ``gh --jq`` emits a raw string rather than a JSON value, so this reads the
    process output directly instead of going through the JSON helper.
    """
    result = run(["gh", "api", "user", "--jq", ".login"], timeout=60)
    if result.returncode:
        raise WatchError("cannot read the authenticated GitHub login: " + bounded(result.stderr, 500))
    login = result.stdout.strip()
    if not login:
        raise WatchError("GitHub did not return the authenticated login")
    return login


def _review_record(raw):
    """Validate the transport shape; identity filtering happens separately."""
    if not isinstance(raw, dict):
        raise WatchError("PR review record is not an object")
    review_id = raw.get("id")
    if not isinstance(review_id, int) or isinstance(review_id, bool) or review_id < 1:
        raise WatchError("PR review has no valid id")
    state = raw.get("state")
    if state not in EFFECTIVE_STATES + NEUTRAL_STATES:
        raise WatchError("PR review %s has an unknown state: %r" % (review_id, state))
    commit_id = raw.get("commit_id")
    if not isinstance(commit_id, str) or not SHA.fullmatch(commit_id):
        raise WatchError("PR review %s has no valid commit id" % review_id)
    user = raw.get("user")
    if not isinstance(user, dict):
        # A null user is a documented GitHub shape for deleted accounts; it can
        # never be a human credential, so it is ignored rather than accepted.
        return None
    login = user.get("login")
    user_type = user.get("type")
    if not isinstance(login, str) or not login.strip() or not isinstance(user_type, str):
        raise WatchError("PR review %s has no valid reviewer identity" % review_id)
    submitted_at = raw.get("submitted_at")
    if state in EFFECTIVE_STATES and (not isinstance(submitted_at, str) or not submitted_at):
        raise WatchError("PR review %s has no submission timestamp" % review_id)
    return {"id": review_id, "state": state, "commit_id": commit_id,
            "login": login.strip(), "user_type": user_type,
            "submitted_at": submitted_at if isinstance(submitted_at, str) else ""}


def classify_human_reviews(raw_reviews, head, self_login):
    """Classify real-user reviews bound to ``head``; pure and side-effect free.

    Returns ``{"decision": "none"}`` or a mapping carrying the deciding
    review's id, login, state, commit id and submission timestamp.
    """
    if not isinstance(head, str) or not SHA.fullmatch(head):
        raise WatchError("human review classification needs the exact head")
    if not isinstance(self_login, str) or not self_login.strip():
        raise WatchError("human review classification needs the authenticated login")
    latest_by_login = {}
    for raw in raw_reviews:
        record = _review_record(raw)
        if record is None:
            continue
        if record["commit_id"] != head:
            # Reviews on older heads never carry over to a new head.
            continue
        if record["user_type"] != "User":
            # Bot/GitHub App/Organization/Mannequin accounts are not humans.
            continue
        if record["login"] == self_login.strip():
            # The watcher's own automated comment-reviews cannot self-approve.
            continue
        previous = latest_by_login.get(record["login"])
        if previous is None or (record["submitted_at"], record["id"]) > \
                (previous["submitted_at"], previous["id"]):
            latest_by_login[record["login"]] = record
    deciding = None
    for record in latest_by_login.values():
        if record["state"] in NEUTRAL_STATES:
            # A dismissal or pending review leaves the reviewer without an
            # effective state on this head.
            continue
        if deciding is None or _STATE_PRECEDENCE[record["state"]] > _STATE_PRECEDENCE[deciding["state"]]:
            deciding = record
    if deciding is None:
        return {"decision": DECISION_NONE}
    return {"decision": _DECISION_BY_STATE[deciding["state"]],
            "review": {"id": deciding["id"], "login": deciding["login"],
                       "state": deciding["state"], "commit_id": deciding["commit_id"],
                       "submitted_at": deciding["submitted_at"]}}
