package main

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

//go:embed static
var staticFiles embed.FS

// FileNode represents a file or directory in the tree.
type FileNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"isDir"`
	Children []*FileNode `json:"children,omitempty"`
}

// SearchResult is one line match from full-text search.
type SearchResult struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// SessionInfo is the listing metadata for one Claude Code transcript (.jsonl).
type SessionInfo struct {
	ID       string `json:"id"`      // relative "<encodedDir>/<uuid>.jsonl"
	Title    string `json:"title"`   // first human prompt, truncated
	Project  string `json:"project"` // real cwd read from inside the file
	Size     int64  `json:"size"`
	Modified string `json:"modified"` // RFC3339
	ModUnix  int64  `json:"modUnix"`
}

// SessionGroup buckets sessions under their project (cwd).
type SessionGroup struct {
	Project  string        `json:"project"`
	Sessions []SessionInfo `json:"sessions"`
}

// Turn is one piece of prose in a transcript: a human prompt or assistant text.
type Turn struct {
	Role      string `json:"role"` // "user" | "assistant"
	Text      string `json:"text"`
	Timestamp string `json:"timestamp,omitempty"`
	Sidechain bool   `json:"sidechain,omitempty"` // from a subagent
}

// SessionData is the parsed, prose-only transcript returned to the client.
type SessionData struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Title   string `json:"title"`
	Turns   []Turn `json:"turns"`
}

// rawRecord decodes only the fields we need from a transcript line. Lines we
// don't care about (file-history-snapshot, permission-mode, ...) have no
// "message" field, so Message stays empty and decoding is cheap.
type rawRecord struct {
	Type        string          `json:"type"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   string          `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	Message     json.RawMessage `json:"message"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Broker manages SSE clients.
type Broker struct {
	mu      sync.Mutex
	clients map[chan string]struct{}
}

func (b *Broker) Subscribe() chan string {
	ch := make(chan string, 8)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
}

func (b *Broker) Publish(msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: mdserver [path]\n\n  path  directory or file to open (default: current directory)\n")
	}
	flag.Parse()

	// Resolve root dir and optional initial file from positional arg.
	var absDir, initialFile string
	arg := flag.Arg(0)
	if arg == "" {
		arg = "."
	}
	absPath, err := filepath.Abs(arg)
	if err != nil {
		log.Fatalf("invalid path: %v", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		log.Fatalf("path not found: %v", err)
	}
	if info.IsDir() {
		absDir = absPath
	} else {
		absDir = filepath.Dir(absPath)
		rel, _ := filepath.Rel(absDir, absPath)
		initialFile = filepath.ToSlash(rel)
	}

	broker := &Broker{clients: make(map[chan string]struct{})}
	go watchDir(absDir, broker)

	mux := http.NewServeMux()

	staticFS, _ := fs.Sub(staticFiles, "static")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(staticFS, "index.html")
		if err != nil {
			http.Error(w, "index.html not found", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.HandleFunc("/api/files", handleFiles(absDir))
	mux.HandleFunc("/api/file", handleFile(absDir))
	mux.HandleFunc("/api/search", handleSearch(absDir))
	mux.HandleFunc("/api/config", handleConfig(initialFile))
	mux.HandleFunc("/events", handleSSE(broker))

	// Claude Code transcript browsing (~/.claude/projects).
	if projectsDir, err := claudeProjectsDir(); err == nil {
		sw := newSessionWatcher(broker)
		mux.HandleFunc("/api/sessions", handleSessions(projectsDir))
		mux.HandleFunc("/api/session", handleSession(projectsDir))
		mux.HandleFunc("/api/watch-session", handleWatchSession(projectsDir, sw))
	} else {
		log.Printf("session browsing disabled: %v", err)
	}

	// Bind to a random localhost port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("listen error: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if initialFile != "" {
		url += "/?file=" + initialFile
	}
	log.Printf("Serving %s at %s", absDir, url)

	// Open browser shortly after server starts.
	time.AfterFunc(150*time.Millisecond, func() { openBrowser(url) })

	log.Fatal(http.Serve(ln, mux))
}

func handleConfig(initialFile string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"initialFile": initialFile})
	}
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
		args = []string{url}
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		cmd = "xdg-open"
		args = []string{url}
	}
	if err := exec.Command(cmd, args...).Start(); err != nil {
		log.Printf("could not open browser: %v", err)
	}
}

