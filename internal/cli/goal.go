package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thisisnic/lifeo/internal/goal"
)

func goalCmd(dbPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "goal",
		Short: "Add, list and update goals",
	}
	cmd.AddCommand(
		goalAddCmd(dbPath),
		goalListCmd(dbPath),
		goalShowCmd(dbPath),
		goalEditCmd(dbPath),
		goalProgressCmd(dbPath),
		goalMarkCmd(dbPath),
		goalDeleteCmd(dbPath),
	)
	return cmd
}

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("goal id %q: want a positive integer", s)
	}
	return id, nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func goalAddCmd(dbPath *string) *cobra.Command {
	var in goal.NewGoal
	var parent int64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add STATEMENT",
		Short: "Add a goal",
		Long: `Add a goal for a period. The level (year, quarter, month) comes from the
period: 2026, 2026-Q3 or 2026-09. Give --target to make the goal numeric;
otherwise it is a yes/no goal.`,
		Example: `  lifeo goal add "hit the big number" --period 2026 --target 70000 --unit £ --why "..."
  lifeo goal add "finish the garden" --period 2026-Q3 --parent 1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			in.Statement = args[0]
			if cmd.Flags().Changed("parent") {
				in.ParentID = &parent
			}
			g, err := store.Add(cmd.Context(), in)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), g)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added goal %d: %s (%s %s)\n", g.ID, g.Statement, g.Level, g.Period)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.Period, "period", "", "period the goal is for: YYYY, YYYY-Qn or YYYY-MM (required)")
	cmd.Flags().StringVar(&in.Why, "why", "", "why this goal matters")
	cmd.Flags().Int64Var(&parent, "parent", 0, "id of the goal this one sits under")
	cmd.Flags().Float64Var(&in.Target, "target", 0, "numeric target; omit for a yes/no goal")
	cmd.Flags().StringVar(&in.Unit, "unit", "", "unit prefix for the target, e.g. £")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the goal as JSON")
	_ = cmd.MarkFlagRequired("period")
	return cmd
}

func goalListCmd(dbPath *string) *cobra.Command {
	var f goal.Filter
	var level string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List goals as a tree",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if level != "" {
				f.Level = goal.Level(strings.ToLower(level))
				switch f.Level {
				case goal.Year, goal.Quarter, goal.Month:
				default:
					return fmt.Errorf("level %q: want year, quarter or month", level)
				}
			}
			goals, err := store.List(cmd.Context(), f)
			if err != nil {
				return err
			}
			if asJSON {
				if goals == nil {
					goals = []goal.Goal{}
				}
				return writeJSON(cmd.OutOrStdout(), goals)
			}
			if len(goals) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no goals yet. add one with: lifeo goal add \"...\" --period 2026")
				return nil
			}
			printTree(cmd.OutOrStdout(), goals)
			return nil
		},
	}
	cmd.Flags().StringVar(&f.Year, "year", "", "only goals in this year")
	cmd.Flags().StringVar(&level, "level", "", "only goals at this level: year, quarter or month")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func printTree(w io.Writer, goals []goal.Goal) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPERIOD\tGOAL\tPROGRESS\tOUTCOME")
	for _, r := range goal.Flatten(goal.Tree(goals)) {
		g := r.Goal
		fmt.Fprintf(tw, "%d\t%s\t%s%s\t%s\t%s\n",
			g.ID, g.Period, strings.Repeat("  ", r.Depth), g.Statement, progressText(g), string(g.Outcome))
	}
	tw.Flush()
}

func progressText(g goal.Goal) string {
	if g.Kind != goal.Numeric {
		return "yes/no"
	}
	return fmt.Sprintf("%s / %s (%.0f%%)", g.Amount(g.Current), g.Amount(g.Target), g.Percent())
}

type goalDetail struct {
	goal.Goal
	History []goal.Progress `json:"history"`
}

func goalShowCmd(dbPath *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show a goal with its why and progress history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			g, err := store.Get(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("goal %d: %w", id, err)
			}
			hist, err := store.History(cmd.Context(), id)
			if err != nil {
				return err
			}
			if asJSON {
				if hist == nil {
					hist = []goal.Progress{}
				}
				return writeJSON(cmd.OutOrStdout(), goalDetail{Goal: g, History: hist})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "#%d  %s\n", g.ID, g.Statement)
			fmt.Fprintf(out, "period:   %s (%s)\n", g.Period, g.Level)
			if g.ParentID != nil {
				fmt.Fprintf(out, "parent:   #%d\n", *g.ParentID)
			}
			if g.Why != "" {
				fmt.Fprintf(out, "why:      %s\n", g.Why)
			}
			fmt.Fprintf(out, "progress: %s\n", progressText(g))
			if g.Outcome != goal.Unmarked {
				fmt.Fprintf(out, "outcome:  %s\n", g.Outcome)
			}
			if len(hist) > 0 {
				fmt.Fprintln(out, "history:")
				for _, p := range hist {
					line := fmt.Sprintf("  %s  %s", p.RecordedAt.Local().Format("2006-01-02"), g.Amount(p.Value))
					if p.Note != "" {
						line += "  " + p.Note
					}
					fmt.Fprintln(out, line)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func goalEditCmd(dbPath *string) *cobra.Command {
	var statement, why, unit string
	var target float64
	var parent int64
	var clearParent bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Change a goal's statement, why, target, unit or parent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			var e goal.Edit
			if cmd.Flags().Changed("statement") {
				e.Statement = &statement
			}
			if cmd.Flags().Changed("why") {
				e.Why = &why
			}
			if cmd.Flags().Changed("target") {
				e.Target = &target
			}
			if cmd.Flags().Changed("unit") {
				e.Unit = &unit
			}
			if clearParent {
				var none *int64
				e.ParentID = &none
			} else if cmd.Flags().Changed("parent") {
				p := &parent
				e.ParentID = &p
			}
			if e == (goal.Edit{}) {
				return fmt.Errorf("nothing to change; give at least one flag")
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			g, err := store.Update(cmd.Context(), id, e)
			if err != nil {
				return fmt.Errorf("goal %d: %w", id, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated goal %d: %s\n", g.ID, g.Statement)
			return nil
		},
	}
	cmd.Flags().StringVar(&statement, "statement", "", "new statement")
	cmd.Flags().StringVar(&why, "why", "", "new why")
	cmd.Flags().Float64Var(&target, "target", 0, "new target; 0 makes the goal yes/no")
	cmd.Flags().StringVar(&unit, "unit", "", "new unit prefix")
	cmd.Flags().Int64Var(&parent, "parent", 0, "new parent goal id")
	cmd.Flags().BoolVar(&clearParent, "no-parent", false, "remove the parent link")
	return cmd
}

func goalProgressCmd(dbPath *string) *cobra.Command {
	var note string
	cmd := &cobra.Command{
		Use:     "progress ID VALUE",
		Short:   "Record the running total of a numeric goal",
		Example: `  lifeo goal progress 1 35000 --note "end of June"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			value, err := strconv.ParseFloat(strings.ReplaceAll(args[1], ",", ""), 64)
			if err != nil {
				return fmt.Errorf("value %q: want a number", args[1])
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if _, err := store.RecordProgress(cmd.Context(), id, value, note); err != nil {
				return fmt.Errorf("goal %d: %w", id, err)
			}
			g, err := store.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "goal %d now at %s\n", g.ID, progressText(g))
			return nil
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "optional note for this update")
	return cmd
}

func goalMarkCmd(dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "mark ID hit|missed|clear",
		Short: "Mark a goal hit or missed at the end of its period",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			o, err := goal.ParseOutcome(args[1])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.Mark(cmd.Context(), id, o); err != nil {
				return fmt.Errorf("goal %d: %w", id, err)
			}
			if o == goal.Unmarked {
				fmt.Fprintf(cmd.OutOrStdout(), "goal %d unmarked\n", id)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "goal %d marked %s\n", id, o)
			}
			return nil
		},
	}
}

func goalDeleteCmd(dbPath *string) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete ID",
		Short: "Delete a goal and its progress history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			store, err := openStore(dbPath)
			if err != nil {
				return err
			}
			defer store.Close()
			g, err := store.Get(cmd.Context(), id)
			if err != nil {
				return fmt.Errorf("goal %d: %w", id, err)
			}
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "delete goal %d %q and its history? [y/N] ", g.ID, g.Statement)
				var answer string
				fmt.Fscanln(cmd.InOrStdin(), &answer)
				if !strings.HasPrefix(strings.ToLower(answer), "y") {
					fmt.Fprintln(cmd.OutOrStdout(), "kept")
					return nil
				}
			}
			if err := store.Delete(cmd.Context(), id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted goal %d\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}
