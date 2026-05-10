package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseCodexModernJSONL verifies the modern Codex envelope parser.
func TestParseCodexModernJSONL(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-modern.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"modern-id","cwd":"/data/projects/ntm","cli_version":"0.49.0"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"investigate cass"}]}}
{"timestamp":"2026-05-08T23:09:02.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git log --grep='bd-2mb03'\"}","call_id":"call-modern-1"}}
{"timestamp":"2026-05-08T23:09:03.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-modern-1","output":"Output:\ncommit abc123 bd-2mb03.6.5 add CASS-backed handoff context enrichment\n"}}
{"timestamp":"2026-05-08T23:09:04.000Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"Let me think about bd-2mb03."}}
{"timestamp":"2026-05-08T23:09:05.000Z","type":"event_msg","payload":{"type":"agent_message","message":"The raw session contains bd-2mb03."}}
{"timestamp":"2026-05-08T23:09:05.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The raw session contains bd-2mb03."}],"phase":"commentary"}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	conversation, err := ParseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if conversation.Agent != "codex" {
		t.Fatalf("agent = %q", conversation.Agent)
	}
	if conversation.Workspace != "/data/projects/ntm" {
		t.Fatalf("workspace = %q", conversation.Workspace)
	}
	if conversation.ResumeToken != "modern-id" {
		t.Fatalf("resume token = %q", conversation.ResumeToken)
	}
	if conversation.Title != "investigate cass" {
		t.Fatalf("title = %q", conversation.Title)
	}
	if len(conversation.Messages) != 5 {
		t.Fatalf("message count = %d", len(conversation.Messages))
	}
	if !strings.Contains(
		conversation.Messages[1].Content, "git log --grep='bd-2mb03'",
	) {

		t.Fatalf(
			"function call arguments were not indexed: %q",
			conversation.Messages[1].Content,
		)
	}
	if conversation.Messages[3].Author != "reasoning" {
		t.Fatalf(
			"reasoning author = %q",
			conversation.Messages[3].Author,
		)
	}
	if got := strings.Count(
		joinMessageContent(conversation.Messages),
		"The raw session contains bd-2mb03.",
	); got != 1 {

		t.Fatalf(
			"duplicate assistant event was not collapsed, count "+
				"= %d", got,
		)
	}
}

