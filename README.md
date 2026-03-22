# claudio

A structured Claude orchestrator for your full-stack build.

claudio injects your canonical project context into every Claude API call,
validates output before writing any files, and tracks session state across
your entire build.

---

## Setup

### 1. Install

```bash
cd claudio
go mod tidy
go build -o claudio .
# Move to your PATH or run as ./claudio
```

### 2. Set your API key

```bash
export ANTHROPIC_API_KEY=sk-ant-...
```

### 3. Initialise in your project root

```bash
cd /path/to/your/project
/path/to/claudio init
```

This creates:
```
config/context.yaml     ← Edit this first. This is the heart of claudio.
tasks/tasks.yaml        ← Define your build tasks here
session/session.json    ← Auto-managed. Tracks what's been built.
prompts/               ← Per-task-type prompt templates
output/                ← Staging area (files go here before you approve)
```

### 4. Edit config/context.yaml

Fill in your actual project details:
- Stack versions and packages
- File structure conventions
- Naming rules
- Anti-patterns (what Claude keeps getting wrong — add them here)
- SDK notes (version-specific API patterns)

This file is injected into **every single Claude call**. It's worth spending
time on.

---

## Usage

### Run a defined task

```bash
claudio task user-model
claudio task auth-middleware
claudio task login-endpoint
```

Tasks are defined in `tasks/tasks.yaml`. Dependencies are checked before running.

### Run a free-form prompt (with context injected)

```bash
claudio run --prompt "Add rate limiting middleware to the chi router"
```

### Check session status

```bash
claudio status
```

Shows completed tasks and all artifacts tracked in session state.

---

## Interaction flow

```
$ claudio task auth-middleware

  Loading session state...          ✓
  Resolving task...                 ✓
  Assembling prompt...              ✓
  Calling Claude API...             ✓
  [tokens: 2847 in / 1203 out]
  Parsing output...                 ✓  (3 files)

  Running validators...
  ⚠  Validation warnings:
     • [internal/middleware/auth.go] Contains 'any' type ...

  ── Staged output ──────────────────────────────
  + internal/middleware/auth.go          (87 lines)
  + internal/middleware/auth_test.go     (54 lines)
  ~ internal/router/router.go            (modified, 34 lines)

  ── Preview (first 40 lines of each file) ──────
  ...

  Accept / Reject / Retry with feedback? > 
```

**Responses:**
- `accept` or `a` — write files, update session
- `reject` or `r` — discard, nothing written
- Any other text — treated as feedback, retries with that context

---

## Adding tasks

Edit `tasks/tasks.yaml`:

```yaml
tasks:
  - id: my-task
    type: api_endpoint          # matches prompts/api_endpoint.tmpl
    depends_on: [user-model]   # must be completed first
    description: "What to build"
    inputs:
      - internal/models/user.go  # existing files Claude should reference
    output_files:
      - internal/handlers/thing.go
      - internal/handlers/thing_test.go
    notes: "Any extra constraints for this specific task"
```

Available task types (prompt templates):
- `api_endpoint` — Go HTTP handlers
- `component` — React TypeScript components
- `db_migration` — SQL migrations + Go models
- Add your own: create `prompts/yourtype.tmpl`

---

## How the context injection works

Every Claude API call gets a system prompt assembled from `config/context.yaml` containing:

1. Stack declaration
2. File structure conventions
3. Naming rules
4. Error handling patterns
5. Anti-patterns (NEVER DO THESE)
6. SDK/library notes
7. **Session state** — a list of every file already built

That last point is key: Claude always knows the current shape of your codebase
without you re-explaining it each session.

---

## Validators

Claudio validates output before you approve it. Current checks:

**Go:**
- `panic()` in handlers
- Global DB variables
- Discarded errors (`_ =`)
- CGO packages (ARM64 warning)
- Possible truncated output

**TypeScript:**
- Bare `any` usage
- `console.log` left in
- react-query v4 API style

**SQL:**
- Missing PRIMARY KEY
- Floating point types for numeric data

**Universal:**
- Hardcoded secrets
- Truncation signals (`// ...`, `// rest of`, etc.)

Add your own checks in `internal/validator/validator.go`.

---

## Project structure

```
claudio/
├── main.go                          CLI entrypoint (cobra commands)
├── internal/
│   ├── anthropic/client.go          Claude API client
│   ├── assembler/assembler.go       Context loading + prompt assembly + output parsing
│   ├── state/state.go               Session state + task resolution
│   ├── validator/validator.go       Output validation rules
│   └── runner/runner.go             Staging, preview, commit
├── config/context.yaml              Your canonical project context
├── tasks/tasks.yaml                 Task definitions
├── prompts/                         Per-task-type prompt templates
├── session/session.json             Persisted build state
└── output/                          Staging area
```
