// Command sg searches local coding-agent session history.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// agentCodex is the canonical agent slug emitted by this Codex-only
	// build.
	agentCodex = "codex"

	// maxIndexedToolOutputChars caps tool-output text stored in searchable
	// messages.
	maxIndexedToolOutputChars = 128 * 1024
)

// Conversation is the normalized session-level record produced from a Codex
// rollout file.
type Conversation struct {
	// Agent identifies the source agent, currently always "codex".
	Agent string `json:"agent"`

	// ResumeToken is the Codex session id accepted by `codex resume`.
	ResumeToken string `json:"resume_token,omitempty"`

	// SourceLabel is the rollout JSONL file name without its extension.
	SourceLabel string `json:"source_label,omitempty"`

	// Workspace is the cwd recorded by Codex session metadata.
	Workspace string `json:"workspace,omitempty"`

	// Title is a short human-readable label derived from the first useful
	// prompt.
	Title string `json:"title,omitempty"`

	// SourcePath is the absolute or caller-provided path to the rollout
	// file.
	SourcePath string `json:"source_path"`

	// StartedAt is the first parsed message timestamp in Unix milliseconds.
	StartedAt int64 `json:"started_at,omitempty"`

	// EndedAt is the last parsed message timestamp in Unix milliseconds.
	EndedAt int64 `json:"ended_at,omitempty"`

	// FirstMessage is the first useful message summary for tabular session
	// output.
	FirstMessage MessageSummary `json:"first_message,omitempty"`

	// LastMessage is the last useful message summary for tabular session
	// output.
	LastMessage MessageSummary `json:"last_message,omitempty"`

	// Messages contains the normalized searchable messages in chronological
	// order.
	Messages []Message `json:"messages,omitempty"`
}

// MessageSummary is the compact message representation used by session
// listings.
type MessageSummary struct {
	// Role is the normalized speaker role, such as user, assistant, tool,
	// or system.
	Role string `json:"role,omitempty"`

	// Author is an optional sub-role, currently used for assistant
	// reasoning.
	Author string `json:"author,omitempty"`

	// CreatedAt is the message timestamp in Unix milliseconds when
	// available.
	CreatedAt int64 `json:"created_at,omitempty"`

	// Content is a one-line preview of the message body.
	Content string `json:"content,omitempty"`
}

// SessionRecord is the public session-listing shape for both text and JSON
// output.
type SessionRecord struct {
	// FirstAt is the first useful message timestamp formatted as local
	// RFC3339.
	FirstAt string `json:"first_at"`

	// LastAt is the last useful message timestamp formatted as local
	// RFC3339.
	LastAt string `json:"last_at"`

	// ResumeToken is the Codex session id accepted by `codex resume`.
	ResumeToken string `json:"resume_token"`

	// JSONLLabel is the rollout JSONL file name without its extension.
	JSONLLabel string `json:"jsonl_label"`

	// Workspace is the cwd recorded by Codex session metadata.
	Workspace string `json:"workspace"`

	// SourcePath is the path to the rollout JSONL file.
	SourcePath string `json:"source_path"`

	// FirstRole is the normalized role for the first useful message.
	FirstRole string `json:"first_role"`

	// FirstMessage is a one-line preview of the first useful message.
	FirstMessage string `json:"first_message"`

	// LastRole is the normalized role for the last useful message.
	LastRole string `json:"last_role"`

	// LastMessage is a one-line preview of the last useful message.
	LastMessage string `json:"last_message"`
}

// Message is a normalized searchable message within a conversation.
type Message struct {
	// Index is the zero-based message index after sorting and
	// de-duplication.
	Index int `json:"index"`

	// Role is the normalized speaker role, such as user, assistant, tool,
	// or system.
	Role string `json:"role"`

	// Author is an optional sub-role, currently used for assistant
	// reasoning.
	Author string `json:"author,omitempty"`

	// CreatedAt is the message timestamp in Unix milliseconds when
	// available.
	CreatedAt int64 `json:"created_at,omitempty"`

	// Content is the searchable message body.
	Content string `json:"content"`

	// Line is the one-based line number in the source rollout file.
	Line int `json:"line,omitempty"`
}

// SearchResult is a single message-level hit returned by lexical search.
type SearchResult struct {
	// Agent identifies the source agent for the hit.
	Agent string `json:"agent"`

	// SourcePath is the rollout file containing the matching message.
	SourcePath string `json:"source_path"`

	// ResumeToken is the Codex session id accepted by `codex resume`.
	ResumeToken string `json:"resume_token,omitempty"`

	// SourceLabel is the rollout JSONL file name without its extension.
	SourceLabel string `json:"source_label,omitempty"`

	// Line is the one-based source line for the matching message.
	Line int `json:"line,omitempty"`

	// MessageIndex is the zero-based normalized message index.
	MessageIndex int `json:"message_index"`

	// Role is the normalized speaker role for the matching message.
	Role string `json:"role"`

	// Author is an optional sub-role for the matching message.
	Author string `json:"author,omitempty"`

	// Workspace is the conversation workspace recorded by Codex.
	Workspace string `json:"workspace,omitempty"`

	// Title is the conversation title associated with the hit.
	Title string `json:"title,omitempty"`

	// CreatedAt is the matching message timestamp in Unix milliseconds.
	CreatedAt int64 `json:"created_at,omitempty"`

	// Score is a simple term-frequency score used for result ordering.
	Score int `json:"score"`

	// Snippet is a compact excerpt centered near the first query term.
	Snippet string `json:"snippet"`
}

