package handoff

// Receipt acknowledges evaluation of a durable hint, not Worker execution or
// verifier success. Duplicate returns the persisted processed acknowledgement.
type Receipt struct {
	Processed bool   `json:"processed"`
	Duplicate bool   `json:"duplicate"`
	Applied   bool   `json:"applied"`
	Reason    string `json:"reason"`
}
