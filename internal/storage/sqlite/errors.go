package sqlite

import (
	"errors"

	"pellets/internal/domain"
)

const (
	sqliteBusy    = 5
	sqliteLocked  = 6
	sqliteCorrupt = 11
	sqliteFormat  = 24
	sqliteNotADB  = 26
)

// stableDatabaseError maps SQLite conditions that cross every repository
// boundary to one public contract. The driver-specific extended result code is
// reduced to its primary result code, while the original cause remains private.
func stableDatabaseError(operation string, err error) error {
	var public *domain.Error
	if errors.As(err, &public) {
		return err
	}

	var sqliteError interface{ Code() int }
	if !errors.As(err, &sqliteError) {
		return nil
	}

	details := map[string]any{"operation": operation}
	switch sqliteError.Code() & 0xff {
	case sqliteBusy, sqliteLocked:
		return domain.WrapError(
			domain.Conflict,
			"database_busy",
			"the Pellets database is busy",
			details,
			err,
		)
	case sqliteCorrupt:
		return domain.WrapError(
			domain.Storage,
			"database_corrupt",
			"the Pellets database is corrupt",
			details,
			err,
		)
	case sqliteFormat, sqliteNotADB:
		return domain.WrapError(
			domain.Storage,
			"database_incompatible",
			"the file is not a compatible Pellets SQLite database",
			details,
			err,
		)
	default:
		return nil
	}
}

func databaseCorruptError(operation string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"database_corrupt",
		"the Pellets database is corrupt",
		map[string]any{"operation": operation},
		err,
	)
}

func databaseIntegrityError(err error) error {
	return domain.WrapError(
		domain.Storage,
		"database_integrity_check_failed",
		"could not verify database integrity",
		map[string]any{"operation": "verify database integrity"},
		err,
	)
}
