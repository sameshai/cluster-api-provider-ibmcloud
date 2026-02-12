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
	rosicontrolplanev1 "sigs.k8s.io/cluster-api-provider-ibmcloud/v2/controlplane/rosi/api/v1beta2"
	expinfrav1 "sigs.k8s.io/cluster-api-provider-ibmcloud/v2/exp/api/v1beta2"
	"sigs.k8s.io/cluster-api-provider-ibmcloud/v2/pkg/cloud/scope"
	"sigs.k8s.io/cluster-api-provider-ibmcloud/v2/pkg/logger"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
)

// ROSIClusterReconciler reconciles ROSICluster.
type ROSIClusterReconciler struct {
	client.Client
	Recorder         record.EventRecorder
	WatchFilterValue string
	Endpoints        []scope.ServiceEndpoint
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=rosiclusters,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=rosiclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=rosicontrolplanes;rosicontrolplanes/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func (r *ROSIClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling ROSICluster")

	// Fetch the ROSICluster instance
	rosiCluster := &expinfrav1.ROSICluster{}
	err := r.Get(ctx, req.NamespacedName, rosiCluster)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, rosiCluster.ObjectMeta)
	if err != nil {
		return reconcile.Result{}, err
	}
	if cluster == nil {
		log.Info("Cluster Controller has not yet set OwnerRef")
		return reconcile.Result{}, nil
	}

	if annotations.IsPaused(cluster, rosiCluster) {
		log.Info("ROSICluster or linked Cluster is marked as paused. Won't reconcile")
		return reconcile.Result{}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	controlPlane := &rosicontrolplanev1.ROSIControlPlane{}
	controlPlaneRef := types.NamespacedName{
		Name:      cluster.Spec.ControlPlaneRef.Name,
		Namespace: cluster.Spec.ControlPlaneRef.Namespace,
	}

	if err := r.Get(ctx, controlPlaneRef, controlPlane); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get control plane ref: %w", err)
	}

	log = log.WithValues("controlPlane", controlPlaneRef.Name)

	patchHelper, err := patch.NewHelper(rosiCluster, r.Client)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to init patch helper: %w", err)
	}

	// Set the values from the managed control plane
	rosiCluster.Status.Ready = true
	rosiCluster.Spec.ControlPlaneEndpoint = controlPlane.Spec.ControlPlaneEndpoint

	if err := patchHelper.Patch(ctx, rosiCluster); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to patch ROSICluster: %w", err)
	}

	log.Info("Successfully reconciled ROSICluster")

	return reconcile.Result{}, nil
}

func (r *ROSIClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	log := logger.FromContext(ctx)

	rosiCluster := &expinfrav1.ROSICluster{}

	controller, err := ctrl.NewControllerManagedBy(mgr).
		WithOptions(options).
		For(rosiCluster).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Build(r)

	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}

	// Add a watch for clusterv1.Cluster unpaise
	if err = controller.Watch(
		source.Kind(mgr.GetCache(), &clusterv1.Cluster{}),
		handler.EnqueueRequestsFromMapFunc(util.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind("ROSICluster"), mgr.GetClient(), &expinfrav1.ROSICluster{})),
		predicates.ClusterUnpaused(log.GetLogger()),
	); err != nil {
		return fmt.Errorf("failed adding a watch for ready clusters: %w", err)
	}

	// Add a watch for ROSIControlPlane
	if err = controller.Watch(
		source.Kind(mgr.GetCache(), &rosicontrolplanev1.ROSIControlPlane{}),
		handler.EnqueueRequestsFromMapFunc(r.rosiControlPlaneToManagedCluster(log)),
	); err != nil {
		return fmt.Errorf("failed adding watch on ROSIControlPlane: %w", err)
	}

	return nil
}

func (r *ROSIClusterReconciler) rosiControlPlaneToManagedCluster(log *logger.Logger) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []ctrl.Request {
		rosiControlPlane, ok := o.(*rosicontrolplanev1.ROSIControlPlane)
		if !ok {
			log.Error(errors.Errorf("expected a ROSIControlPlane, got %T instead", o), "failed to map ROSIControlPlane")
			return nil
		}

		log := log.WithValues("objectMapper", "rosicpTorosic", "ROSIcontrolplane", klog.KRef(rosiControlPlane.Namespace, rosiControlPlane.Name))

		if !rosiControlPlane.ObjectMeta.DeletionTimestamp.IsZero() {
			log.Info("ROSIControlPlane has a deletion timestamp, skipping mapping")
			return nil
		}

		if rosiControlPlane.Spec.ControlPlaneEndpoint.IsZero() {
			log.Debug("ROSIControlPlane has no control plane endpoint, skipping mapping")
			return nil
		}

		cluster, err := util.GetOwnerCluster(ctx, r.Client, rosiControlPlane.ObjectMeta)
		if err != nil {
			log.Error(err, "failed to get owning cluster")
			return nil
		}
		if cluster == nil {
			log.Info("no owning cluster, skipping mapping")
			return nil
		}

		rosiClusterRef := cluster.Spec.InfrastructureRef
		if rosiClusterRef == nil || rosiClusterRef.Kind != "ROSICluster" {
			log.Info("InfrastructureRef is nil or not ROSICluster, skipping mapping")
			return nil
		}

		return []ctrl.Request{
			{
				NamespacedName: types.NamespacedName{
					Name:      rosiClusterRef.Name,
					Namespace: rosiClusterRef.Namespace,
				},
			},
		}
	}
}
