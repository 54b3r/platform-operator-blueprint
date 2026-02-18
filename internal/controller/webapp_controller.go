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

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	appv1alpha1 "github.com/54b3r/platform-operator-blueprint/api/v1alpha1"
)

// webappFinalizer is the finalizer added to every WebApp resource.
// It ensures cleanup logic runs before the resource is deleted from the API server.
const webappFinalizer = "app.54b3r.io/finalizer"

// requeueAfter is the standard requeue interval for periodic reconciliation.
// This ensures the operator self-heals even if watch events are missed.
const requeueAfter = time.Minute

// WebAppReconciler reconciles a WebApp object.
// It manages a Deployment and a Service as child resources, keeping them
// in sync with the desired state expressed in WebAppSpec.
type WebAppReconciler struct {
	// Client is the controller-runtime client for interacting with the Kubernetes API.
	client.Client
	// Scheme holds the runtime scheme used for setting owner references.
	Scheme *runtime.Scheme
}

// Needed to read and manage WebApp resources and their status subresource.
// +kubebuilder:rbac:groups=app.54b3r.io,resources=webapps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=app.54b3r.io,resources=webapps/status,verbs=get;update;patch

// Needed to manage the finalizer on WebApp resources.
// +kubebuilder:rbac:groups=app.54b3r.io,resources=webapps/finalizers,verbs=update

// Needed to create and manage the Deployment child resource.
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

// Needed to create and manage the Service child resource.
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Needed for leader election to work correctly in multi-replica deployments.
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile manages the full lifecycle of a WebApp resource.
// It reconciles the following child resources:
//   - Deployment: runs the container image specified in WebAppSpec.Image
//   - Service: exposes the container on WebAppSpec.Port within the cluster
//
// On deletion, the finalizer ensures child resources are cleaned up before
// the WebApp is removed from the API server.
//
// The reconciler requeues after requeueAfter to self-heal against drift.
func (r *WebAppReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the WebApp resource. If it no longer exists, nothing to do.
	webapp := &appv1alpha1.WebApp{}
	if err := r.Get(ctx, req.NamespacedName, webapp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion: if the resource is being deleted and our finalizer is present,
	// run cleanup logic then remove the finalizer to allow deletion to proceed.
	if !webapp.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(webapp, webappFinalizer) {
			log.Info("running finalizer cleanup", "name", webapp.Name)
			if err := r.cleanupChildResources(ctx, webapp); err != nil {
				return ctrl.Result{}, fmt.Errorf("finalizer cleanup: %w", err)
			}
			controllerutil.RemoveFinalizer(webapp, webappFinalizer)
			if err := r.Update(ctx, webapp); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		// Requeue not needed — object is being deleted.
		return ctrl.Result{}, nil
	}

	// Add finalizer on first reconcile if not already present.
	if !controllerutil.ContainsFinalizer(webapp, webappFinalizer) {
		controllerutil.AddFinalizer(webapp, webappFinalizer)
		if err := r.Update(ctx, webapp); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		// Requeue to continue reconciliation after the update.
		return ctrl.Result{Requeue: true}, nil
	}

	// Mark the resource as Progressing while we reconcile.
	if err := r.setCondition(ctx, webapp, appv1alpha1.TypeProgressing, metav1.ConditionTrue,
		"Reconciling", "reconciliation in progress"); err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile the Deployment child resource.
	if err := r.reconcileDeployment(ctx, webapp); err != nil {
		_ = r.setCondition(ctx, webapp, appv1alpha1.TypeDegraded, metav1.ConditionTrue,
			"DeploymentFailed", err.Error())
		return ctrl.Result{}, fmt.Errorf("reconciling deployment: %w", err)
	}

	// Reconcile the Service child resource.
	if err := r.reconcileService(ctx, webapp); err != nil {
		_ = r.setCondition(ctx, webapp, appv1alpha1.TypeDegraded, metav1.ConditionTrue,
			"ServiceFailed", err.Error())
		return ctrl.Result{}, fmt.Errorf("reconciling service: %w", err)
	}

	// Fetch the current Deployment to read available replicas for status.
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: webapp.Name, Namespace: webapp.Namespace}, dep); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching deployment for status: %w", err)
	}

	// Update status with observed replica count and Available condition.
	webapp.Status.AvailableReplicas = dep.Status.AvailableReplicas
	available := dep.Status.AvailableReplicas > 0
	availStatus := metav1.ConditionFalse
	availReason := "DeploymentUnavailable"
	availMsg := "no replicas are available yet"
	if available {
		availStatus = metav1.ConditionTrue
		availReason = "DeploymentAvailable"
		availMsg = fmt.Sprintf("%d replica(s) available", dep.Status.AvailableReplicas)
	}
	if err := r.setCondition(ctx, webapp, appv1alpha1.TypeAvailable, availStatus, availReason, availMsg); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setCondition(ctx, webapp, appv1alpha1.TypeProgressing, metav1.ConditionFalse,
		"ReconcileComplete", "reconciliation complete"); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setCondition(ctx, webapp, appv1alpha1.TypeDegraded, metav1.ConditionFalse,
		"ReconcileComplete", "no errors"); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconciliation complete",
		"name", webapp.Name,
		"availableReplicas", webapp.Status.AvailableReplicas,
	)

	// Requeue after requeueAfter to self-heal against any drift not caught by watches.
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileDeployment creates or updates the Deployment for the given WebApp.
// It sets an owner reference so the Deployment is garbage-collected with the WebApp.
// Only the image, replicas, and port fields are updated on an existing Deployment
// to avoid clobbering fields managed by other controllers (e.g. HPA).
func (r *WebAppReconciler) reconcileDeployment(ctx context.Context, webapp *appv1alpha1.WebApp) error {
	log := logf.FromContext(ctx)

	replicas := int32(1)
	if webapp.Spec.Replicas != nil {
		replicas = *webapp.Spec.Replicas
	}

	desired := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webapp.Name,
			Namespace: webapp.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForWebApp(webapp.Name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForWebApp(webapp.Name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "webapp",
							Image: webapp.Spec.Image,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: webapp.Spec.Port,
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
					},
				},
			},
		},
	}

	// Set the WebApp as the owner of the Deployment so it is garbage-collected on deletion.
	if err := controllerutil.SetControllerReference(webapp, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on deployment: %w", err)
	}

	existing := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: webapp.Name, Namespace: webapp.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		log.Info("creating deployment", "name", webapp.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting deployment: %w", err)
	}

	// Selectively update only the fields we own to avoid conflicts with other controllers.
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template.Spec.Containers[0].Image = desired.Spec.Template.Spec.Containers[0].Image
	existing.Spec.Template.Spec.Containers[0].Ports = desired.Spec.Template.Spec.Containers[0].Ports
	log.Info("updating deployment", "name", webapp.Name)
	return r.Update(ctx, existing)
}