func safeAbs(root, relPath string) (string, bool) {
	joined := filepath.Join(root, filepath.FromSlash(relPath))
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", false
	}
	rootWithSep := root + string(os.PathSeparator)
	if abs != root && !strings.HasPrefix(abs, rootWithSep) {
		return "", false
	}
	return abs, true
}

func handleFiles(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes, err := buildTree(root, root)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodes)
	}
}

func buildTree(root, current string) ([]*FileNode, error) {
	entries, err := os.ReadDir(current)
	if err != nil {
		return nil, err
	}
	var nodes []*FileNode
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		relPath, _ := filepath.Rel(root, filepath.Join(current, e.Name()))
		node := &FileNode{
			Name:  e.Name(),
			Path:  filepath.ToSlash(relPath),
			IsDir: e.IsDir(),
		}
		if e.IsDir() {
			node.Children, _ = buildTree(root, filepath.Join(current, e.Name()))
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func handleFile(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		relPath := r.URL.Query().Get("path")
		if relPath == "" {
			http.Error(w, "missing path", 400)
			return
		}
		abs, ok := safeAbs(root, relPath)
		if !ok {
			http.Error(w, "forbidden", 403)
			return
		}

		switch r.Method {
		case http.MethodGet:
			data, err := os.ReadFile(abs)
			if err != nil {
				http.Error(w, "not found", 404)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(data)

		case http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			if err := os.WriteFile(abs, body, 0644); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "method not allowed", 405)
		}
	}
}

func handleSearch(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		w.Header().Set("Content-Type", "application/json")
		if len(q) < 2 {
			w.Write([]byte("[]"))
			return
		}
		// Cap query length
		if len(q) > 200 {
			q = q[:200]
		}

		var results []SearchResult
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			// Skip binary files
			check := data
			if len(check) > 512 {
				check = check[:512]
			}
			if bytes.IndexByte(check, 0) != -1 {
				return nil
			}

			lines := strings.Split(string(data), "\n")
			rel, _ := filepath.Rel(root, path)
			for i, line := range lines {
				if strings.Contains(strings.ToLower(line), q) {
					snippet := strings.TrimSpace(line)
					if len(snippet) > 200 {
						snippet = snippet[:200]
					}
					results = append(results, SearchResult{
						File:    filepath.ToSlash(rel),
						Line:    i + 1,
						Snippet: snippet,
					})
				}
			}
			if len(results) >= 200 {
				return filepath.SkipAll
			}
			return nil
		})

		json.NewEncoder(w).Encode(results)
	}
}

func handleSSE(broker *Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		fmt.Fprintf(w, "data: {\"type\":\"ping\"}\n\n")
		flusher.Flush()

		ch := broker.Subscribe()
		defer broker.Unsubscribe(ch)

		for {
			select {
			case msg := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

// ── Claude Code session browsing ────────────────────────────────────────

func claudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("%s not found", dir)
	}
	return dir, nil
}

// metaScanBudget bounds how many bytes we read per file when building the
// session list, so a giant snapshot near the top of a file can't stall listing.
const metaScanBudget = 256 * 1024

// titleMaxLen caps the human-prompt title shown in the session list.
const titleMaxLen = 100

// handleSessions lists every transcript grouped by project, newest first.
func handleSessions(projectsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(projectsDir)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		byProject := map[string][]SessionInfo{}
		for _, dirEnt := range entries {
			if !dirEnt.IsDir() {
				continue
			}
			subDir := filepath.Join(projectsDir, dirEnt.Name())
			files, err := os.ReadDir(subDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
					continue
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				abs := filepath.Join(subDir, f.Name())
				project, title := scanSessionMeta(abs, dirEnt.Name())
				si := SessionInfo{
					ID:       dirEnt.Name() + "/" + f.Name(),
					Title:    title,
					Project:  project,
					Size:     info.Size(),
					Modified: info.ModTime().UTC().Format(time.RFC3339),
					ModUnix:  info.ModTime().Unix(),
				}
				byProject[project] = append(byProject[project], si)
			}
		}

		groups := make([]SessionGroup, 0, len(byProject))
		for project, sessions := range byProject {
			sort.Slice(sessions, func(i, j int) bool { return sessions[i].ModUnix > sessions[j].ModUnix })
			groups = append(groups, SessionGroup{Project: project, Sessions: sessions})
		}
		// Projects ordered by their most-recently-touched session.
		sort.Slice(groups, func(i, j int) bool {
			return groups[i].Sessions[0].ModUnix > groups[j].Sessions[0].ModUnix
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(groups)
	}
}

// scanSessionMeta reads the head of a transcript to recover its real cwd and
// first human prompt. Falls back to a decoded directory name / "(untitled)".
func scanSessionMeta(abs, encodedDir string) (project, title string) {
	f, err := os.Open(abs)
	if err != nil {
		return decodeProjectDir(encodedDir), "(unreadable)"
	}
	defer f.Close()

	reader := bufio.NewReader(io.LimitReader(f, metaScanBudget))
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var rec rawRecord
			if json.Unmarshal(line, &rec) == nil {
				if project == "" && rec.Cwd != "" {
					project = rec.Cwd
				}
				if title == "" && rec.Type == "user" && !rec.IsMeta {
					if t := userPromptText(rec.Message); t != "" {
						title = truncate(t, titleMaxLen)
					}
				}
			}
		}
		if (project != "" && title != "") || err != nil {
			break
		}
	}
	if project == "" {
		project = decodeProjectDir(encodedDir)
	}
	if title == "" {
		title = "(no prompt)"
	}
	return project, title
}

