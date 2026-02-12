/*
Copyright 2026 The Kubernetes Authors.

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

package v1beta2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

type RoksControlPlaneSpec struct { //nolint: maligned
	// The Subnet IDs to use when installing the cluster.
	// SubnetIDs should come in pairs; two per availability zone, one private and one public.
	Subnets []string `json:"subnets"`

	// IBM AvailabilityZones of the worker nodes
	// should match the AvailabilityZones of the Subnets.
	AvailabilityZones []string `json:"availabilityZones"`

	// Block of IP addresses used by OpenShift while installing the cluster, for example "10.0.0.0/16".
	MachineCIDR *string `json:"machineCIDR"`

	// The IBM Region the cluster lives in.
	Region *string `json:"region"`

	// Openshift version, for example "openshift-v4.12.15".
	Version *string `json:"version"`

	// ControlPlaneEndpoint represents the endpoint used to communicate with the control plane.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint"`

	// IBM IAM roles used to perform credential requests by the openshift operators.
	RolesRef IBMRolesRef `json:"rolesRef"`

	// The ID of the OpenID Connect Provider.
	OIDCID *string `json:"oidcID"`

	// TODO: these are to satisfy ocm sdk. Explore how to drop them.
	AccountID        *string `json:"accountID"`
	CreatorARN       *string `json:"creatorARN"`
	InstallerRoleARN *string `json:"installerRoleARN"`
	SupportRoleARN   *string `json:"supportRoleARN"`
}

// IBMRolesRef contains references to various IBM IAM roles required for operators to make calls against the IBM API.
type IBMRolesRef struct {
	IngressARN              string `json:"ingressARN"`
	ImageRegistryARN        string `json:"imageRegistryARN"`
	StorageARN              string `json:"storageARN"`
	NetworkARN              string `json:"networkARN"`
	KubeCloudControllerARN  string `json:"kubeCloudControllerARN"`
	NodePoolManagementARN   string `json:"nodePoolManagementARN"`
	ControlPlaneOperatorARN string `json:"controlPlaneOperatorARN"`
	KMSProviderARN          string `json:"kmsProviderARN"`
}

type RoksControlPlaneStatus struct {
	// ExternalManagedControlPlane indicates to cluster-api that the control plane
	// is managed by an external service such as AKS, EKS, GKE, etc.
	// +kubebuilder:default=true
	ExternalManagedControlPlane *bool `json:"externalManagedControlPlane,omitempty"`
	// Initialized denotes whether or not the control plane has the
	// uploaded kubernetes config-map.
	// +optional
	Initialized bool `json:"initialized"`
	// Ready denotes that the IBMManagedControlPlane API Server is ready to
	// receive requests and that the VPC infra is ready.
	// +kubebuilder:default=false
	Ready bool `json:"ready"`
	// ErrorMessage indicates that there is a terminal problem reconciling the
	// state, and will be set to a descriptive error message.
	// +optional
	FailureMessage *string `json:"failureMessage,omitempty"`
	// Conditions specifies the cpnditions for the managed control plane
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=rokscontrolplanes,shortName=rokscp,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels.cluster\\.x-k8s\\.io/cluster-name",description="Cluster to which this RoksControl belongs"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.ready",description="Control plane infrastructure is ready for worker nodes"

type ROKSControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RoksControlPlaneSpec   `json:"spec,omitempty"`
	Status RoksControlPlaneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type ROKSControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ROKSControlPlane `json:"items"`
}

// GetConditions returns the control planes conditions.
func (r *ROKSControlPlane) GetConditions() clusterv1.Conditions {
	return r.Status.Conditions
}

// SetConditions sets the status conditions for the IBMManagedControlPlane.
func (r *ROKSControlPlane) SetConditions(conditions clusterv1.Conditions) {
	r.Status.Conditions = conditions
}

func init() {
	SchemeBuilder.Register(&ROKSControlPlane{}, &ROKSControlPlaneList{})
}
