# Mermaid Diagram Test

## Flowchart

```mermaid
flowchart LR
    A([Start]) --> B{Is it working?}
    B -->|Yes| C[Great!]
    B -->|No| D[Debug]
    D --> E[Fix it]
    E --> B
    C --> F([End])
```

## Sequence Diagram

```mermaid
sequenceDiagram
    participant Browser
    participant Server
    participant FSNotify

    Browser->>Server: GET /events (SSE)
    Server-->>Browser: ping
    FSNotify->>Server: file changed
    Server-->>Browser: reload event
    Browser->>Server: GET /api/file?path=...
    Server-->>Browser: file content
    Browser->>Browser: re-render
```

## Class Diagram

```mermaid
classDiagram
    class Broker {
        +map clients
        +Subscribe() chan
        +Unsubscribe(ch)
        +Publish(msg)
    }
    class FileNode {
        +string Name
        +string Path
        +bool IsDir
        +FileNode[] Children
    }
    class SearchResult {
        +string File
        +int Line
        +string Snippet
    }
    Broker --> FileNode : watches
```

## State Diagram

```mermaid
stateDiagram-v2
    [*] --> Idle
    Idle --> Loading : click file
    Loading --> Rendering : fetch complete
    Rendering --> Viewing : render complete
    Viewing --> Editing : click Edit
    Editing --> Saving : blur
    Saving --> Viewing : save complete
    Viewing --> Loading : SSE reload
    Viewing --> [*]
```

## Entity Relationship

```mermaid
erDiagram
    DIRECTORY ||--o{ FILE : contains
    FILE ||--o{ SEARCH_RESULT : "matched in"
    FILE {
        string path
        string name
        string ext
        bool isMarkdown
    }
    SEARCH_RESULT {
        int line
        string snippet
    }
```

## Gantt

```mermaid
gantt
    title mdserver Build Timeline
    dateFormat  YYYY-MM-DD
    section Backend
    Go server + routing     :done, 2026-04-11, 1d
    File watcher            :done, 2026-04-11, 1d
    Search API              :done, 2026-04-11, 1d
    section Frontend
    File tree               :done, 2026-04-11, 1d
    Markdown render         :done, 2026-04-11, 1d
    Mermaid support         :done, 2026-04-11, 1d
    Edit mode               :done, 2026-04-11, 1d
    Dark mode               :done, 2026-04-11, 1d
```

## Pie Chart

```mermaid
pie title Lines of Code
    "main.go" : 350
    "index.html CSS" : 280
    "index.html JS" : 420
    "index.html HTML" : 80
```

## Git Graph

```mermaid
gitGraph
    commit id: "init"
    commit id: "go server + SSE"
    branch feature/frontend
    checkout feature/frontend
    commit id: "file tree"
    commit id: "markdown render"
    commit id: "diagram support"
    checkout main
    merge feature/frontend
    commit id: "edit mode"
    commit id: "dark mode"
    commit id: "fix hljs CDN"
```

## Quadrant Chart

```mermaid
quadrantChart
    title Library Selection
    x-axis Low Complexity --> High Complexity
    y-axis Low Features --> High Features
    quadrant-1 Overkill
    quadrant-2 Sweet Spot
    quadrant-3 Too Simple
    quadrant-4 Painful
    marked.js: [0.3, 0.7]
    mermaid.js: [0.6, 0.9]
    CodeMirror: [0.7, 0.8]
    highlight.js: [0.2, 0.6]
```

## Timeline

```mermaid
timeline
    title mdserver Feature History
    section Rendering
        Markdown      : marked.js
                      : syntax highlighting
        Diagrams      : mermaid.js
    section Editing
        Inline editor : CodeMirror 6
                      : save on blur
    section UX
        Live reload   : SSE + fsnotify
        Dark mode     : localStorage
        URL routing   : history.pushState
```

## XY Chart

```mermaid
xychart-beta
    title "Hypothetical File Load Times (ms)"
    x-axis [1kb, 10kb, 50kb, 100kb, 500kb]
    y-axis "Time (ms)" 0 --> 300
    bar [5, 12, 45, 95, 280]
    line [5, 12, 45, 95, 280]
```
