package health

// Default returns the registry of every built-in check and fix.
func Default() *Registry {
	var checks []Check
	checks = append(checks, coreChecks()...)
	checks = append(checks, dbSchemaChecks()...)
	checks = append(checks, workflowPublicationChecks()...)
	checks = append(checks, monoesAccountChecks()...)
	checks = append(checks, monomindChecks()...)
	checks = append(checks, monomindDoctorChecks()...)
	checks = append(checks, monomindUpkeepChecks()...)
	checks = append(checks, runtimeChecks()...)
	checks = append(checks, browserChecks()...)
	checks = append(checks, serviceChecks()...)
	checks = append(checks, integrationChecks()...)
	checks = append(checks, accountChecks()...)
	checks = append(checks, jevChecks()...)
	checks = append(checks, automationChecks()...)
	var fixes []Fix
	fixes = append(fixes, coreFixes()...)
	fixes = append(fixes, workflowPublicationFixes()...)
	fixes = append(fixes, monoesAccountFixes()...)
	fixes = append(fixes, monomindFixes()...)
	fixes = append(fixes, monomindDoctorFixes()...)
	fixes = append(fixes, monomindUpkeepFixes()...)
	fixes = append(fixes, runtimeFixes()...)
	fixes = append(fixes, browserFixes()...)
	fixes = append(fixes, serviceFixes()...)
	fixes = append(fixes, integrationFixes()...)
	fixes = append(fixes, accountFixes()...)
	fixes = append(fixes, jevFixes()...)
	fixes = append(fixes, automationFixes()...)
	return NewRegistry(checks, fixes)
}
