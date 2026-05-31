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
	"maps"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	ephemeralv1alpha1 "github.com/Hoaqim/EE-operator/api/v1alpha1"
)

const ephemeralFinalizer = "ephemeral.hoaqim.dev/finalizer"

// EphemeralEnvironmentReconciler reconciles a EphemeralEnvironment object
type EphemeralEnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=ephemeral.hoaqim.dev,resources=ephemeralenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ephemeral.hoaqim.dev,resources=ephemeralenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ephemeral.hoaqim.dev,resources=ephemeralenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *EphemeralEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var ee ephemeralv1alpha1.EphemeralEnvironment
	if err := r.Get(ctx, req.NamespacedName, &ee); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ee.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &ee)
	}

	if !controllerutil.ContainsFinalizer(&ee, ephemeralFinalizer) {
		return r.ensureFinalizer(ctx, &ee)
	}

	if r.isExpired(&ee) {
		return r.reconcileExpiry(ctx, &ee)
	}

	return r.reconcileNormal(ctx, &ee)
}

func (r *EphemeralEnvironmentReconciler) reconcileDelete(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(ee, ephemeralFinalizer) {
		if ee.Status.Namespace != "" {
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ee.Status.Namespace}}
			if err := r.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			logger.Info("deleted namespace", "namespace", ee.Status.Namespace)
			r.Recorder.Eventf(ee, corev1.EventTypeNormal, "Cleanup",
				"Deleted namespace %s", ee.Status.Namespace)
		}
		controllerutil.RemoveFinalizer(ee, ephemeralFinalizer)
		if err := r.Update(ctx, ee); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *EphemeralEnvironmentReconciler) ensureFinalizer(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment) (ctrl.Result, error) {
	controllerutil.AddFinalizer(ee, ephemeralFinalizer)
	if err := r.Update(ctx, ee); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *EphemeralEnvironmentReconciler) reconcileExpiry(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("TTL expired, deleting environment", "expiresAt", ee.Status.ExpiresAt)

	base := ee.DeepCopy()
	ee.Status.Phase = ephemeralv1alpha1.PhaseExpiring
	r.Recorder.Event(ee, corev1.EventTypeNormal, "Expired",
		"TTL elapsed; tearing down environment")
	if err := r.Status().Patch(ctx, ee, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Delete(ctx, ee); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *EphemeralEnvironmentReconciler) reconcileNormal(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "ephemeral-environment-operator",
		"ephemeral.hoaqim.dev/owner":   ee.Name,
	}

	if err := r.ensureNamespaceAssigned(ctx, ee); err != nil {
		return ctrl.Result{}, err
	}
	nsName := ee.Status.Namespace

	if err := r.ensureNamespace(ctx, ee, nsName, labels); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureDeployment(ctx, ee, nsName, labels); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureService(ctx, ee, nsName, labels); err != nil {
		return ctrl.Result{}, err
	}

	expiry, err := r.updateReadyStatus(ctx, ee)
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("environment ready", "namespace", nsName, "expiresAt", expiry)
	return ctrl.Result{RequeueAfter: time.Until(expiry.Time)}, nil
}

func (r *EphemeralEnvironmentReconciler) ensureNamespaceAssigned(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment) error {
	if ee.Status.Namespace != "" {
		return nil
	}
	logger := log.FromContext(ctx)
	base := ee.DeepCopy()
	ee.Status.Namespace = fmt.Sprintf("ee-%s-%s", ee.Name, rand.String(5))
	ee.Status.Phase = ephemeralv1alpha1.PhasePending
	if err := r.Status().Patch(ctx, ee, client.MergeFrom(base)); err != nil {
		return err
	}
	logger.Info("assigned namespace", "namespace", ee.Status.Namespace)
	r.Recorder.Eventf(ee, corev1.EventTypeNormal, "NamespaceAssigned",
		"Assigned namespace %s", ee.Status.Namespace)

	return nil
}

func (r *EphemeralEnvironmentReconciler) ensureNamespace(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment, nsName string, labels map[string]string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		maps.Copy(ns.Labels, labels)

		return nil
	})
	if err != nil {
		return err
	}
	if op == controllerutil.OperationResultCreated {
		r.Recorder.Eventf(ee, corev1.EventTypeNormal, "WorkloadProvisioned",
			"Created namespace %s and workload", nsName)
	}
	return nil
}

func (r *EphemeralEnvironmentReconciler) ensureDeployment(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment, nsName string, labels map[string]string) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: ee.Name, Namespace: nsName}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		replicas := int32(1)
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}

		tmpl := ee.Spec.Template.DeepCopy()
		if tmpl.Labels == nil {
			tmpl.Labels = map[string]string{}
		}
		for k, v := range labels {
			tmpl.Labels[k] = v
		}
		deploy.Spec.Template = *tmpl
		return nil
	})
	return err
}

func (r *EphemeralEnvironmentReconciler) ensureService(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment, nsName string, labels map[string]string) error {
	containers := ee.Spec.Template.Spec.Containers
	if len(containers) == 0 || len(containers[0].Ports) == 0 {
		return nil
	}
	port := containers[0].Ports[0].ContainerPort
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ee.Name, Namespace: nsName}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       port,
			TargetPort: intstr.FromInt32(port),
		}}
		return nil
	})
	return err
}

func (r *EphemeralEnvironmentReconciler) updateReadyStatus(ctx context.Context, ee *ephemeralv1alpha1.EphemeralEnvironment) (metav1.Time, error) {
	base := ee.DeepCopy()
	ee.Status.Phase = ephemeralv1alpha1.PhaseReady
	expiry := metav1.NewTime(ee.CreationTimestamp.Add(ee.Spec.TTL.Duration))
	ee.Status.ExpiresAt = &expiry
	if err := r.Status().Patch(ctx, ee, client.MergeFrom(base)); err != nil {
		return metav1.Time{}, err
	}
	return expiry, nil
}

func (r *EphemeralEnvironmentReconciler) isExpired(ee *ephemeralv1alpha1.EphemeralEnvironment) bool {
	return ee.Status.ExpiresAt != nil && time.Now().After(ee.Status.ExpiresAt.Time)
}

// SetupWithManager sets up the controller with the Manager.
func (r *EphemeralEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ephemeralv1alpha1.EphemeralEnvironment{}).
		Named("ephemeralenvironment").
		Complete(r)
}
