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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	ephemeralv1alpha1 "github.com/Hoaqim/EE-operator/api/v1alpha1"
)

const ephemeralFinalizer = "ephemeral.hoaqim.dev/finalizer"

// EphemeralEnvironmentReconciler reconciles a EphemeralEnvironment object
type EphemeralEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=ephemeral.hoaqim.dev,resources=ephemeralenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ephemeral.hoaqim.dev,resources=ephemeralenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ephemeral.hoaqim.dev,resources=ephemeralenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *EphemeralEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	var ee ephemeralv1alpha1.EphemeralEnvironment
	if err := r.Get(ctx, req.NamespacedName, &ee); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !ee.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&ee, ephemeralFinalizer) {
			if ee.Status.Namespace != "" {
				ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ee.Status.Namespace}}
				if err := r.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
				logger.Info("deleted namespace", "namespace", ee.Status.Namespace)
			}
			controllerutil.RemoveFinalizer(&ee, ephemeralFinalizer)
			if err := r.Update(ctx, &ee); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&ee, ephemeralFinalizer) {
		controllerutil.AddFinalizer(&ee, ephemeralFinalizer)
		if err := r.Update(ctx, &ee); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	labels := map[string]string{
		"app.kubernetes.io/managed-by": "ephemeral-environment-operator",
		"ephemeral.hoaqim.dev/owner":   ee.Name,
	}

	if ee.Status.Namespace == "" {
		ee.Status.Namespace = fmt.Sprintf("ee-%s-%s", ee.Name, rand.String(5))
		ee.Status.Phase = ephemeralv1alpha1.PhasePending
		if err := r.Status().Update(ctx, &ee); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("assigned namespace", "namespace", ee.Status.Namespace)
	}
	nsName := ee.Status.Namespace

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		maps.Copy(ns.Labels, labels)
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}

	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: ee.Name, Namespace: nsName}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		replicas := int32(1)
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}

		tmpl := ee.Spec.Template.DeepCopy()
		if tmpl.Labels == nil {
			tmpl.Labels = map[string]string{}
		}
		maps.Copy(tmpl.Labels, labels)
		deploy.Spec.Template = *tmpl
		return nil
	}); err != nil {
		return ctrl.Result{}, err
	}

	containers := ee.Spec.Template.Spec.Containers
	if len(containers) > 0 && len(containers[0].Ports) > 0 {
		port := containers[0].Ports[0].ContainerPort
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ee.Name, Namespace: nsName}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
			svc.Spec.Selector = labels
			svc.Spec.Ports = []corev1.ServicePort{{
				Name:       "http",
				Port:       port,
				TargetPort: intstr.FromInt32(port),
			}}
			return nil
		}); err != nil {
			return ctrl.Result{}, err
		}
	}
	ee.Status.Phase = ephemeralv1alpha1.PhaseReady
	expiry := metav1.NewTime(ee.CreationTimestamp.Add(ee.Spec.TTL.Duration))
	ee.Status.ExpiresAt = &expiry
	if err := r.Status().Update(ctx, &ee); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("environment ready", "namespace", nsName, "expiresAt", expiry)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EphemeralEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ephemeralv1alpha1.EphemeralEnvironment{}).
		Named("ephemeralenvironment").
		Complete(r)
}
