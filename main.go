package main

import (
	"fmt"
	"os"
	"strings"

	"claudio/internal/anthropic"
	"claudio/internal/assembler"
	"claudio/internal/runner"
	"claudio/internal/state"
	"claudio/internal/validator"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "claudio",
	Short: "Claudio — structured Claude orchestrator for your full-stack build",
	Long: `Claudio is a build orchestrator that injects your canonical project context
into every Claude API call, validates output before writing files, and tracks
session state across your entire build.`,
}

// task command — the main workhorse
var taskCmd = &cobra.Command{
	Use:   "task [task-id]",
	Short: "Run a task from tasks.yaml",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		taskID := args[0]
		noGit, _ := cmd.Flags().GetBool("no-git")
		return runTask(taskID, !noGit)
	},
}

// run command — run a free-form prompt with context injected
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a free-form prompt with full context injected",
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		if prompt == "" {
			return fmt.Errorf("--prompt is required")
		}
		return runFreeform(prompt)
	},
}

// status command — show session state
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current session state and built artifacts",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showStatus()
	},
}

// git-status command — show recent claudio commits
var gitStatusCmd = &cobra.Command{
	Use:   "git-status",
	Short: "Show recent claudio commits and unpushed changes",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showGitStatus()
	},
}

// init command — initialise a new claudio project
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialise claudio in the current project (creates config/context.yaml and tasks.yaml)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return initProject()
	},
}

