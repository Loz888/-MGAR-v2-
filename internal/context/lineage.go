package context

import "github.com/Loz888/-MGAR-v2-/internal/artifact"

type ExecutionConfig struct {
	ModelVersion      string `json:"model_config_version"`
	PolicyVersion     string `json:"tool_policy_version"`
	OutputToolVersion string `json:"output_tool_version"`
}

func (c ExecutionConfig) Valid() bool {
	return ValidVersion(c.ModelVersion) && ValidVersion(c.PolicyVersion) && ValidVersion(c.OutputToolVersion)
}

type Lineage struct {
	TaskID           string
	RequirementsHash string
	Config           ExecutionConfig
	Artifact         artifact.Metadata
	Parents          []string
	Inputs           []Ref
	Valid            bool
}
