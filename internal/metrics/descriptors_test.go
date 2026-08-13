/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"sort"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// metricsContract pins every metric's fully-qualified name together with the exact
// spelling of its variable label names, one metric per line: "<name> [label...]".
//
// Metric and label names are the operator's observable interface - user dashboards
// and alerting rules match on them verbatim. Refactors that introduce named constants
// for label names must leave every string here untouched.
//
// This contract is written out as plain text on purpose. Deriving it from the
// constants in metrics.go would make it agree with any rename instead of catching
// one, which is the whole point of the test below.
const metricsContract = `
nio_machines_total
nio_machines_discoverable
nio_machines_with_configuration
nio_configurations_total
nio_jobs_active
nio_machines_by_state          state
nio_configs_by_state           state
nio_configurations_applied_total
nio_configurations_failed_total
nio_ssh_connections_total      result
nio_git_clones_total           result
nio_nixos_builds_total         operation result
nio_retries_total              resource_type
nio_errors_total               error_type
nio_jobs_failed_total
nio_secret_watch_triggers_total
nio_reconcile_duration_seconds controller result
nio_ssh_connection_duration_seconds
nio_git_clone_duration_seconds
nio_nixos_build_duration_seconds operation
nio_job_duration_seconds       operation result
`

// registeredCollectors maps each metric's fully-qualified name to the collector this
// package registers under it.
func registeredCollectors() map[string]prometheus.Collector {
	return map[string]prometheus.Collector{
		"nio_machines_total":                  MachinesTotal,
		"nio_machines_discoverable":           MachinesDiscoverable,
		"nio_machines_with_configuration":     MachinesWithConfiguration,
		"nio_configurations_total":            ConfigurationsTotal,
		"nio_jobs_active":                     JobsActive,
		"nio_machines_by_state":               MachinesByState,
		"nio_configs_by_state":                ConfigsByState,
		"nio_configurations_applied_total":    ConfigurationsAppliedTotal,
		"nio_configurations_failed_total":     ConfigurationsFailedTotal,
		"nio_ssh_connections_total":           SSHConnectionsTotal,
		"nio_git_clones_total":                GitClonesTotal,
		"nio_nixos_builds_total":              NixosBuildsTotal,
		"nio_retries_total":                   RetriesTotal,
		"nio_errors_total":                    ErrorsTotal,
		"nio_jobs_failed_total":               JobsFailedTotal,
		"nio_secret_watch_triggers_total":     SecretWatchTriggersTotal,
		"nio_reconcile_duration_seconds":      ReconcileDuration,
		"nio_ssh_connection_duration_seconds": SSHConnectionDuration,
		"nio_git_clone_duration_seconds":      GitCloneDuration,
		"nio_nixos_build_duration_seconds":    NixosBuildDuration,
		"nio_job_duration_seconds":            JobDuration,
	}
}

// parseContract turns metricsContract into a metric name -> sorted label names map.
func parseContract(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range strings.Split(metricsContract, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name, labels := fields[0], fields[1:]
		if _, dup := out[name]; dup {
			t.Fatalf("metric %q listed twice in metricsContract", name)
		}
		sort.Strings(labels)
		out[name] = labels
	}
	if len(out) == 0 {
		t.Fatal("metricsContract parsed to nothing; the test would assert nothing")
	}
	return out
}

// materialize creates one child series using exactly the label names given, so the
// metric shows up when the registry is gathered. It returns an error if the metric's
// real label names differ from the ones supplied, which is what makes a renamed label
// fail this test.
func materialize(c prometheus.Collector, labelNames []string) error {
	if len(labelNames) == 0 {
		return nil
	}
	labels := prometheus.Labels{}
	for _, n := range labelNames {
		labels[n] = "x"
	}
	var err error
	switch v := c.(type) {
	case *prometheus.CounterVec:
		_, err = v.GetMetricWith(labels)
	case *prometheus.GaugeVec:
		_, err = v.GetMetricWith(labels)
	case *prometheus.HistogramVec:
		_, err = v.GetMetricWith(labels)
	default:
		return nil
	}
	return err
}

// TestMetricDescriptorsAreStable is the guard for the claim that introducing named
// constants for metric label names changed no observable string. It fails if any
// metric name or label name is renamed, added or removed.
func TestMetricDescriptorsAreStable(t *testing.T) {
	want := parseContract(t)
	collectors := registeredCollectors()

	registry := prometheus.NewPedanticRegistry()
	for name, wantLabels := range want {
		c, ok := collectors[name]
		if !ok {
			t.Fatalf("metric %q is in metricsContract but no collector is registered for it", name)
		}
		if err := materialize(c, wantLabels); err != nil {
			t.Fatalf("metric %s does not accept label names %v: %v", name, wantLabels, err)
		}
		if err := registry.Register(c); err != nil {
			t.Fatalf("registering %s: %v", name, err)
		}
	}
	for name := range collectors {
		if _, ok := want[name]; !ok {
			t.Errorf("collector %q is not covered by metricsContract", name)
		}
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gathering metrics: %v", err)
	}

	got := map[string][]string{}
	for _, f := range families {
		var names []string
		if len(f.GetMetric()) > 0 {
			for _, lp := range f.GetMetric()[0].GetLabel() {
				names = append(names, lp.GetName())
			}
		}
		sort.Strings(names)
		got[f.GetName()] = names
	}

	for name, wantLabels := range want {
		gotLabels, ok := got[name]
		if !ok {
			t.Errorf("metric %q is missing from the registry; renaming or removing a metric breaks user dashboards", name)
			continue
		}
		if strings.Join(gotLabels, ",") != strings.Join(wantLabels, ",") {
			t.Errorf("metric %q label names = %v, want %v", name, gotLabels, wantLabels)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected metric %q in registry; add it to metricsContract if it is intentional", name)
		}
	}
}

// TestMetricDescriptorsAreStable_DetectsRename proves the guard above has teeth: a
// metric queried under a misspelled label name must be rejected, not silently accepted.
func TestMetricDescriptorsAreStable_DetectsRename(t *testing.T) {
	if err := materialize(SSHConnectionsTotal, []string{"resultt"}); err == nil {
		t.Error("expected a misspelled label name to be rejected, got no error")
	}
	if err := materialize(JobDuration, []string{"operation", "outcome"}); err == nil {
		t.Error("expected a renamed label name to be rejected, got no error")
	}
}
