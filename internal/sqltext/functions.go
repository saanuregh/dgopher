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
		TSQL: "ceiling power square sign rand isnull iif choose len datalength charindex patindex left right replicate reverse " +
			"stuff concat_ws string_agg string_split format quotename translate space str char ascii unicode " +
			"getdate getutcdate sysdatetime sysutcdatetime sysdatetimeoffset current_timestamp dateadd datediff datediff_big " +
			"datename datepart datefromparts datetimefromparts eomonth year month day isdate try_cast try_convert try_parse " +
			"convert newid count_big stdev var checksum hashbytes json_value json_query json_modify isjson openjson " +
			"object_id object_name schema_name db_name db_id user_name suser_sname host_name app_name scope_identity " +
			"ident_current error_message error_number error_line error_severity error_state error_procedure xact_state",
		Oracle: "nvl nvl2 decode lnnvl nanvl trunc power sign exp ln log remainder bitand chr ascii initcap instr substr " +
			"lengthb lpad rpad translate regexp_like regexp_substr regexp_replace regexp_instr regexp_count soundex " +
			"listagg xmlagg json_value json_query json_object json_arrayagg json_table to_char to_date to_number " +
			"to_timestamp to_timestamp_tz to_clob to_nchar to_dsinterval to_yminterval numtodsinterval numtoyminterval " +
			"sysdate systimestamp current_date current_timestamp localtimestamp add_months months_between last_day " +
			"next_day extract from_tz sys_extract_utc sessiontimezone dbtimezone sys_context sys_guid user uid " +
			"ora_hash standard_hash dump vsize hextoraw rawtohex empty_clob empty_blob bfilename median stddev variance " +
			"percentile_cont percentile_disc cume_dist percent_rank ratio_to_report width_bucket",
	}
)

// Functions returns the sorted names of the dialect's common functions.
func Functions(d Dialect) []string {
	out := strings.Fields(commonFunctions + " " + functionsByDialect[d])
	slices.Sort(out)
	return slices.Compact(out)
}