// TestParseCodexSummaryReadsResumeToken verifies the fast sessions parser.
func TestParseCodexSummaryReadsResumeToken(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-summary.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"019de495-ffb4-7263-aced-d83cadfbd0e4","cwd":"/work/summary"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n<cwd>/work/summary</cwd>\n</environment_context>"}]}}
{"timestamp":"2026-05-08T23:09:02.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"resume-token title"}]}}
{"timestamp":"2026-05-08T23:09:03.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"slow","output":"this line should not be needed for summary"}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	conversation, err := ParseCodexSummary(path, 1770000000000)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.ResumeToken != "019de495-ffb4-7263-aced-d83cadfbd0e4" {
		t.Fatalf("resume token = %q", conversation.ResumeToken)
	}
	if conversation.Workspace != "/work/summary" {
		t.Fatalf("workspace = %q", conversation.Workspace)
	}
	if conversation.Title != "resume-token title" {
		t.Fatalf("title = %q", conversation.Title)
	}
	if conversation.SourceLabel != "rollout-summary" {
		t.Fatalf("source label = %q", conversation.SourceLabel)
	}
	if conversation.FirstMessage.Content != "resume-token title" {
		t.Fatalf("first message = %#v", conversation.FirstMessage)
	}
	if !strings.Contains(
		conversation.LastMessage.Content,
		"this line should not be needed",
	) {

		t.Fatalf("last message = %#v", conversation.LastMessage)
	}
	if len(conversation.Messages) != 0 {
		t.Fatalf(
			"summary should not populate messages, got %d",
			len(conversation.Messages),
		)
	}
}

// TestRunSessionsPrintsKV verifies default labeled output is readable and
// scriptable.
func TestRunSessionsPrintsKV(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-simple.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"019de495-ffb4-7263-aced-d83cadfbd0e4","cwd":"/work"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello sessions"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{"sessions", "--home", dir, "--limit", "1"}, &out,
		&strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 10 {
		t.Fatalf("output = %q", out.String())
	}
	want := map[string]string{
		"resume_token":  "019de495-ffb4-7263-aced-d83cadfbd0e4",
		"jsonl_label":   "rollout-simple",
		"workspace":     "/work",
		"first_role":    "user",
		"first_message": "hello sessions",
		"last_role":     "user",
		"last_message":  "hello sessions",
	}
	got := labelValueMap(t, lines)
	for label, value := range want {
		if got[label] != value {
			t.Fatalf(
				"%s = %q, want %q; output=%q", label,
				got[label], value, out.String(),
			)
		}
	}
}

// TestRunSessionsJSONUsesSummaryShape verifies JSON matches the sessions
// fields.
func TestRunSessionsJSONUsesSummaryShape(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-simple.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"019de495-ffb4-7263-aced-d83cadfbd0e4","cwd":"/work"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello sessions"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{"sessions", "--home", dir, "--limit", "1", "--json"},
		&out, &strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		`"first_at"`,
		`"last_at"`,
		`"resume_token"`,
		`"jsonl_label"`,
		`"first_message"`,
		`"last_message"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in JSON: %s", want, text)
		}
	}
	for _, unwanted := range []string{
		`"external_id"`,
		`"started_at"`,
		`"ended_at"`,
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("unexpected %s in JSON: %s", unwanted, text)
		}
	}
}

// TestSearchFindsToolOutput verifies that tool output participates in search.
func TestSearchFindsToolOutput(t *testing.T) {
	conversations := []Conversation{{
		Agent:       "codex",
		ResumeToken: "resume-123",
		SourceLabel: "rollout-test",
		Title:       "debug history",
		SourcePath:  "/tmp/rollout-test.jsonl",
		Messages: []Message{
			{
				Index:   0,
				Role:    "user",
				Content: "please debug",
			},
			{
				Index:   1,
				Role:    "tool",
				Content: "[Tool output]\ncommit abc123 bd-2mb03.6.5",
			},
		},
	}}

	results := Search(conversations, "bd-2mb03", 10)
	if len(results) != 1 {
		t.Fatalf("result count = %d", len(results))
	}
	if results[0].MessageIndex != 1 {
		t.Fatalf("message index = %d", results[0].MessageIndex)
	}
	if results[0].ResumeToken != "resume-123" ||
		results[0].SourceLabel != "rollout-test" {

		t.Fatalf(
			"resume/source = %q / %q", results[0].ResumeToken,
			results[0].SourceLabel,
		)
	}
	if !strings.Contains(results[0].Snippet, "bd-2mb03") {
		t.Fatalf("snippet = %q", results[0].Snippet)
	}
}

// TestRunSearchPrintsKV verifies default search output uses labeled records.
func TestRunSearchPrintsKV(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-search.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"search-resume-token","cwd":"/work/search"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"find needle please"}]}}
{"timestamp":"2026-05-08T23:09:02.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"needle is here"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{"search", "--home", dir, "--limit", "1", "needle"},
		&out, &strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	got := labelValueMap(
		t,
		strings.Split(
			strings.TrimSpace(
				out.String(),
			),
			"\n",
		),
	)
	want := map[string]string{
		"resume_token":  "search-resume-token",
		"jsonl_label":   "rollout-search",
		"workspace":     "/work/search",
		"source_path":   path,
		"match_role":    "assistant",
		"match_message": "needle is here",
	}
	for label, value := range want {
		if got[label] != value {
			t.Fatalf(
				"%s = %q, want %q; output=%q", label,
				got[label], value, out.String(),
			)
		}
	}
	if got["match_at"] == "" {
		t.Fatalf("match_at missing in output=%q", out.String())
	}
	if _, ok := got["score"]; ok {
		t.Fatalf(
			"normal search output should hide score: %q",
			out.String(),
		)
	}
}

// TestRunSearchPrintsBestMatchLast keeps the most relevant normal result
// nearest the prompt.
func TestRunSearchPrintsBestMatchLast(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-search.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"search-resume-token","cwd":"/work/search"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"needle"}]}}
{"timestamp":"2026-05-08T23:09:02.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"needle needle needle"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{"search", "--home", dir, "--limit", "2", "needle"},
		&out, &strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	records := strings.Split(strings.TrimSpace(out.String()), "\n\n")
	if len(records) != 2 {
		t.Fatalf("output = %q", out.String())
	}
	first := labelValueMap(t, strings.Split(records[0], "\n"))
	last := labelValueMap(t, strings.Split(records[1], "\n"))
	if first["match_message"] != "needle" {
		t.Fatalf(
			"least relevant first = %q; output=%q",
			first["match_message"], out.String(),
		)
	}
	if last["match_message"] != "needle needle needle" {
		t.Fatalf(
			"most relevant last = %q; output=%q",
			last["match_message"], out.String(),
		)
	}
}

// TestRunSearchJSONUsesSummaryShape verifies search JSON matches search fields.
func TestRunSearchJSONUsesSummaryShape(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-search.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"search-resume-token","cwd":"/work/search"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"find needle please"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{
			"search", "--home", dir, "--limit", "1", "--json",
			"needle",
		},
		&out,
		&strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		`"match_at"`,
		`"resume_token"`,
		`"jsonl_label"`,
		`"workspace"`,
		`"source_path"`,
		`"match_role"`,
		`"match_message"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in JSON: %s", want, text)
		}
	}
	for _, unwanted := range []string{
		`"created_at"`,
		`"message_index"`,
		`"line"`,
		`"score"`,
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("unexpected %s in JSON: %s", unwanted, text)
		}
	}
}

