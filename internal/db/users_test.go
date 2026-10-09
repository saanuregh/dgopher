package db

import (
	"strings"
	"testing"
)

func TestRevokeOf(t *testing.T) {
	for grant, want := range map[string]string{
		"GRANT SELECT, INSERT ON `shop`.* TO `ada`@`%`":             "REVOKE SELECT, INSERT ON `shop`.* FROM `ada`@`%`",
		"GRANT SELECT ON `shop`.`t` TO `ada`@`%` WITH GRANT OPTION": "REVOKE SELECT ON `shop`.`t` FROM `ada`@`%`",
		"GRANT `reader`@`%` TO `ada`@`%`":                           "REVOKE `reader`@`%` FROM `ada`@`%`",
		"GRANT USAGE ON *.* TO `ada`@`%`":                           "",
		"GRANT SELECT ON default.* TO ada":                          "REVOKE SELECT ON default.* FROM ada",
		"GRANT SELECT ON `to`.` TO ` TO ada":                        "REVOKE SELECT ON `to`.` TO ` FROM ada",
	} {
		if got := revokeOf(grant); got != want {
			t.Errorf("%q: %q, want %q", grant, got, want)
		}
	}
}

func TestAccountSQL(t *testing.T) {
	pg, my, ch := DialectOf(Postgres), DialectOf(MySQL), DialectOf(ClickHouse)
	ada := Account{Name: "ada", Host: "%"}
	sql, err := CreateAccountSQL(pg, NewAccount{Account: Account{Name: "ada"}, Password: "s3cret", CreateDB: true})
	if err != nil || !strings.HasPrefix(sql, `CREATE ROLE "ada" WITH LOGIN CREATEDB PASSWORD 'SCRAM-SHA-256$4096:`) || strings.Contains(sql, "s3cret") {
		t.Errorf("postgres: %s %v", sql, err)
	}
	if sql, _ := CreateAccountSQL(pg, NewAccount{Account: Account{Name: "ada"}, Password: "pässword"}); !strings.HasSuffix(sql, `PASSWORD 'pässword'`) {
		t.Errorf("postgres, not ASCII: %s", sql)
	}
	if sql, _ := CreateAccountSQL(my, NewAccount{Account: ada, Password: `a'b\c`}); sql != `CREATE USER 'ada'@'%' IDENTIFIED BY 'a''b\\c'` {
		t.Errorf("mysql: %s", sql)
	}
	if sql, _ := CreateAccountSQL(ch, NewAccount{Account: Account{Name: "reader", Role: true}}); sql != "CREATE ROLE `reader`" {
		t.Errorf("clickhouse role: %s", sql)
	}
	if got, _ := GrantSQL(pg, []string{"SELECT"}, "shop", "", Account{Name: "ada"}); len(got) != 2 || got[1] != `GRANT SELECT ON ALL TABLES IN SCHEMA "shop" TO "ada"` {
		t.Errorf("grant: %q", got)
	}
	if got := DropAccountSQL(my, ada); len(got) != 1 || got[0] != `DROP USER 'ada'@'%'` {
		t.Errorf("drop: %q", got)
	}
	if got := DropAccountSQL(pg, Account{Name: "ada"}); len(got) != 3 || got[0] != `REASSIGN OWNED BY "ada" TO CURRENT_USER` {
		t.Errorf("drop on postgres: %q", got)
	}
}

// The verifier of RFC 7677's example password and salt, as PostgreSQL
// would keep it.
func TestScramVerifier(t *testing.T) {
	salt := []byte{0x5b, 0x6c, 0x4d, 0x0d, 0x48, 0x9b, 0xda, 0x88, 0x01, 0x5e, 0x35, 0x2c, 0xf8, 0x72, 0x88, 0x05}
	got, err := scramVerifier("pencil", salt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "SCRAM-SHA-256$4096:W2xNDUib2ogBXjUs+HKIBQ==$") || strings.Count(got, ":") != 2 {
		t.Fatalf("verifier %s", got)
	}
}
