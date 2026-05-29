# sg

`sg` is a tiny Codex-first session search tool written in Go with only the
standard library. It discovers Codex rollout files, normalizes common message
records, and searches them directly.

It currently reads:

- `~/.codex/sessions/**/rollout-*.jsonl`
- modern Codex envelope records: `session_meta`, `response_item`, `event_msg`
- user/assistant messages, agent reasoning, tool calls, and tool output

## Usage

```sh
go run . search "authentication error"
go run . search --workspace /Users/me/work "authentication error"
go run . search --json --limit 5 "tool_call"
go run . sessions
go run . show ~/.codex/sessions/2026/05/10/rollout-example.jsonl
```

With no explicit command, arguments are treated as a search query:

```sh
go run . "bd-2mb03"
```

Use `--home` to point at a fixture or non-default Codex home:

```sh
go run . search --home /path/to/.codex "query"
```

For search, `--home`, `--workspace` (or `--cwd`), `--limit`, and `--json` can
appear before or after query terms, so `sg search "query" --json --limit 5` is
also valid.

Use `--workspace` to search only sessions whose recorded Codex cwd contains a
directory fragment:

```sh
go run . search "query" --workspace work/sg
```

## Sessions Output

`sg sessions` and `sg search` write readable label/value records by default.
Each non-empty line is:

```text
label:<TAB>content
```

Records are separated by a blank line:

```text
first_at:	2026-05-10T19:21:16+02:00
last_at:	2026-05-10T20:07:12+02:00
resume_token:	019e12de-6481-7e91-ad6e-9d274d51615c
jsonl_label:	rollout-2026-05-10T19-10-25-019e12de-6481-7e91-ad6e-9d274d51615c
workspace:	/Users/me/work
source_path:	/Users/me/.codex/sessions/2026/05/10/rollout-....jsonl
first_role:	user
first_message:	implement the parser
last_role:	assistant
last_message:	done
```

Search records use the same layout with match-specific fields:

```text
match_at:	2026-05-10T19:22:48+02:00
resume_token:	019e12de-6481-7e91-ad6e-9d274d51615c
jsonl_label:	rollout-2026-05-10T19-10-25-019e12de-6481-7e91-ad6e-9d274d51615c
workspace:	/Users/me/work
source_path:	/Users/me/.codex/sessions/2026/05/10/rollout-....jsonl
match_role:	assistant
match_message:	the matching message preview
```

The `resume_token` field is the value accepted by `codex resume`:

```sh
codex resume "$(sg sessions --limit 1 | awk -F '\t' '$1 == "resume_token:" {print $2; exit}')"
```

Use `--json` when structured output is more convenient.

## Disclaimer

This is 100% vibe coded with Codex. It was inspired by
[`coding_agent_session_search`](https://github.com/Dicklesworthstone/coding_agent_session_search),
with the goal of creating a minimal, dependency-free version focused only on
Codex sessions.
