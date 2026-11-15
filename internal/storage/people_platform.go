package storage

import (
	"database/sql"
	"strings"
)

// ResolvePlatform is a stub.
func ResolvePlatform(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, profileID, platform, username string) (string, error) {
	return strings.ToUpper(platform), nil
}
