package stop

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSqliteURIEscapesWhatIsNotPath: a ?, #, % (or %41) in a path is part of the name, never URI syntax; the string is
// exact so a wrong file cannot be named by a path component.
func TestSqliteURIEscapesWhatIsNotPath(t *testing.T) {
	cases := []struct{ goos, path, want string }{
		// no special characters: the shape the pin always produced
		{"linux", "/home/u/mono.db", "file:/home/u/mono.db"},
		{"windows", `C:\Users\u\.monoagent\mono.db`, "file:///C:/Users/u/.monoagent/mono.db"},
		{"windows", `\\host\share\mono.db`, "file:////host/share/mono.db"},
		{"windows", `rel\mono.db`, "file:rel/mono.db"},
		{"linux", "rel/mono.db", "file:rel/mono.db"},
		// metacharacters
		{"linux", "/h/a#b/m.db", "file:/h/a%23b/m.db"},
		{"linux", "/h/a?b/m.db", "file:/h/a%3Fb/m.db"},
		{"linux", "/h/a%b/m.db", "file:/h/a%25b/m.db"},
		{"linux", "/h/a%41b/m.db", "file:/h/a%2541b/m.db"},
		{"linux", "/h/a b/m.db", "file:/h/a%20b/m.db"},
		{"linux", `/h/a\b/m.db`, "file:/h/a%5Cb/m.db"},
		{"windows", `C:\a#b\m.db`, "file:///C:/a%23b/m.db"},
		{"windows", `C:\a?b\m%41.db`, "file:///C:/a%3Fb/m%2541.db"},
		{"windows", `C:\a b\m.db`, "file:///C:/a%20b/m.db"},
		{"windows", `\\host\sh#are\m.db`, "file:////host/sh%23are/m.db"},
		{"windows", `rel#\m.db`, "file:rel%23/m.db"},
	}
	for _, c := range cases {
		if got := sqliteURI(c.goos, c.path); got != c.want {
			t.Errorf("sqliteURI(%s, %q) = %q, want %q", c.goos, c.path, got, c.want)
		}
	}
}

// The URI must carry no query or fragment of its own (the pin appends its pragmas with a single "?").
func TestSqliteURIHasNoQueryOrFragment(t *testing.T) {
	for _, p := range []string{"/a#b", "/a?b=c", "/a%3Fb", `/a b#c?d%e`} {
		for _, goos := range []string{"linux", "windows"} {
			if got := sqliteURI(goos, p); strings.ContainsAny(got, "?#") {
				t.Errorf("sqliteURI(%s, %q) = %q holds a ? or #", goos, p, got)
			}
		}
	}
	u, err := url.Parse(sqliteURI("linux", "/a#b?c%41/m.db"))
	if err != nil || u.Path != "/a#b?c%41/m.db" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatalf("round trip: %v %+v", err, u)
	}
}

// A real pin of a database under a directory whose name holds URI metacharacters opens exactly that file.
func TestPinOpensDatabaseUnderMetacharacterDirectory(t *testing.T) {
	names := []string{"a#b", "a%41b", "a b"}
	if runtime.GOOS != "windows" { // ? and a backslash are not valid in a Windows name
		names = append(names, "a?b", `a\b`)
	}
	for _, name := range names {
		dir := filepath.Join(t.TempDir(), name)
		mustNoErr(t, os.Mkdir(dir, 0o700))
		path := filepath.Join(dir, "m.db")
		// what a wrongly parsed URI would create instead (a "#" or "?" cuts the path short)
		cut := filepath.Join(filepath.Dir(dir), "a")
		mustNoErr(t, makeScratchDB(path))
		p, err := Pin(context.Background(), PinOptions{DBPath: path})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		err = p.Do(context.Background(), func(c *sql.Conn, pre func() error) error {
			_, err := c.ExecContext(context.Background(), `INSERT INTO t VALUES (1)`)
			return err
		})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		mustNoErr(t, p.CloseClean())
		if _, err := os.Stat(cut); err == nil {
			t.Fatalf("%q: a file or directory %s appeared", name, cut)
		}
	}
}
