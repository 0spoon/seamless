#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""distill.py -- a recorded Claude Code transcript -> landing-page scene data.

Reads `$CLAUDE_CONFIG_DIR/projects/<slug>/*.jsonl` (a real take recorded against
the throwaway fixture that `scripts/branding/record.sh` stands up) and emits the
scene-data shape `docs/static/scenes.js` holds: the file the scene player
animates and `cmd/docsgen` renders the /scenarios/ pages from.

    uv run scripts/branding/distill.py steps    <take.jsonl>
    uv run scripts/branding/distill.py scaffold --id ... --pane with=<take.jsonl> -o spec.json
    uv run scripts/branding/distill.py build    spec.json [more.json ...] -o /tmp/scenes.js
    uv run scripts/branding/distill.py verify   spec.json --scenes docs/static/scenes.js
    uv run scripts/branding/distill.py selftest

Full recipe: scripts/branding/README.md.

================================ THE VERBATIM RULE ============================

Everything the model or the operator actually SAID that survives into scenes.js
is byte-identical to the transcript. This is the entire point of the tool: the
landing page claims "transcripts verbatim", and that claim is only worth
anything if nothing between the terminal and the page can quietly improve a
sentence.

Curation is allowed. Editing is not:

  ALLOWED   drop a whole step; collapse a run of dropped steps into one `ffwd`
            marker; keep a contiguous PREFIX of a step (clip at a paragraph,
            line, or word boundary) and optionally mark the cut with " ...".
  FORBIDDEN reword, paraphrase, summarize, fix a typo, re-punctuate, normalize
            whitespace, splice two non-adjacent sentences together, or type a
            line the model did not produce into a verbatim role.

How the rule is enforced rather than merely stated:

  1. Roles are split into VERBATIM_ROLES (user, agent, inject, tool) and
     EDITORIAL_ROLES (ffwd, comment, cmd, files, fm). There is no code path that
     puts a transcript-derived string into an editorial role, and no code path
     that puts an authored string into a verbatim role: the spec's `epilogue`
     (the only place the operator may write prose that reaches the page, besides
     ffwd text) rejects every verbatim role outright.
  2. Every verbatim string is produced by `Verbatim.of(source, text, ...)`, which
     refuses to construct unless `text` is a contiguous BYTE substring of the
     transcript string it claims to come from. A rewrite, a re-punctuation, a
     smart-quote swap, a CRLF normalization, or a two-sentence splice all fail
     here, because none of them is a contiguous span of the original.
  3. Before anything is written, `audit_scene` re-derives the corpus from the
     transcript ON DISK and re-checks every verbatim field against it, ignoring
     the objects that built them. Construction-time trust is not enough: this
     second pass is what makes a hand-edited intermediate, or a hand-edited
     scenes.js, fail too. `verify` is that same pass run on its own, so a
     committed scenes.js can be re-audited against the takes it came from.
  4. Output is written only if the audit passes. There is no --skip-audit flag,
     on purpose. If a take needs a gloss the transcript does not contain, that
     gloss is an `ffwd` line -- the one channel that is editorial BY DEFINITION,
     rendered as a dim fast-forward marker so a reader can see it is not speech.

The sanctioned truncation marker is ELLIPSIS (" ..."); the audit strips exactly
that suffix before the substring check, so it cannot be used to smuggle text.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

# ---------------------------------------------------------------------------
# The scene-data contract.
#
# These sets mirror the json tags in cmd/docsgen/scenes.go, which parses
# docs/static/scenes.js with DisallowUnknownFields -- a field the scenario
# renderer would drop is dropped published content, so it must fail the build.
# `selftest` re-derives them from that Go file and fails on drift, so this is a
# checked mirror rather than a hand-transcribed one.
# ---------------------------------------------------------------------------

SCENE_FIELDS = ["id", "kicker", "tab", "title", "prompt", "layout", "caption", "panes"]
PANE_FIELDS = ["key", "label", "source", "outcome", "steps"]
STEP_FIELDS = ["role", "text", "tag", "focus", "label", "result", "beat", "emphasis", "k", "v", "files"]
FILE_FIELDS = ["name", "tag"]

LAYOUTS = ("with-without", "split")

# Roles whose text is the model's or the operator's own words, byte-identical.
VERBATIM_ROLES = ("user", "agent", "inject", "tool")
# Roles that are editorial by definition and never carry transcript speech.
EDITORIAL_ROLES = ("ffwd", "comment", "cmd", "files", "fm")

ELLIPSIS = " …"

DEFAULT_CAPTION = (
    "real sessions against a seeded demo instance — transcripts verbatim, "
    "only the typing is animated"
)
DEFAULT_LABELS = {
    "with": "with Seamless",
    "without": "without Seamless",
    "A": "agent A",
    "B": "agent B",
}
TODO_MARKER = "TODO:"

GENERATED_HEADER = """\
// scenes.js -- verbatim transcript data for the landing page's with/without
// terminal scenes. Generated by scripts/branding/distill.py from real recorded
// Claude Code takes; see scripts/branding/README.md for the whole recipe.
//
// VERBATIM: user/agent/inject text and tool excerpts are byte-identical to the
// transcript. Curation is dropping whole steps, collapsing dropped runs into
// {role:"ffwd"} markers, and keeping contiguous prefixes (cut marked " ...").
// No line is ever reworded; ffwd text is the one editorial channel.
//
// Step shapes (role):
//   user   {text}                the typed prompt, verbatim
//   inject {tag, text, focus?}   a hook injection, full verbatim block;
//                                 focus[] = verbatim lines to emphasize
//   agent  {text}                assistant prose, verbatim
//   tool   {label, result?}      a tool call; result = verbatim excerpt
//   ffwd   {text}                an editorial span summary for skipped steps
//   comment/cmd/files/fm         illustrative epilogue (not a recorded line)
//
// Scene shape: {id, kicker, tab, title, prompt, layout, caption,
//               panes:[{key,label,source,outcome,steps}]}.
// layout: "with-without" (two panes, toggle) | "split" (two agents, one timeline).
// Split-only step metadata: beat (shared clock), emphasis ("win"|"bounce").

"""

ASSIGNMENT = "window.SEAMLESS_SCENES"

