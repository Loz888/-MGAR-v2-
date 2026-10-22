package control

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestFunctionalHealth(t *testing.T) {
	s := httptest.NewServer(Handler())
	defer s.Close()
	r, e := s.Client().Get(s.URL + "/healthz")
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	var body map[string]string
	if e = json.NewDecoder(r.Body).Decode(&body); e != nil || r.StatusCode != 200 || body["provider"] != "mock" {
		t.Fatalf("invalid health response: %+v %v", body, e)
	}
}