// SearchRecord is the public search-output shape for both text and JSON output.
type SearchRecord struct {
	// MatchAt is the matching message timestamp formatted as local RFC3339.
	MatchAt string `json:"match_at"`

	// ResumeToken is the Codex session id accepted by `codex resume`.
	ResumeToken string `json:"resume_token"`

	// JSONLLabel is the rollout JSONL file name without its extension.
	JSONLLabel string `json:"jsonl_label"`

	// Workspace is the cwd recorded by Codex session metadata.
	Workspace string `json:"workspace"`

	// SourcePath is the path to the rollout JSONL file.
	SourcePath string `json:"source_path"`

	// MatchRole is the normalized role for the matching message.
	MatchRole string `json:"match_role"`

	// MatchMessage is a one-line preview of the matching message.
	MatchMessage string `json:"match_message"`
}

// main runs the command-line entrypoint and reports fatal errors to stderr.
func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sg:", err)
		os.Exit(1)
	}
}

// run dispatches the top-level command, treating unknown commands as search
// text.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)

		return nil
	}

	switch args[0] {
	case "search":
		return runSearch(args[1:], stdout)

	case "sessions":
		return runSessions(args[1:], stdout)

	case "show", "view":
		return runShow(args[1:], stdout)

	case "help", "-h", "--help":
		printUsage(stdout)

		return nil

	default:
		return runSearch(args, stdout)
	}
}

// runSearch parses search flags, scans Codex sessions, and writes matching
// hits.
func runSearch(args []string, stdout io.Writer) error {
	home, limit, jsonOut, workspace, query, err := parseSearchArgs(args)
	if err != nil {
		return err
	}
	if query == "" {
		return errors.New("search query is required")
	}

	results, err := SearchCodex(home, query, limit, workspace)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(stdout, searchRecords(results))
	}

	return writeSearchKV(stdout, searchRecordsLeastRelevantFirst(results))
}

// parseSearchArgs parses search options while allowing flags before or after
// query terms.
func parseSearchArgs(args []string) (string, int, bool, string, string, error) {
	home := defaultCodexHome()
	limit := 10
	jsonOut := false
	workspace := ""
	queryParts := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			queryParts = append(queryParts, args[i+1:]...)
			i = len(args)

		case arg == "--json" || arg == "-json":
			jsonOut = true

		case arg == "--home" || arg == "-home":
			value, next, err := parseSearchFlagValue(args, i, arg)
			if err != nil {
				return "", 0, false, "", "", err
			}
			home = value
			i = next

		case strings.HasPrefix(arg, "--home=") ||
			strings.HasPrefix(arg, "-home="):

			home = valueAfterEquals(arg)

		case arg == "--limit" || arg == "-limit":
			value, next, err := parseSearchFlagValue(args, i, arg)
			if err != nil {
				return "", 0, false, "", "", err
			}
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return "", 0, false, "", "", fmt.Errorf("invalid "+
					"%s value %q", arg, value)
			}
			limit = parsed
			i = next

		case strings.HasPrefix(arg, "--limit=") ||
			strings.HasPrefix(arg, "-limit="):

			value := valueAfterEquals(arg)
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return "", 0, false, "", "", fmt.Errorf("invalid "+
					"limit value %q", value)
			}
			limit = parsed

		case arg == "--workspace" || arg == "-workspace" ||
			arg == "--cwd" || arg == "-cwd":

			value, next, err := parseSearchFlagValue(args, i, arg)
			if err != nil {
				return "", 0, false, "", "", err
			}
			workspace, err = normalizeWorkspaceFilter(value)
			if err != nil {
				return "", 0, false, "", "", err
			}
			i = next

		case strings.HasPrefix(arg, "--workspace=") ||
			strings.HasPrefix(arg, "-workspace=") ||
			strings.HasPrefix(arg, "--cwd=") ||
			strings.HasPrefix(arg, "-cwd="):

			var err error
			workspace, err = normalizeWorkspaceFilter(
				valueAfterEquals(arg),
			)
			if err != nil {
				return "", 0, false, "", "", err
			}

		case strings.HasPrefix(arg, "-"):
			return "", 0, false, "", "", fmt.Errorf("unknown "+
				"search flag %q", arg)

		default:
			queryParts = append(queryParts, arg)
		}
	}

	return home, limit, jsonOut, workspace, strings.TrimSpace(
		strings.Join(queryParts, " "),
	), nil
}

// normalizeWorkspaceFilter trims workspace filters while preserving partial
// path fragments such as a directory basename.
func normalizeWorkspaceFilter(workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", nil
	}

	return filepath.Clean(workspace), nil
}

// parseSearchFlagValue returns the next argument used as a value for a search
// flag.
func parseSearchFlagValue(args []string, index int, name string) (string, int,
	error) {

	next := index + 1
	if next >= len(args) {
		return "", index, fmt.Errorf("missing value for %s", name)
	}

	return args[next], next, nil
}

// valueAfterEquals returns the substring after the first equals sign in a flag.
func valueAfterEquals(arg string) string {
	_, value, _ := strings.Cut(arg, "=")

	return value
}

// runSessions lists recent parsed Codex conversations.
func runSessions(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	home := fs.String("home", defaultCodexHome(), "Codex home directory")
	limit := fs.Int("limit", 20, "maximum session count")
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	conversations, err := ScanCodexSummaries(*home, *limit)
	if err != nil {
		return err
	}
	sort.SliceStable(conversations, func(i, j int) bool {
		return conversations[i].EndedAt > conversations[j].EndedAt
	})

	if *jsonOut {
		return writeJSON(stdout, sessionRecords(conversations))
	}

	return writeSessionsKV(stdout, sessionRecords(conversations))
}

