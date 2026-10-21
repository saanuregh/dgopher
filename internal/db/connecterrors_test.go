package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"

	"dgopher/internal/sshtunnel"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestClassifyConnectError reads each driver's and tunnel's failure as the
// problem it most likely means, also when it is wrapped.
func TestClassifyConnectError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ProblemKind
	}{
		{"pg wrong password", &pgconn.PgError{Code: "28P01"}, ProblemAuth},
		{"pg no database", &pgconn.PgError{Code: "3D000"}, ProblemUnknownDatabase},
		{"mysql wrong password", &mysql.MySQLError{Number: 1045}, ProblemAuth},
		{"mysql no database", &mysql.MySQLError{Number: 1049}, ProblemUnknownDatabase},
		{"clickhouse wrong password", &proto.Exception{Code: 516}, ProblemAuth},
		{"clickhouse password required", &proto.Exception{Code: 194}, ProblemPasswordRequired},
		{"clickhouse no database", &proto.Exception{Code: 81}, ProblemUnknownDatabase},
		{"redis wrong password", errors.New("WRONGPASS invalid username-password pair"), ProblemAuth},
		{"redis password required", errors.New("NOAUTH Authentication required."), ProblemPasswordRequired},
		{"refused", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, ProblemRefused},
		{"refused on Windows", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connectex", syscall.Errno(10061))}, ProblemRefused},
		// pg_hba.conf rejections and unknown roles: not a wrong password.
		{"pg 28000", &pgconn.PgError{Code: "28000"}, ProblemUnknown},
		{"host not found", &net.DNSError{Name: "nowhere.invalid", IsNotFound: true}, ProblemHostNotFound},
		{"timeout", context.DeadlineExceeded, ProblemTimeout},
		{"unknown authority", x509.UnknownAuthorityError{}, ProblemTLS},
		{"certificate not verified", &tls.CertificateVerificationError{}, ProblemTLS},
		{"ssh auth", errors.New("ssh: unable to authenticate, attempted methods [none password]"), ProblemSSHAuth},
		{"ssh host key", &sshtunnel.HostKeyError{Host: "bastion:22", Fingerprint: "SHA256:x", Mismatch: true}, ProblemSSHHostKey},
		{"tunnel closed", &TunnelClosedError{Err: errors.New("EOF")}, ProblemTunnelClosed},
		{"keychain", &SecretReadError{Name: "password", Err: errors.New("locked")}, ProblemKeychain},
		{"unknown", errors.New("something else"), ProblemUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyConnectError(c.err); got != c.want {
				t.Errorf("ClassifyConnectError(%v) = %v, want %v", c.err, got, c.want)
			}
			if got := ClassifyConnectError(fmt.Errorf("x: %w", c.err)); got != c.want {
				t.Errorf("wrapped: ClassifyConnectError = %v, want %v", got, c.want)
			}
		})
	}

	if hint := ProblemUnknown.Hint(); hint != "" {
		t.Errorf("ProblemUnknown.Hint() = %q, want empty", hint)
	}

	// A pg error quoting the password is redacted to text only, yet keeps its class.
	cfg := Config{Password: "hunter2secret"}
	quoting := &pgconn.PgError{Code: "28P01", Message: "password hunter2secret rejected"}
	redacted := redact(fmt.Errorf("connect: %w", quoting), cfg)
	var pgErr *pgconn.PgError
	if errors.As(redacted, &pgErr) {
		t.Fatalf("redacted error still reaches the PgError: %v", redacted)
	}
	if got := ClassifyConnectError(redacted); got != ProblemAuth {
		t.Errorf("redacted: ClassifyConnectError = %v, want %v", got, ProblemAuth)
	}
}

// TestHintsForEveryKind gives every known problem a hint to show.
func TestHintsForEveryKind(t *testing.T) {
	for k := ProblemUnknown + 1; k <= ProblemKeychain; k++ {
		if k.Hint() == "" {
			t.Errorf("ProblemKind %d has no hint", k)
		}
	}
}
