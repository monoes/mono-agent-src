package connections

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	goftp "github.com/jlaffaye/ftp"
	"golang.org/x/crypto/ssh"

	"github.com/monoes/mono-agent/internal/credhttp"
	"github.com/monoes/mono-agent/internal/sshhosts"
)

// ValidateConnection calls the platform-specific validator and returns the
// resolved account identifier (username/email/org name). Returns ("", nil)
// for platforms with no validation or browser-session platforms.
func ValidateConnection(ctx context.Context, c *Connection) (accountID string, err error) {
	switch c.Platform {
	case "github":
		return validateGitHub(ctx, c)
	case "notion":
		return validateNotion(ctx, c)
	case "airtable":
		return validateAirtable(ctx, c)
	case "jira":
		return validateJira(ctx, c)
	case "linear":
		return validateLinear(ctx, c)
	case "stripe":
		return validateStripe(ctx, c)
	case "slack":
		return validateSlack(ctx, c)
	case "discord":
		return validateDiscord(ctx, c)
	case "bluesky":
		return validateBluesky(ctx, c)
	case "reddit":
		return validateReddit(ctx, c)
	case "mastodon":
		return validateMastodon(ctx, c)
	case "twilio":
		return validateTwilio(ctx, c)
	case "telegram":
		return validateTelegram(ctx, c)
	case "google_sheets", "google_drive", "gmail":
		return validateGoogle(ctx, c)
	case "youtube":
		return validateYouTube(ctx, c)
	case "google":
		return validateGoogleAccount(ctx, c)
	case "google_service_account":
		return validateGoogleServiceAccount(c)
	case "microsoft":
		return validateMicrosoftAccount(ctx, c)
	case "outlook":
		if c.Method == MethodOAuth {
			return validateOutlookOAuth(ctx, c)
		}
		return validateOutlookAppPassword(ctx, c)
	case "hubspot":
		return validateHubSpot(ctx, c)
	case "salesforce":
		return validateSalesforce(ctx, c)
	case "devto":
		return validateDevTo(ctx, c)
	case "hashnode":
		return validateHashnode(ctx, c)
	case "producthunt":
		return validateProductHunt(ctx, c)
	case "ssh", "sftp":
		return validateSSH(ctx, c)
	case "ftp":
		return validateFTP(ctx, c)
	case "generic_api", "generic_basic":
		baseURL := getStr(c.Data, "base_url")
		if baseURL == "" {
			return "", fmt.Errorf("validate %s: missing base_url", c.Platform)
		}
		return baseURL, nil
	case "postgresql", "mysql", "mongodb", "redis":
		cs := getStr(c.Data, "connection_string")
		if cs == "" {
			return "", fmt.Errorf("validate %s: missing connection_string", c.Platform)
		}
		// The connection string embeds the DB password; never return it
		// verbatim — AccountID/Label survive Redact() and reach `connect
		// list --json` and GUI output. url.URL.Redacted() masks the
		// password while keeping the useful host/user/db identifier.
		if u, err := url.Parse(cs); err == nil && u.Host != "" {
			return u.Redacted(), nil
		}
		// Unparseable (e.g. key=value DSN) — a generic label rather than
		// risk leaking a password embedded somewhere in the raw string.
		return c.Platform + " database", nil
	default:
		return "", nil
	}
}

