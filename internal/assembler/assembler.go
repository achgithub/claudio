package assembler

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"

	"github.com/achgithub/claudio/internal/state"

	"gopkg.in/yaml.v3"
)

// ── Context types ────────────────────────────────────────────────────────────

type Context struct {
	Project     ProjectConfig     `yaml:"project"`
	Stack       StackConfig       `yaml:"stack"`
	Conventions ConventionsConfig `yaml:"conventions"`
	AntiPatterns []string         `yaml:"anti_patterns"`
	SDKNotes    map[string]string `yaml:"sdk_notes"`
}

type ProjectConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	RootPath    string `yaml:"root_path"`
}

type StackConfig struct {
	Backend  BackendStack  `yaml:"backend"`
	Frontend FrontendStack `yaml:"frontend"`
	Infra    InfraStack    `yaml:"infra"`
}

type BackendStack struct {
	Language  string   `yaml:"language"`
	Version   string   `yaml:"version"`
	Framework string   `yaml:"framework"`
	Packages  []string `yaml:"packages"`
}

type FrontendStack struct {
	Language  string   `yaml:"language"`
	Framework string   `yaml:"framework"`
	Version   string   `yaml:"version"`
	Packages  []string `yaml:"packages"`
}

type InfraStack struct {
	Database string `yaml:"database"`
	Cache    string `yaml:"cache"`
	Platform string `yaml:"platform"`
	Notes    string `yaml:"notes"`
}

type ConventionsConfig struct {
	FileStructure map[string]string `yaml:"file_structure"`
	Naming        map[string]string `yaml:"naming"`
	ErrorHandling string            `yaml:"error_handling"`
	Testing       string            `yaml:"testing"`
}

// ── StagedFile is a parsed output file from Claude ───────────────────────────

type StagedFile struct {
	Path    string
	Content string
	IsNew   bool // true = new file, false = modification
}

// ── LoadContext reads and parses config/context.yaml ─────────────────────────

func LoadContext(path string) (*Context, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read context file: %w", err)
	}

	var ctx Context
	if err := yaml.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse context yaml: %w", err)
	}

	return &ctx, nil
}

// ── BuildSystemPrompt assembles the system prompt from context + session ──────

func BuildSystemPrompt(ctx *Context, sess *state.Session) string {
	var sb strings.Builder

	sb.WriteString("# Project Context\n\n")
	sb.WriteString(fmt.Sprintf("**Project:** %s\n", ctx.Project.Name))
	sb.WriteString(fmt.Sprintf("**Description:** %s\n\n", ctx.Project.Description))

	sb.WriteString("## Stack\n")
	sb.WriteString(fmt.Sprintf("- **Backend:** %s %s", ctx.Stack.Backend.Language, ctx.Stack.Backend.Version))
	if ctx.Stack.Backend.Framework != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", ctx.Stack.Backend.Framework))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("- **Frontend:** %s %s (%s)\n",
		ctx.Stack.Frontend.Language, ctx.Stack.Frontend.Version, ctx.Stack.Frontend.Framework))
	sb.WriteString(fmt.Sprintf("- **Database:** %s\n", ctx.Stack.Infra.Database))
	sb.WriteString(fmt.Sprintf("- **Cache:** %s\n", ctx.Stack.Infra.Cache))
	sb.WriteString(fmt.Sprintf("- **Platform:** %s\n", ctx.Stack.Infra.Platform))
	if ctx.Stack.Infra.Notes != "" {
		sb.WriteString(fmt.Sprintf("- **Platform notes:** %s\n", ctx.Stack.Infra.Notes))
	}
	sb.WriteString("\n")

	// File structure conventions
	if len(ctx.Conventions.FileStructure) > 0 {
		sb.WriteString("## File Structure Conventions\n")
		for area, convention := range ctx.Conventions.FileStructure {
			sb.WriteString(fmt.Sprintf("- **%s:** %s\n", area, convention))
		}
		sb.WriteString("\n")
	}

	// Naming conventions
	if len(ctx.Conventions.Naming) > 0 {
		sb.WriteString("## Naming Conventions\n")
		for thing, convention := range ctx.Conventions.Naming {
			sb.WriteString(fmt.Sprintf("- **%s:** %s\n", thing, convention))
		}
		sb.WriteString("\n")
	}

	// Error handling
	if ctx.Conventions.ErrorHandling != "" {
		sb.WriteString(fmt.Sprintf("## Error Handling\n%s\n\n", ctx.Conventions.ErrorHandling))
	}

	// Anti-patterns
	if len(ctx.AntiPatterns) > 0 {
		sb.WriteString("## NEVER DO THESE\n")
		for _, ap := range ctx.AntiPatterns {
			sb.WriteString(fmt.Sprintf("- %s\n", ap))
		}
		sb.WriteString("\n")
	}

	// SDK notes
	if len(ctx.SDKNotes) > 0 {
		sb.WriteString("## SDK / Library Notes\n")
		for lib, note := range ctx.SDKNotes {
			sb.WriteString(fmt.Sprintf("- **%s:** %s\n", lib, note))
		}
		sb.WriteString("\n")
	}

	// Session: what's already been built
	if sess != nil && len(sess.Artifacts) > 0 {
		sb.WriteString("## Already Built (do not recreate these)\n")
		for _, a := range sess.Artifacts {
			sb.WriteString(fmt.Sprintf("- `%s` (task: %s)\n", a.Path, a.TaskID))
		}
		sb.WriteString("\n")
	}

	// Output format contract — critical for consistent parsing
	sb.WriteString(`## Output Format

You MUST respond with file blocks only. Each file must use this exact format:

` + "```" + `filepath/to/file.go
// file contents here
` + "```" + `

Rules:
- The opening fence must include the file path immediately after the backticks
- One block per file
- No explanation text outside of code blocks
- If modifying an existing file, output the COMPLETE file contents
- Do not truncate or summarise file contents
`)

	return sb.String()
}

