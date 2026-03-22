package validator

import (
	"fmt"
	"strings"

	"github.com/achgithub/claudio/internal/assembler"
	"github.com/achgithub/claudio/internal/state"
)

// Validate runs all checks on staged files and returns a list of violation messages.
// Non-empty result = warnings (caller decides whether to block or warn).
func Validate(files []assembler.StagedFile, sess *state.Session) []string {
	var violations []string

	for _, f := range files {
		violations = append(violations, checkFile(f, sess)...)
	}

	return violations
}

func checkFile(f assembler.StagedFile, sess *state.Session) []string {
	var v []string

	// Route to language-specific checks
	switch {
	case strings.HasSuffix(f.Path, ".go"):
		v = append(v, checkGo(f, sess)...)
	case strings.HasSuffix(f.Path, ".ts"), strings.HasSuffix(f.Path, ".tsx"):
		v = append(v, checkTypeScript(f)...)
	case strings.HasSuffix(f.Path, ".sql"):
		v = append(v, checkSQL(f)...)
	}

	// Universal checks
	v = append(v, checkUniversal(f)...)

	return v
}

// ── Go checks ─────────────────────────────────────────────────────────────────

func checkGo(f assembler.StagedFile, sess *state.Session) []string {
	var v []string
	c := f.Content

	// Anti-pattern: panic in handlers
	if strings.Contains(f.Path, "handler") && strings.Contains(c, "panic(") {
		v = append(v, fmt.Sprintf("[%s] Contains panic() in a handler — use error returns instead", f.Path))
	}

	// Anti-pattern: global db variable
	if strings.Contains(c, "var db ") || strings.Contains(c, "var DB ") {
		v = append(v, fmt.Sprintf("[%s] Global DB variable detected — use dependency injection", f.Path))
	}

	// Anti-pattern: discarded errors
	if strings.Contains(c, "_ = ") {
		v = append(v, fmt.Sprintf("[%s] Discarded error(s) detected (\"_ =\") — check these are intentional", f.Path))
	}

	// CGO warning for Pi
	cgoPkgs := []string{"\"github.com/mattn/go-sqlite3\"", "\"github.com/lib/pq\""}
	for _, pkg := range cgoPkgs {
		if strings.Contains(c, pkg) {
			v = append(v, fmt.Sprintf("[%s] CGO-dependent package %s detected — verify ARM64 compatibility", f.Path, pkg))
		}
	}

	// Check imports reference known artifacts
	if strings.Contains(c, "import") {
		v = append(v, checkGoImports(f, sess)...)
	}

	// Missing error wrapping
	if strings.Contains(c, "errors.New(") && !strings.Contains(c, "fmt.Errorf") {
		v = append(v, fmt.Sprintf("[%s] Uses errors.New() — prefer fmt.Errorf(\"context: %%w\", err) for wrapping", f.Path))
	}

	return v
}

func checkGoImports(f assembler.StagedFile, sess *state.Session) []string {
	var v []string

	// Build set of known internal paths from session
	known := map[string]bool{}
	for _, a := range sess.Artifacts {
		// Convert file path to import path fragment
		// e.g. internal/models/user.go → internal/models
		parts := strings.Split(a.Path, "/")
		if len(parts) > 1 {
			known[strings.Join(parts[:len(parts)-1], "/")] = true
		}
	}

	// Extract import lines and check internal ones exist
	lines := strings.Split(f.Content, "\n")
	inImport := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "import (" {
			inImport = true
			continue
		}
		if inImport && trimmed == ")" {
			inImport = false
			continue
		}
		if inImport || strings.HasPrefix(trimmed, "import \"") {
			// Check internal imports
			if strings.Contains(trimmed, "github.com/achgithub/claudio/internal/") || strings.Contains(trimmed, f.Path[:strings.Index(f.Path, "/")+1]) {
				// Extract the path
				start := strings.Index(trimmed, "\"")
				end := strings.LastIndex(trimmed, "\"")
				if start != -1 && end > start {
					importPath := trimmed[start+1 : end]
					// Strip module name prefix to get relative path
					if idx := strings.Index(importPath, "internal/"); idx != -1 {
						relPath := importPath[idx:]
						if !known[relPath] && len(sess.Artifacts) > 0 {
							v = append(v, fmt.Sprintf("[%s] Imports %q which may not exist yet — check session state", f.Path, importPath))
						}
					}
				}
			}
		}
	}

	return v
}

// ── TypeScript checks ─────────────────────────────────────────────────────────

func checkTypeScript(f assembler.StagedFile) []string {
	var v []string
	c := f.Content

	// Bare any usage
	if strings.Contains(c, ": any") || strings.Contains(c, "<any>") || strings.Contains(c, "as any") {
		v = append(v, fmt.Sprintf("[%s] Contains 'any' type — add justification comment or use unknown/generics", f.Path))
	}

	// Console.log left in
	if strings.Contains(c, "console.log(") {
		v = append(v, fmt.Sprintf("[%s] Contains console.log() — remove before committing", f.Path))
	}

	// Old react-query v4 API
	if strings.Contains(c, "useQuery(queryKey") || strings.Contains(c, "useQuery([") {
		v = append(v, fmt.Sprintf("[%s] Possible react-query v4 API style — v5 uses useQuery({ queryKey, queryFn })", f.Path))
	}

	return v
}

// ── SQL checks ────────────────────────────────────────────────────────────────

func checkSQL(f assembler.StagedFile) []string {
	var v []string
	c := strings.ToUpper(f.Content)

	// No primary key
	if strings.Contains(c, "CREATE TABLE") && !strings.Contains(c, "PRIMARY KEY") {
		v = append(v, fmt.Sprintf("[%s] CREATE TABLE without PRIMARY KEY", f.Path))
	}

	// Float for money
	if strings.Contains(c, "FLOAT") || strings.Contains(c, "REAL") || strings.Contains(c, "DOUBLE") {
		v = append(v, fmt.Sprintf("[%s] Uses floating point type — use NUMERIC or INTEGER (cents) for monetary values", f.Path))
	}

	return v
}

// ── Universal checks ──────────────────────────────────────────────────────────

func checkUniversal(f assembler.StagedFile) []string {
	var v []string
	c := f.Content

	// Hardcoded secrets
	secretPatterns := []string{
		"password = \"", "secret = \"", "api_key = \"",
		"PASSWORD=\"", "SECRET=\"", "API_KEY=\"",
		"Bearer eyJ", // hardcoded JWT
	}
	for _, p := range secretPatterns {
		if strings.Contains(c, p) {
			v = append(v, fmt.Sprintf("[%s] Possible hardcoded secret: %q", f.Path, p))
		}
	}

	// Truncated output warning
	truncationSignals := []string{"// ...", "// rest of", "// TODO: implement", "/* ... */"}
	for _, sig := range truncationSignals {
		if strings.Contains(c, sig) {
			v = append(v, fmt.Sprintf("[%s] Contains truncation signal %q — Claude may have abbreviated output", f.Path, sig))
		}
	}

	return v
}
