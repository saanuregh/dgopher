package dataview

import (
	"strings"
	"sync/atomic"

	"dgopher/internal/params"
	"dgopher/internal/project"
)

// queryParamsFile keeps the values each user gave the parameters of
// the editors, by "connection/:name", in the project's own state: a
// value may be no one else's to see.
const queryParamsFile = "query-parameters.json"

// paramsVersion changes as values are kept, so that an editor reads
// them again only then.
var paramsVersion atomic.Int64

// ParamsVersion is the version of the values kept, which changes with
// them.
func ParamsVersion() int64 { return paramsVersion.Load() }

// ParamValues are the values kept for the parameters of a connection's
// editors, by key, each with its kind.
func ParamValues(p *project.Project, connID string) map[string]params.Input {
	out := map[string]params.Input{}
	var all map[string]params.Input
	if p.Local == nil || p.Local.LoadJSON(queryParamsFile, &all) != nil {
		return out
	}
	prefix := connID + "/"
	for k, in := range all {
		if key, ok := strings.CutPrefix(k, prefix); ok {
			if in.Kind == "" {
				in.Kind = params.Kinds[0]
			}
			out[key] = in
		}
	}
	return out
}

// SaveParamValue keeps a parameter's value for a connection's editors:
// a kind chosen before the value is kept too; an empty value of the
// default kind is forgotten.
func SaveParamValue(p *project.Project, connID, key string, in params.Input) error {
	if p.Local == nil {
		return nil
	}
	defer paramsVersion.Add(1)
	all := map[string]params.Input{}
	p.Local.LoadJSON(queryParamsFile, &all)
	if in.Value == "" && (in.Kind == "" || in.Kind == params.Kinds[0]) {
		delete(all, connID+"/"+key)
	} else {
		all[connID+"/"+key] = in
	}
	return p.Local.SaveJSON(queryParamsFile, all)
}
