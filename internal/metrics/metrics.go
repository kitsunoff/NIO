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

// Package metrics provides Prometheus metrics for the NixOS operator.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	namespace = "nio"

	// ResultSuccess is the label value for successful operations.
	ResultSuccess = "success"
	// ResultFailure is the label value for failed operations.
	ResultFailure = "failure"
)

var (
	// Gauge metrics - current state

	// MachinesTotal is the total number of Machine resources.
	MachinesTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "machines_total",
		Help:      "Total number of Machine resources",
	})

	// MachinesDiscoverable is the number of discoverable machines.
	MachinesDiscoverable = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "machines_discoverable",
		Help:      "Number of machines that are reachable via SSH",
	})

	// MachinesWithConfiguration is the number of machines with applied configuration.
	MachinesWithConfiguration = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "machines_with_configuration",
		Help:      "Number of machines with applied NixOS configuration",
	})

	// ConfigurationsTotal is the total number of NixosConfiguration resources.
	ConfigurationsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "configurations_total",
		Help:      "Total number of NixosConfiguration resources",
	})

	// JobsActive is the number of currently active apply jobs.
	JobsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "jobs_active",
		Help:      "Number of currently active apply jobs",
	})

	// MachinesByState shows distribution of machines by state.
	MachinesByState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "machines_by_state",
		Help:      "Number of machines by state (discoverable, undiscoverable)",
	}, []string{"state"})

	// ConfigsByState shows distribution of configurations by state.
	ConfigsByState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "configs_by_state",
		Help:      "Number of configurations by state (pending, applying, applied, failed)",
	}, []string{"state"})

	// Counter metrics - accumulated values

	// ConfigurationsAppliedTotal is the total number of successful configuration applies.
	ConfigurationsAppliedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "configurations_applied_total",
		Help:      "Total number of successful configuration applies",
	})

	// ConfigurationsFailedTotal is the total number of failed configuration applies.
	ConfigurationsFailedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "configurations_failed_total",
		Help:      "Total number of failed configuration applies",
	})

	// SSHConnectionsTotal is the total number of SSH connection attempts.
	SSHConnectionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "ssh_connections_total",
		Help:      "Total number of SSH connection attempts",
	}, []string{"result"}) // success, failure

	// GitClonesTotal is the total number of git clone operations.
	GitClonesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "git_clones_total",
		Help:      "Total number of git clone operations",
	}, []string{"result"}) // success, failure

	// NixosBuildsTotal is the total number of NixOS build operations.
	NixosBuildsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "nixos_builds_total",
		Help:      "Total number of NixOS build operations",
	}, []string{"operation", "result"}) // operation: rebuild/anywhere, result: success/failure

	// RetriesTotal is the total number of retry attempts.
	RetriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "retries_total",
		Help:      "Total number of retry attempts",
	}, []string{"resource_type"}) // machine, configuration

	// ErrorsTotal is the total number of errors by type.
	ErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "errors_total",
		Help:      "Total number of errors by type",
	}, []string{"error_type"}) // ssh, git, nix, k8s

	// JobsFailedTotal is the total number of failed jobs.
	JobsFailedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "jobs_failed_total",
		Help:      "Total number of failed jobs",
	})

	// SecretWatchTriggersTotal is the total number of secret watch triggers.
	SecretWatchTriggersTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      "secret_watch_triggers_total",
		Help:      "Total number of reconciliations triggered by secret changes",
	})

	// Histogram metrics - durations

	// ReconcileDuration is the duration of reconcile operations.
	ReconcileDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "reconcile_duration_seconds",
		Help:      "Duration of reconcile operations in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.01, 2, 10), // 10ms to ~10s
	}, []string{"controller", "result"})

	// SSHConnectionDuration is the duration of SSH connection operations.
	SSHConnectionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "ssh_connection_duration_seconds",
		Help:      "Duration of SSH connection operations in seconds",
		Buckets:   prometheus.ExponentialBuckets(0.1, 2, 8), // 100ms to ~25s
	})

	// GitCloneDuration is the duration of git clone operations.
	GitCloneDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "git_clone_duration_seconds",
		Help:      "Duration of git clone operations in seconds",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 8), // 1s to ~256s
	})

	// NixosBuildDuration is the duration of NixOS build operations.
	NixosBuildDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "nixos_build_duration_seconds",
		Help:      "Duration of NixOS build operations in seconds",
		Buckets:   prometheus.ExponentialBuckets(10, 2, 10), // 10s to ~2.8 hours
	}, []string{"operation"}) // rebuild, anywhere

	// JobDuration is the duration of apply jobs.
	JobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace,
		Name:      "job_duration_seconds",
		Help:      "Duration of apply jobs in seconds",
		Buckets:   prometheus.ExponentialBuckets(10, 2, 10), // 10s to ~2.8 hours
	}, []string{"operation", "result"})
)

