package db

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/binary"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Oracle's tools need Java, which the test server's image lacks, so the
// tests write Oracle wallets themselves, laid out as orapki lays out
// auto-login ones (read from the image's own XDB wallet): a PKCS#12 whose
// one encrypted SafeContents holds the key and the certificates, behind
// a header that keeps its password.

var (
	oidData            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidEncryptedData   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 6}
	oidKeyBag          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 1}
	oidCertBag         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 3}
	oidX509Certificate = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 22, 1}
	oidLocalKeyID      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 21}
	oidPBES2           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACSHA256      = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidAES256CBC       = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oidSHA1            = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
)

// ssoIV is the fixed IV of the auto-login header's password (go-ora's
// configurations.Wallet reads it so).
var ssoIV = []byte{192, 52, 216, 49, 28, 2, 206, 248, 81, 240, 20, 75, 129, 237, 75, 242}

const walletIterations = 5000

type algorithm struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue // under an explicit [0]: explicit0
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values []asn1.RawValue `asn1:"set"`
}

type safeBag struct {
	ID         asn1.ObjectIdentifier
	Value      asn1.RawValue // under an explicit [0]: explicit0
	Attributes []attribute   `asn1:"set,optional"`
}

type certBag struct {
	ID   asn1.ObjectIdentifier
	Data []byte `asn1:"tag:0,explicit"`
}

func mustDER(t testing.TB, v any) []byte {
	t.Helper()
	b, err := asn1.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rawDER(der []byte) asn1.RawValue { return asn1.RawValue{FullBytes: der} }

// explicit0 is der under an explicit [0] tag, which encoding/asn1 does not
// add to a RawValue's FullBytes.
func explicit0(der []byte) asn1.RawValue {
	return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: der}
}

// oracleWallet returns an auto-login wallet, cwallet.sso's bytes: key and
// cert, when key is set, the certificate it logs in with; trusted, the
// certificates it trusts.
func oracleWallet(t testing.TB, key *rsa.PrivateKey, cert *x509.Certificate, trusted ...*x509.Certificate) []byte {
	t.Helper()
	// Oracle's are 7-bit bytes, which the MAC's key takes as BMP
	// characters and PBES2's as they are.
	password := make([]byte, 16)
	rand.Read(password)
	for i := range password {
		password[i] = 'a' + password[i]%26
	}
	localKeyID := []attribute{{Type: oidLocalKeyID, Values: []asn1.RawValue{rawDER(mustDER(t, []byte{1}))}}}
	var bags []safeBag
	if key != nil {
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		bags = append(bags,
			safeBag{ID: oidKeyBag, Value: explicit0(pkcs8), Attributes: localKeyID},
			safeBag{ID: oidCertBag, Value: explicit0(mustDER(t, certBag{ID: oidX509Certificate, Data: cert.Raw})), Attributes: localKeyID})
	}
	for _, c := range trusted {
		bags = append(bags, safeBag{ID: oidCertBag, Value: explicit0(mustDER(t, certBag{ID: oidX509Certificate, Data: c.Raw}))})
	}
	safeContents := mustDER(t, bags)

	salt, iv := make([]byte, 16), make([]byte, 16)
	rand.Read(salt)
	rand.Read(iv)
	aesKey, err := pbkdf2.Key(sha256.New, string(password), salt, walletIterations, 32)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(aesKey)
	pad := aes.BlockSize - len(safeContents)%aes.BlockSize
	plain := append(safeContents, bytes.Repeat([]byte{byte(pad)}, pad)...)
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(encrypted, plain)
	kdf := mustDER(t, struct {
		Salt       []byte
		Iterations int
		PRF        algorithm
	}{salt, walletIterations, algorithm{Algorithm: oidHMACSHA256, Parameters: asn1.NullRawValue}})
	pbes2 := mustDER(t, struct{ KDF, Encryption algorithm }{
		algorithm{Algorithm: oidPBKDF2, Parameters: rawDER(kdf)},
		algorithm{Algorithm: oidAES256CBC, Parameters: rawDER(mustDER(t, iv))},
	})
	encryptedData := mustDER(t, struct {
		Version int
		Info    struct {
			ContentType asn1.ObjectIdentifier
			Algorithm   algorithm
			Content     []byte `asn1:"tag:0"`
		}
	}{Info: struct {
		ContentType asn1.ObjectIdentifier
		Algorithm   algorithm
		Content     []byte `asn1:"tag:0"`
	}{oidData, algorithm{Algorithm: oidPBES2, Parameters: rawDER(pbes2)}, encrypted}})
	authSafe := mustDER(t, []contentInfo{{ContentType: oidEncryptedData, Content: explicit0(encryptedData)}})

	macSalt := make([]byte, 8)
	rand.Read(macSalt)
	mac := hmac.New(sha1.New, pkcs12MACKey(password, macSalt, walletIterations))
	mac.Write(authSafe)
	pfx := mustDER(t, struct {
		Version  int
		AuthSafe contentInfo
		MAC      struct {
			Digest struct {
				Algorithm algorithm
				Digest    []byte
			}
			Salt       []byte
			Iterations int
		}
	}{
		Version:  3,
		AuthSafe: contentInfo{ContentType: oidData, Content: explicit0(mustDER(t, authSafe))},
		MAC: struct {
			Digest struct {
				Algorithm algorithm
				Digest    []byte
			}
			Salt       []byte
			Iterations int
		}{Digest: struct {
			Algorithm algorithm
			Digest    []byte
		}{algorithm{Algorithm: oidSHA1, Parameters: asn1.NullRawValue}, mac.Sum(nil)}, Salt: macSalt, Iterations: walletIterations},
	})

	headerKey := make([]byte, 16)
	rand.Read(headerKey)
	hb, _ := aes.NewCipher(headerKey)
	sealed := make([]byte, len(password))
	cipher.NewCBCEncrypter(hb, ssoIV).CryptBlocks(sealed, password)
	out := []byte{0xa1, 0xf8, 0x4e, 0x36, 0, 0, 0, 6}
	out = binary.BigEndian.AppendUint32(out, uint32(1+len(headerKey)+len(sealed)))
	out = append(out, 6)
	out = append(out, headerKey...)
	out = append(out, sealed...)
	return append(out, pfx...)
}