# Transcript rows whose user content is machinery, not a typed prompt.
SYNTHETIC_USER_PREFIXES = (
    "<local-command-",
    "<command-name>",
    "<command-message>",
    "<bash-input>",
    "<bash-stdout>",
    "<system-reminder>",
    "<user-prompt-submit-hook>",
)


class DistillError(Exception):
    """Anything the operator can fix: a bad spec, a bad transcript, a bad path."""


class VerbatimError(DistillError):
    """The verbatim rule was violated. Never catch this to keep going."""


# ---------------------------------------------------------------------------
# Verbatim strings
# ---------------------------------------------------------------------------


class Verbatim(str):
    """A string proven to be a contiguous byte span of a transcript string.

    Construct only through `Verbatim.of`. `origin` is a human-readable pointer
    at the transcript field it came from, used in audit failures.
    """

    __slots__ = ("origin",)

    def __new__(cls, text: str, origin: str) -> "Verbatim":
        obj = super().__new__(cls, text)
        obj.origin = origin  # type: ignore[attr-defined]
        return obj

    @classmethod
    def of(cls, source: str, text: str, origin: str, ellipsis: bool = False) -> "Verbatim":
        body = text[: -len(ELLIPSIS)] if ellipsis and text.endswith(ELLIPSIS) else text
        if not contiguous(source, body):
            raise VerbatimError(
                f"{origin}: the emitted text is not a contiguous span of the transcript.\n"
                f"  emitted: {shorten(body)!r}\n"
                f"  source:  {shorten(source)!r}\n"
                "  Curation is dropping and clipping, never rewriting. If you need a "
                "gloss the transcript does not contain, make it an ffwd step."
            )
        return cls(text, origin)


def contiguous(source: str, text: str) -> bool:
    """True when `text` is a contiguous byte substring of `source`.

    Compared as UTF-8 bytes on purpose: a smart-quote swap, an NFC/NFD shuffle,
    or a CRLF normalization all survive a casual string compare in some other
    tool's hands, and none of them survives this one.
    """
    return text.encode("utf-8") in source.encode("utf-8")


def shorten(text: str, limit: int = 160) -> str:
    flat = text.replace("\n", "\\n")
    return flat if len(flat) <= limit else flat[:limit] + "…"


# ---------------------------------------------------------------------------
# Transcript reading
# ---------------------------------------------------------------------------


@dataclass
class Candidate:
    """One curatable step from a transcript, still carrying its full source."""

    index: int
    role: str
    text: str = ""
    tag: str = ""
    name: str = ""
    args: dict[str, Any] = field(default_factory=dict)
    result: str = ""
    tool_use_id: str = ""

    def preview(self) -> str:
        if self.role == "tool":
            head = self.name
            if self.result:
                head += f" -> {len(self.result)} char result"
            return head
        return shorten(self.text, 90)

    def size(self) -> int:
        return len(self.result) if self.role == "tool" else len(self.text)


@dataclass
class Transcript:
    path: Path
    session_id: str
    candidates: list[Candidate]
    corpus: list[str]
    tool_names: set[str]


def _text_of(content: Any) -> str:
    """Join the text blocks of a content value, verbatim, in order."""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "".join(b.get("text", "") for b in content if isinstance(b, dict))
    return ""


def _strings(value: Any, into: list[str]) -> None:
    if isinstance(value, str):
        into.append(value)
    elif isinstance(value, dict):
        for v in value.values():
            _strings(v, into)
    elif isinstance(value, list):
        for v in value:
            _strings(v, into)


def _inject_tag(chunk: str) -> str:
    m = re.match(r"<([a-z][a-z0-9-]*)>", chunk.lstrip())
    return m.group(1) if m else "hook"


def load_transcript(path: Path, include_sidechains: bool = False) -> Transcript:
    """Parse a Claude Code .jsonl take into ordered candidate steps.

    Thinking blocks are dropped outright (they are never shown). Sidechain rows
    (subagent chatter) are dropped unless asked for. Nothing here rewrites: each
    candidate holds the source string exactly as the transcript stored it.
    """
    if not path.is_file():
        raise DistillError(f"transcript not found: {path}")
    rows: list[dict[str, Any]] = []
    with path.open(encoding="utf-8") as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
            except json.JSONDecodeError as err:
                raise DistillError(f"{path}:{lineno}: not JSON: {err}") from err
            if isinstance(row, dict):
                rows.append(row)

    session_id = ""
    candidates: list[Candidate] = []
    corpus: list[str] = []
    tool_names: set[str] = set()
    results: dict[str, str] = {}

    def add(cand: Candidate) -> None:
        cand.index = len(candidates)
        candidates.append(cand)

    for row in rows:
        session_id = session_id or str(row.get("sessionId") or "")
        if row.get("isSidechain") and not include_sidechains:
            continue
        kind = row.get("type")

        if kind == "attachment":
            att = row.get("attachment")
            if not isinstance(att, dict) or att.get("type") != "hook_additional_context":
                continue
            chunks = att.get("content")
            if isinstance(chunks, str):
                chunks = [chunks]
            for chunk in chunks or []:
                if not isinstance(chunk, str) or not chunk.strip():
                    continue
                corpus.append(chunk)
                add(Candidate(index=-1, role="inject", text=chunk, tag=_inject_tag(chunk)))
            continue

        message = row.get("message")
        if not isinstance(message, dict):
            continue
        content = message.get("content")

        if kind == "user":
            if isinstance(content, str):
                if row.get("isMeta"):
                    continue
                if content.lstrip().startswith(SYNTHETIC_USER_PREFIXES):
                    continue
                if not content.strip():
                    continue
                corpus.append(content)
                add(Candidate(index=-1, role="user", text=content))
            elif isinstance(content, list):
                for block in content:
                    if not isinstance(block, dict):
                        continue
                    if block.get("type") == "tool_result":
                        text = _text_of(block.get("content"))
                        if text:
                            corpus.append(text)
                            results[str(block.get("tool_use_id") or "")] = text
                    elif block.get("type") == "text" and block.get("text", "").strip():
                        corpus.append(block["text"])
                        add(Candidate(index=-1, role="user", text=block["text"]))
            continue

        if kind == "assistant" and isinstance(content, list):
            for block in content:
                if not isinstance(block, dict):
                    continue
                btype = block.get("type")
                if btype == "text":
                    text = block.get("text", "")
                    if not text.strip():
                        continue
                    corpus.append(text)
                    add(Candidate(index=-1, role="agent", text=text))
                elif btype == "tool_use":
                    name = str(block.get("name") or "")
                    args = block.get("input") if isinstance(block.get("input"), dict) else {}
                    tool_names.add(name)
                    _strings(args, corpus)
                    add(
                        Candidate(
                            index=-1,
                            role="tool",
                            name=name,
                            args=args,
                            tool_use_id=str(block.get("id") or ""),
                        )
                    )
                # thinking blocks are never candidates.

    for cand in candidates:
        if cand.role == "tool" and cand.tool_use_id in results:
            cand.result = results[cand.tool_use_id]

    if not candidates:
        raise DistillError(f"{path}: no curatable steps found (is this a Claude Code transcript?)")
    return Transcript(path=path, session_id=session_id, candidates=candidates, corpus=corpus, tool_names=tool_names)


