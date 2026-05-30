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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EphemeralEnvironmentSpec struct {
	// ttl is how long the environment lives before it automatically tears down
	// Go duration string eg. "30m"
	// +optional
	// +kubebuilder:default="1h"
	TTL metav1.Duration `json:"ttl,omitempty"`

	// template is the pod template for the workload to run inside
	// the environment. The first containerPort is exposed via a Service.
	// +required
	Template corev1.PodTemplateSpec `json:"template,omitempty"`
}

// EphemeralEnvironmentPhase is the lifecycle phase of EphemeralEnvironment
// +kubebuilder:validation:Enum=Pending;Ready;Expiring;Failed
type EphemeralEnvironmentPhase string

const (
	// Pending means the environment is being provisioned
	PhasePending EphemeralEnvironmentPhase = "Pending"
	// Ready means namespace and workload exists
	PhaseReady EphemeralEnvironmentPhase = "Ready"
	// Expiring means ttl has elapsed and teardown is underway
	PhaseExpiring EphemeralEnvironmentPhase = "Expiring"
	// Failed means provisioning or teardown failed
	PhaseFailed EphemeralEnvironmentPhase = "Failed"
)

type EphemeralEnvironmentStatus struct {
	// phase is the current lifecycle phase of environment
	// +optional
	Phase EphemeralEnvironmentPhase `json:"phase,omitempty"`

	// namespace is name of provisioned namespace
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// expiresAt is the time at which the environment tears down
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ephenv
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="TTL",type="string",JSONPath=".spec.ttl"
// +kubebuilder:printcolumn:name="Expires",type="date",JSONPath=".status.expiresAt"
// +kubebuilder:printcolumn:name="Namespace",type="string",JSONPath=".status.namespace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EphemeralEnvironment is the Schema for the ephemeralenvironments API
type EphemeralEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitzero"`

	Spec EphemeralEnvironmentSpec `json:"spec,omitempty"`

	Status EphemeralEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EphemeralEnvironmentList contains a list of EphemeralEnvironment
type EphemeralEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []EphemeralEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EphemeralEnvironment{}, &EphemeralEnvironmentList{})
}
