// Package sshtest runs an SSH server for tests: it logs in with a
// password or a key, and forwards the connections clients ask for, as a
// bastion does.
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Server is an SSH server listening on 127.0.0.1.
type Server struct {
	Addr    string // host:port
	Host    string
	Port    int
	HostKey ssh.Signer // its ed25519 key

	listener  net.Listener
	mu        sync.Mutex
	conns     []net.Conn
	passwords []string
}

// Passwords are the passwords clients tried, in order.
func (s *Server) Passwords() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.passwords)
}

// NewSigner returns a new ed25519 key.
func NewSigner(t testing.TB) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, priv
}

// Start starts a server that logs in with password, or clientKey when
// set, and presents extraHostKeys beside its ed25519 key. It stops when
// the test ends.
func Start(t testing.TB, password string, clientKey ssh.PublicKey, extraHostKeys ...ssh.Signer) *Server {
	t.Helper()
	hostKey, _ := NewSigner(t)
	s := &Server{HostKey: hostKey}
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			s.mu.Lock()
			s.passwords = append(s.passwords, string(pw))
			s.mu.Unlock()
			if string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	config.AddHostKey(hostKey)
	for _, k := range extraHostKeys {
		config.AddHostKey(k)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Addr, s.listener = l.Addr().String(), l
	host, port, _ := net.SplitHostPort(s.Addr)
	s.Host = host
	s.Port, _ = strconv.Atoi(port)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			go s.serve(c, config)
		}
	}()
	t.Cleanup(s.Stop)
	return s
}

// Stop closes the server and every connection to it.
func (s *Server) Stop() {
	s.listener.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
}

func (s *Server) serve(c net.Conn, config *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, config)
	if err != nil {
		c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			newChan.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		var target struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(newChan.ExtraData(), &target); err != nil {
			newChan.Reject(ssh.ConnectionFailed, "bad payload")
			continue
		}
		go func() {
			remote, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
			if err != nil {
				newChan.Reject(ssh.ConnectionFailed, err.Error())
				return
			}
			ch, chReqs, err := newChan.Accept()
			if err != nil {
				remote.Close()
				return
			}
			go ssh.DiscardRequests(chReqs)
			go func() {
				io.Copy(ch, remote)
				ch.CloseWrite()
			}()
			io.Copy(remote, ch)
			remote.(*net.TCPConn).CloseWrite()
		}()
	}
}