// validateSSH validates an SSH/SFTP connection by performing a live SSH
// handshake and authentication (no remote command is run) — without this,
// any garbage host/username/password/private_key gets saved as "connected"
// and only fails later when a workflow node tries to actually use it.
func validateSSH(ctx context.Context, c *Connection) (string, error) {
	host := getStr(c.Data, "host")
	username := getStr(c.Data, "username")
	if host == "" || username == "" {
		return "", fmt.Errorf("validateSSH: host and username are required")
	}

	port := 22
	if p := getStr(c.Data, "port"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	var authMethods []ssh.AuthMethod
	if privateKeyPEM := getStr(c.Data, "private_key"); privateKeyPEM != "" {
		var signer ssh.Signer
		var err error
		if passphrase := getStr(c.Data, "passphrase"); passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(privateKeyPEM), []byte(passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(privateKeyPEM))
		}
		if err != nil {
			return "", fmt.Errorf("validateSSH: parse private key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if password := getStr(c.Data, "password"); password != "" {
		authMethods = append(authMethods, ssh.Password(password))
	}
	if len(authMethods) == 0 {
		return "", fmt.Errorf("validateSSH: password or private_key is required")
	}

	// Mirrors internal/nodes/http/ssh.go: verify against a known_hosts file
	// when given, otherwise skip host key verification for this liveness check.
	hostKeyCallback := ssh.InsecureIgnoreHostKey() //nolint:gosec
	if knownHosts := getStr(c.Data, "known_hosts"); knownHosts != "" {
		cb, err := sshhosts.New(knownHosts)
		if err != nil {
			return "", fmt.Errorf("validateSSH: parse known_hosts: %w", err)
		}
		hostKeyCallback = cb
	}

	cfg := &ssh.ClientConfig{
		User:            username,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("validateSSH: dial: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
	if err != nil {
		netConn.Close()
		return "", fmt.Errorf("validateSSH: handshake: %w", err)
	}
	ssh.NewClient(sshConn, chans, reqs).Close()

	return username + "@" + host, nil
}

// validateFTP validates an FTP connection by performing a live dial and
// login, mirroring the check internal/nodes/http/ftp.go performs at run time.
func validateFTP(ctx context.Context, c *Connection) (string, error) {
	host := getStr(c.Data, "host")
	username := getStr(c.Data, "username")
	password := getStr(c.Data, "password")
	if host == "" || username == "" {
		return "", fmt.Errorf("validateFTP: host and username are required")
	}

	port := 21
	if p := getStr(c.Data, "port"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := goftp.Dial(addr, goftp.DialWithTimeout(15*time.Second), goftp.DialWithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("validateFTP: dial: %w", err)
	}
	defer conn.Quit()

	if err := conn.Login(username, password); err != nil {
		return "", fmt.Errorf("validateFTP: login: %w", err)
	}

	return username + "@" + host, nil
}

// validateOutlookAppPassword validates an Outlook/Hotmail app-password
// connection by performing a live IMAP LOGIN — without this, any garbage
// email/password gets saved as a "connected" credential that only fails
// later when an actual workflow node tries to use it. Note: Microsoft has
// deprecated Basic Auth for IMAP on outlook.com/hotmail.com accounts, so
// this will legitimately fail for those even with correct credentials —
// use OAuth instead for those accounts.
func validateOutlookAppPassword(ctx context.Context, c *Connection) (string, error) {
	email := getStr(c.Data, "email")
	password := getStr(c.Data, "app_password")
	if email == "" || password == "" {
		return "", fmt.Errorf("validateOutlookAppPassword: email and app_password are required")
	}

	const host = "outlook.office365.com"
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 15 * time.Second},
		Config:    &tls.Config{ServerName: host},
	}
	conn, err := dialer.DialContext(ctx, "tcp", host+":993")
	if err != nil {
		return "", fmt.Errorf("validateOutlookAppPassword: connect: %w", err)
	}
	tc := textproto.NewConn(conn)
	defer tc.Close()

	if _, err := tc.ReadLine(); err != nil {
		return "", fmt.Errorf("validateOutlookAppPassword: greeting: %w", err)
	}
	if _, err := fmt.Fprintf(conn, "A1 LOGIN %q %q\r\n", email, password); err != nil {
		return "", fmt.Errorf("validateOutlookAppPassword: login send: %w", err)
	}
	for {
		line, err := tc.ReadLine()
		if err != nil {
			return "", fmt.Errorf("validateOutlookAppPassword: login: %w", err)
		}
		if strings.HasPrefix(line, "A1 OK") {
			return email, nil
		}
		if strings.HasPrefix(line, "A1 NO") || strings.HasPrefix(line, "A1 BAD") {
			return "", fmt.Errorf("validateOutlookAppPassword: IMAP login rejected: %s", line)
		}
	}
}

// getStr extracts a string value from a map, returning "" if missing or not a string.
func getStr(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// validateHTTP sends every validation request: each carries the
// connection's token or password, so a redirect elsewhere gets none of it
// (credhttp).
var validateHTTP = credhttp.Client(nil)

// doGET makes a GET request with the given Authorization header (empty = no header),
// sets Accept: application/json, and returns body bytes, HTTP status code, and any error.
func doGET(ctx context.Context, url, authHeader string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("doGET: create request: %w", err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := validateHTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("doGET: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("doGET: read body: %w", err)
	}
	return body, resp.StatusCode, nil
}
