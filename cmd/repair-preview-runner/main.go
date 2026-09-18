package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	repairpreview "github.com/vincent-wuhan/opskeeper/internal/control/repairpreview"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	dsn := flag.String("dsn", "", "PostgreSQL preview database DSN (required)")
	controlDSN := flag.String("control-dsn", "", "Manager control-plane PostgreSQL DSN (defaults to --dsn)")
	tenantID := flag.String("tenant-id", "", "tenant ID (required)")
	incidentID := flag.String("incident-id", "", "incident ID (required)")
	workloadPath := flag.String("workload", "deploy/repair-preview/pg-pool-workload.yaml", "workload YAML path")
	runID := flag.String("run-id", "", "preview run UUID (required)")
	dryRun := flag.Bool("dry-run", false, "execute and print the run without saving")
	flag.Parse()

	if *dsn == "" || *tenantID == "" || *incidentID == "" || *runID == "" {
		fmt.Fprintln(os.Stderr, "repair-preview-runner: --dsn, --tenant-id, --incident-id, and --run-id are required")
		os.Exit(2)
	}
	workloadData, err := os.ReadFile(*workloadPath)
	if err != nil {
		fail(err)
	}
	spec, err := repairpreview.LoadWorkload(workloadData)
	if err != nil {
		fail(err)
	}
	spec.RuntimeBinding = repairpreview.WorkloadBinding{
		RunID: *runID, TenantID: *tenantID, IncidentID: *incidentID,
	}
	database, err := sqlOpen(*dsn)
	if err != nil {
		fail(err)
	}
	run, err := repairpreview.Execute(context.Background(), database, spec)
	closeErr := database.Close()
	if err != nil {
		fail(err)
	}
	if closeErr != nil {
		fail(closeErr)
	}
	if !*dryRun {
		targetControlDSN := *controlDSN
		if targetControlDSN == "" {
			targetControlDSN = *dsn
		}
		controlDB, err := gorm.Open(postgres.Open(targetControlDSN), &gorm.Config{Logger: logger.Discard})
		if err != nil {
			fail(err)
		}
		if err := repairpreview.Migrate(controlDB); err != nil {
			fail(err)
		}
		if err := repairpreview.NewSQLRepository(controlDB).Save(context.Background(), run); err != nil {
			fail(err)
		}
	}
	if err := printJSON(run); err != nil {
		fail(err)
	}
}

func sqlOpen(dsn string) (*sql.DB, error) {
	return sql.Open("pgx", dsn)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, repairpreview.SanitizeErrorSummary(err.Error()))
	os.Exit(1)
}

func printJSON(run repairpreview.Run) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(run)
}