// decodeProjectDir is a best-effort reversal of Claude's "/" → "-" encoding.
// It is ambiguous (paths may contain "-"), so it's only a fallback when the
// real cwd can't be read from inside the file.
func decodeProjectDir(name string) string {
	return strings.ReplaceAll(name, "-", "/")
}

// sessionCacheEntry memoises a parsed transcript, keyed by file identity.
type sessionCacheEntry struct {
	modUnix int64
	size    int64
	data    *SessionData
}

var sessCache sync.Map // abs path -> *sessionCacheEntry

// handleSession returns the prose-only turns of one transcript.
func handleSession(projectsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", 400)
			return
		}
		abs, ok := safeAbs(projectsDir, id)
		if !ok {
			http.Error(w, "forbidden", 403)
			return
		}
		info, err := os.Stat(abs)
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}

		// Serve from cache when the file is unchanged (transcripts are append-only).
		if v, ok := sessCache.Load(abs); ok {
			e := v.(*sessionCacheEntry)
			if e.modUnix == info.ModTime().Unix() && e.size == info.Size() {
				writeJSON(w, e.data)
				return
			}
		}

		data, err := parseSession(abs, id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		sessCache.Store(abs, &sessionCacheEntry{
			modUnix: info.ModTime().Unix(),
			size:    info.Size(),
			data:    data,
		})
		writeJSON(w, data)
	}
}

// parseSession streams a transcript and extracts human prompts + assistant
// text, discarding tool calls, tool results, thinking and bookkeeping records.
func parseSession(abs, id string) (*SessionData, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data := &SessionData{ID: id}
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			// Cheap pre-filter: only user/assistant lines can yield prose.
			if bytes.Contains(line, []byte(`"type":"user"`)) || bytes.Contains(line, []byte(`"type":"assistant"`)) {
				if turn, ok := turnFromLine(line); ok {
					if data.Project == "" {
						// cwd lives on these records; capture it once.
						var rec rawRecord
						if json.Unmarshal(line, &rec) == nil {
							data.Project = rec.Cwd
						}
					}
					if data.Title == "" && turn.Role == "user" {
						data.Title = truncate(turn.Text, titleMaxLen)
					}
					data.Turns = append(data.Turns, turn)
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return data, nil
}

// turnFromLine extracts a single prose Turn from a transcript line, or false.
func turnFromLine(line []byte) (Turn, bool) {
	var rec rawRecord
	if json.Unmarshal(line, &rec) != nil || rec.IsMeta || len(rec.Message) == 0 {
		return Turn{}, false
	}
	var msg rawMessage
	if json.Unmarshal(rec.Message, &msg) != nil {
		return Turn{}, false
	}

	switch rec.Type {
	case "user":
		text := userPromptText(rec.Message)
		if text == "" || isNoisePrompt(text) {
			return Turn{}, false
		}
		return Turn{Role: "user", Text: text, Timestamp: rec.Timestamp, Sidechain: rec.IsSidechain}, true
	case "assistant":
		var blocks []contentBlock
		if json.Unmarshal(msg.Content, &blocks) != nil {
			return Turn{}, false
		}
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(b.Text)
			}
		}
		if sb.Len() == 0 {
			return Turn{}, false
		}
		return Turn{Role: "assistant", Text: sb.String(), Timestamp: rec.Timestamp, Sidechain: rec.IsSidechain}, true
	}
	return Turn{}, false
}

