package cache

import (
	"strconv"
	"strings"
)

type bindingPath struct {
	domain, collection string
	major, minor       uint32
}

func splitBinding(key string) *bindingPath {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || !name(parts[0]) {
		return nil
	}
	at := strings.LastIndex(parts[1], ".v")
	if at < 1 || !name(parts[1][:at]) {
		return nil
	}
	major, e := strconv.ParseUint(parts[1][at+2:], 10, 32)
	if e != nil || major == 0 {
		return nil
	}
	minor, e := strconv.ParseUint(parts[2], 10, 32)
	if e != nil {
		return nil
	}
	return &bindingPath{parts[0], parts[1][:at], uint32(major), uint32(minor)}
}
