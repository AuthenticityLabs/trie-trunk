package search

import (
	"net/url"
	"strconv"
	"time"
)

// Query encodes the collection selection without serializing events or a JSON
// request body. A nil limit is explicitly unbounded; omission defaults to 100.
func (c Config) Query() url.Values {
	values := url.Values{
		"tap":    {c.Tap},
		"from":   {c.Start.Format(time.RFC3339Nano)},
		"tail":   {strconv.FormatBool(c.Tail)},
		"follow": {strconv.FormatBool(c.Follow)},
		"limit":  {"all"},
	}
	if !c.End.IsZero() && !(c.Follow && c.End.Equal(time.UnixMilli(1<<48))) {
		values.Set("to", c.End.Format(time.RFC3339Nano))
	}
	if c.Limit != nil {
		values.Set("limit", strconv.FormatUint(*c.Limit, 10))
	}
	for _, clause := range c.Where {
		values.Add("where", clause)
	}
	if c.Headers != nil {
		values["headers"] = c.Headers
		if len(c.Headers) == 0 {
			values.Set("headers", "")
		}
	}
	return values
}
