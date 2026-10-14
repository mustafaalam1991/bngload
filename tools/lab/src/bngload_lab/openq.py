"""Open-question register (design section 17.7).

The register is a TOML file (``docs/phase0/open-questions.toml``) with one
``[[question]]`` table per question::

    [[question]]
    id = "OQ-05"
    priority = 5
    question = "Does the generator NIC support hardware TX and RX timestamps …?"
    owner = "jane.doe"          # required for Phase 0 acceptance
    due = "2026-11-15"          # optional, ISO date
    status = "open"             # open | answered | deferred
    answer = ""                 # required when status = "answered"
    evidence = "runs/…/tstamp.json"

Phase 0 acceptance requires every question to have an owner; answers may
still be pending.
"""

from __future__ import annotations

import datetime as dt
import sys
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

from .envelope import FAIL, PASS, WARN, Envelope, Finding, worst_of

if sys.version_info >= (3, 11):
    import tomllib
else:
    import tomli as tomllib

STATUSES = ("open", "answered", "deferred")


@dataclass
class Question:
    id: str
    question: str
    priority: int = 0
    owner: str = ""
    due: str = ""
    status: str = "open"
    answer: str = ""
    evidence: str = ""


def load(path: str | Path) -> list[Question]:
    doc = tomllib.loads(Path(path).read_text())
    out = []
    for i, q in enumerate(doc.get("question", [])):
        known = {k: v for k, v in q.items() if k in Question.__dataclass_fields__}
        if "id" not in known or "question" not in known:
            raise ValueError(f"question #{i + 1}: 'id' and 'question' are required")
        out.append(Question(**known))
    return out


def check(questions: list[Question], today: dt.date | None = None) -> list[Finding]:
    """One finding per problem class, plus the acceptance finding."""
    today = today or dt.date.today()
    fs = []
    ids = [q.id for q in questions]
    dupes = sorted({i for i in ids if ids.count(i) > 1})
    if dupes:
        fs.append(Finding("openq.ids", "Question IDs are unique", FAIL, "duplicates: " + ", ".join(dupes)))
    bad_status = [q.id for q in questions if q.status not in STATUSES]
    if bad_status:
        fs.append(Finding("openq.status", "Valid status values", FAIL, f"{', '.join(bad_status)} (use {'/'.join(STATUSES)})"))
    unanswered = [q.id for q in questions if q.status == "answered" and not q.answer.strip()]
    if unanswered:
        fs.append(Finding("openq.answers", "Answered questions carry the answer", WARN, ", ".join(unanswered)))
    overdue = []
    for q in questions:
        if q.status == "open" and q.due:
            try:
                if dt.date.fromisoformat(q.due) < today:
                    overdue.append(q.id)
            except ValueError:
                fs.append(Finding("openq.due", "Due dates are ISO dates", WARN, f"{q.id}: {q.due!r}"))
    if overdue:
        fs.append(Finding("openq.overdue", "No open question is overdue", WARN, ", ".join(overdue)))
    no_owner = [q.id for q in questions if not q.owner.strip()]
    fs.insert(
        0,
        Finding(
            "openq.owners",
            "Every open question has an owner",
            PASS if questions and not no_owner else FAIL,
            (f"{len(questions) - len(no_owner)}/{len(questions)} owned" + (f"; missing: {', '.join(no_owner)}" if no_owner else ""))
            if questions
            else "register is empty",
            ref="Phase 0 acceptance",
            remedy="" if not no_owner else "assign an owner to each question in the register",
        ),
    )
    return fs


def report(path: str | Path) -> Envelope:
    qs = load(path)
    fs = check(qs)
    counts = {s: sum(1 for q in qs if q.status == s) for s in STATUSES}
    data = {"register": str(path), "questions": [asdict(q) for q in qs], "counts": counts, "findings": [f.to_dict() for f in fs]}
    owned = sum(1 for q in qs if q.owner.strip())
    summary = f"{len(qs)} questions, {owned} owned, {counts['answered']} answered"
    return Envelope(tool="bngload-lab open-questions").finish(worst_of(fs), summary, data)


def to_markdown(qs: list[Question]) -> str:
    lines = ["| ID | P | Question | Owner | Due | Status |", "| --- | --- | --- | --- | --- | --- |"]
    for q in sorted(qs, key=lambda q: (q.priority or 99, q.id)):
        lines.append(f"| {q.id} | {q.priority or ''} | {q.question} | {q.owner or '**unassigned**'} | {q.due} | {q.status} |")
    return "\n".join(lines) + "\n"


def as_dicts(qs: list[Question]) -> list[dict[str, Any]]:
    return [asdict(q) for q in qs]
