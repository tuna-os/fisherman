package varlinkapi

// PlannedSteps returns the steps an install of plan runs, in order. The
// service and every Executor use it, so the step list a frontend sees is
// the same whatever the backend.
func PlannedSteps(plan InstallPlan) []string {
	var out []string
	for _, s := range StepIDs {
		switch s {
		case StepLUKS:
			if plan.Encryption.Type == "" || plan.Encryption.Type == "none" {
				continue
			}
		case StepTPM2Enroll:
			if plan.Encryption.Type != "tpm2_luks" && plan.Encryption.Type != "tpm2_luks_passphrase" {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

// stepWeights is each step's share of the whole install, in percent. They
// are estimates that shape the bar, like the weights of the legacy pipeline.
var stepWeights = map[string]int{
	StepValidateDevice: 1,
	StepPartition:      72, // repart copies the medium's partitions here
	StepLUKS:           3,
	StepTPM2Enroll:     2,
	StepMount:          5,
	StepInstallOS:      1, // the copy is only checked
	StepConfigure:      3,
	StepPostFixups:     6,
	StepBootloader:     2,
	StepFinalize:       5,
	StepCleanup:        0,
}