# ---------------------------------------------------------------------------
# Clipping -- every result is a contiguous PREFIX of its input
# ---------------------------------------------------------------------------


def clip(text: str, spec: Any) -> str:
    """Keep a contiguous prefix of `text`. Never anything else."""
    if spec in (None, True):
        return text
    if not isinstance(spec, dict):
        raise DistillError(f"clip spec must be an object, got {spec!r}")
    unknown = set(spec) - {"paragraphs", "lines", "chars", "ellipsis"}
    if unknown:
        raise DistillError(f"unknown clip keys {sorted(unknown)} (paragraphs|lines|chars|ellipsis)")
    out = text
    if "paragraphs" in spec:
        out = _prefix_upto(out, "\n\n", int(spec["paragraphs"]))
    if "lines" in spec:
        out = _prefix_upto(out, "\n", int(spec["lines"]))
    if "chars" in spec:
        out = _prefix_chars(out, int(spec["chars"]))
    if spec.get("ellipsis") and out != text:
        out += ELLIPSIS
    return out


def _prefix_upto(text: str, sep: str, count: int) -> str:
    if count < 1:
        raise DistillError("clip counts must be >= 1")
    at = 0
    for _ in range(count):
        found = text.find(sep, at)
        if found < 0:
            return text
        at = found + len(sep)
    return text[:at].rstrip("\n")


def _prefix_chars(text: str, count: int) -> str:
    if count < 1:
        raise DistillError("clip counts must be >= 1")
    if len(text) <= count:
        return text
    cut = text[:count]
    space = cut.rfind(" ")
    if space > count // 2:
        cut = cut[:space]
    return cut.rstrip()


# ---------------------------------------------------------------------------
# Spec -> steps
# ---------------------------------------------------------------------------

PANE_SPEC_KEYS = {
    "key", "label", "source", "outcome", "transcript", "include_sidechains",
    "keep", "ffwd", "clip", "focus", "result", "label_arg", "beat", "emphasis", "epilogue",
}
SCENE_SPEC_KEYS = set(SCENE_FIELDS) - {"panes"}


def parse_indices(value: Any, limit: int, what: str) -> list[int]:
    """Accept [0, 2, "5-9"] and return a sorted, de-duplicated index list."""
    out: set[int] = set()
    if value is None:
        return []
    if not isinstance(value, list):
        raise DistillError(f"{what} must be a list of indices and \"a-b\" ranges")
    for item in value:
        if isinstance(item, bool):
            raise DistillError(f"{what}: {item!r} is not an index")
        if isinstance(item, int):
            lo = hi = item
        elif isinstance(item, str) and re.fullmatch(r"\d+-\d+", item.strip()):
            lo, hi = (int(p) for p in item.strip().split("-"))
        elif isinstance(item, str) and item.strip().isdigit():
            lo = hi = int(item.strip())
        else:
            raise DistillError(f"{what}: {item!r} is not an index or an \"a-b\" range")
        if lo > hi:
            raise DistillError(f"{what}: range {item!r} runs backwards")
        for i in range(lo, hi + 1):
            if not 0 <= i < limit:
                raise DistillError(f"{what}: index {i} is outside 0..{limit - 1}")
            out.add(i)
    return sorted(out)


def _by_index(mapping: Any, limit: int, what: str) -> dict[int, Any]:
    if mapping is None:
        return {}
    if not isinstance(mapping, dict):
        raise DistillError(f"{what} must be an object keyed by step index")
    out: dict[int, Any] = {}
    for key, value in mapping.items():
        if not str(key).isdigit():
            raise DistillError(f"{what}: {key!r} is not a step index")
        idx = int(key)
        if not 0 <= idx < limit:
            raise DistillError(f"{what}: index {idx} is outside 0..{limit - 1}")
        out[idx] = value
    return out


def default_ffwd_text(span: list[Candidate]) -> str:
    """A factual shape summary. Deliberately contains none of the model's words."""
    tools = sum(1 for c in span if c.role == "tool")
    agents = sum(1 for c in span if c.role == "agent")
    users = sum(1 for c in span if c.role == "user")
    parts = []
    if tools:
        parts.append(f"{tools} tool call{'s' if tools != 1 else ''}")
    if agents:
        parts.append(f"{agents} repl{'ies' if agents != 1 else 'y'}")
    if users:
        parts.append(f"{users} prompt{'s' if users != 1 else ''}")
    return ", ".join(parts) or f"{len(span)} steps"


def build_label(cand: Candidate, spec: Any, where: str) -> Verbatim:
    """`<tool name>` or `<tool name> <verbatim argument span>`. Nothing else."""
    if not cand.name:
        raise DistillError(f"{where}: tool step has no name")
    if spec is None:
        return Verbatim(cand.name, f"{where} name")
    if isinstance(spec, str):
        spec = {"key": spec}
    if not isinstance(spec, dict) or "key" not in spec:
        raise DistillError(f"{where}: label_arg must be an argument name or {{key, basename?, chars?}}")
    unknown = set(spec) - {"key", "basename", "chars"}
    if unknown:
        raise DistillError(f"{where}: unknown label_arg keys {sorted(unknown)}")
    raw = cand.args.get(spec["key"])
    if not isinstance(raw, str):
        raise DistillError(
            f"{where}: argument {spec['key']!r} is {type(raw).__name__}, not a string; "
            "a label may only quote a string argument verbatim"
        )
    value = raw
    if spec.get("basename"):
        value = value.rsplit("/", 1)[-1]
    ellipsis = False
    if "chars" in spec:
        clipped = _prefix_chars(value, int(spec["chars"]))
        if clipped != value:
            ellipsis = True
            clipped += ELLIPSIS
        value = clipped
    Verbatim.of(raw, value, f"{where} label argument {spec['key']!r}", ellipsis=ellipsis)
    return Verbatim(f"{cand.name} {value}", f"{where} label")


