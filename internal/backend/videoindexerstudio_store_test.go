package backend

import "testing"

func TestDecodeVideoIndexerJobsAcceptsLegacyCompositionWithoutNarrativeIntent(t *testing.T) {
	jobs, ok, err := decodeVideoIndexerJobs([]byte(`{"version":1,"jobs":[{"id":"composition-1","composition":true,"compositionPlan":{"schemaVersion":1,"compositionId":"composition-1"}}]}`))
	if err != nil || !ok || len(jobs) != 1 || jobs[0].NarrativeIntent != "" || jobs[0].CompositionPlan == nil || jobs[0].CompositionPlan.NarrativeIntent != "" {
		t.Fatalf("legacy composition decode = %#v, %t, %v", jobs, ok, err)
	}
}
