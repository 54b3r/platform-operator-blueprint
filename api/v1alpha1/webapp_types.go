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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebAppSpec defines the desired state of WebApp.
// All fields represent intent — the operator reconciles the cluster toward this state.
type WebAppSpec struct {
	// Image is the container image to run, including tag.
	// Example: "nginx:1.25"
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Replicas is the desired number of running pod replicas.
	// Defaults to 1 if not specified.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Port is the container port the application listens on.
	// This port is exposed via the managed Service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	// +optional
	Port int32 `json:"port,omitempty"`
}

// WebAppStatus defines the observed state of WebApp.
// All fields represent runtime observations — never set these from Spec.
type WebAppStatus struct {
	// AvailableReplicas is the number of pods running and ready to serve traffic.
	// Updated by the operator after each reconcile.
	// +optional
	AvailableReplicas int32 `json:"availableReplicas,omitempty"`

	// Conditions holds the latest available observations of the WebApp's state.
	// Uses the standard metav1.Condition type for compatibility with kubectl and tooling.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Condition type constants for WebApp status.
const (
	// TypeAvailable indicates the WebApp Deployment has the desired number of ready replicas.
	TypeAvailable = "Available"

	// TypeProgressing indicates the WebApp is being reconciled (e.g. Deployment is rolling out).
	TypeProgressing = "Progressing"

	// TypeDegraded indicates the WebApp has encountered an error during reconciliation.
	TypeDegraded = "Degraded"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Image",type="string",JSONPath=".spec.image",description="Container image"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas",description="Desired replicas"
// +kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.availableReplicas",description="Available replicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// WebApp is the Schema for the webapps API.
// It represents a web application workload managed by the platform-operator,
// consisting of a Deployment and a Service.
type WebApp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebAppSpec   `json:"spec,omitempty"`
	Status WebAppStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WebAppList contains a list of WebApp.
type WebAppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebApp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WebApp{}, &WebAppList{})
}