// TestRunSearchAllowsJSONFlagAfterQuery verifies search flags work after query
// terms.
func TestRunSearchAllowsJSONFlagAfterQuery(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-search.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"search-resume-token","cwd":"/work/search"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"late json needle"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{
			"search", "needle", "--home", dir, "--limit", "1",
			"--json",
		},
		&out,
		&strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	text := strings.TrimSpace(out.String())
	if !strings.HasPrefix(text, "[") ||
		!strings.Contains(text, `"match_message"`) {

		t.Fatalf("expected JSON output, got %q", out.String())
	}
	if strings.Contains(text, "match_at:\t") {
		t.Fatalf(
			"flag after query produced labeled output instead "+
				"of JSON: %q", out.String(),
		)
	}
}

// TestRunHelpIncludesDetails verifies the expanded command help covers common
// workflows.
func TestRunHelpIncludesDetails(t *testing.T) {
	var out strings.Builder
	if err := run(
		[]string{"--help"}, &out, &strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Commands:",
		"Flags:",
		"Text output:",
		"resume_token",
		"codex resume <token>",
		"sg search \"auth error\" --json --limit 5",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("help missing %q:\n%s", want, text)
		}
	}
}

// TestTitleSkipsEnvironmentContext keeps generated context out of session
// titles.
func TestTitleSkipsEnvironmentContext(t *testing.T) {
	title := titleFromMessages([]Message{
		{Role: "user", Content: "<environment_context>\n<cwd>/tmp</cwd>\n</environment_context>"},
		{Role: "user", Content: "<permissions instructions>\nFilesystem sandboxing...\n</permissions instructions>"},
		{Role: "user", Content: "recreate Codex session search partially"},
	})
	if title != "recreate Codex session search partially" {
		t.Fatalf("title = %q", title)
	}
}

// TestRunSearchDefaultsToQuery covers the bare-argument search shorthand.
func TestRunSearchDefaultsToQuery(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions", "2026", "05", "08")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-simple.jsonl")
	sample := `{"timestamp":"2026-05-08T23:09:00.000Z","type":"session_meta","payload":{"id":"simple-id","cwd":"/work"}}
{"timestamp":"2026-05-08T23:09:01.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello searchable world"}]}}
`
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	if err := run(
		[]string{"--home", dir, "searchable"}, &out, &strings.Builder{},
	); err != nil {

		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hello searchable world") {
		t.Fatalf("output = %q", out.String())
	}
}

// joinMessageContent concatenates message bodies for test assertions.
func joinMessageContent(messages []Message) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Content)
	}

	return strings.Join(parts, "\n")
}

// labelValueMap parses label-tab-value test output into a map.
func labelValueMap(t *testing.T, lines []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			t.Fatalf("line is not label-tab-value: %q", line)
		}
		label := strings.TrimSuffix(parts[0], ":")
		out[label] = parts[1]
	}

	return out
}