func init() {
	// Register all metrics with controller-runtime metrics registry
	metrics.Registry.MustRegister(
		// Gauges
		MachinesTotal,
		MachinesDiscoverable,
		MachinesWithConfiguration,
		ConfigurationsTotal,
		JobsActive,
		MachinesByState,
		ConfigsByState,

		// Counters
		ConfigurationsAppliedTotal,
		ConfigurationsFailedTotal,
		SSHConnectionsTotal,
		GitClonesTotal,
		NixosBuildsTotal,
		RetriesTotal,
		ErrorsTotal,
		JobsFailedTotal,
		SecretWatchTriggersTotal,

		// Histograms
		ReconcileDuration,
		SSHConnectionDuration,
		GitCloneDuration,
		NixosBuildDuration,
		JobDuration,
	)
}

// RecordSSHConnection records an SSH connection attempt.
func RecordSSHConnection(success bool, duration float64) {
	result := ResultSuccess
	if !success {
		result = ResultFailure
	}
	SSHConnectionsTotal.WithLabelValues(result).Inc()
	SSHConnectionDuration.Observe(duration)
}

// RecordGitClone records a git clone operation.
func RecordGitClone(success bool, duration float64) {
	result := ResultSuccess
	if !success {
		result = ResultFailure
	}
	GitClonesTotal.WithLabelValues(result).Inc()
	GitCloneDuration.Observe(duration)
}

// RecordNixosBuild records a NixOS build operation.
func RecordNixosBuild(operation string, success bool, duration float64) {
	result := ResultSuccess
	if !success {
		result = ResultFailure
	}
	NixosBuildsTotal.WithLabelValues(operation, result).Inc()
	NixosBuildDuration.WithLabelValues(operation).Observe(duration)
}

// RecordJobCompletion records job completion.
func RecordJobCompletion(operation string, success bool, duration float64) {
	result := ResultSuccess
	if !success {
		result = ResultFailure
		JobsFailedTotal.Inc()
	}
	JobDuration.WithLabelValues(operation, result).Observe(duration)

	if success {
		ConfigurationsAppliedTotal.Inc()
	} else {
		ConfigurationsFailedTotal.Inc()
	}
}

// RecordError records an error by type.
func RecordError(errorType string) {
	ErrorsTotal.WithLabelValues(errorType).Inc()
}

// UpdateMachineState updates machine state metrics.
func UpdateMachineState(total, discoverable, configured int) {
	MachinesTotal.Set(float64(total))
	MachinesDiscoverable.Set(float64(discoverable))
	MachinesWithConfiguration.Set(float64(configured))
	MachinesByState.WithLabelValues("discoverable").Set(float64(discoverable))
	MachinesByState.WithLabelValues("undiscoverable").Set(float64(total - discoverable))
}

// UpdateConfigState updates configuration state metrics.
func UpdateConfigState(total, pending, applying, applied, failed int) {
	ConfigurationsTotal.Set(float64(total))
	ConfigsByState.WithLabelValues("pending").Set(float64(pending))
	ConfigsByState.WithLabelValues("applying").Set(float64(applying))
	ConfigsByState.WithLabelValues("applied").Set(float64(applied))
	ConfigsByState.WithLabelValues("failed").Set(float64(failed))
}
