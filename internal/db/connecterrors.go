package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"syscall"

	"dgopher/internal/sshtunnel"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
	"golang.org/x/crypto/ssh"
)

// wsaeConnRefused is Windows' WSAECONNREFUSED, which its
// syscall.ECONNREFUSED does not match.
const wsaeConnRefused = syscall.Errno(10061)

// ProblemKind is what a connection error most likely means.
type ProblemKind int

const (
	ProblemUnknown ProblemKind = iota
	ProblemAuth
	ProblemPasswordRequired
	ProblemUnknownDatabase
	ProblemRefused
	ProblemHostNotFound
	ProblemTimeout
	ProblemTLS
	ProblemSSHAuth
	ProblemSSHHostKey
	ProblemTunnelClosed
	ProblemKeychain
	ProblemReadOnlyRefused
)

var problemHints = map[ProblemKind]string{
	ProblemAuth:             "The server rejected the user name or password.",
	ProblemPasswordRequired: "The server needs a password, and none was given.",
	ProblemUnknownDatabase:  "The server has no database by that name.",
	ProblemRefused:          "Nothing is listening at that host and port. Is the server running?",
	ProblemHostNotFound:     "The host name could not be found. Check it, or your VPN or DNS.",
	ProblemTimeout:          "The server did not answer in time. Check the host, port, firewall or VPN.",
	ProblemTLS:              "The server's certificate could not be verified, or TLS failed. Check the TLS settings.",
	ProblemSSHAuth:          "The SSH server rejected the user, key or password.",
	ProblemSSHHostKey:       "The SSH server's host key does not match the one trusted before.",
	ProblemTunnelClosed:     "The SSH tunnel closed, so the connection to the database was lost.",
	ProblemKeychain:         "A saved password could not be read from the keychain. Unlock the keychain, or edit the connection.",
	ProblemReadOnlyRefused:  "This server refuses the read-only setting, as TiDB does: set the connection's engine to TiDB, whose read-only is the app's check.",
}

// Hint is a short plain-language cause and next step, "" for ProblemUnknown.
func (k ProblemKind) Hint() string { return problemHints[k] }

// TunnelClosedError is an SSH tunnel's end after connecting.
type TunnelClosedError struct{ Err error }

func (e *TunnelClosedError) Error() string {
	if e.Err == nil {
		return "SSH tunnel closed"
	}
	return "SSH tunnel closed: " + e.Err.Error()
}
func (e *TunnelClosedError) Unwrap() error { return e.Err }

// SecretReadError is a keychain read that failed for a reason other than absence.
type SecretReadError struct {
	Name string // "password", "ssh-password", ... as secretNames
	Err  error
}

func (e *SecretReadError) Error() string {
	msg := "could not read the saved " + e.Name + " from the keychain"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}
func (e *SecretReadError) Unwrap() error { return e.Err }

// ClassifyConnectError reads what a connect or connection error means; a
// *redactedError answers with the class it kept.
func ClassifyConnectError(err error) ProblemKind {
	if err == nil {
		return ProblemUnknown
	}
	var redacted *redactedError
	if errors.As(err, &redacted) {
		return redacted.kind
	}
	// The wrappers of this package come first: a tunnel that closed on a
	// timeout is a closed tunnel.
	var secretErr *SecretReadError
	var tunnelErr *TunnelClosedError
	var hostKeyErr *sshtunnel.HostKeyError
	var passphraseErr *ssh.PassphraseMissingError
	switch {
	case errors.As(err, &secretErr):
		return ProblemKeychain
	case errors.As(err, &tunnelErr):
		return ProblemTunnelClosed
	case errors.As(err, &hostKeyErr):
		return ProblemSSHHostKey
	case errors.As(err, &passphraseErr), strings.Contains(err.Error(), "ssh: unable to authenticate"):
		return ProblemSSHAuth
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01": // 28000 is also pg_hba.conf's refusal, not a password's
			return ProblemAuth
		case "3D000":
			return ProblemUnknownDatabase
		}
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1045:
			return ProblemAuth
		case 1049:
			return ProblemUnknownDatabase
		case 1235:
			// Not supported: TiDB's refusal of the read-only setting a
			// read-only connection sends.
			if strings.Contains(mysqlErr.Message, "READ ONLY") {
				return ProblemReadOnlyRefused
			}
		}
	}
	var msErr mssql.Error
	if errors.As(err, &msErr) {
		switch msErr.Number {
		case 18456:
			return ProblemAuth
		case 4060:
			return ProblemUnknownDatabase
		}
	}
	var chErr *proto.Exception
	if errors.As(err, &chErr) {
		switch chErr.Code {
		case 516:
			return ProblemAuth
		case 194:
			return ProblemPasswordRequired
		case 81:
			return ProblemUnknownDatabase
		}
	}
	// rueidis reports server errors as text; wrapped, the code may not lead.
	if msg := err.Error(); strings.Contains(msg, "WRONGPASS") {
		return ProblemAuth
	} else if strings.Contains(msg, "NOAUTH") {
		return ProblemPasswordRequired
	}

	var certErr *tls.CertificateVerificationError
	var alertErr tls.AlertError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	if errors.As(err, &certErr) || errors.As(err, &alertErr) || errors.As(err, &authorityErr) ||
		errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) || errors.Is(err, mysql.ErrNoTLS) ||
		// SQL Server's driver words its TLS failures without wrapping them.
		strings.Contains(err.Error(), "TLS Handshake failed") || strings.Contains(err.Error(), "server does not support encryption") {
		return ProblemTLS
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return ProblemHostNotFound
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaeConnRefused) {
		return ProblemRefused
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) || errors.As(err, &netErr) && netErr.Timeout() {
		return ProblemTimeout
	}
	return ProblemUnknown
}