def build_step(cand: Candidate, pane: dict[str, Any], where: str) -> dict[str, Any]:
    clips = pane["_clip"]
    focus_spec = pane["_focus"]
    result_spec = pane["_result"]
    label_spec = pane["_label_arg"]
    beats = pane["_beat"]
    emphasis = pane["_emphasis"]

    step: dict[str, Any] = {"role": cand.role}
    if cand.role in ("user", "agent"):
        spec = clips.get(cand.index)
        text = clip(cand.text, spec)
        step["text"] = Verbatim.of(
            cand.text, text, f"{where} {cand.role}", ellipsis=bool(isinstance(spec, dict) and spec.get("ellipsis"))
        )
    elif cand.role == "inject":
        spec = clips.get(cand.index)
        text = clip(cand.text, spec)
        step["tag"] = cand.tag
        step["text"] = Verbatim.of(
            cand.text, text, f"{where} inject", ellipsis=bool(isinstance(spec, dict) and spec.get("ellipsis"))
        )
        wanted = focus_spec.get(cand.index) or []
        if not isinstance(wanted, list):
            raise DistillError(f"{where}: focus must be a list of verbatim lines")
        focus = [Verbatim.of(text, line, f"{where} inject focus") for line in wanted]
        if focus:
            step["focus"] = focus
    elif cand.role == "tool":
        step["label"] = build_label(cand, label_spec.get(cand.index), where)
        spec = result_spec.get(cand.index)
        if spec is not None:
            if not cand.result:
                raise DistillError(f"{where}: no tool result was recorded for this call")
            text = clip(cand.result, spec)
            step["result"] = Verbatim.of(
                cand.result, text, f"{where} tool result",
                ellipsis=bool(isinstance(spec, dict) and spec.get("ellipsis")),
            )
    else:  # pragma: no cover -- load_transcript emits no other role
        raise DistillError(f"{where}: unexpected candidate role {cand.role!r}")

    if cand.index in beats:
        step["beat"] = int(beats[cand.index])
    if cand.index in emphasis:
        value = emphasis[cand.index]
        if value not in ("win", "bounce"):
            raise DistillError(f"{where}: emphasis must be \"win\" or \"bounce\", got {value!r}")
        step["emphasis"] = value
    return step


def check_epilogue(steps: Any, where: str) -> list[dict[str, Any]]:
    """The one place authored prose may reach the page -- and never as speech."""
    if steps is None:
        return []
    if not isinstance(steps, list):
        raise DistillError(f"{where}: epilogue must be a list of steps")
    out = []
    for i, step in enumerate(steps):
        at = f"{where} epilogue[{i}]"
        if not isinstance(step, dict):
            raise DistillError(f"{at}: not an object")
        role = step.get("role")
        if role in VERBATIM_ROLES:
            raise DistillError(
                f"{at}: role {role!r} is a verbatim role -- an epilogue step is authored, "
                "so it may only use " + "|".join(EDITORIAL_ROLES) + ". Distill it from the "
                "transcript instead of typing it."
            )
        if role not in EDITORIAL_ROLES:
            raise DistillError(f"{at}: unknown role {role!r} ({'|'.join(EDITORIAL_ROLES)})")
        unknown = set(step) - set(STEP_FIELDS)
        if unknown:
            raise DistillError(f"{at}: unknown step fields {sorted(unknown)}")
        for j, entry in enumerate(step.get("files") or []):
            bad = set(entry) - set(FILE_FIELDS)
            if bad:
                raise DistillError(f"{at}.files[{j}]: unknown fields {sorted(bad)}")
        out.append(step)
    return out


def curate_pane(pane: dict[str, Any], scene_id: str) -> tuple[dict[str, Any], Transcript]:
    unknown = set(pane) - PANE_SPEC_KEYS
    if unknown:
        raise DistillError(f"scene {scene_id}: pane has unknown keys {sorted(unknown)}")
    for required in ("key", "transcript", "outcome"):
        if not pane.get(required):
            raise DistillError(f"scene {scene_id}: pane is missing {required!r}")
    key = str(pane["key"])
    where = f"scene {scene_id} pane {key}"

    tr = load_transcript(Path(pane["transcript"]).expanduser(), bool(pane.get("include_sidechains")))
    n = len(tr.candidates)

    explicit_keep = "keep" in pane
    keep = set(parse_indices(pane.get("keep"), n, f"{where}: keep"))

    # ffwd spans are resolved first so an implicit "keep everything" means
    # "everything the ffwd spans did not already swallow".
    spans: dict[int, tuple[int, str | None]] = {}
    covered: set[int] = set()
    for entry in pane.get("ffwd") or []:
        if isinstance(entry, (str, int)):
            entry = {"span": entry}
        if not isinstance(entry, dict) or "span" not in entry:
            raise DistillError(f"{where}: each ffwd entry needs a \"span\"")
        unknown = set(entry) - {"span", "text"}
        if unknown:
            raise DistillError(f"{where}: unknown ffwd keys {sorted(unknown)}")
        idxs = parse_indices([entry["span"]], n, f"{where}: ffwd span")
        if idxs != list(range(idxs[0], idxs[-1] + 1)):
            raise DistillError(f"{where}: ffwd span {entry['span']!r} is not contiguous")
        clash = sorted(set(idxs) & (keep | covered))
        if clash:
            raise DistillError(f"{where}: ffwd span {entry['span']!r} overlaps kept/collapsed steps {clash}")
        covered.update(idxs)
        spans[idxs[0]] = (idxs[-1], entry.get("text"))

    if not explicit_keep:
        keep = set(range(n)) - covered

    pane["_clip"] = _by_index(pane.get("clip"), n, f"{where}: clip")
    pane["_focus"] = _by_index(pane.get("focus"), n, f"{where}: focus")
    pane["_result"] = _by_index(pane.get("result"), n, f"{where}: result")
    pane["_label_arg"] = _by_index(pane.get("label_arg"), n, f"{where}: label_arg")
    pane["_beat"] = _by_index(pane.get("beat"), n, f"{where}: beat")
    pane["_emphasis"] = _by_index(pane.get("emphasis"), n, f"{where}: emphasis")

    steps: list[dict[str, Any]] = []
    i = 0
    while i < n:
        if i in spans:
            end, text = spans[i]
            steps.append({"role": "ffwd", "text": text or default_ffwd_text(tr.candidates[i : end + 1])})
            i = end + 1
            continue
        if i in keep:
            steps.append(build_step(tr.candidates[i], pane, f"{where} step {i}"))
        i += 1
    steps.extend(check_epilogue(pane.get("epilogue"), where))
    if not steps:
        raise DistillError(f"{where}: curation kept no steps")

    out = {
        "key": key,
        "label": str(pane.get("label") or DEFAULT_LABELS.get(key, key)),
        "source": str(pane.get("source") or tr.session_id),
        "outcome": str(pane["outcome"]),
        "steps": steps,
    }
    return out, tr


