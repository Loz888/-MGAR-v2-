package config

import "testing"

func TestOfflineDefaults(t *testing.T) {
	c, err := Load(func(string) string { return "" })
	if err != nil || c.Provider != "mock" {
		t.Fatalf("offline default: %+v %v", c, err)
	}
}
func TestRealProviderDeniedWithoutCredential(t *testing.T) {
	_, err := Load(func(k string) string {
		if k == "MGAR_PROVIDER" {
			return "real"
		}
		return ""
	})
	if err == nil {
		t.Fatal("real provider allowed")
	}
}
func TestProductionDatabaseDenied(t *testing.T) {
	for _, dsn := range []string{"postgres://localhost/production", "postgres://remote.invalid/mgar_test", "file:///tmp/db"} {
		if ValidateTestDSN(dsn) == nil {
			t.Fatalf("unsafe test target accepted: %s", dsn)
		}
	}
	if err := ValidateTestDSN("postgres://127.0.0.1:55432/mgar_test"); err != nil {
		t.Fatal(err)
	}
}
