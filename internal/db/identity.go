package db

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"dgopher/internal/secretcmd"
)

// Identity is a cloud identity a connection logs in with: a short-lived
// token its cloud's command-line tool prints, in place of a password.
type Identity string

const (
	IdentityAWS   Identity = "aws-rds-iam"
	IdentityGCP   Identity = "gcp-cloud-sql-iam"
	IdentityAzure Identity = "azure-entra-id"
)

// Identities lists the identities in the order the form offers them.
func Identities() []Identity { return []Identity{IdentityAWS, IdentityGCP, IdentityAzure} }

// Label names an identity.
func (i Identity) Label() string {
	switch i {
	case IdentityAWS:
		return "AWS IAM (RDS, Aurora)"
	case IdentityGCP:
		return "Google Cloud IAM (Cloud SQL)"
	case IdentityAzure:
		return "Microsoft Entra ID (Azure)"
	}
	return string(i)
}

// IdentityCommand is the command a connection's cloud identity runs to
// print its token, word for word, as the app asks the user to agree to it;
// nil without an identity. It runs without a shell; the values of the
// connection are words of their own.
func IdentityCommand(cfg *Config) []string {
	switch cfg.Identity {
	case IdentityAWS:
		argv := []string{"aws", "rds", "generate-db-auth-token", "--hostname", cfg.Host, "--port", strconv.Itoa(cfg.port()), "--username", cfg.User}
		if cfg.IdentityRegion != "" {
			argv = append(argv, "--region", cfg.IdentityRegion)
		}
		if cfg.IdentityProfile != "" {
			argv = append(argv, "--profile", cfg.IdentityProfile)
		}
		return argv
	case IdentityGCP:
		return []string{"gcloud", "sql", "generate-login-token"}
	case IdentityAzure:
		return []string{"az", "account", "get-access-token", "--resource-type", "oss-rdbms", "--query", "accessToken", "--output", "tsv"}
	}
	return nil
}

// tokenReuse is how long a token serves new connections: shorter than
// the tokens last, AWS's fifteen minutes being the shortest.
const tokenReuse = 10 * time.Minute

var tokens = struct {
	sync.Mutex
	byCommand map[string]token
}{byCommand: map[string]token{}}

type token struct {
	value string
	made  time.Time
}

// identityToken is the token a connection logs in with, made again once
// it is old: each new connection of a pool asks, long after the first.
func identityToken(ctx context.Context, cfg Config) (string, error) {
	argv := IdentityCommand(&cfg)
	key := strings.Join(argv, "\x00")
	tokens.Lock()
	t, ok := tokens.byCommand[key]
	tokens.Unlock()
	if ok && time.Since(t.made) < tokenReuse {
		return t.value, nil
	}
	value, err := secretcmd.RunArgv(ctx, argv)
	if err != nil {
		return "", fmt.Errorf("%s: %w", cfg.Identity.Label(), err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s: %s printed no token", cfg.Identity.Label(), argv[0])
	}
	tokens.Lock()
	tokens.byCommand[key] = token{value: value, made: time.Now()}
	tokens.Unlock()
	return value, nil
}