// ── Assemble builds the full system + user prompt for a task ─────────────────

func Assemble(contextPath string, task *state.Task, sess *state.Session) (string, string, error) {
	ctx, err := LoadContext(contextPath)
	if err != nil {
		return "", "", err
	}

	systemPrompt := BuildSystemPrompt(ctx, sess)

	// Load task-type template if it exists
	tmplPath := fmt.Sprintf("prompts/%s.tmpl", task.Type)
	userPrompt, err := renderTaskPrompt(tmplPath, task, sess)
	if err != nil {
		// Fall back to simple prompt if no template exists
		userPrompt = buildFallbackPrompt(task)
	}

	return systemPrompt, userPrompt, nil
}

func renderTaskPrompt(tmplPath string, task *state.Task, sess *state.Session) (string, error) {
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("task").Parse(string(tmplData))
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}

	data := map[string]interface{}{
		"Task":    task,
		"Session": sess,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}

	return buf.String(), nil
}

func buildFallbackPrompt(task *state.Task) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task: %s\n\n", task.ID))
	sb.WriteString(fmt.Sprintf("Description: %s\n\n", task.Description))

	if len(task.Inputs) > 0 {
		sb.WriteString("Inputs / dependencies:\n")
		for _, input := range task.Inputs {
			sb.WriteString(fmt.Sprintf("- %s\n", input))
		}
		sb.WriteString("\n")
	}

	if len(task.OutputFiles) > 0 {
		sb.WriteString("Expected output files:\n")
		for _, f := range task.OutputFiles {
			sb.WriteString(fmt.Sprintf("- %s\n", f))
		}
		sb.WriteString("\n")
	}

	if task.Notes != "" {
		sb.WriteString(fmt.Sprintf("Additional notes: %s\n", task.Notes))
	}

	return sb.String()
}

// ── ParseOutput extracts StagedFiles from Claude's response ──────────────────

// Matches ```path/to/file.go\n...contents...\n```
var fileBlockRe = regexp.MustCompile("(?s)```([^\\n`]+)\\n(.*?)```")

func ParseOutput(response string) ([]StagedFile, error) {
	matches := fileBlockRe.FindAllStringSubmatch(response, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no file blocks found in response — Claude may not have followed the output format")
	}

	var files []StagedFile
	for _, m := range matches {
		path := strings.TrimSpace(m[1])
		content := m[2]

		// Skip if path looks like a language tag (e.g. ```go, ```typescript)
		// Real paths contain a slash or a dot
		if !strings.Contains(path, "/") && !strings.Contains(path, ".") {
			continue
		}

		_, statErr := os.Stat(path)
		files = append(files, StagedFile{
			Path:    path,
			Content: content,
			IsNew:   os.IsNotExist(statErr),
		})
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("response contained code blocks but none had valid file paths")
	}

	return files, nil
}
