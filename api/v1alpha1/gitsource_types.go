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
	"k8s.io/apimachinery/pkg/runtime"
)

// GitSourceSpec defines the desired state of GitSource
type GitSourceSpec struct {
	// repo is the public HTTPS URL of the Git repository to sync from.
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`

	// branch is the branch to read manifests from.
	// +kubebuilder:default="main"
	// +optional
	Branch string `json:"branch,omitempty"`

	// path is the subdirectory within the repository to read. "." means the root.
	// +kubebuilder:default="."
	// +optional
	Path string `json:"path,omitempty"`

	// interval is how often to re-sync the cluster with the repository.
	// +kubebuilder:default="30s"
	// +optional
	Interval metav1.Duration `json:"interval,omitempty"`

	// prune enables deletion of objects that have been removed from Git.
	// +kubebuilder:default=false
	// +optional
	Prune bool `json:"prune,omitempty"`
}

// ResourceRef identifies a single Kubernetes object this GitSource applied.
type ResourceRef struct {
	// group is the API group of the object. Empty for core types such as Service.
	// +optional
	Group string `json:"group,omitempty"`

	// version is the API version of the object.
	Version string `json:"version"`

	// kind is the kind of the object.
	Kind string `json:"kind"`

	// namespace is the namespace of the object. Empty for cluster-scoped objects.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// name is the name of the object.
	Name string `json:"name"`
}

// GitSourceStatus defines the observed state of GitSource.
type GitSourceStatus struct {
	// phase is a one-word summary of the last sync attempt.
	// +kubebuilder:validation:Enum=Pending;Synced;Drifted;Error
	// +optional
	Phase string `json:"phase,omitempty"`

	// message explains the phase when something went wrong.
	// +optional
	Message string `json:"message,omitempty"`

	// lastSyncTime is when the last successful sync completed.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// lastSyncedCommit is the short commit SHA that was last applied.
	// +optional
	LastSyncedCommit string `json:"lastSyncedCommit,omitempty"`

	// inventory lists every object this GitSource currently owns.
	// +optional
	Inventory []ResourceRef `json:"inventory,omitempty"`

	// driftedObjects lists objects the last sync had to correct.
	// +optional
	DriftedObjects []ResourceRef `json:"driftedObjects,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Commit",type=string,JSONPath=`.status.lastSyncedCommit`
// +kubebuilder:printcolumn:name="Last Sync",type=date,JSONPath=`.status.lastSyncTime`

// GitSource is the Schema for the gitsources API
type GitSource struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of GitSource
	// +required
	Spec GitSourceSpec `json:"spec"`

	// status defines the observed state of GitSource
	// +optional
	Status GitSourceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GitSourceList contains a list of GitSource
type GitSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []GitSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &GitSource{}, &GitSourceList{})
		return nil
	})
}
