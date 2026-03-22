package runner

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/achgithub/claudio/internal/assembler"
	"github.com/achgithub/claudio/internal/state"

	"github.com/fatih/color"
)

// ── Git ───────────────────────────────────────────────────────────────────────

// GitResult holds the outcome of a git push operation
type GitResult struct {
	Branch    string
	CommitSHA string
	Pushed    bool
}

// CommitToGit stages the given files, commits with a structured message, and pushes.
// Returns a GitResult and a non-nil error only for hard failures (commit failed).
// Push failures are returned in GitResult.Pushed=false so the caller can warn
// without treating the whole task as failed.
func CommitToGit(files []assembler.StagedFile, taskID string) (*GitResult, error) {
	result := &GitResult{}

	// Collect file paths
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}

	// git add <files>
	addArgs := append([]string{"add"}, paths...)
	if out, err := gitCmd(addArgs...); err != nil {
		return nil, fmt.Errorf("git add failed: %w\n%s", err, out)
	}

	// Check there's actually something to commit
	out, _ := gitCmd("status", "--porcelain")
	if strings.TrimSpace(out) == "" {
		color.New(color.FgHiBlack).Println("  (nothing to commit — files unchanged)")
		result.Pushed = false
		return result, nil
	}

	// Build commit message
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("claudio: %s\n\n", taskID))
	msg.WriteString("Files:\n")
	for _, f := range files {
		if f.IsNew {
			msg.WriteString(fmt.Sprintf("  + %s\n", f.Path))
		} else {
			msg.WriteString(fmt.Sprintf("  ~ %s\n", f.Path))
		}
	}
	msg.WriteString("\n[claudio auto-commit]")

	if out, err := gitCmd("commit", "-m", msg.String()); err != nil {
		return nil, fmt.Errorf("git commit failed: %w\n%s", err, out)
	}

	// Capture the commit SHA for the result
	if sha, err := gitCmdOut("rev-parse", "--short", "HEAD"); err == nil {
		result.CommitSHA = strings.TrimSpace(sha)
	}

	// Capture current branch
	if branch, err := gitCmdOut("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		result.Branch = strings.TrimSpace(branch)
	}

	// Push — failure here is a warning, not fatal
	if out, err := gitCmd("push"); err != nil {
		color.New(color.FgYellow).Printf("  ⚠  git push failed: %v\n", err)
		if out != "" {
			color.New(color.FgYellow).Printf("     %s\n", strings.TrimSpace(out))
		}
		color.New(color.FgYellow).Println("     Files committed locally. Push manually when ready.")
		result.Pushed = false
	} else {
		result.Pushed = true
	}

	return result, nil
}

// gitCmd runs a git command, returns combined output and error.
func gitCmd(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// gitCmdOut runs a git command and returns stdout only (for capturing values).
func gitCmdOut(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), err
}

// GitOut is the exported version of gitCmdOut for use by other packages (e.g. main).
func GitOut(args ...string) (string, error) {
	return gitCmdOut(args...)
}

// Stage writes files to the output/ staging directory (not the real project)
func Stage(files []assembler.StagedFile, stagingDir string) error {
	// Clear previous staging
	if err := os.RemoveAll(stagingDir); err != nil {
		return fmt.Errorf("clear staging: %w", err)
	}

	for _, f := range files {
		stagedPath := filepath.Join(stagingDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(stagedPath), 0755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", stagedPath, err)
		}
		if err := os.WriteFile(stagedPath, []byte(f.Content), 0644); err != nil {
			return fmt.Errorf("write staged file %s: %w", stagedPath, err)
		}
	}

	return nil
}

// Preview prints a human-readable diff-style summary of staged files
func Preview(files []assembler.StagedFile) {
	bold := color.New(color.Bold)
	green := color.New(color.FgGreen)
	yellow := color.New(color.FgYellow)
	cyan := color.New(color.FgCyan)

	bold.Println("\n  ── Staged output ────────────────────────────────────────")

	for _, f := range files {
		if f.IsNew {
			green.Printf("  + %s", f.Path)
		} else {
			yellow.Printf("  ~ %s", f.Path)
		}

		lines := strings.Count(f.Content, "\n")
		fmt.Printf("  (%d lines)\n", lines)
	}

	fmt.Println()
	bold.Println("  ── Preview (first 40 lines of each file) ────────────────")

	for _, f := range files {
		cyan.Printf("\n  ▸ %s\n", f.Path)
		fmt.Println(strings.Repeat("  ", 1) + strings.Repeat("─", 56))

		lines := strings.Split(f.Content, "\n")
		limit := 40
		if len(lines) < limit {
			limit = len(lines)
		}

		for i, line := range lines[:limit] {
			fmt.Printf("  %3d  %s\n", i+1, line)
		}

		if len(lines) > 40 {
			color.New(color.FgHiBlack).Printf("\n  ... (%d more lines)\n", len(lines)-40)
		}
	}

	fmt.Println()
}

// Commit writes staged files to the real project and updates session state
func Commit(files []assembler.StagedFile, sess *state.Session) error {
	// Infer task ID from the first file (best effort)
	taskID := inferTaskID(files)

	for _, f := range files {
		// Ensure directory exists
		if err := os.MkdirAll(filepath.Dir(f.Path), 0755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Path, err)
		}

		// Write file
		if err := os.WriteFile(f.Path, []byte(f.Content), 0644); err != nil {
			return fmt.Errorf("write file %s: %w", f.Path, err)
		}

		// Record in session
		checksum := sha256sum(f.Content)
		sess.AddArtifact(f.Path, taskID, checksum)

		color.New(color.FgGreen).Printf("  ✓ wrote %s\n", f.Path)
	}

	return nil
}

// CommitForTask writes files and marks the task complete in session
func CommitForTask(files []assembler.StagedFile, taskID string, sess *state.Session) error {
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.Path), 0755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(f.Path, []byte(f.Content), 0644); err != nil {
			return fmt.Errorf("write file %s: %w", f.Path, err)
		}
		checksum := sha256sum(f.Content)
		sess.AddArtifact(f.Path, taskID, checksum)
		color.New(color.FgGreen).Printf("  ✓ wrote %s\n", f.Path)
	}

	sess.MarkTaskComplete(taskID)
	return nil
}

// WriteLog appends an interaction to a human-readable build log
func WriteLog(taskID, prompt, response string) error {
	if err := os.MkdirAll("session", 0755); err != nil {
		return err
	}

	entry := fmt.Sprintf("=== %s | %s ===\n\nPROMPT:\n%s\n\nRESPONSE:\n%s\n\n",
		taskID, time.Now().Format(time.RFC3339), prompt, response)

	f, err := os.OpenFile("session/build.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(entry)
	return err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func sha256sum(content string) string {
	h := sha256.New()
	h.Write([]byte(content))
	return fmt.Sprintf("%x", h.Sum(nil))[:12]
}

func inferTaskID(files []assembler.StagedFile) string {
	if len(files) == 0 {
		return "unknown"
	}
	// Use the directory of the first file as a rough task ID
	parts := strings.Split(files[0].Path, "/")
	if len(parts) > 1 {
		return parts[len(parts)-2]
	}
	return "unknown"
}