def build_scene(spec: dict[str, Any], path: Path) -> tuple[dict[str, Any], dict[str, Transcript]]:
    unknown = set(spec) - {"scene", "panes"}
    if unknown:
        raise DistillError(f"{path}: unknown spec keys {sorted(unknown)} (scene|panes)")
    meta = spec.get("scene")
    if not isinstance(meta, dict):
        raise DistillError(f"{path}: spec needs a \"scene\" object")
    unknown = set(meta) - SCENE_SPEC_KEYS
    if unknown:
        raise DistillError(f"{path}: scene has unknown keys {sorted(unknown)}")
    for required in ("id", "title", "prompt"):
        if not meta.get(required):
            raise DistillError(f"{path}: scene is missing {required!r}")
    layout = str(meta.get("layout") or "with-without")
    if layout not in LAYOUTS:
        raise DistillError(f"{path}: layout {layout!r} is not one of {'|'.join(LAYOUTS)}")
    panes_spec = spec.get("panes")
    if not isinstance(panes_spec, list) or not panes_spec:
        raise DistillError(f"{path}: spec needs a non-empty \"panes\" list")

    scene_id = str(meta["id"])
    panes = []
    transcripts: dict[str, Transcript] = {}
    for pane_spec in panes_spec:
        if not isinstance(pane_spec, dict):
            raise DistillError(f"{path}: each pane must be an object")
        pane, tr = curate_pane(dict(pane_spec), scene_id)
        if pane["key"] in transcripts:
            raise DistillError(f"{path}: duplicate pane key {pane['key']!r}")
        transcripts[pane["key"]] = tr
        panes.append(pane)

    scene = {
        "id": scene_id,
        "kicker": str(meta.get("kicker") or ""),
        "tab": str(meta.get("tab") or ""),
        "title": str(meta["title"]),
        "prompt": str(meta["prompt"]),
        "layout": layout,
        "caption": str(meta.get("caption") or DEFAULT_CAPTION),
        "panes": panes,
    }
    scene = {k: v for k, v in scene.items() if v not in ("", [], None)}

    # The page labels this field "prompt" and shows it as what was typed, both in
    # the player and in the crawlable SSR fallback -- so it is verbatim too. It
    # is checked against the full transcript, not against the (possibly clipped)
    # user step, so clipping a long prompt on screen stays legal.
    corpora = [line for tr in transcripts.values() for line in tr.corpus]
    if not any(contiguous(source, scene["prompt"]) for source in corpora):
        raise VerbatimError(
            f"scene {scene_id}: the scene prompt is not in any take.\n"
            f"  prompt: {shorten(scene['prompt'])!r}\n"
            "  The page presents it as the prompt that was typed. Quote it from the "
            "transcript, or reframe it as the scene title instead."
        )
    return scene, transcripts


# ---------------------------------------------------------------------------
# The audit: re-derive from disk, trust nothing that was built
# ---------------------------------------------------------------------------


def audit_scene(scene: dict[str, Any], transcripts: dict[str, Transcript]) -> None:
    """Re-check every verbatim field against the transcript corpus on disk.

    Deliberately ignores the Verbatim objects that produced the values: this
    pass is what catches a hand-edited intermediate or a hand-edited scenes.js.
    """
    scene_id = scene.get("id", "?")
    check_shape(scene, f"scene {scene_id}")
    for pane in scene["panes"]:
        key = pane["key"]
        tr = transcripts.get(key)
        if tr is None:
            raise DistillError(f"scene {scene_id}: no transcript supplied for pane {key!r} -- cannot audit")
        corpus = tr.corpus
        for i, step in enumerate(pane["steps"]):
            where = f"scene {scene_id} pane {key} step {i} ({step.get('role')})"
            role = step.get("role")
            if role not in VERBATIM_ROLES:
                continue
            if role in ("user", "agent", "inject"):
                _audit_span(str(step.get("text", "")), corpus, f"{where} text")
                for j, line in enumerate(step.get("focus") or []):
                    if not contiguous(str(step.get("text", "")), str(line)):
                        raise VerbatimError(f"{where} focus[{j}]: not a span of the injected block")
            elif role == "tool":
                label = str(step.get("label", ""))
                name, _, rest = label.partition(" ")
                if name not in tr.tool_names:
                    raise VerbatimError(
                        f"{where}: label starts with {name!r}, which no tool call in "
                        f"{tr.path.name} used"
                    )
                if rest:
                    _audit_span(rest, corpus, f"{where} label argument")
                if "result" in step:
                    _audit_span(str(step["result"]), corpus, f"{where} result")


def _audit_span(text: str, corpus: list[str], where: str) -> None:
    body = text[: -len(ELLIPSIS)] if text.endswith(ELLIPSIS) else text
    if not body:
        raise VerbatimError(f"{where}: empty")
    if not any(contiguous(source, body) for source in corpus):
        raise VerbatimError(
            f"{where}: no transcript string contains this text as a contiguous span.\n"
            f"  emitted: {shorten(body)!r}\n"
            "  Something rewrote, re-punctuated, or spliced a recorded line."
        )