// runShow renders one rollout file as normalized conversation content.
func runShow(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "write JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("show requires a rollout file path")
	}

	conversation, err := ParseCodexFile(fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSON(stdout, conversation)
	}

	title := conversation.Title
	if title == "" {
		title = filepath.Base(conversation.SourcePath)
	}
	fmt.Fprintf(stdout, "%s\n%s\n\n", title, conversation.SourcePath)
	for _, message := range conversation.Messages {
		who := message.Role
		if message.Author != "" {
			who += "/" + message.Author
		}
		fmt.Fprintf(
			stdout, "[%d] %s\n%s\n\n", message.Index, who,
			message.Content,
		)
	}

	return nil
}

// printUsage writes the command help text.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, `sg is a tiny Codex-first session search tool.

Usage:
  sg search [--home ~/.codex] [--workspace DIR] [--limit 10] [--json] <query>
  sg sessions [--home ~/.codex] [--limit 20] [--json]
  sg show [--json] <rollout.jsonl>
  sg <query>

Commands:
  search      Search Codex rollout files for messages matching all query terms.
              Normal output prints the least relevant result first and best last.
  sessions    List recent Codex sessions without fully parsing every file.
  show        Print one rollout file as normalized conversation messages.

Flags:
  --home DIR  Codex home directory. Defaults to CODEX_HOME or ~/.codex.
  --workspace DIR
              Only search sessions whose recorded cwd contains DIR.
  --cwd DIR   Alias for --workspace.
  --limit N   Maximum results or sessions to print.
  --json      Write structured JSON instead of labeled text records.

Search flags may appear before or after query terms:
  sg search --json --limit 5 "auth error"
  sg search "auth error" --json --limit 5

Text output:
  Each non-empty line is "label:<TAB>content"; records are separated by a
  blank line. sessions prints first_at, last_at, resume_token, jsonl_label,
  workspace, source_path, first_role, first_message, last_role, last_message.
  search prints match_at, resume_token, jsonl_label, workspace, source_path,
  match_role, match_message.

