package main

import (
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
