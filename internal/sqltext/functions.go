package sqltext

import (
	"slices"
	"strings"
)

// The functions completion offers, by dialect: those most queries use,
// not every one an engine has.
var (
	commonFunctions = "abs ceil floor round mod sqrt coalesce nullif greatest least length lower upper trim ltrim rtrim " +
		"substring replace concat count sum avg min max row_number rank dense_rank lag lead first_value last_value ntile cast"

	functionsByDialect = map[Dialect]string{
		Postgres: "trunc power random char_length initcap btrim split_part concat_ws position left right lpad rpad repeat reverse " +
			"md5 format regexp_replace regexp_matches starts_with string_agg array_agg json_agg jsonb_agg json_build_object " +
			"jsonb_build_object jsonb_set jsonb_array_elements jsonb_each to_json to_jsonb row_to_json bool_and bool_or every " +
			"now current_date current_timestamp date_trunc date_part extract age to_char to_date to_timestamp make_date " +
			"make_interval generate_series unnest array_length cardinality percentile_cont percentile_disc mode " +
			"gen_random_uuid pg_size_pretty pg_total_relation_size",
		MySQL: "truncate pow rand ifnull if char_length substring_index concat_ws locate instr left right lpad rpad repeat reverse " +
			"md5 sha2 regexp_replace regexp_like group_concat json_arrayagg json_objectagg json_object json_array json_extract " +
			"json_unquote json_set now curdate current_timestamp date date_format date_add date_sub datediff timestampdiff " +
			"str_to_date unix_timestamp from_unixtime year month day hour minute uuid last_insert_id convert",
		SQLite: "ifnull iif substr instr printf format hex quote random typeof total group_concat json json_extract json_object " +
			"json_array json_group_array json_group_object json_each date time datetime julianday strftime unixepoch",
		ClickHouse: "uniq uniqExact any anyLast argMin argMax groupArray groupUniqArray quantile quantiles median countIf sumIf " +
			"avgIf if multiIf ifNull toDate toDateTime toStartOfDay toStartOfHour toStartOfMonth toYYYYMM now today " +
			"yesterday dateDiff formatDateTime toString toInt64 toFloat64 toDecimal64 replaceAll splitByChar arrayJoin has " +
			"indexOf arrayMap arrayFilter JSONExtract JSONExtractString round intDiv rand generateUUIDv4 cityHash64 toTypeName",
	}
)

// Functions returns the sorted names of the dialect's common functions.
func Functions(d Dialect) []string {
	out := strings.Fields(commonFunctions + " " + functionsByDialect[d])
	slices.Sort(out)
	return slices.Compact(out)
}
