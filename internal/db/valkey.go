package db

var _ KeyValueRules = valkeyEngine{}

// valkeyEngine is Valkey, the open-source fork of Redis 7.2: Redis's
// rules but for its name and URLs.
type valkeyEngine struct{ redisEngine }

func (valkeyEngine) Label() string { return "Valkey" }
func (valkeyEngine) Base() Engine  { return Redis }

// ColorHex is the blue of Valkey's logo: the project publishes no colour.
func (valkeyEngine) ColorHex() string     { return "#6983ff" }
func (valkeyEngine) Mark() string         { return "VK" }
func (valkeyEngine) URLSchemes() []string { return []string{"valkey", "valkeys"} }

// ClusterDatabases: from Valkey 9.0, with the servers' cluster-databases.
func (valkeyEngine) ClusterDatabases() bool { return true }

// BloomFilterType is valkey-bloom's.
func (valkeyEngine) BloomFilterType() string { return "bloomfltr" }
func (valkeyEngine) NoSearchNote() string {
	return "This server has no Valkey Search: it comes with valkey-bundle, or as the valkey-search module."
}