Notes:
  resume_token is the value accepted by `+"`codex resume <token>`"+`.
  With no command, sg treats the arguments as a search query.`)
}

// writeSearchKV writes human-readable label-content records for search matches.
func writeSearchKV(w io.Writer, records []SearchRecord) error {
	for i, record := range records {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		for _, row := range searchKVRows(record) {
			if _, err := fmt.Fprintf(
				w, "%s:	%s\n", row[0], row[1],
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// searchRecords converts internal search results to the public search shape.
func searchRecords(results []SearchResult) []SearchRecord {
	records := make([]SearchRecord, 0, len(results))
	for _, result := range results {
		records = append(records, searchRecord(result))
	}

	return records
}

// searchRecordsLeastRelevantFirst returns normal-output records with best hits
// last.
func searchRecordsLeastRelevantFirst(results []SearchResult) []SearchRecord {
	records := make([]SearchRecord, 0, len(results))
	for i := len(results) - 1; i >= 0; i-- {
		records = append(records, searchRecord(results[i]))
	}

	return records
}

// searchRecord converts one internal hit to the public search shape.
func searchRecord(result SearchResult) SearchRecord {
	return SearchRecord{
		MatchAt:      recordCell(formatMillis(result.CreatedAt)),
		ResumeToken:  recordCell(result.ResumeToken),
		JSONLLabel:   recordCell(result.SourceLabel),
		Workspace:    recordCell(result.Workspace),
		SourcePath:   recordCell(result.SourcePath),
		MatchRole:    recordCell(searchResultRole(result)),
		MatchMessage: recordCell(result.Snippet),
	}
}

// searchKVRows returns stable label-value rows for one search match.
func searchKVRows(record SearchRecord) [][2]string {
	return [][2]string{
		{
			"match_at",
			record.MatchAt,
		},
		{
			"resume_token",
			record.ResumeToken,
		},
		{
			"jsonl_label",
			record.JSONLLabel,
		},
		{
			"workspace",
			record.Workspace,
		},
		{
			"source_path",
			record.SourcePath,
		},
		{
			"match_role",
			record.MatchRole,
		},
		{
			"match_message",
			record.MatchMessage,
		},
	}
}

// searchResultRole combines role and author for compact search output.
func searchResultRole(result SearchResult) string {
	if result.Author == "" {
		return result.Role
	}

	return result.Role + "/" + result.Author
}

// writeSessionsKV writes human-readable label-content records for sessions.
func writeSessionsKV(w io.Writer, records []SessionRecord) error {
	for i, record := range records {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		for _, row := range sessionKVRows(record) {
			if _, err := fmt.Fprintf(
				w, "%s:	%s\n", row[0], row[1],
			); err != nil {
				return err
			}
		}
	}

	return nil
}

// sessionRecords converts internal conversations to the public sessions shape.
func sessionRecords(conversations []Conversation) []SessionRecord {
	records := make([]SessionRecord, 0, len(conversations))
	for _, conversation := range conversations {
		records = append(records, sessionRecord(conversation))
	}

	return records
}

// sessionRecord converts one conversation summary to the public sessions shape.
func sessionRecord(conversation Conversation) SessionRecord {
	first := conversation.FirstMessage
	last := conversation.LastMessage

	return SessionRecord{
		FirstAt:      recordCell(formatMillis(first.CreatedAt)),
		LastAt:       recordCell(formatMillis(last.CreatedAt)),
		ResumeToken:  recordCell(conversation.ResumeToken),
		JSONLLabel:   recordCell(conversation.SourceLabel),
		Workspace:    recordCell(conversation.Workspace),
		SourcePath:   recordCell(conversation.SourcePath),
		FirstRole:    recordCell(messageSummaryRole(first)),
		FirstMessage: recordCell(first.Content),
		LastRole:     recordCell(messageSummaryRole(last)),
		LastMessage:  recordCell(last.Content),
	}
}

// sessionKVRows returns stable label-value rows for one session summary.
func sessionKVRows(record SessionRecord) [][2]string {
	return [][2]string{
		{
			"first_at",
			record.FirstAt,
		},
		{
			"last_at",
			record.LastAt,
		},
		{
			"resume_token",
			record.ResumeToken,
		},
		{
			"jsonl_label",
			record.JSONLLabel,
		},
		{
			"workspace",
			record.Workspace,
		},
		{
			"source_path",
			record.SourcePath,
		},
		{
			"first_role",
			record.FirstRole,
		},
		{
			"first_message",
			record.FirstMessage,
		},
		{
			"last_role",
			record.LastRole,
		},
		{
			"last_message",
			record.LastMessage,
		},
	}
}

// messageSummaryRole combines role and author for compact tabular output.
func messageSummaryRole(message MessageSummary) string {
	if message.Author == "" {
		return message.Role
	}

	return message.Role + "/" + message.Author
}

// recordCell removes record separators from a value while preserving readable
// text.
func recordCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")

	return strings.TrimSpace(value)
}

// writeJSON writes an indented JSON representation of v.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	return enc.Encode(v)
}

// defaultCodexHome returns CODEX_HOME or the default ~/.codex path.
func defaultCodexHome() string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".codex")
	}

	return ".codex"
}

// ScanCodex discovers and parses all usable Codex rollout files under home.
func ScanCodex(home string) ([]Conversation, error) {
	files, err := DiscoverCodexFiles(home)
	if err != nil {
		return nil, err
	}

	conversations := make([]Conversation, 0, len(files))
	for _, file := range files {
		conversation, err := ParseCodexFile(file)
		if err != nil {
			continue
		}
		if len(conversation.Messages) == 0 {
			continue
		}
		conversations = append(conversations, conversation)
	}

	sort.SliceStable(conversations, func(i, j int) bool {
		if conversations[i].StartedAt == conversations[j].StartedAt {
			return conversations[i].SourcePath < conversations[j].SourcePath
		}

		return conversations[i].StartedAt < conversations[j].StartedAt
	})

	return conversations, nil
}

// ScanCodexSummaries parses recent Codex rollout files for fast session
// listing.
func ScanCodexSummaries(home string, limit int) ([]Conversation, error) {
	files, err := DiscoverCodexFilesByModTime(home)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}

	conversations := make([]Conversation, 0, len(files))
	for _, file := range files {
		conversation, err := ParseCodexSummary(
			file.Path, file.ModTimeMs,
		)
		if err != nil {
			continue
		}
		conversations = append(conversations, conversation)
	}

	return conversations, nil
}

// DiscoverCodexFiles returns sorted rollout file paths under home/sessions.
func DiscoverCodexFiles(home string) ([]string, error) {
	sessions := filepath.Join(home, "sessions")
	if info, err := os.Stat(sessions); err != nil || !info.IsDir() {
		return nil, nil
	}

	var files []string
	err := filepath.WalkDir(
		sessions,
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if isCodexRolloutFile(path) {
				files = append(files, path)
			}

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	sort.Strings(files)

	return files, nil
}

// DiscoveredCodexFile is a rollout file with modification-time metadata.
type DiscoveredCodexFile struct {
	// Path is the rollout file path.
	Path string `json:"path"`

	// ModTimeMs is the file modification time in Unix milliseconds.
	ModTimeMs int64 `json:"mod_time_ms"`
}

// DiscoverCodexFilesByModTime returns rollout files sorted newest first.
func DiscoverCodexFilesByModTime(home string) ([]DiscoveredCodexFile, error) {
	sessions := filepath.Join(home, "sessions")
	if info, err := os.Stat(sessions); err != nil || !info.IsDir() {
		return nil, nil
	}

	var files []DiscoveredCodexFile
	err := filepath.WalkDir(
		sessions,
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() || !isCodexRolloutFile(path) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			files = append(files, DiscoveredCodexFile{
				Path:      path,
				ModTimeMs: info.ModTime().UnixMilli(),
			})

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(files, func(i, j int) bool {
		if files[i].ModTimeMs == files[j].ModTimeMs {
			return files[i].Path > files[j].Path
		}

		return files[i].ModTimeMs > files[j].ModTimeMs
	})

	return files, nil
}

// isCodexRolloutFile reports whether path names a supported Codex rollout file.
func isCodexRolloutFile(path string) bool {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, "rollout-") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))

	return ext == ".jsonl" || ext == ".json"
}

// sourceLabel returns the rollout JSONL basename without the file extension.
func sourceLabel(path string) string {
	name := filepath.Base(path)

	return strings.TrimSuffix(name, filepath.Ext(name))
}

// ParseCodexFile normalizes a single Codex rollout file into one conversation.
func ParseCodexFile(path string) (Conversation, error) {
	file, err := os.Open(path)
	if err != nil {
		return Conversation{}, err
	}
	defer file.Close()

	conversation := Conversation{
		Agent:       agentCodex,
		SourcePath:  path,
		SourceLabel: sourceLabel(path),
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		applyCodexRecord(&conversation, raw, lineNo)
	}
	if err := scanner.Err(); err != nil {
		return Conversation{}, err
	}

	sort.SliceStable(conversation.Messages, func(i, j int) bool {
		left, right := conversation.Messages[i], conversation.Messages[j]
		if left.CreatedAt == right.CreatedAt {
			return left.Line < right.Line
		}

		return left.CreatedAt < right.CreatedAt
	})
	conversation.Messages = dedupeMessages(conversation.Messages)
	for i := range conversation.Messages {
		conversation.Messages[i].Index = i
		if conversation.Messages[i].CreatedAt > 0 {
			if conversation.StartedAt == 0 ||
				conversation.Messages[i].CreatedAt < conversation.StartedAt {

				conversation.StartedAt = conversation.Messages[i].CreatedAt
			}
			if conversation.Messages[i].CreatedAt > conversation.EndedAt {
				conversation.EndedAt = conversation.Messages[i].CreatedAt
			}
		}
	}
	if len(conversation.Messages) > 0 {
		conversation.FirstMessage = messageToSummary(
			conversation.Messages[0],
		)
		conversation.LastMessage = messageToSummary(
			conversation.Messages[len(conversation.Messages)-1],
		)
	}
	if conversation.Title == "" {
		conversation.Title = titleFromMessages(conversation.Messages)
	}

	return conversation, nil
}

// ParseCodexSummary reads only enough of a rollout file for the sessions view.
func ParseCodexSummary(path string, modTimeMs int64) (Conversation, error) {
	file, err := os.Open(path)
	if err != nil {
		return Conversation{}, err
	}
	defer file.Close()

	conversation := Conversation{
		Agent:       agentCodex,
		SourcePath:  path,
		SourceLabel: sourceLabel(path),
		EndedAt:     modTimeMs,
	}

	if err := readCodexSummaryFront(file, &conversation); err != nil {
		return Conversation{}, err
	}
	if last, ok := readCodexSummaryTail(file); ok {
		conversation.LastMessage = last
		conversation.EndedAt = last.CreatedAt
	}
	if conversation.StartedAt == 0 {
		conversation.StartedAt = conversation.EndedAt
	}
	if conversation.FirstMessage.CreatedAt == 0 {
		conversation.FirstMessage.CreatedAt = conversation.StartedAt
	}
	if conversation.LastMessage.CreatedAt == 0 {
		conversation.LastMessage = conversation.FirstMessage
		conversation.EndedAt = conversation.FirstMessage.CreatedAt
	}

	return conversation, nil
}

// readCodexSummaryFront reads metadata and the first useful message from the
// file start.
func readCodexSummaryFront(file *os.File, conversation *Conversation) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		applyCodexSummaryRecord(conversation, raw, lineNo)
		if conversation.ResumeToken != "" &&
			conversation.Workspace != "" &&
			conversation.Title != "" &&
			conversation.FirstMessage.Content != "" {

			break
		}
	}

	return scanner.Err()
}

// readCodexSummaryTail reads backward chunks to find the last useful message
// quickly.
func readCodexSummaryTail(file *os.File) (MessageSummary, bool) {
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 {
		return MessageSummary{}, false
	}

	const chunkSize int64 = 64 * 1024
	const maxTailBytes int64 = 8 * 1024 * 1024
	var suffix string
	var readBytes int64
	for offset := info.Size(); offset > 0 && readBytes < maxTailBytes; {
		n := chunkSize
		if offset < n {
			n = offset
		}
		offset -= n
		readBytes += n
		buf := make([]byte, n)
		if _, err := file.ReadAt(buf, offset); err != nil &&
			!errors.Is(err, io.EOF) {
			return MessageSummary{}, false
		}
		suffix = string(buf) + suffix
		lines := strings.Split(suffix, "\n")
		start := len(lines) - 1
		if start >= 0 && strings.TrimSpace(lines[start]) == "" {
			start--
		}
		for i := start; i >= 0; i-- {
			if offset > 0 && i == 0 {
				continue
			}
			if message, ok := summaryMessageFromLine(
				lines[i], 0,
			); ok &&
				isUsefulLastSummaryMessage(message) {
				return message, true
			}
		}
	}

	return MessageSummary{}, false
}

// applyCodexRecord applies one decoded Codex JSONL envelope to a conversation.
func applyCodexRecord(conversation *Conversation, raw map[string]any,
	lineNo int) {

	entryType, _ := raw["type"].(string)
	payload, _ := raw["payload"].(map[string]any)
	createdAt := parseTimestamp(raw["timestamp"])

	switch entryType {
	case "session_meta":
		if id, ok := payload["id"].(string); ok && id != "" {
			conversation.ResumeToken = id
		}
		if cwd, ok := payload["cwd"].(string); ok && cwd != "" {
			conversation.Workspace = cwd
		}

	case "response_item":
		if msg, ok := responseItemMessage(
			payload, createdAt, lineNo,
		); ok {

			conversation.Messages = append(
				conversation.Messages, msg,
			)
		}

	case "event_msg":
		if msg, ok := eventMessage(payload, createdAt, lineNo); ok {
			conversation.Messages = append(
				conversation.Messages, msg,
			)
		}
	}
}

// applyCodexSummaryRecord extracts metadata and first-message candidates for
// sessions.
func applyCodexSummaryRecord(conversation *Conversation, raw map[string]any,
	lineNo int) {

	entryType, _ := raw["type"].(string)
	payload, _ := raw["payload"].(map[string]any)
	createdAt := parseTimestamp(raw["timestamp"])
	if createdAt > 0 && conversation.StartedAt == 0 {
		conversation.StartedAt = createdAt
	}

	switch entryType {
	case "session_meta":
		if id, ok := payload["id"].(string); ok && id != "" {
			conversation.ResumeToken = id
		}
		if cwd, ok := payload["cwd"].(string); ok && cwd != "" {
			conversation.Workspace = cwd
		}

	case "response_item":
		if conversation.FirstMessage.Content != "" {
			return
		}
		message, ok := responseItemMessage(payload, createdAt, lineNo)
		if !ok {
			return
		}
		applyFirstSummaryMessage(conversation, message)

	case "event_msg":
		if conversation.FirstMessage.Content != "" {
			return
		}
		message, ok := eventMessage(payload, createdAt, lineNo)
		if !ok {
			return
		}
		applyFirstSummaryMessage(conversation, message)
	}
}

// applyFirstSummaryMessage records the first useful message and title
// candidate.
func applyFirstSummaryMessage(conversation *Conversation, message Message) {
	summary := messageToSummary(message)
	if !isUsefulFirstSummaryMessage(summary) {
		return
	}
	conversation.FirstMessage = summary
	if conversation.Title == "" && isGoodTitleCandidate(summary.Content) {
		conversation.Title = truncateOneLine(summary.Content, 120)
	}
}

// summaryMessageFromLine decodes one JSONL line into a compact message summary.
func summaryMessageFromLine(line string, lineNo int) (MessageSummary, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return MessageSummary{}, false
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return MessageSummary{}, false
	}
	entryType, _ := raw["type"].(string)
	payload, _ := raw["payload"].(map[string]any)
	createdAt := parseTimestamp(raw["timestamp"])
	var message Message
	var ok bool
	switch entryType {
	case "response_item":
		message, ok = responseItemMessage(payload, createdAt, lineNo)

	case "event_msg":
		message, ok = eventMessage(payload, createdAt, lineNo)

	default:
		return MessageSummary{}, false
	}
	if !ok {
		return MessageSummary{}, false
	}

	return messageToSummary(message), true
}

// responseItemMessage normalizes a Codex response_item payload into a message.
func responseItemMessage(payload map[string]any, createdAt int64,
	lineNo int) (Message, bool) {

	itemType, _ := payload["type"].(string)
	switch itemType {
	case "", "message":
		content := flattenContent(payload["content"])
		if content == "" {
			return Message{}, false
		}
		role, _ := payload["role"].(string)
		if role == "" {
			role = "assistant"
		}

		return newMessage(role, "", createdAt, content, lineNo), true

	case "function_call":
		name, _ := payload["name"].(string)
		if name == "" {
			name = "unknown"
		}
		content := "[Tool: " + name + "]"
		if arguments := argumentText(
			payload["arguments"],
		); arguments != "" {

			content += "\n" + arguments
		}

		return newMessage("assistant", "", createdAt, content, lineNo), true

	case "function_call_output":
		output, _ := payload["output"].(string)
		if output == "" {
			return Message{}, false
		}
		callID, _ := payload["call_id"].(string)

		return newMessage(
			"tool", "", createdAt,
			toolOutputContent(callID, output), lineNo,
		), true

	default:
		return Message{}, false
	}
}

// eventMessage normalizes a Codex event_msg payload into a message when
// searchable.
func eventMessage(payload map[string]any, createdAt int64,
	lineNo int) (Message, bool) {

	eventType, _ := payload["type"].(string)
	switch eventType {
	case "agent_message":
		content := firstString(payload, "message", "text")
		if strings.TrimSpace(content) == "" {
			return Message{}, false
		}

		return newMessage(
			"assistant", "", createdAt, strings.TrimSpace(content),
			lineNo,
		), true

	case "agent_reasoning":
		content := firstString(payload, "text", "message")
		if strings.TrimSpace(content) == "" {
			return Message{}, false
		}

		return newMessage(
			"assistant", "reasoning", createdAt,
			strings.TrimSpace(content), lineNo,
		), true

	case "tool_result":
		output := firstString(payload, "output", "result")
		if strings.TrimSpace(output) == "" {
			return Message{}, false
		}
		callID := firstString(payload, "call_id", "id")

		return newMessage(
			"tool", "", createdAt,
			toolOutputContent(callID, output), lineNo,
		), true

	default:
		return Message{}, false
	}
}

// newMessage builds a normalized message with trimmed content and source line
// metadata.
func newMessage(role, author string, createdAt int64, content string,
	lineNo int) Message {

	return Message{
		Role:      normalizeRole(role),
		Author:    author,
		CreatedAt: createdAt,
		Content:   strings.TrimSpace(content),
		Line:      lineNo,
	}
}

// messageToSummary converts a full message into its compact session-listing
// form.
func messageToSummary(message Message) MessageSummary {
	return MessageSummary{
		Role:      message.Role,
		Author:    message.Author,
		CreatedAt: message.CreatedAt,
		Content:   truncateOneLine(message.Content, 240),
	}
}

// isUsefulFirstSummaryMessage reports whether a summary is useful as first
// context.
func isUsefulFirstSummaryMessage(message MessageSummary) bool {
	if strings.TrimSpace(message.Content) == "" {
		return false
	}

	return isGoodTitleCandidate(message.Content)
}

// isUsefulLastSummaryMessage reports whether a summary is useful as last
// context.
func isUsefulLastSummaryMessage(message MessageSummary) bool {
	return strings.TrimSpace(message.Content) != ""
}

// normalizeRole maps Codex and cass-style role names to sg's small role set.
func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return "user"

	case "assistant", "agent":
		return "assistant"

	case "tool":
		return "tool"

	case "system":
		return "system"

	default:
		if strings.TrimSpace(role) == "" {
			return "assistant"
		}

		return strings.TrimSpace(role)
	}
}

// flattenContent extracts searchable text from modern Codex content blocks.
func flattenContent(v any) string {
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(value)

	case []any:
		var parts []string
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				if text := strings.TrimSpace(
					typed,
				); text != "" {

					parts = append(parts, text)
				}

			case map[string]any:
				itemType, _ := typed["type"].(string)
				if itemType != "" && itemType != "text" &&
					itemType != "input_text" &&
					itemType != "output_text" {

					continue
				}
				if text, ok := typed["text"].(string); ok {
					if text = strings.TrimSpace(
						text,
					); text != "" {

						parts = append(parts, text)
					}
				}
			}
		}

		return strings.Join(parts, "\n")

	default:
		return ""
	}
}

// argumentText formats function-call arguments for search indexing.
func argumentText(v any) string {
	switch value := v.(type) {
	case nil:
		return ""

	case string:
		return strings.TrimSpace(value)

	default:
		b, err := json.Marshal(value)
		if err != nil {
			return ""
		}

		return string(b)
	}
}

// toolOutputContent formats a tool result with an optional call id label.
func toolOutputContent(callID, output string) string {
	label := "[Tool output]"
	if callID != "" {
		label = "[Tool output: " + callID + "]"
	}
	output = truncateToolOutput(strings.TrimSpace(output))
	if output == "" {
		return label
	}

	return label + "\n" + output
}

// truncateToolOutput bounds very large tool outputs while preserving searchable
// text.
func truncateToolOutput(output string) string {
	if len([]rune(output)) <= maxIndexedToolOutputChars {
		return output
	}
	runes := []rune(output)
	omitted := len(runes) - maxIndexedToolOutputChars

	return string(runes[:maxIndexedToolOutputChars]) + fmt.Sprintf(
		"\n[truncated %d additional chars from tool output]", omitted)
}

// firstString returns the first string value found for the provided keys.
func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok {
			return value
		}
	}

	return ""
}

// parseTimestamp converts known Codex timestamp shapes to Unix milliseconds.
func parseTimestamp(v any) int64 {
	switch value := v.(type) {
	case string:
		if value == "" {
			return 0
		}
		if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return t.UnixMilli()
		}

	case float64:
		if value > 0 {
			return int64(value)
		}
	}

	return 0
}

// titleFromMessages chooses the first useful message to represent a
// conversation.
func titleFromMessages(messages []Message) string {
	for _, message := range messages {
		if message.Role == "user" &&
			isGoodTitleCandidate(message.Content) {
			return truncateOneLine(message.Content, 120)
		}
	}
	for _, message := range messages {
		if isGoodTitleCandidate(message.Content) {
			return truncateOneLine(message.Content, 120)
		}
	}

	return ""
}

// isGoodTitleCandidate rejects scaffolding and tool-only text as session
// titles.
func isGoodTitleCandidate(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	lower := strings.ToLower(content)
	for _, prefix := range generatedTitlePrefixes() {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	if strings.HasPrefix(lower, "[tool output") ||
		strings.HasPrefix(lower, "[tool:") {
		return false
	}

	return true
}

// generatedTitlePrefixes returns Codex wrapper blocks that should not title
// sessions.
func generatedTitlePrefixes() []string {
	return []string{
		"<apps_instructions>",
		"<collaboration_mode>",
		"<environment_context>",
		"<permissions instructions>",
		"<plugins_instructions>",
		"<skills_instructions>",
	}
}

// dedupeMessages removes duplicate normalized messages while preserving order.
func dedupeMessages(messages []Message) []Message {
	seen := make(map[string]bool, len(messages))
	out := messages[:0]
	for _, message := range messages {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", message.Role,
			message.Author, message.CreatedAt, message.Content)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, message)
	}

	return out
}

// SearchCodex performs a streaming concurrent lexical search over Codex
// rollouts.
func SearchCodex(home, query string, limit int,
	workspaceFilter string) ([]SearchResult, error) {

	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}

	files, err := DiscoverCodexFiles(home)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	if workers > len(files) {
		workers = len(files)
	}

	jobs := make(chan string)
	results := make(chan []SearchResult, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []SearchResult
			for path := range jobs {
				local = append(
					local,
					SearchCodexFile(path, terms, workspaceFilter)...,
				)
			}
			results <- local
		}()
	}

	go func() {
		for _, file := range files {
			jobs <- file
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	var all []SearchResult
	for batch := range results {
		all = append(all, batch...)
	}
	sortSearchResults(all)
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

// SearchCodexFile streams one rollout file and returns matching message
// results.
func SearchCodexFile(path string, terms []string,
	workspaceFilter string) []SearchResult {

	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	state := codexSearchFileState{
		path:        path,
		sourceLabel: sourceLabel(path),
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	messageIndex := 0
	var results []SearchResult
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lowerLine := strings.ToLower(line)
		if !strings.Contains(lowerLine, `"session_meta"`) &&
			!lineMightMatch(lowerLine, state, terms) {

			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		if applyCodexSearchMetadata(&state, raw) {
			continue
		}

		message, ok := searchMessageFromRaw(raw, lineNo)
		if !ok {
			continue
		}
		message.Index = messageIndex
		messageIndex++

		if state.title == "" && message.Role == "user" &&
			isGoodTitleCandidate(message.Content) {

			state.title = truncateOneLine(message.Content, 120)
		}
		if !workspaceMatches(state.workspace, workspaceFilter) {
			continue
		}
		if result, ok := searchResultFromMessage(state, message, terms); ok {

			results = append(results, result)
		}
	}

	return results
}

// workspaceMatches reports whether a recorded Codex cwd satisfies the optional
// partial workspace filter.
func workspaceMatches(workspace, filter string) bool {
	if filter == "" {
		return true
	}
	if workspace == "" {
		return false
	}

	return strings.Contains(
		strings.ToLower(filepath.Clean(workspace)),
		strings.ToLower(filter),
	)
}

// codexSearchFileState holds metadata discovered while streaming one file.
type codexSearchFileState struct {
	path        string
	sourceLabel string
	resumeToken string
	workspace   string
	title       string
}

// applyCodexSearchMetadata updates per-file metadata from a raw envelope.
func applyCodexSearchMetadata(state *codexSearchFileState,
	raw map[string]any) bool {

	entryType, _ := raw["type"].(string)
	if entryType != "session_meta" {
		return false
	}
	payload, _ := raw["payload"].(map[string]any)
	if id, ok := payload["id"].(string); ok {
		state.resumeToken = id
	}
	if cwd, ok := payload["cwd"].(string); ok {
		state.workspace = cwd
	}

	return true
}

// searchMessageFromRaw extracts a searchable message from a raw Codex envelope.
func searchMessageFromRaw(raw map[string]any, lineNo int) (Message, bool) {
	entryType, _ := raw["type"].(string)
	payload, _ := raw["payload"].(map[string]any)
	createdAt := parseTimestamp(raw["timestamp"])
	switch entryType {
	case "response_item":
		return responseItemMessage(payload, createdAt, lineNo)

	case "event_msg":
		return eventMessage(payload, createdAt, lineNo)

	default:
		return Message{}, false
	}
}

// searchResultFromMessage checks one message and returns a result when it
// matches.
func searchResultFromMessage(state codexSearchFileState, message Message,
	terms []string) (SearchResult, bool) {

	haystack := strings.ToLower(
		strings.Join(
			[]string{
				state.title,
				state.workspace,
				state.path,
				message.Role,
				message.Author,
				message.Content,
			}, "\n",
		),
	)
	if !matchesAllTerms(haystack, terms) {
		return SearchResult{}, false
	}

	return SearchResult{
		Agent:        agentCodex,
		SourcePath:   state.path,
		ResumeToken:  state.resumeToken,
		SourceLabel:  state.sourceLabel,
		Line:         message.Line,
		MessageIndex: message.Index,
		Role:         message.Role,
		Author:       message.Author,
		Workspace:    state.workspace,
		Title:        state.title,
		CreatedAt:    message.CreatedAt,
		Score:        scoreMatch(haystack, terms),
		Snippet:      snippet(message.Content, terms),
	}, true
}

// lineMightMatch cheaply checks whether a raw JSONL line plus file context can
// contain all terms.
func lineMightMatch(lowerLine string, state codexSearchFileState,
	terms []string) bool {

	context := ""
	if state.workspace != "" || state.path != "" || state.title != "" {
		context = strings.ToLower(
			state.workspace + "\n" + state.path + "\n" +
				state.title,
		)
	}
	for _, term := range terms {
		if !strings.Contains(lowerLine, term) &&
			!strings.Contains(context, term) {
			return false
		}
	}

	return true
}

// Search performs a simple all-terms lexical scan over conversations.
func Search(conversations []Conversation, query string,
	limit int) []SearchResult {

	terms := queryTerms(query)
	if len(terms) == 0 {
		return nil
	}

	var results []SearchResult
	for _, conversation := range conversations {
		for _, message := range conversation.Messages {
			haystack := strings.ToLower(
				strings.Join(
					[]string{
						conversation.Title,
						conversation.Workspace,
						conversation.SourcePath,
						message.Role, message.Author,
						message.Content,
					}, "\n",
				),
			)
			if !matchesAllTerms(haystack, terms) {
				continue
			}
			results = append(results, SearchResult{
				Agent:        conversation.Agent,
				SourcePath:   conversation.SourcePath,
				ResumeToken:  conversation.ResumeToken,
				SourceLabel:  conversation.SourceLabel,
				Line:         message.Line,
				MessageIndex: message.Index,
				Role:         message.Role,
				Author:       message.Author,
				Workspace:    conversation.Workspace,
				Title:        conversation.Title,
				CreatedAt:    message.CreatedAt,
				Score:        scoreMatch(haystack, terms),
				Snippet:      snippet(message.Content, terms),
			})
		}
	}

	sortSearchResults(results)
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results
}

// sortSearchResults orders results by most relevant first.
func sortSearchResults(results []SearchResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].CreatedAt > results[j].CreatedAt
		}

		return results[i].Score > results[j].Score
	})
}

// queryTerms lowercases and tokenizes user query text.
func queryTerms(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	out := fields[:0]
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}

	return out
}

// matchesAllTerms reports whether haystack contains every query term.
func matchesAllTerms(haystack string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}

	return true
}

// scoreMatch counts term occurrences for rough result ranking.
func scoreMatch(haystack string, terms []string) int {
	score := 0
	for _, term := range terms {
		score += strings.Count(haystack, term)
	}

	return score
}

// snippet returns a compact excerpt near the earliest matched term.
func snippet(content string, terms []string) string {
	flat := strings.Join(strings.Fields(content), " ")
	lower := strings.ToLower(flat)
	pos := len(flat)
	for _, term := range terms {
		if idx := strings.Index(lower, term); idx >= 0 && idx < pos {
			pos = idx
		}
	}
	if pos == len(flat) {
		return truncateOneLine(flat, 240)
	}

	start := pos - 90
	if start < 0 {
		start = 0
	}
	end := pos + 150
	if end > len(flat) {
		end = len(flat)
	}
	prefix, suffix := "", ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(flat) {
		suffix = "..."
	}

	return prefix + strings.TrimSpace(flat[start:end]) + suffix
}

// truncateOneLine collapses whitespace and shortens text to max runes.
func truncateOneLine(text string, max int) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= max {
		return flat
	}
	if max <= 1 {
		return string(runes[:max])
	}

	return string(runes[:max-1]) + "..."
}

// formatMillis renders Unix milliseconds in the local RFC3339 time zone.
func formatMillis(ms int64) string {
	if ms == 0 {
		return ""
	}

	return time.UnixMilli(ms).Format(time.RFC3339)
}

// takeConversations applies a positive limit to a conversation slice.
func takeConversations(conversations []Conversation, limit int) []Conversation {
	if limit > 0 && len(conversations) > limit {
		return conversations[:limit]
	}

	return conversations
}
