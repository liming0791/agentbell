package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
	"github.com/liming0791/agentbell/core/internal/paths"
)

var hookAuditAdapterIDs = []string{"codex", "claude-code", "kimi-code"}

type hookAuditor interface {
	AuditHooks() (hookaudit.Report, error)
}

type namedHookReport struct {
	Adapter string           `json:"adapter"`
	Report  hookaudit.Report `json:"report"`
}

type hookReconcileResult struct {
	Adapter string           `json:"adapter"`
	DryRun  bool             `json:"dryRun"`
	Before  hookaudit.Report `json:"before"`
	Install any              `json:"install"`
	After   hookaudit.Report `json:"after"`
}

func runHook(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(
			"usage: agentbell hook <conflicts|reconcile> [all|codex|claude-code|kimi-code] [--dry-run] [--json]",
		)
	}
	operation := args[0]
	if operation != "conflicts" && operation != "reconcile" {
		return fmt.Errorf("unsupported hook command %q", args[0])
	}
	target := "all"
	flagArgs := args[1:]
	if len(flagArgs) > 0 && !strings.HasPrefix(flagArgs[0], "-") {
		target = flagArgs[0]
		flagArgs = flagArgs[1:]
	}
	flags := flag.NewFlagSet("hook "+operation, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	dryRun := flags.Bool("dry-run", false, "validate without writing")
	if err := flags.Parse(flagArgs); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected hook %s arguments", operation)
	}
	if operation == "conflicts" && *dryRun {
		return errors.New("hook conflicts does not support --dry-run")
	}
	ids := []string{target}
	if target == "all" {
		ids = hookAuditAdapterIDs
	} else if !containsString(hookAuditAdapterIDs, target) {
		return fmt.Errorf("Hook audit is not implemented for adapter %q", target)
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	reports := make([]namedHookReport, 0, len(ids))
	reconciled := make([]hookReconcileResult, 0, len(ids))
	for _, id := range ids {
		selected, err := adapterForID(id, resolved)
		if err != nil {
			return err
		}
		auditor, ok := selected.(hookAuditor)
		if !ok {
			return fmt.Errorf("adapter %q does not support Hook audit", id)
		}
		report, err := auditor.AuditHooks()
		if err != nil {
			return fmt.Errorf("audit %s Hooks: %w", id, err)
		}
		if operation == "conflicts" {
			reports = append(reports, namedHookReport{
				Adapter: id,
				Report:  report,
			})
			continue
		}
		if report.Plan.Blocked {
			return fmt.Errorf(
				"Hook reconcile for %s is blocked by unsafe structure",
				id,
			)
		}
		installResult, err := selected.Install(*dryRun)
		if err != nil {
			return fmt.Errorf("reconcile %s Hooks: %w", id, err)
		}
		after := report
		if !*dryRun {
			after, err = auditor.AuditHooks()
			if err != nil {
				return fmt.Errorf("verify reconciled %s Hooks: %w", id, err)
			}
		}
		reconciled = append(reconciled, hookReconcileResult{
			Adapter: id,
			DryRun:  *dryRun,
			Before:  report,
			Install: installResult,
			After:   after,
		})
	}
	if *asJSON {
		if operation == "reconcile" {
			if len(reconciled) == 1 {
				return writeJSON(stdout, reconciled[0])
			}
			return writeJSON(stdout, reconciled)
		}
		if len(reports) == 1 {
			return writeJSON(stdout, reports[0].Report)
		}
		return writeJSON(stdout, reports)
	}
	if operation == "reconcile" {
		for _, result := range reconciled {
			fmt.Fprintf(
				stdout,
				"%s: dry-run=%t blocked=%t remaining-actions=%d\n",
				result.Adapter,
				result.DryRun,
				result.After.Plan.Blocked,
				len(result.After.Plan.Actions),
			)
		}
		return nil
	}
	for _, report := range reports {
		fmt.Fprintf(
			stdout,
			"%s: current=%d missing=%d owned-legacy=%d duplicate=%d external=%d unsafe=%d blocked=%t actions=%d\n",
			report.Adapter,
			report.Report.Summary.CurrentStableBridge,
			report.Report.Summary.MissingStableBridge,
			report.Report.Summary.OwnedLegacy,
			report.Report.Summary.OwnedDuplicate,
			report.Report.Summary.ExternalSameEvent,
			report.Report.Summary.UnsafeStructure,
			report.Report.Plan.Blocked,
			len(report.Report.Plan.Actions),
		)
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
