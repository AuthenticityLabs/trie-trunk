package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

type environment map[string]string

func (e environment) lookup(name string) (string, bool) {
	if value, ok := os.LookupEnv(name); ok {
		return value, true
	}
	value, ok := e[name]
	return value, ok
}

func applyEnvironment(flags *flag.FlagSet, names ...string) (environment, error) {
	explicit := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	path := flags.Lookup("env").Value.String()
	env, err := readEnvironment(path, explicit["env"])
	if err != nil {
		return nil, err
	}
	if !explicit["env"] {
		profile, _ := env.lookup("ENVIRONMENT")
		profile = strings.TrimSpace(profile)
		if profile != "" {
			for _, c := range profile {
				if c != '_' && c != '-' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
					return nil, fmt.Errorf("ENVIRONMENT must contain only letters, digits, underscores or hyphens")
				}
			}
			overlay, err := readEnvironment(path+"."+profile, true)
			if err != nil {
				return nil, err
			}
			if env == nil {
				env = make(environment)
			}
			for key, value := range overlay {
				env[key] = value
			}
		}
	}
	for _, name := range names {
		if explicit[name] {
			continue
		}
		key := "TRUNK_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if value, ok := env.lookup(key); ok {
			if err := flags.Set(name, value); err != nil {

				return nil, fmt.Errorf("invalid %s", key)
			}
		}
	}
	return env, nil
}

func readEnvironment(path string, required bool) (environment, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return nil, nil
		}
		return nil, fmt.Errorf("read environment file: %w", err)
	}
	defer file.Close()
	env := make(environment)
	scanner := bufio.NewScanner(file)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "export ") || strings.HasPrefix(text, "export\t") {
			text = strings.TrimSpace(text[len("export"):])
		}
		key, raw, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvironmentKey(key) {
			return nil, fmt.Errorf("%s:%d: expected NAME=value", path, line)
		}
		value, ok := environmentValue(strings.TrimSpace(raw))
		if !ok {
			return nil, fmt.Errorf("%s:%d: invalid quoted value", path, line)
		}
		if _, exists := env[key]; exists {
			return nil, fmt.Errorf("%s:%d: duplicate environment key", path, line)
		}
		env[key] = value
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("%s:%d: cannot read environment file (maximum line length 64 KiB)", path, line+1)
	}
	return env, nil
}

func validEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func environmentValue(raw string) (string, bool) {
	if raw != "" && (raw[0] == '\'' || raw[0] == '"') {
		end := strings.IndexByte(raw[1:], raw[0])
		if end < 0 {
			return "", false
		}
		end++
		tail := strings.TrimSpace(raw[end+1:])
		if tail != "" && !strings.HasPrefix(tail, "#") {
			return "", false
		}
		return raw[1:end], true
	}
	for i, c := range raw {
		if c == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i]), true
		}
	}
	return raw, true
}