def check_shape(scene: dict[str, Any], where: str) -> None:
    """Reject any field cmd/docsgen's DisallowUnknownFields decoder would."""
    unknown = set(scene) - set(SCENE_FIELDS)
    if unknown:
        raise DistillError(f"{where}: unknown scene fields {sorted(unknown)}")
    for text in (scene.get("title", ""), scene.get("prompt", ""), scene.get("caption", "")):
        if TODO_MARKER in str(text):
            raise DistillError(f"{where}: a {TODO_MARKER} placeholder is still in the scene metadata")
    if not scene.get("panes"):
        raise DistillError(f"{where}: no panes")
    for pane in scene["panes"]:
        unknown = set(pane) - set(PANE_FIELDS)
        if unknown:
            raise DistillError(f"{where} pane {pane.get('key')!r}: unknown fields {sorted(unknown)}")
        if not pane.get("outcome") or not pane.get("steps"):
            raise DistillError(f"{where} pane {pane.get('key')!r}: missing outcome or steps")
        if TODO_MARKER in str(pane["outcome"]):
            raise DistillError(
                f"{where} pane {pane['key']!r}: the outcome is still a {TODO_MARKER} placeholder"
            )
        for i, step in enumerate(pane["steps"]):
            at = f"{where} pane {pane['key']!r} step {i}"
            unknown = set(step) - set(STEP_FIELDS)
            if unknown:
                raise DistillError(f"{at}: unknown step fields {sorted(unknown)}")
            if step.get("role") not in VERBATIM_ROLES + EDITORIAL_ROLES:
                raise DistillError(f"{at}: unknown role {step.get('role')!r}")
            for j, entry in enumerate(step.get("files") or []):
                bad = set(entry) - set(FILE_FIELDS)
                if bad:
                    raise DistillError(f"{at}.files[{j}]: unknown fields {sorted(bad)}")


# ---------------------------------------------------------------------------
# Emitting
# ---------------------------------------------------------------------------


def render(scenes: list[dict[str, Any]], header: str) -> str:
    body = json.dumps(scenes, indent=2, ensure_ascii=False)
    return f"{header}{ASSIGNMENT} = {body};\n"


def split_scenes_js(raw: str, path: str) -> tuple[str, list[dict[str, Any]]]:
    """Return (header comment, parsed scenes) for a scenes.js file.

    Mirrors cmd/docsgen/scenes.go loadScenes: everything from the first `[`
    after the assignment to the last `]` is plain JSON.
    """
    at = raw.find(ASSIGNMENT)
    if at < 0:
        raise DistillError(f"{path}: no {ASSIGNMENT} assignment found")
    start = raw.find("[", at)
    end = raw.rfind("]")
    if start < 0 or start > end:
        raise DistillError(f"{path}: could not locate the scene array after {ASSIGNMENT}")
    return raw[:at], json.loads(raw[start : end + 1])


def guard_output(out: Path, force: bool) -> None:
    parts = out.resolve().parts
    if "docs" in parts and "static" in parts and out.name == "scenes.js" and not force:
        raise DistillError(
            f"{out} is the LIVE landing-page animation, and a build only writes the scenes "
            "its specs name -- writing here would drop every scene you did not pass.\n"
            "  Build to a temp file and diff it first:\n"
            "    uv run scripts/branding/distill.py build <specs...> -o /tmp/scenes.js\n"
            "  Then, deliberately, with every scene's spec on the command line: --force"
        )


# ---------------------------------------------------------------------------
# Subcommands
# ---------------------------------------------------------------------------


def cmd_steps(args: argparse.Namespace) -> int:
    tr = load_transcript(Path(args.transcript).expanduser(), args.include_sidechains)
    if args.json:
        print(json.dumps(
            [
                {"index": c.index, "role": c.role, "tag": c.tag, "name": c.name, "chars": c.size()}
                for c in tr.candidates
            ],
            indent=2,
        ))
        return 0
    print(f"# {tr.path}  session {tr.session_id or '(unknown)'}  {len(tr.candidates)} steps")
    for cand in tr.candidates:
        marker = cand.tag or cand.name or ""
        print(f"{cand.index:4d}  {cand.role:<6}  {marker:<16}  {cand.size():6d}  {cand.preview()}")
    return 0


def cmd_scaffold(args: argparse.Namespace) -> int:
    panes = []
    for entry in args.pane:
        key, sep, path = entry.partition("=")
        if not sep or not key or not path:
            raise DistillError(f"--pane wants key=path, got {entry!r}")
        tr = load_transcript(Path(path).expanduser())
        panes.append({
            "key": key,
            "label": DEFAULT_LABELS.get(key, key),
            "source": tr.session_id,
            "outcome": f"{TODO_MARKER} one sentence -- what this session actually shipped",
            "transcript": str(Path(path).expanduser().resolve()),
            "keep": [f"0-{len(tr.candidates) - 1}"],
            "ffwd": [],
            "clip": {},
            "focus": {},
            "result": {},
            "label_arg": {},
        })
    spec = {
        "scene": {
            "id": args.id,
            "kicker": args.kicker or args.id.replace("-", " "),
            "tab": args.tab or args.id.replace("-", " "),
            "title": args.title,
            "prompt": args.prompt,
            "layout": args.layout,
            "caption": args.caption or DEFAULT_CAPTION,
        },
        "panes": panes,
    }
    text = json.dumps(spec, indent=2, ensure_ascii=False) + "\n"
    if args.output:
        Path(args.output).write_text(text, encoding="utf-8")
        print(f"wrote {args.output}", file=sys.stderr)
        print(
            "Next: `steps` each transcript, then trim `keep`, add `ffwd` spans, and "
            f"replace every {TODO_MARKER} before building.",
            file=sys.stderr,
        )
    else:
        sys.stdout.write(text)
    return 0


def load_spec(path: Path) -> dict[str, Any]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as err:
        raise DistillError(f"spec not found: {path}") from err
    except json.JSONDecodeError as err:
        raise DistillError(f"{path}: not JSON: {err}") from err


