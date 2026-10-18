// Package config reads settings from the environment. Secrets can be given
// directly (NAME) or as a file path (NAME_FILE), e.g. for Docker secrets.
package config

import (
	"fmt"
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

// IntIn is Int limited to [lo, hi]; a value outside exits, so limits derived
// from it (shifted, multiplied) cannot overflow.
func IntIn(name string, def, lo, hi int64) int64 {
	n := Int(name, def)
	if n < lo || n > hi {
		log.Fatalf("%s must be between %d and %d", name, lo, hi)
	}
	return n
}

// SmallInt reads an int setting in [lo, hi] (counts, ports); a value outside
// exits.
func SmallInt(name string, def, lo, hi int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < int64(lo) || n > int64(hi) {
		log.Fatalf("%s must be a whole number between %d and %d", name, lo, hi)
	}
	return int(n)
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

// ParseSize reads a byte count: "0", a plain number, or a number with M, G or
// T (binary units), as in HOME_VOLUME_SIZE.
func ParseSize(v string) (int64, error) {
	s := strings.ToUpper(strings.TrimSpace(v))
	shift := 0
	switch {
	case strings.HasSuffix(s, "M"):
		shift = 20
	case strings.HasSuffix(s, "G"):
		shift = 30
	case strings.HasSuffix(s, "T"):
		shift = 40
	}
	if shift > 0 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > (int64(1)<<62)>>shift {
		return 0, fmt.Errorf("size %q must look like 0, 512M, 20G or 1T", v)
	}
	return n << shift, nil
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
