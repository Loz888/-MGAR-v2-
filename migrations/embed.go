package migrations

import _ "embed"

// ControlSQL is immutable once migration 001 has been released.
//
//go:embed 001_control.sql
var ControlSQL string

//go:embed 002_artifact_cas.sql
var ArtifactSQL string
