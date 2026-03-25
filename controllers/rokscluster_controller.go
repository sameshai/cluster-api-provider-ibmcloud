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

package controllers

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	infrav1 "sigs.k8s.io/cluster-api-provider-ibmcloud/v2/api/v1beta2"
	rokscontrolplanev1 "sigs.k8s.io/cluster-api-provider-ibmcloud/v2/controlplane/roks/api/v1beta2"
	expinfrav1 "sigs.k8s.io/cluster-api-provider-ibmcloud/v2/exp/api/v1beta2"
	"sigs.k8s.io/cluster-api-provider-ibmcloud/v2/pkg/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-ibmcloud/v2/pkg/logger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// ROKSClusterReconciler reconciles ROKSCluster.
type ROKSClusterReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	WatchFilterValue string
	Endpoints        []scope.ServiceEndpoint
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=roksclusters,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=roksclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=rokscontrolplanes;rokscontrolplanes/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *ROKSClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling ROKSCluster")

	// Fetch the ROKSCluster instance
	roksCluster := &expinfrav1.ROKSCluster{}
	err := r.Get(ctx, req.NamespacedName, roksCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, roksCluster.ObjectMeta)
	if err != nil {
		return reconcile.Result{}, err
	}
	if cluster == nil {
		log.Info("Cluster Controller has not yet set OwnerRef")
		return reconcile.Result{}, nil
	}

	if annotations.IsPaused(cluster, roksCluster) {
		log.Info("ROKSCluster or linked Cluster is marked as paused. Won't reconcile")
		return reconcile.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	controlPlane := &rokscontrolplanev1.ROKSControlPlane{}
	controlPlaneRef := types.NamespacedName{
		Name:      cluster.Spec.ControlPlaneRef.Name,
		Namespace: cluster.Spec.ControlPlaneRef.Namespace,
	}

	if err := r.Get(ctx, controlPlaneRef, controlPlane); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get control plane ref: %w", err)
	}

	log = log.WithValues("controlPlane", controlPlaneRef.Name)

	patchHelper, err := patch.NewHelper(roksCluster, r.Client)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to init patch helper: %w", err)
	}

	// Set the values from the managed control plane
	roksCluster.Status.Ready = true
	roksCluster.Spec.ControlPlaneEndpoint = controlPlane.Spec.ControlPlaneEndpoint

	if err := patchHelper.Patch(ctx, roksCluster); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to patch ROKSCluster: %w", err)
	}

	log.Info("Successfully reconciled ROKSCluster")

	return reconcile.Result{}, nil
}

func (r *ROKSClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	log := logger.FromContext(ctx)

	roksCluster := &expinfrav1.ROKSCluster{}

	controller, err := ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(roksCluster).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Build(r)

	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}

	// Add a watch for clusterv1.Cluster unpaise
	if err = controller.Watch(
		source.Kind(mgr.GetCache(), &clusterv1.Cluster{}),
		handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind("ROKSCluster"), mgr.GetClient(), &expinfrav1.ROKSCluster{})),
		predicates.ClusterUnpaused(log.GetLogger()),
	); err != nil {
		return fmt.Errorf("failed adding a watch for ready clusters: %w", err)
	}

	// Add a watch for ROKSControlPlane
	if err = controller.Watch(
		source.Kind(mgr.GetCache(), &rokscontrolplanev1.ROKSControlPlane{}),
		handler.EnqueueRequestsFromMapFunc(r.roksControlPlaneToManagedCluster(log)),
	); err != nil {
		return fmt.Errorf("failed adding watch on ROKSControlPlane: %w", err)
	}

	return nil
}

func (r *ROKSClusterReconciler) roksControlPlaneToManagedCluster(log *logger.Logger) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []ctrl.Request {
		roksControlPlane, ok := o.(*rokscontrolplanev1.ROKSControlPlane)
		if !ok {
			log.Error(errors.Errorf("expected a ROKSControlPlane, got %T instead", o), "failed to map ROKSControlPlane")
			return nil
		}

		log := log.WithValues("objectMapper", "rokscpToroksc", "ROKScontrolplane", klog.KRef(roksControlPlane.Namespace, roksControlPlane.Name))

		if !roksControlPlane.ObjectMeta.DeletionTimestamp.IsZero() {
			log.Info("ROKSControlPlane has a deletion timestamp, skipping mapping")
			return nil
		}

		if roksControlPlane.Spec.ControlPlaneEndpoint.IsZero() {
			log.Debug("ROKSControlPlane has no control plane endpoint, skipping mapping")
			return nil
		}

		cluster, err := util.GetOwnerCluster(ctx, r.Client, roksControlPlane.ObjectMeta)
		if err != nil {
			log.Error(err, "failed to get owning cluster")
			return nil
		}
		if cluster == nil {
			log.Info("no owning cluster, skipping mapping")
			return nil
		}

		roksClusterRef := cluster.Spec.InfrastructureRef
		if roksClusterRef == nil || roksClusterRef.Kind != "ROKSCluster" {
			log.Info("InfrastructureRef is nil or not ROKSCluster, skipping mapping")
			return nil
		}

		return []ctrl.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      roksClusterRef.Name,
					Namespace: roksClusterRef.Namespace,
				},
			},
		}
	}
}