// reconcileService creates or updates the ClusterIP Service for the given WebApp.
// It sets an owner reference so the Service is garbage-collected with the WebApp.
func (r *WebAppReconciler) reconcileService(ctx context.Context, webapp *appv1alpha1.WebApp) error {
	log := logf.FromContext(ctx)

	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      webapp.Name,
			Namespace: webapp.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: labelsForWebApp(webapp.Name),
			Ports: []corev1.ServicePort{
				{
					Port:       webapp.Spec.Port,
					TargetPort: intstr.FromInt32(webapp.Spec.Port),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	// Set the WebApp as the owner of the Service so it is garbage-collected on deletion.
	if err := controllerutil.SetControllerReference(webapp, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on service: %w", err)
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: webapp.Name, Namespace: webapp.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		log.Info("creating service", "name", webapp.Name)
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("getting service: %w", err)
	}

	// Update the port mapping only; ClusterIP and other fields are immutable.
	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Selector = desired.Spec.Selector
	log.Info("updating service", "name", webapp.Name)
	return r.Update(ctx, existing)
}

// cleanupChildResources removes any resources that are not automatically garbage-collected
// via owner references. For this operator, owner references handle Deployment and Service
// cleanup, so this function is a no-op placeholder for future use (e.g. external resources).
func (r *WebAppReconciler) cleanupChildResources(_ context.Context, webapp *appv1alpha1.WebApp) error {
	// Child resources (Deployment, Service) are owned via SetControllerReference and will
	// be garbage-collected by Kubernetes automatically. No manual cleanup required here.
	_ = webapp
	return nil
}

// setCondition updates a single status condition on the WebApp and persists it via
// the status subresource. It uses meta.SetStatusCondition to handle deduplication.
func (r *WebAppReconciler) setCondition(ctx context.Context, webapp *appv1alpha1.WebApp,
	condType string, status metav1.ConditionStatus, reason, message string) error {
	meta.SetStatusCondition(&webapp.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: webapp.Generation,
	})
	if err := r.Status().Update(ctx, webapp); err != nil {
		return fmt.Errorf("updating status condition %s: %w", condType, err)
	}
	return nil
}

// labelsForWebApp returns the standard label set applied to all resources
// managed by this operator for a given WebApp name.
func labelsForWebApp(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "webapp",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "platform-operator",
	}
}

// SetupWithManager sets up the controller with the Manager.
// It watches WebApp resources and also watches owned Deployments and Services
// so that changes to child resources trigger reconciliation.
func (r *WebAppReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appv1alpha1.WebApp{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("webapp").
		Complete(r)
}
