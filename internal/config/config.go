package config

import (
	"errors"
	"net/url"
	"strings"
)

type Config struct{ Provider, Listen string }

func ValidateTestDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !strings.HasPrefix(u.Path, "/mgar_test") || strings.Contains(u.Path[1:], "/") {
		return errors.New("only a loopback mgar_test* synthetic database is allowed")
	}
	return nil
}

func Load(get func(string) string) (Config, error) {
	c := Config{Provider: get("MGAR_PROVIDER"), Listen: get("MGAR_LISTEN")}
	if c.Provider == "" {
		c.Provider = "mock"
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Provider != "mock" {
		return c, errors.New("real provider is not enabled; no network call performed")
	}
	if dsn := get("MGAR_TEST_DATABASE_URL"); dsn != "" {
		if err := ValidateTestDSN(dsn); err != nil {
			return c, err
		}
	}
	return c, nil
}