// pkcs12MACKey derives the key of a PKCS#12 MAC with SHA-1 (RFC 7292,
// appendix B.2).
func pkcs12MACKey(password, salt []byte, iterations int) []byte {
	const u, v = sha1.Size, 64
	bmp := make([]byte, 0, 2*len(password)+2)
	for _, b := range password {
		bmp = append(bmp, 0, b)
	}
	bmp = append(bmp, 0, 0)
	fill := func(s []byte) []byte {
		if len(s) == 0 {
			return nil
		}
		n := v * ((len(s) + v - 1) / v)
		out := make([]byte, n)
		for i := range out {
			out[i] = s[i%len(s)]
		}
		return out
	}
	d := bytes.Repeat([]byte{3}, v)
	in := append(fill(salt), fill(bmp)...)
	h := sha1.New()
	h.Write(d)
	h.Write(in)
	a := h.Sum(nil)
	for range iterations - 1 {
		sum := sha1.Sum(a)
		a = sum[:]
	}
	return a[:u]
}

// oracleTLSDir holds the test server's TLS: its CA, whose key signs the
// tests' client certificates, and the wallet its listener's TCPS takes
// (compose.yaml).
var oracleTLSDir = filepath.Join("testdata", "oracle-tls")

// oracleTestCA reads the test server's CA and its key.
func oracleTestCA(t testing.TB) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(oracleTLSDir, name))
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(b)
		if block == nil {
			t.Fatalf("%s holds no PEM", name)
		}
		return block.Bytes
	}
	cert, err := x509.ParseCertificate(read("ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKCS1PrivateKey(read("ca-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// rsaCertificate makes an RSA key and its certificate, signed by parent,
// self-signed when parent is nil: go-ora reads a wallet's RSA keys alone.
func rsaCertificate(t testing.TB, name string, usage x509.ExtKeyUsage, parent *x509.Certificate, parentKey *rsa.PrivateKey) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(100, 0, 0),
	}
	if parent == nil {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
		tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
		parent, parentKey = tmpl, key
	} else {
		tmpl.DNSNames = []string{name}
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{usage}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// TestWriteOracleTLS writes the test server's CA and wallet again, for
// when they need replacing: DGOPHER_WRITE_ORACLE_TLS=1 go test
// ./internal/db -run TestWriteOracleTLS, then docker compose --profile
// enterprise up -d --force-recreate --wait oracle.
func TestWriteOracleTLS(t *testing.T) {
	if os.Getenv("DGOPHER_WRITE_ORACLE_TLS") == "" {
		t.Skip("set DGOPHER_WRITE_ORACLE_TLS=1 to write the Oracle test server's CA and wallet")
	}
	ca, caKey := rsaCertificate(t, "DGopher test CA", 0, nil, nil)
	server, serverKey := rsaCertificate(t, "localhost", x509.ExtKeyUsageServerAuth, ca, caKey)
	write := func(name string, b []byte) {
		path := filepath.Join(oracleTLSDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// Readable by the server's oracle user, whose id is not the
		// host's.
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))
	write("ca-key.pem", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)}))
	write("server/cwallet.sso", oracleWallet(t, serverKey, server, ca))
}
