package config

import (
	"errors"
	"github.com/jackc/pgx/v5"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

type Config struct{ Provider, Listen string }

func ValidateTestDSN(dsn string) error {
	u, err := url.Parse(dsn)
	denied := errors.New("only loopback port 55432 and the synthetic mgar_test database namespace are allowed")
	database := regexp.MustCompile(`^mgar_test(?:_[0-9a-f]{16})?$`)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.Port() != "55432" || !database.MatchString(strings.TrimPrefix(u.Path, "/")) {
		return denied
	}
	params, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return denied
	}
	for key, values := range params {
		if len(values) != 1 {
			return denied
		}
		switch key {
		case "sslmode":
		case "connect_timeout":
			n, e := strconv.Atoi(values[0])
			if e != nil || n < 1 || n > 30 {
				return denied
			}
		default:
			return denied
		}
	}
	// pgx supports service/env settings and query routing overrides. Check the
	// resolved target too, not just the apparent URI host/path.
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		return denied
	}
	if (parsed.Host != "127.0.0.1" && parsed.Host != "localhost") || parsed.Port != 55432 || parsed.Database != strings.TrimPrefix(u.Path, "/") {
		return denied
	}
	for _, fallback := range parsed.Fallbacks {
		if (fallback.Host != "127.0.0.1" && fallback.Host != "localhost") || fallback.Port != 55432 {
			return denied
		}
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
