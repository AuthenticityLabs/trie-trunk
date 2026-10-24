package cache

// Test-only extension of the optimized compact codec: a 16-bit integer has
// exactly one shorter width. Its 11 mask state therefore means one byte, with
// zero selector bits. All other type contracts and mask meanings are unchanged.
func compileInteger16Experiment(d Definition) (*fastIntegerLayout, error) {
	ref, err := compileCompactIntegerExperiment(d)
	if err != nil {
		return nil, err
	}
	plan := compileFastIntegerExperiment(ref)
	for i, f := range d.Fields {
		if f.Type == "U16" || f.Type == "I16" {
			plan.fields[i].eligible = true
			plan.fields[i].selectorBits = 0
		}
	}
	return plan, nil
}