// userPromptText returns the human prompt text when a user message's content is
// a plain string. List-shaped content (tool results) is intentionally skipped.
func userPromptText(rawMsg json.RawMessage) string {
	if len(rawMsg) == 0 {
		return ""
	}
	var msg rawMessage
	if json.Unmarshal(rawMsg, &msg) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(msg.Content, &s) != nil {
		return "" // content was an array (tool_result), not a prompt
	}
	return strings.TrimSpace(s)
}

// isNoisePrompt filters slash-command plumbing and system scaffolding that is
// stored as user text but isn't something a person typed as prose.
func isNoisePrompt(s string) bool {
	switch {
	case strings.HasPrefix(s, "<command-name>"),
		strings.HasPrefix(s, "<command-message>"),
		strings.HasPrefix(s, "<local-command-stdout>"),
		strings.HasPrefix(s, "Caveat: The messages below"),
		strings.HasPrefix(s, "<bash-input>"),
		strings.HasPrefix(s, "<bash-stdout>"):
		return true
	}
	return false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// sessionWatcher watches the directory of whichever transcript the browser is
// currently viewing and publishes a "session-reload" event when that file
// changes (Claude Code appends to it live). Only one file is watched at a time
// — re-pointing on each open keeps it cheap rather than watching all of
// ~/.claude/projects, where every active session would spam events.
type sessionWatcher struct {
	mu         sync.Mutex
	watcher    *fsnotify.Watcher
	broker     *Broker
	currentDir string
	current    string // abs path of the watched file
	id         string // its client-facing id ("<dir>/<file>.jsonl")
	debounce   *time.Timer
}

func newSessionWatcher(broker *Broker) *sessionWatcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("session watcher disabled: %v", err)
		return &sessionWatcher{broker: broker}
	}
	sw := &sessionWatcher{watcher: w, broker: broker}
	go sw.loop()
	return sw
}

func (sw *sessionWatcher) loop() {
	for {
		select {
		case ev, ok := <-sw.watcher.Events:
			if !ok {
				return
			}
			if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) {
				continue
			}
			sw.mu.Lock()
			if ev.Name == sw.current {
				id := sw.id
				if sw.debounce != nil {
					sw.debounce.Stop()
				}
				sw.debounce = time.AfterFunc(150*time.Millisecond, func() {
					msg, _ := json.Marshal(map[string]string{"type": "session-reload", "id": id})
					sw.broker.Publish(string(msg))
				})
			}
			sw.mu.Unlock()
		case _, ok := <-sw.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

// Watch re-points the watcher at a single transcript file. We watch its parent
// directory and filter by name so appends, creates and renames are all caught.
func (sw *sessionWatcher) Watch(abs, id string) {
	if sw.watcher == nil {
		return
	}
	dir := filepath.Dir(abs)
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.current == abs {
		return
	}
	if sw.currentDir != dir {
		if sw.currentDir != "" {
			sw.watcher.Remove(sw.currentDir)
		}
		if err := sw.watcher.Add(dir); err != nil {
			log.Printf("watch session dir: %v", err)
			sw.currentDir, sw.current, sw.id = "", "", ""
			return
		}
		sw.currentDir = dir
	}
	sw.current = abs
	sw.id = id
}

func handleWatchSession(projectsDir string, sw *sessionWatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", 400)
			return
		}
		abs, ok := safeAbs(projectsDir, id)
		if !ok {
			http.Error(w, "forbidden", 403)
			return
		}
		sw.Watch(abs, id)
		w.WriteHeader(http.StatusNoContent)
	}
}

func watchDir(root string, broker *Broker) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("watcher error: %v", err)
		return
	}
	defer watcher.Close()

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			watcher.Add(path)
		}
		return nil
	})

	type debounceEntry struct {
		timer *time.Timer
	}
	debounce := make(map[string]*debounceEntry)
	var mu sync.Mutex

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) {
				rel, _ := filepath.Rel(root, event.Name)
				rel = filepath.ToSlash(rel)

				mu.Lock()
				if e, exists := debounce[rel]; exists {
					e.timer.Stop()
				}
				relCopy := rel
				debounce[rel] = &debounceEntry{
					timer: time.AfterFunc(100*time.Millisecond, func() {
						msg, _ := json.Marshal(map[string]string{
							"type": "reload",
							"path": relCopy,
						})
						broker.Publish(string(msg))
					}),
				}
				mu.Unlock()

				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						watcher.Add(event.Name)
					}
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("watcher error:", err)
		}
	}
}
