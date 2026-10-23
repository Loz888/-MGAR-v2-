// Package budget describes the durable, conservative model-call ledger.
// These types grant no authority to contact a paid provider.
package budget

import (
	"fmt"
	"regexp"
)

var logicalID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var modelName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type ModelRequest struct {
	LogicalID  string `json:"logical_call_id"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Tokens     int64  `json:"tokens"`
	CostMicros int64  `json:"cost_micros"`
}

func (r ModelRequest) Valid() bool {
	return logicalID.MatchString(r.LogicalID) && r.Provider == "mock" && modelName.MatchString(r.Model) && r.Tokens > 0 && r.Tokens <= 10_000_000 && r.CostMicros > 0 && r.CostMicros <= 1_000_000_000
}

type ModelReservation struct {
	Schema     string `json:"schema_version"`
	Owner      string `json:"owner"`
	Epoch      int64  `json:"epoch"`
	Tokens     int64  `json:"tokens"`
	CostMicros int64  `json:"cost_micros"`
}

func (r ModelReservation) Valid() bool {
	return r.Schema == "model_reservation.v1" && r.Owner != "" && r.Epoch > 0 && r.Tokens > 0 && r.Tokens <= 10_000_000 && r.CostMicros > 0 && r.CostMicros <= 1_000_000_000
}

type ModelCall struct {
	ID          string
	Reservation ModelReservation
}
type ModelUsage struct {
	Status     string `json:"status"`
	Known      bool   `json:"known"`
	Tokens     int64  `json:"tokens"`
	CostMicros int64  `json:"cost_micros"`
	Source     string `json:"source"`
}

func (u ModelUsage) Valid() bool {
	if !u.Known {
		return u.Status == "unknown" && u.Tokens == 0 && u.CostMicros == 0 && u.Source == "unknown"
	}
	return (u.Status == "succeeded" || u.Status == "failed") && u.Tokens >= 0 && u.CostMicros >= 0 && (u.Source == "provider" || u.Source == "local_stub")
}

// Decimal preserves micro-dollar precision; never round through float64.
func Decimal(micros int64) string { return fmt.Sprintf("%d.%06d", micros/1_000_000, micros%1_000_000) }

type CallRecovery struct {
	Unknown, Pending int
	UnsafeTool       bool
}
