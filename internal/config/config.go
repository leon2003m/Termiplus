// Package config reads settings from the environment. Secrets can be given
// directly (NAME) or as a file path (NAME_FILE), e.g. for Docker secrets.
package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

func Get(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func Must(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("%s is required", name)
	}
	return v
}

func Int(name string, def int64) int64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return n
}

func Duration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return d
}

// Secret reads NAME or NAME_FILE. If required and missing, it exits.
func Secret(name string, required bool) string {
	v := os.Getenv(name)
	f := os.Getenv(name + "_FILE")
	if v != "" && f != "" {
		log.Fatalf("set either %s or %s_FILE, not both", name, name)
	}
	if f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("%s_FILE: %v", name, err)
		}
		v = strings.TrimSpace(string(b))
	}
	if required && len(v) < 32 {
		log.Fatalf("%s is required and must be at least 32 characters", name)
	}
	return v
}
