package state

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fatih/color"
	"gopkg.in/yaml.v3"
)

// ── Session ───────────────────────────────────────────────────────────────────

type Session struct {
	ProjectName    string     `json:"project_name"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUpdated    time.Time  `json:"last_updated"`
	CompletedTasks []string   `json:"completed_tasks"`
	Artifacts      []Artifact `json:"artifacts"`
}

type Artifact struct {
	Path      string    `json:"path"`
	TaskID    string    `json:"task_id"`
	Checksum  string    `json:"checksum"`
	CreatedAt time.Time `json:"created_at"`
}

func Load(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Return a fresh session if none exists
		return &Session{
			CreatedAt:   time.Now(),
			LastUpdated: time.Now(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}

	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("parse session: %w", err)
	}

	return &sess, nil
}

func Save(sess *Session, path string) error {
	sess.LastUpdated = time.Now()

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	if err := os.MkdirAll("session", 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func (s *Session) AddArtifact(path, taskID, checksum string) {
	// Update if exists
	for i, a := range s.Artifacts {
		if a.Path == path {
			s.Artifacts[i].Checksum = checksum
			s.Artifacts[i].TaskID = taskID
			return
		}
	}
	s.Artifacts = append(s.Artifacts, Artifact{
		Path:      path,
		TaskID:    taskID,
		Checksum:  checksum,
		CreatedAt: time.Now(),
	})
}

func (s *Session) MarkTaskComplete(taskID string) {
	for _, t := range s.CompletedTasks {
		if t == taskID {
			return
		}
	}
	s.CompletedTasks = append(s.CompletedTasks, taskID)
}

func (s *Session) IsTaskComplete(taskID string) bool {
	for _, t := range s.CompletedTasks {
		if t == taskID {
			return true
		}
	}
	return false
}

func PrintStatus(sess *Session) {
	bold := color.New(color.Bold)
	green := color.New(color.FgGreen)
	cyan := color.New(color.FgCyan)

	bold.Printf("\n  Session: %s\n", sess.ProjectName)
	fmt.Printf("  Last updated: %s\n\n", sess.LastUpdated.Format("2006-01-02 15:04:05"))

	bold.Printf("  Completed tasks (%d):\n", len(sess.CompletedTasks))
	for _, t := range sess.CompletedTasks {
		green.Printf("    ✓ %s\n", t)
	}

	fmt.Println()
	bold.Printf("  Artifacts (%d):\n", len(sess.Artifacts))
	for _, a := range sess.Artifacts {
		cyan.Printf("    %s", a.Path)
		fmt.Printf(" (task: %s)\n", a.TaskID)
	}
	fmt.Println()
}

// ── Tasks ─────────────────────────────────────────────────────────────────────

type TasksConfig struct {
	Tasks []Task `yaml:"tasks"`
}

type Task struct {
	ID          string   `yaml:"id"`
	Type        string   `yaml:"type"`
	Description string   `yaml:"description"`
	DependsOn   []string `yaml:"depends_on"`
	Inputs      []string `yaml:"inputs"`
	OutputFiles []string `yaml:"output_files"`
	Notes       string   `yaml:"notes"`
}

func ResolveTask(tasksPath, taskID string, sess *Session) (*Task, error) {
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		return nil, fmt.Errorf("read tasks file: %w", err)
	}

	var cfg TasksConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse tasks yaml: %w", err)
	}

	var task *Task
	for i, t := range cfg.Tasks {
		if t.ID == taskID {
			task = &cfg.Tasks[i]
			break
		}
	}

	if task == nil {
		return nil, fmt.Errorf("task %q not found in tasks.yaml", taskID)
	}

	// Check dependencies
	var unmet []string
	for _, dep := range task.DependsOn {
		if !sess.IsTaskComplete(dep) {
			unmet = append(unmet, dep)
		}
	}

	if len(unmet) > 0 {
		return nil, fmt.Errorf("unmet dependencies for task %q: %v", taskID, unmet)
	}

	return task, nil
}

// ── Init project ──────────────────────────────────────────────────────────────

func InitProject() error {
	dirs := []string{"config", "tasks", "session", "prompts", "output"}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return err
		}
	}

	// Write context.yaml if not exists
	if _, err := os.Stat("config/context.yaml"); os.IsNotExist(err) {
		if err := os.WriteFile("config/context.yaml", []byte(defaultContext), 0644); err != nil {
			return err
		}
		fmt.Println("  Created config/context.yaml")
	}

	// Write tasks.yaml if not exists
	if _, err := os.Stat("tasks/tasks.yaml"); os.IsNotExist(err) {
		if err := os.WriteFile("tasks/tasks.yaml", []byte(defaultTasks), 0644); err != nil {
			return err
		}
		fmt.Println("  Created tasks/tasks.yaml")
	}

	// Write example prompt templates
	templates := map[string]string{
		"prompts/api_endpoint.tmpl": apiEndpointTemplate,
		"prompts/component.tmpl":    componentTemplate,
		"prompts/db_migration.tmpl": dbMigrationTemplate,
	}
	for path, content := range templates {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
			fmt.Printf("  Created %s\n", path)
		}
	}

	// Init empty session
	sess := &Session{
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}
	if err := Save(sess, "session/session.json"); err != nil {
		return err
	}
	fmt.Println("  Created session/session.json")

	color.New(color.FgGreen).Println("\n  ✓ claudio initialised. Edit config/context.yaml and tasks/tasks.yaml to get started.")
	return nil
}

// ── Default file contents ─────────────────────────────────────────────────────

var defaultContext = `project:
  name: "my-app"
  description: "Full-stack app: Go backend, React/TypeScript frontend, Postgres, Redis on Raspberry Pi"
  root_path: "."

stack:
  backend:
    language: Go
    version: "1.22"
    framework: "net/http + chi router"
    packages:
      - "github.com/go-chi/chi/v5"
      - "github.com/jackc/pgx/v5"
      - "github.com/redis/go-redis/v9"
      - "github.com/golang-jwt/jwt/v5"

  frontend:
    language: TypeScript
    framework: React
    version: "18"
    packages:
      - "react-query"
      - "react-router-dom"
      - "axios"

  infra:
    database: "PostgreSQL 15"
    cache: "Redis 7"
    platform: "Raspberry Pi 4 (ARM64, linux/arm64)"
    notes: "Must build for linux/arm64. Avoid CGO where possible. No x86-only libraries."

conventions:
  file_structure:
    backend: "internal/ for packages, cmd/ for entrypoints, no flat main packages"
    frontend: "src/components/, src/hooks/, src/api/, src/types/"
    migrations: "db/migrations/ — numbered sequentially e.g. 001_create_users.sql"

  naming:
    go_packages: "lowercase, single word"
    go_interfaces: "Verb-er pattern e.g. UserStorer, TokenValidator"
    react_components: "PascalCase"
    api_routes: "kebab-case, versioned: /api/v1/resource"
    db_tables: "snake_case, plural"

  error_handling: |
    Go: always wrap errors with fmt.Errorf("context: %w", err). Never discard errors.
    HTTP handlers: return structured JSON errors { "error": "message" } with appropriate status codes.
    Frontend: all API calls wrapped in react-query, errors surfaced via error boundaries.

  testing: |
    Go: table-driven tests, *_test.go files alongside source. Use testify/assert.
    Frontend: Vitest + React Testing Library for components.

anti_patterns:
  - "Never use global variables for database connections — pass via context or dependency injection"
  - "Never store secrets in code — use environment variables"
  - "Never use float64 for currency or precise decimals — use integer cents or pgtype.Numeric"
  - "Never import cgo-dependent packages without ARM64 verification"
  - "Never use panic() in HTTP handlers"
  - "Never return 200 for errors"
  - "Never use any in TypeScript without a type assertion comment"

sdk_notes:
  pgx: "Use pgxpool.Pool for connection pooling. Use pgx/v5/pgtype for Postgres-native types."
  redis: "go-redis/v9 — use context-aware methods. Prefix all keys with app name."
  jwt: "golang-jwt/v5 — RS256 for production, HS256 acceptable for dev."
  react-query: "v5 API — useQuery takes an options object, not positional args."
`

var defaultTasks = `tasks:
  - id: user-model
    type: db_migration
    description: "Create users table with id, email, password_hash, created_at, updated_at"
    output_files:
      - db/migrations/001_create_users.sql
      - internal/models/user.go

  - id: auth-middleware
    type: api_endpoint
    depends_on: [user-model]
    description: "JWT authentication middleware for protected routes"
    inputs:
      - internal/models/user.go
    output_files:
      - internal/middleware/auth.go
      - internal/middleware/auth_test.go

  - id: login-endpoint
    type: api_endpoint
    depends_on: [user-model, auth-middleware]
    description: "POST /api/v1/auth/login — validate credentials, return JWT"
    output_files:
      - internal/handlers/auth.go
      - internal/handlers/auth_test.go
`

var apiEndpointTemplate = `Build the following API endpoint for the Go backend.

Task: {{ .Task.ID }}
Description: {{ .Task.Description }}

{{ if .Task.Inputs }}
Existing files to reference (do not recreate):
{{ range .Task.Inputs }}- {{ . }}
{{ end }}{{ end }}

{{ if .Task.OutputFiles }}
Expected output files:
{{ range .Task.OutputFiles }}- {{ . }}
{{ end }}{{ end }}

{{ if .Task.Notes }}
Additional notes: {{ .Task.Notes }}
{{ end }}

Requirements:
- Follow the chi router pattern
- Return structured JSON for all responses
- Wrap all errors with fmt.Errorf
- Include table-driven tests in the _test.go file
- Use dependency injection — no global state
`

var componentTemplate = `Build the following React TypeScript component.

Task: {{ .Task.ID }}
Description: {{ .Task.Description }}

{{ if .Task.Inputs }}
Existing files to reference:
{{ range .Task.Inputs }}- {{ . }}
{{ end }}{{ end }}

Requirements:
- Strict TypeScript — no any without justification comment
- Use react-query for any data fetching
- Export types/interfaces to src/types/ if reusable
- Include basic error and loading states
`

var dbMigrationTemplate = `Create the following database migration and corresponding Go model.

Task: {{ .Task.ID }}
Description: {{ .Task.Description }}

Requirements:
- SQL migration file with UP migration only (we handle DOWN separately)
- Use snake_case for table and column names
- Include created_at and updated_at with DEFAULT NOW()
- Go model struct with pgx-compatible types
- Include a basic repository interface and implementation stub
`
