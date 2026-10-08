package sqltext

import (
	"strings"
	"testing"
)

var benchChunks = map[Dialect]string{
	Postgres: `-- report
SELECT u.id, u.name, o.total::numeric(10,2), o.meta->>'k' AS k
FROM users u JOIN orders o ON o.user_id = u.id
WHERE u.email LIKE 'a%' AND o.total >= 1.5e3 AND u.tag <> E'x\'y' /* note /* nested */ */
  AND o.body = $fn$ select 1; $fn$ AND u.id = $1 OR o.flags @> '{1,2}'
ORDER BY 2 DESC LIMIT 100;
`,
	MySQL: `# report
SELECT u.id, ` + "`u`.`name`" + `, o.total, @var := 0x1F
FROM users u LEFT JOIN orders o ON o.user_id = u.id
WHERE u.email LIKE "a\"%" AND o.total >= 1.5e3 AND u.tag != 'x\'y' /* c */
  AND o.n <=> NULL -- tail
ORDER BY 2 DESC LIMIT ?;
`,
	ClickHouse: `-- report
SELECT u.id, u.name, sum(o.total) AS t, 1_000 + 0b101
FROM users AS u ANY LEFT JOIN orders AS o ON o.user_id = u.id
WHERE u.email LIKE 'a%' AND o.total >= 1.5e3 AND u.tag != 'x\'y' /* c /* n */ */
  AND o.body = $$ raw $$ AND x = {p:UInt32}
GROUP BY u.id, u.name ORDER BY t DESC LIMIT 100;
`,
	SQLite: `-- report
SELECT u.id, [u name], o.total, :p
FROM users u INNER JOIN orders o ON o.user_id = u.id
WHERE u.email LIKE 'a%' AND o.total >= 1.5e3 AND u.tag || 'x''y' <> 'z' /* c */
  AND o.n IS NOT NULL
ORDER BY 2 DESC LIMIT 100;
`,
}

func BenchmarkTokenize(b *testing.B) {
	for _, d := range []Dialect{Postgres, MySQL, ClickHouse, SQLite} {
		chunk := benchChunks[d]
		src := strings.Repeat(chunk, (1<<20)/len(chunk)+1)
		name := map[Dialect]string{Postgres: "Postgres", MySQL: "MySQL", ClickHouse: "ClickHouse", SQLite: "SQLite"}[d]
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(src)))
			b.ReportAllocs()
			for b.Loop() {
				Tokenize(src, d)
			}
		})
	}
}
