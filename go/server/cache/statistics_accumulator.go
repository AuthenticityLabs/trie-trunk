package cache

import (
	"fmt"
	"strings"
)

// Dashboard dimensions and exact mergeable duration distributions accompany
// five-minute aggregate columns. They never retain event payloads.
type statisticsGroup struct {
	Period    int64       `pil:"4" json:"period"`
	Labels    [4]string   `pil:"0" json:"labels"`
	Count     uint64      `pil:"1" json:"count"`
	Errors    uint64      `pil:"2,omitempty" json:"errors"`
	Durations [][2]uint64 `pil:"3,omitempty" json:"durations"`
}

type statisticsLayout struct {
	defaults [3]string
	fields   [4]int
}

func compileStatistics(l *Layout) *statisticsLayout {
	s := &statisticsLayout{defaults: [3]string{l.Definition.Resource.Service, fmt.Sprintf("%s.v%d", l.Definition.Resource.Resource, l.Definition.Resource.Major), "Unspecified"}}
	for i, name := range []string{"service", "resource", "operation", "level"} {
		s.fields[i] = fieldIndex(l, name)
	}
	return s
}
func statisticsLabels(l *Layout, branch Branch, f *Frame) [4]string {
	binding := l.statistics
	if binding == nil {
		binding = compileStatistics(l)
	}
	labels := [4]string{binding.defaults[0], binding.defaults[1], binding.defaults[2], [...]string{"INFO", "INFO", "ERROR", "DEBUG", "TRACE"}[branch]}
	for j, i := range binding.fields {
		if i >= 0 {
			if value := statLabel(l.Definition.Fields[i], f.Values[i]); value != "" {
				labels[j] = value
			}
		}
	}
	labels[3] = strings.ToUpper(labels[3])
	return labels
}