def cmd_build(args: argparse.Namespace) -> int:
    out = Path(args.output).expanduser() if args.output else None
    if out is not None:
        guard_output(out, args.force)

    scenes = []
    for spec_path in args.spec:
        path = Path(spec_path).expanduser()
        scene, transcripts = build_scene(load_spec(path), path)
        audit_scene(scene, transcripts)
        scenes.append(scene)
        shape = ", ".join("{}={} steps".format(p["key"], len(p["steps"])) for p in scene["panes"])
        print(f"distill: {path.name}: scene {scene['id']} ({shape}) audited verbatim", file=sys.stderr)

    ids = [s["id"] for s in scenes]
    if len(set(ids)) != len(ids):
        raise DistillError(f"duplicate scene ids: {sorted(ids)}")

    header = GENERATED_HEADER
    if args.header:
        header, _ = split_scenes_js(Path(args.header).expanduser().read_text(encoding="utf-8"), args.header)

    text = json.dumps(scenes, indent=2, ensure_ascii=False) + "\n" if args.format == "json" else render(scenes, header)
    if out is None:
        sys.stdout.write(text)
    else:
        out.write_text(text, encoding="utf-8")
        print(f"distill: wrote {out}", file=sys.stderr)
    return 0


def cmd_verify(args: argparse.Namespace) -> int:
    _, scenes = split_scenes_js(Path(args.scenes).expanduser().read_text(encoding="utf-8"), args.scenes)
    by_id = {s.get("id"): s for s in scenes}
    checked = 0
    for spec_path in args.spec:
        path = Path(spec_path).expanduser()
        spec = load_spec(path)
        scene_id = (spec.get("scene") or {}).get("id")
        published = by_id.get(scene_id)
        if published is None:
            raise DistillError(f"{args.scenes}: no scene {scene_id!r} (spec {path})")
        transcripts = {}
        for pane in spec.get("panes") or []:
            if not pane.get("key") or not pane.get("transcript"):
                raise DistillError(f"{path}: every pane needs key and transcript to verify")
            transcripts[str(pane["key"])] = load_transcript(
                Path(pane["transcript"]).expanduser(), bool(pane.get("include_sidechains"))
            )
        audit_scene(published, transcripts)
        checked += 1
        print(f"distill: {args.scenes}: scene {scene_id} verbatim against {len(transcripts)} take(s)")
    if not checked:
        raise DistillError("nothing verified: pass at least one spec")
    return 0


# ---------------------------------------------------------------------------
# selftest -- the committed proof that the verbatim rule bites
# ---------------------------------------------------------------------------

GO_STRUCTS = {"Scene": SCENE_FIELDS, "ScenePane": PANE_FIELDS, "SceneStep": STEP_FIELDS, "SceneFile": FILE_FIELDS}


def check_docsgen_mirror(repo_root: Path) -> str:
    """Re-derive the scene schema from cmd/docsgen/scenes.go and fail on drift."""
    go = repo_root / "cmd" / "docsgen" / "scenes.go"
    if not go.is_file():
        return f"skipped (no {go})"
    source = go.read_text(encoding="utf-8")
    for name, expected in GO_STRUCTS.items():
        block = re.search(rf"type {name} struct \{{(.*?)\n\}}", source, re.S)
        if block is None:
            raise DistillError(f"{go}: no `type {name} struct` -- the scene schema moved")
        tags = re.findall(r'json:"([a-zA-Z_]+)"', block.group(1))
        if tags != list(expected):
            raise DistillError(
                f"{go}: {name} json tags are {tags}, this tool mirrors {list(expected)}. "
                "Update the constants at the top of distill.py in the same change."
            )
    return f"mirrors {go}"


def _row(**kw: Any) -> str:
    return json.dumps(kw)


SELFTEST_PROMPT = "continue where we left off"
SELFTEST_REPLY = "I'll check the plan.\n\nThe claimable step is **\"Rate-limit POST /auth/refresh\"** -- let me read it.\n\nMore prose that the scene does not need."
SELFTEST_BRIEFING = "<seam-briefing>\nSeam project: myapp -- 7 memories (2 constraints).\nPLAN: auth-refresh -- 4/6 done, 1 claimable\n</seam-briefing>"
SELFTEST_RESULT = "ready: Rate-limit POST /auth/refresh (per-IP and per-family), plus a long tail of JSON nobody needs to read on a landing page"


def write_selftest_transcript(path: Path) -> None:
    rows = [
        _row(type="attachment", sessionId="deadbeef", attachment={"type": "hook_additional_context", "content": [SELFTEST_BRIEFING]}),
        _row(type="user", sessionId="deadbeef", message={"role": "user", "content": SELFTEST_PROMPT}),
        _row(type="assistant", sessionId="deadbeef", message={"role": "assistant", "content": [
            {"type": "thinking", "thinking": "never shown"},
            {"type": "text", "text": SELFTEST_REPLY},
            {"type": "tool_use", "id": "toolu_1", "name": "tasks_ready", "input": {"plan": "auth-refresh"}},
        ]}),
        _row(type="user", sessionId="deadbeef", message={"role": "user", "content": [
            {"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": SELFTEST_RESULT}]},
        ]}),
        _row(type="assistant", sessionId="deadbeef", isSidechain=True, message={"role": "assistant", "content": [
            {"type": "text", "text": "subagent chatter that must not become a step"},
        ]}),
        _row(type="assistant", sessionId="deadbeef", message={"role": "assistant", "content": [
            {"type": "tool_use", "id": "toolu_2", "name": "Edit", "input": {"file_path": "/tmp/myapp/auth.go"}},
        ]}),
        _row(type="assistant", sessionId="deadbeef", message={"role": "assistant", "content": [
            {"type": "text", "text": "Done. The limiter is shared-storage-first."},
        ]}),
    ]
    path.write_text("\n".join(rows) + "\n", encoding="utf-8")


