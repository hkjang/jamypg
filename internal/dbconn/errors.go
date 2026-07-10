package dbconn

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// dbErrCode classifies an execution error into a stable, greppable code:
// context states, PG-<SQLSTATE> for PostgreSQL, MY-<errno> for MySQL/MariaDB.
func dbErrCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELED"
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "PG-" + pgErr.Code
	}
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return fmt.Sprintf("MY-%d", myErr.Number)
	}
	return "INTERNAL"
}

// sanitizeDBError trims driver noise and newlines from error messages so they
// are safe to surface through the API without leaking connection details.
func sanitizeDBError(err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) > 500 {
		msg = msg[:500] + "..."
	}
	return msg
}
