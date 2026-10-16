# Shared agent notebook: design starting points

Status: discussion notes, 2026-10-16. This document records the motivation,
constraints, and a candidate direction for later design. It does not specify an
implemented feature or finalize the API, schema, or storage layout.

Implementation follow-up: the first version is now available in the repository.
See [notebook.md](notebook.md) for its actual API, limits, setup and recovery
contract. The discussion below remains the original motivation.

## Problem and intended use

Agents repeatedly ask for facts already established in earlier conversations:
the workshop printer's address, the machine and path where the slicer is
installed, the working profile, or a verified invocation. These facts often
apply across projects and should survive a new conversation or a change of
agent.

The intended notebook is maintained primarily by agents. The owner should be
able to inspect and correct it, but should not have to organize a personal wiki
or explicitly request every useful write.

An example set of records might be:

- `workshop/3d-printer`: address, access method, relevant device details, and a
  reference to the slicer record.
- `tools/slicer`: host, installation path, working command, and profile location.
- A reusable printing procedure linked to those records and, where relevant,
  the Kanboard task in which it was established.

These keys are illustrative; there are no actual printer credentials or
configuration values in this proposal.

## Relationship to Kanboard

Kanboard already owns project work: tasks, current status, remaining scope,
blockers, acceptance, verification, and the event history in comments. The
notebook should complement that workflow rather than create another task list.

| Information | Authoritative home |
| --- | --- |
| A defect to fix or an unfinished migration | Kanboard |
| What remains to finish or verify a task | Kanboard |
| Device addresses, tool locations, environment facts | Notebook |
| Reusable procedures, findings, preferences, and decision rationale | Notebook, or existing repository documentation when appropriate |
| Current project priorities and task dependencies | Kanboard |

Link records and tasks where useful. A closed task can supply the evidence for
a reusable lesson without copying its entire progress log into the notebook.
An old note about work in progress must not override the current card. Existing
repository documentation should remain authoritative for documented project
contracts; the notebook can point to it.

## Why consider a small implementation in Migi

Migi already provides a self-hosted Go server, SQLite integration, authenticated
remote agent access with revocable credentials, and a web administration panel.
A notebook could reuse these operational foundations without another separately
deployed service and credential setup. External agents on different machines
would access the same records through a server API.

The built-in Python agent and the reading-document feature for glasses are
experimental and, according to the owner, have not been especially successful.
They should not influence this design or become dependencies. Notebook utility
must stand on external agent access and ordinary server/web functionality.

A candidate implementation is a separate notebook module within the Go server,
with its own persistence boundary. Whether it shares the existing SQLite file
or uses a separate one remains a design choice. Keep it separable enough to
export or move out of Migi later.

The shared temporary file inbox has expiry semantics unsuitable for permanent
knowledge. Notebook writes also should not publish phone notifications or
depend on the delivery journal, device acknowledgements, or an active phone.

The principal operational tradeoff is shared availability: restarting or
breaking Migi also makes its notebook unavailable. For this personal system,
that appears acceptable, subject to explicit backup and export support.

## Small initial scope

Candidate record fields:

- Stable key, title, plain text or Markdown body, and a small set of tags.
- Creation/update timestamps and writer identity.
- Revision number for conditional updates.
- Source or verification information, initially expressible in the body.

Candidate operations are search/list, read by key, create, update, and delete.
Search should return bounded summaries, with full records fetched separately.
Start with text search; evaluate SQLite full-text search against actual builds
and representative Russian text, addresses, and paths before committing to an
index implementation.

Updates should detect a stale revision instead of silently overwriting another
agent's changes. Deletion, retry behavior, and recovery need explicit semantics.
A small web view/editor and a portable export are useful parts of the first
usable version. A CLI or thin MCP adapter can expose the same API to agents;
choose the initial interface during design rather than build every interface
at once.

No initial requirement for embeddings, a graph database, background LLM calls,
automatic transcript ingestion, session summaries, autonomous consolidation,
task management, or a native Android editor.

## Agent behavior

The working agent already has the conversation and can decide what is reusable.
The storage service does not need another model to make that decision.

- Save durable environment facts and verified procedures that would otherwise
  need to be explained or discovered again.
- Search before creating a record; update an existing subject under its stable
  key when its facts change.
- Preserve exact paths, addresses, and commands. Distinguish user-provided facts,
  successful verification, and hypotheses; do not promote a suggestion into a
  confirmed fact.
- Retrieve when the task needs prior knowledge, without an unconditional ritual
  of loading all memory at every turn. Experience with Kanboard suggests that
  agents already follow lookup instructions readily, sometimes excessively.
- Keep routine tool output, repeated connection reports, and task progress out
  of durable knowledge unless they establish a reusable finding.

## Existing projects considered

[Engram](https://github.com/Gentleman-Programming/engram) demonstrates a small
SQLite-backed agent memory service and stable topic keys for evolving records.
Its standard memory protocol also asks agents to save completed work and session
summaries with next steps, which overlaps with our Kanboard workflow.

[Basic Memory](https://github.com/basicmachines-co/basic-memory) provides
agent-editable notes, search, and relationships. Its optional
[memory-tasks skill](https://github.com/basicmachines-co/basic-memory/blob/main/skills/memory-tasks/SKILL.md)
introduces task tracking that would duplicate our board. Both projects have
broader lifecycle and organizational features than the motivating printer case
requires. These observations come from documentation review, not a local trial;
they motivate evaluating a smaller implementation, not a general verdict on
either project.

## Questions for the design pass

1. What API and agent interface fit the existing Migi clients best?
2. What key/tag conventions support shared knowledge without enforcing one
   namespace per project? How are records listed and renamed?
3. Which credentials may read or modify the notebook? Existing agent token
   records do not currently define notebook-specific permissions; reuse of
   authentication must not silently decide the intended access policy.
4. How are conditional writes, duplicate creates, retries, deletion, and limited
   revision history handled?
5. What search behavior is sufficient for Russian prose and exact technical
   identifiers, and what are the response and record size limits?
6. How are data backed up, exported, restored, and migrated independently of
   temporary files and delivery history?

A useful first acceptance scenario is the original one: one external agent
saves the printer and slicer facts; another session or agent finds and uses
them without asking again; an address change updates the same record; a stale
concurrent write is rejected. None of this should require the built-in agent,
glasses, a connected phone, or a duplicate Kanboard task.