def cmd_selftest(args: argparse.Namespace) -> int:
    repo_root = Path(__file__).resolve().parents[2]
    checks: list[str] = []
    checks.append(f"docsgen schema mirror: {check_docsgen_mirror(repo_root)}")

    with tempfile.TemporaryDirectory() as tmp:
        take = Path(tmp) / "take.jsonl"
        write_selftest_transcript(take)
        tr = load_transcript(take)
        roles = [c.role for c in tr.candidates]
        assert roles == ["inject", "user", "agent", "tool", "tool", "agent"], roles
        assert all("subagent chatter" not in c.text for c in tr.candidates), "sidechain leaked"
        assert all("never shown" not in c.text for c in tr.candidates), "thinking leaked"
        checks.append(f"transcript walk: {len(tr.candidates)} steps, thinking + sidechain dropped")

        spec = {
            "scene": {"id": "selftest", "title": "Selftest", "prompt": SELFTEST_PROMPT, "layout": "with-without"},
            "panes": [{
                "key": "with",
                "outcome": "It works.",
                "transcript": str(take),
                "keep": [0, 1, 2, 3, 5],
                "ffwd": [{"span": 4}],
                "clip": {"2": {"paragraphs": 2}},
                "focus": {"0": ["PLAN: auth-refresh -- 4/6 done, 1 claimable"]},
                "result": {"3": {"chars": 60, "ellipsis": True}},
                "label_arg": {},
                "epilogue": [{"role": "comment", "text": "# illustrative, like the hero"}],
            }],
        }
        scene, transcripts = build_scene(spec, Path("selftest"))
        audit_scene(scene, transcripts)
        steps = scene["panes"][0]["steps"]
        assert [s["role"] for s in steps] == ["inject", "user", "agent", "tool", "ffwd", "agent", "comment"], steps
        assert steps[1]["text"] == SELFTEST_PROMPT
        assert SELFTEST_REPLY.startswith(steps[2]["text"]), "clip is not a prefix"
        assert steps[2]["text"] != SELFTEST_REPLY, "clip kept everything"
        assert steps[3]["result"].endswith(ELLIPSIS) and SELFTEST_RESULT.startswith(
            steps[3]["result"][: -len(ELLIPSIS)]
        ), "excerpt is not a prefix"
        checks.append("build: curated 6 steps -> 7 (1 ffwd span, 1 epilogue), clips are prefixes")

        text = render([scene], GENERATED_HEADER)
        header, parsed = split_scenes_js(text, "<selftest>")
        assert parsed[0]["id"] == "selftest" and header.startswith("// scenes.js")
        checks.append("emit: re-parses the way cmd/docsgen/scenes.go does")

        # The rule must BITE. Each of these is a realistic "improvement".
        tampers = {
            "re-punctuated an agent line": lambda s: s["panes"][0]["steps"][2].__setitem__(
                "text", str(s["panes"][0]["steps"][2]["text"]).replace("--", "—")
            ),
            "paraphrased the prompt": lambda s: s["panes"][0]["steps"][1].__setitem__(
                "text", "pick up where we left off"
            ),
            "spliced two non-adjacent sentences": lambda s: s["panes"][0]["steps"][2].__setitem__(
                "text", "I'll check the plan. Done. The limiter is shared-storage-first."
            ),
            "invented a tool result": lambda s: s["panes"][0]["steps"][3].__setitem__(
                "result", "SameSite: Lax -> Strict"
            ),
            "invented a tool": lambda s: s["panes"][0]["steps"][3].__setitem__("label", "Bash rm -rf /"),
            "focus line not in the injection": lambda s: s["panes"][0]["steps"][0].__setitem__(
                "focus", ["PLAN: auth-refresh -- 5/6 done"]
            ),
        }
        for name, tamper in tampers.items():
            victim = json.loads(json.dumps(scene))
            tamper(victim)
            try:
                audit_scene(victim, transcripts)
            except VerbatimError:
                continue
            raise DistillError(f"selftest FAILED: the audit accepted a tampered scene ({name})")
        checks.append(f"audit: rejected all {len(tampers)} tampers ({', '.join(tampers)})")

        # Authored prose may not enter through a verbatim role.
        try:
            check_epilogue([{"role": "agent", "text": "typed, not recorded"}], "selftest")
        except DistillError:
            checks.append("epilogue: refuses verbatim roles")
        else:
            raise DistillError("selftest FAILED: the epilogue accepted a verbatim role")

        # The scene's headline prompt is presented as what was typed.
        invented = json.loads(json.dumps(spec))
        invented["scene"]["prompt"] = "a prompt nobody typed"
        try:
            build_scene(invented, Path("selftest"))
        except VerbatimError:
            checks.append("scene prompt: refuses a prompt that is in no take")
        else:
            raise DistillError("selftest FAILED: the build accepted an invented scene prompt")

    for line in checks:
        print(f"ok  {line}")
    print("selftest passed")
    return 0


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="distill.py",
        description="Claude Code transcript -> docs/static/scenes.js scene data (verbatim).",
        epilog="Recipe: scripts/branding/README.md",
    )
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("steps", help="list a transcript's curatable steps, numbered")
    p.add_argument("transcript")
    p.add_argument("--include-sidechains", action="store_true", help="also list subagent rows")
    p.add_argument("--json", action="store_true", help="machine-readable listing")
    p.set_defaults(func=cmd_steps)

    p = sub.add_parser("scaffold", help="write a starter scene spec that keeps every step")
    p.add_argument("--id", required=True)
    p.add_argument("--title", required=True)
    p.add_argument("--prompt", required=True, help="the prompt as typed, verbatim")
    p.add_argument("--kicker")
    p.add_argument("--tab")
    p.add_argument("--caption")
    p.add_argument("--layout", default="with-without", choices=list(LAYOUTS))
    p.add_argument("--pane", action="append", required=True, metavar="KEY=TRANSCRIPT")
    p.add_argument("-o", "--output")
    p.set_defaults(func=cmd_scaffold)

    p = sub.add_parser("build", help="scene spec(s) -> scene data, audited verbatim")
    p.add_argument("spec", nargs="+")
    p.add_argument("-o", "--output", help="default: stdout")
    p.add_argument("--format", default="js", choices=["js", "json"])
    p.add_argument("--header", metavar="SCENES_JS", help="reuse this file's header comment")
    p.add_argument("--force", action="store_true", help="allow writing docs/static/scenes.js")
    p.set_defaults(func=cmd_build)

    p = sub.add_parser("verify", help="re-audit an existing scenes.js against its takes")
    p.add_argument("spec", nargs="+")
    p.add_argument("--scenes", default="docs/static/scenes.js")
    p.set_defaults(func=cmd_verify)

    p = sub.add_parser("selftest", help="prove the verbatim rule and the docsgen mirror still bite")
    p.set_defaults(func=cmd_selftest)

    args = parser.parse_args(argv)
    try:
        return int(args.func(args))
    except DistillError as err:
        print(f"distill: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
