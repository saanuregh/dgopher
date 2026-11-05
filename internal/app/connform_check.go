package app

import (
	"errors"
	"strconv"
	"strings"

	"dgopher/internal/db"
)

// formError is a reason the connection form is refused, with the page
// holding the field it is about, which the form switches to.
type formError struct {
	page int
	err  error
}

func (e formError) Error() string { return e.err.Error() }
func (e formError) Unwrap() error { return e.err }

// pageOf is the page holding what err is about: the General page unless
// err says otherwise.
func pageOf(err error) int {
	if fe, ok := errors.AsType[formError](err); ok {
		return fe.page
	}
	return pageGeneral
}

// portProblem is why a port typed in the form is refused, "" when it is
// empty, for the default, or a port.
func portProblem(text, what string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return ""
	}
	if n, err := strconv.Atoi(t); err != nil || n < 1 || n > 65535 {
		return "the " + what + " must be a number from 1 to 65535"
	}
	return ""
}

// portsProblem is why a port of the form is refused, of the fields the
// engine and the choices made show, nil for none.
func (f *connForm) portsProblem(e db.Engine) error {
	if e.IsFile() {
		return nil
	}
	if why := portProblem(f.port, "port"); why != "" {
		return formError{pageGeneral, errors.New(why)}
	}
	if f.cfg.SSH.Enabled {
		if why := portProblem(f.sshPort, "SSH port"); why != "" {
			return formError{pageNetwork, errors.New(why)}
		}
	}
	if f.proxy != 0 {
		if why := portProblem(f.proxyPort, "proxy port"); why != "" {
			return formError{pageNetwork, errors.New(why)}
		}
	}
	return nil
}

// validateForm is cfg.Validate, its error on the Network page when only
// the network's settings fail.
func validateForm(cfg *db.Config) error {
	err := cfg.Validate()
	if err == nil {
		return nil
	}
	rest := *cfg
	rest.SSH, rest.Proxy, rest.CertFile, rest.KeyFile = db.SSHConfig{}, db.ProxyConfig{}, "", ""
	if rest.Validate() == nil {
		return formError{pageNetwork, err}
	}
	return err
}
