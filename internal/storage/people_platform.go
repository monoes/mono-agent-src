package storage

import (
	"database/sql"
	"errors"
	"strings"
)

// rowQuerier is what *sql.DB and *sql.Tx both offer.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// storedPlatform is the spelling of the platform a person already has under
// this username (compared exactly, as the unique key does) in this profile,
// whatever the case of platform asked for. An exact-spelling row wins, then
// the most recently updated one. ok is false when there is no such person.
func storedPlatform(q rowQuerier, profileID, platform, username string) (spelling string, ok bool, err error) {
	err = q.QueryRow(`SELECT platform FROM people
		WHERE profile_id = ? AND platform_username = ? AND UPPER(platform) = UPPER(?)
		ORDER BY (platform = ?) DESC, updated_at DESC LIMIT 1`,
		profileID, username, platform, platform).Scan(&spelling)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return spelling, true, nil
}

// ResolvePlatform is the platform spelling a write for this person must use:
// the one the existing row already has (so the write updates that row instead
// of adding a twin that differs only in case), and for a new person the
// canonical spelling, upper case, which is what workflow nodes write and what
// the readers (people list, people.save) compare against.
func ResolvePlatform(q rowQuerier, profileID, platform, username string) (string, error) {
	spelling, ok, err := storedPlatform(q, profileID, platform, username)
	if err != nil {
		return "", err
	}
	if ok {
		return spelling, nil
	}
	return strings.ToUpper(platform), nil
}
