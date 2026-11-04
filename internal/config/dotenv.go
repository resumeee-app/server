package config

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv reads key-value configuration pairs from a dotenv formatted file.
// If the specified file does not exist, an empty map is returned without error.
// Existing environment variables are never overwritten.
func LoadDotEnv(filename string) (map[string]string, error) {
	f, err := os.Open(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[string]string), nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	envMap := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		envMap[key] = val
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return envMap, nil
}

// EnvLookup returns an environment variable lookup function that queries the host OS process
// environment first, falling back to the provided dotenv key-value map.
func EnvLookup(envMap map[string]string) func(string) string {
	return func(key string) string {
		if val, ok := os.LookupEnv(key); ok {
			return val
		}
		return envMap[key]
	}
}

// LoadFromDir parses configuration, reading from a local .env file in dir if present,
// and resolving process environment variables as authoritative overrides.
func LoadFromDir(dir string) (*Config, error) {
	envPath := filepath.Join(dir, ".env")
	envMap, err := LoadDotEnv(envPath)
	if err != nil {
		return nil, err
	}

	return LoadFrom(EnvLookup(envMap))
}