func init() {
	taskCmd.Flags().Bool("no-git", false, "Skip git commit and push after accepting output")
	runCmd.Flags().StringP("prompt", "p", "", "The prompt to send (with context injected)")
	rootCmd.AddCommand(taskCmd, runCmd, statusCmd, gitStatusCmd, initCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// ── Command implementations ──────────────────────────────────────────────────

func runTask(taskID string, withGit bool) error {
	bold := color.New(color.Bold)
	green := color.New(color.FgGreen)
	yellow := color.New(color.FgYellow)
	red := color.New(color.FgRed)

	bold.Printf("\n▶  claudio task: %s\n\n", taskID)

	// 1. Load session state
	fmt.Print("  Loading session state... ")
	sess, err := state.Load("session/session.json")
	if err != nil {
		return fmt.Errorf("session load failed: %w", err)
	}
	green.Println("✓")

	// 2. Load and resolve the task
	fmt.Print("  Resolving task... ")
	task, err := state.ResolveTask("tasks/tasks.yaml", taskID, sess)
	if err != nil {
		return fmt.Errorf("task resolution failed: %w", err)
	}
	green.Println("✓")

	// 3. Assemble prompt
	fmt.Print("  Assembling prompt... ")
	systemPrompt, userPrompt, err := assembler.Assemble("config/context.yaml", task, sess)
	if err != nil {
		return fmt.Errorf("prompt assembly failed: %w", err)
	}
	green.Println("✓")

	// 4. Call Claude
	fmt.Print("  Calling Claude API... ")
	client := anthropic.NewClient(os.Getenv("ANTHROPIC_API_KEY"))
	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("Claude API call failed: %w", err)
	}
	green.Println("✓")

	// 5. Parse output into files
	fmt.Print("  Parsing output... ")
	files, err := assembler.ParseOutput(response)
	if err != nil {
		return fmt.Errorf("output parsing failed: %w", err)
	}
	green.Printf("✓  (%d files)\n\n", len(files))

	// 6. Validate
	fmt.Println("  Running validators...")
	violations := validator.Validate(files, sess)
	if len(violations) > 0 {
		yellow.Println("\n  ⚠  Validation warnings:")
		for _, v := range violations {
			fmt.Printf("     • %s\n", v)
		}
		fmt.Println()
	}

	// 7. Stage and preview
	if err := runner.Stage(files, "output/"); err != nil {
		return fmt.Errorf("staging failed: %w", err)
	}
	runner.Preview(files)

	// 8. Interactive approval
	fmt.Println()
	bold.Print("  Accept / Reject / Retry with feedback? > ")
	var decision string
	fmt.Scanln(&decision)

	switch decision {
	case "accept", "a":
		// Write files + update session
		if err := runner.CommitForTask(files, taskID, sess); err != nil {
			return fmt.Errorf("commit failed: %w", err)
		}
		if err := state.Save(sess, "session/session.json"); err != nil {
			return fmt.Errorf("session save failed: %w", err)
		}
		green.Println("\n  ✓  Files written and session updated.")

		// Git commit + push
		if withGit {
			fmt.Println()
			bold.Println("  ── Git ──────────────────────────────────────────────")
			result, err := runner.CommitToGit(files, taskID)
			if err != nil {
				// Hard git failure (e.g. commit failed) — warn but don't unwind
				yellow.Printf("  ⚠  Git error: %v\n", err)
				yellow.Println("     Files are written. Commit manually when ready.")
			} else if result.Pushed {
				green.Printf("  ✓  Committed and pushed")
				if result.CommitSHA != "" {
					color.New(color.FgHiBlack).Printf(" [%s → %s]", result.CommitSHA, result.Branch)
				}
				fmt.Println()
			}
		} else {
			yellow.Println("  (git skipped — --no-git flag set)")
		}

	case "reject", "r":
		red.Println("\n  ✗  Rejected. No files written.")

	default:
		// Treat as retry feedback
		feedback := decision
		yellow.Printf("\n  ↺  Retrying with feedback: %q\n", feedback)
		return runTaskWithFeedback(taskID, feedback, systemPrompt, withGit)
	}

	return nil
}

func runTaskWithFeedback(taskID, feedback, systemPrompt string, withGit bool) error {
	client := anthropic.NewClient(os.Getenv("ANTHROPIC_API_KEY"))
	retryPrompt := fmt.Sprintf(
		"The previous output was rejected. Feedback: %s\n\nPlease regenerate the task output addressing this feedback.",
		feedback,
	)
	response, err := client.Complete(systemPrompt, retryPrompt)
	if err != nil {
		return err
	}
	files, err := assembler.ParseOutput(response)
	if err != nil {
		return err
	}
	runner.Preview(files)
	return nil
}

func runFreeform(prompt string) error {
	fmt.Print("  Loading context... ")
	ctx, err := assembler.LoadContext("config/context.yaml")
	if err != nil {
		return err
	}
	color.New(color.FgGreen).Println("✓")

	sess, _ := state.Load("session/session.json")
	systemPrompt := assembler.BuildSystemPrompt(ctx, sess)

	client := anthropic.NewClient(os.Getenv("ANTHROPIC_API_KEY"))
	response, err := client.Complete(systemPrompt, prompt)
	if err != nil {
		return err
	}

	fmt.Println("\n── Claude response ──────────────────────────────────────")
	fmt.Println(response)
	fmt.Println("─────────────────────────────────────────────────────────")
	return nil
}

func showStatus() error {
	sess, err := state.Load("session/session.json")
	if err != nil {
		return err
	}
	state.PrintStatus(sess)
	return nil
}

func showGitStatus() error {
	bold := color.New(color.Bold)
	green := color.New(color.FgGreen)
	yellow := color.New(color.FgYellow)
	cyan := color.New(color.FgCyan)

	bold.Println("\n  ── Git status ───────────────────────────────────────────")

	// Current branch
	branch, err := runner.GitOut("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("not a git repo: %w", err)
	}
	fmt.Printf("  Branch: ")
	cyan.Println(strings.TrimSpace(branch))

	// Unpushed commits
	unpushed, _ := runner.GitOut("log", "@{u}..", "--oneline")
	unpushed = strings.TrimSpace(unpushed)
	if unpushed == "" {
		green.Println("  ✓  All commits pushed")
	} else {
		yellow.Println("  Unpushed commits:")
		for _, line := range strings.Split(unpushed, "\n") {
			fmt.Printf("     %s\n", line)
		}
	}

	// Recent claudio commits
	bold.Println("\n  Recent claudio commits:")
	log, _ := runner.GitOut("log", "--oneline", "--grep=claudio:", "-10")
	if strings.TrimSpace(log) == "" {
		color.New(color.FgHiBlack).Println("  (none yet)")
	} else {
		for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
			fmt.Printf("  %s\n", line)
		}
	}

	fmt.Println()
	return nil
}

func initProject() error {
	return state.InitProject()
}
