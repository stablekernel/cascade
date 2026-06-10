package changes

// DetectResult is the output of detect-changes command
type DetectResult struct {
	TriggeredBuilds  []string `json:"triggered_builds"`
	TriggeredDeploys []string `json:"triggered_deploys"`
	HasChanges       bool     `json:"has_changes"`
	ChangedFiles     []string `json:"changed_files"`
}
