//go:build pellets_test_failure_injection

package main

import (
	"context"
	"os"
	"strings"

	driversqlite "modernc.org/sqlite"
)

const failureInjectionTriggerEnvironment = "PELLETS_TEST_TEMP_TRIGGER"

func init() {
	statement := os.Getenv(failureInjectionTriggerEnvironment)
	if statement == "" {
		return
	}
	driversqlite.RegisterConnectionHook(func(connection driversqlite.ExecQuerierContext, _ string) error {
		_, err := connection.ExecContext(context.Background(), statement, nil)
		// Schema-contract construction opens an empty in-memory database before
		// applying migrations. The trigger belongs only on connections to the
		// already initialized failure-injection fixture.
		if err != nil && strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	})
}
