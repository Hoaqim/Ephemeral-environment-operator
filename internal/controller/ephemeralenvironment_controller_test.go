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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	ephemeralv1alpha1 "github.com/Hoaqim/EE-operator/api/v1alpha1"
)

var _ = Describe("EphemeralEnvironment controller", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	sampleEE := func(name, ttl string) *ephemeralv1alpha1.EphemeralEnvironment {
		d, err := time.ParseDuration(ttl)
		Expect(err).NotTo(HaveOccurred())
		return &ephemeralv1alpha1.EphemeralEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: ephemeralv1alpha1.EphemeralEnvironmentSpec{
				TTL: metav1.Duration{Duration: d},
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "web",
							Image: "nginx:1.27-alpine",
							Ports: []corev1.ContainerPort{{ContainerPort: 80}},
						}},
					},
				},
			},
		}
	}

	Context("when creating an EphemeralEnvironment", func() {
		const name = "test-create"
		ctx := context.Background()
		key := types.NamespacedName{Name: name, Namespace: "default"}

		AfterEach(func() {
			ee := &ephemeralv1alpha1.EphemeralEnvironment{}
			if err := k8sClient.Get(ctx, key, ee); err == nil {
				_ = k8sClient.Delete(ctx, ee)
			}
		})

		It("provisions a namespace, deployment, service, and reaches Ready", func() {
			Expect(k8sClient.Create(ctx, sampleEE(name, "1h"))).To(Succeed())

			By("recording the namespace and Ready phase in status")
			var nsName string
			Eventually(func(g Gomega) {
				ee := &ephemeralv1alpha1.EphemeralEnvironment{}
				g.Expect(k8sClient.Get(ctx, key, ee)).To(Succeed())
				g.Expect(ee.Status.Namespace).NotTo(BeEmpty())
				g.Expect(ee.Status.Phase).To(Equal(ephemeralv1alpha1.PhaseReady))
				g.Expect(ee.Status.ExpiresAt).NotTo(BeNil())
				nsName = ee.Status.Namespace
			}, timeout, interval).Should(Succeed())

			By("creating the namespace")
			Eventually(func(g Gomega) {
				ns := &corev1.Namespace{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nsName}, ns)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			By("creating the deployment with one replica")
			Eventually(func(g Gomega) {
				deploy := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: nsName}, deploy)).To(Succeed())
				g.Expect(*deploy.Spec.Replicas).To(Equal(int32(1)))
			}, timeout, interval).Should(Succeed())

			By("inferring a service from the container port")
			Eventually(func(g Gomega) {
				svc := &corev1.Service{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: nsName}, svc)).To(Succeed())
				g.Expect(svc.Spec.Ports).To(HaveLen(1))
				g.Expect(svc.Spec.Ports[0].Port).To(Equal(int32(80)))
			}, timeout, interval).Should(Succeed())
		})

		It("sets a finalizer on the resource", func() {
			Expect(k8sClient.Create(ctx, sampleEE("test-finalizer-set", "1h"))).To(Succeed())
			k := types.NamespacedName{Name: "test-finalizer-set", Namespace: "default"}
			Eventually(func(g Gomega) {
				ee := &ephemeralv1alpha1.EphemeralEnvironment{}
				g.Expect(k8sClient.Get(ctx, k, ee)).To(Succeed())
				g.Expect(ee.Finalizers).To(ContainElement(ephemeralFinalizer))
			}, timeout, interval).Should(Succeed())
			ee := &ephemeralv1alpha1.EphemeralEnvironment{}
			_ = k8sClient.Get(ctx, k, ee)
			_ = k8sClient.Delete(ctx, ee)
		})
	})

	Context("when an EphemeralEnvironment is reconciled twice", func() {
		It("is idempotent — no duplicate resources, no error", func() {
			ctx := context.Background()
			const name = "test-idempotent"
			key := types.NamespacedName{Name: name, Namespace: "default"}
			Expect(k8sClient.Create(ctx, sampleEE(name, "1h"))).To(Succeed())

			var nsName string
			Eventually(func(g Gomega) {
				ee := &ephemeralv1alpha1.EphemeralEnvironment{}
				g.Expect(k8sClient.Get(ctx, key, ee)).To(Succeed())
				g.Expect(ee.Status.Namespace).NotTo(BeEmpty())
				nsName = ee.Status.Namespace
			}, timeout, interval).Should(Succeed())

			Consistently(func(g Gomega) {
				ee := &ephemeralv1alpha1.EphemeralEnvironment{}
				g.Expect(k8sClient.Get(ctx, key, ee)).To(Succeed())
				g.Expect(ee.Status.Namespace).To(Equal(nsName))
				deploy := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: nsName}, deploy)).To(Succeed())
			}, time.Second*2, interval).Should(Succeed())

			ee := &ephemeralv1alpha1.EphemeralEnvironment{}
			_ = k8sClient.Get(ctx, key, ee)
			_ = k8sClient.Delete(ctx, ee)
		})
	})

	Context("when the TTL expires", func() {
		It("self-deletes the resource", func() {
			ctx := context.Background()
			const name = "test-ttl"
			key := types.NamespacedName{Name: name, Namespace: "default"}
			Expect(k8sClient.Create(ctx, sampleEE(name, "1s"))).To(Succeed())

			By("the CR being gone after the TTL elapses")
			Eventually(func() bool {
				ee := &ephemeralv1alpha1.EphemeralEnvironment{}
				err := k8sClient.Get(ctx, key, ee)
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})

	Context("when the CR is deleted", func() {
		It("removes the finalizer so the CR is fully deleted", func() {
			ctx := context.Background()
			const name = "test-delete"
			key := types.NamespacedName{Name: name, Namespace: "default"}
			Expect(k8sClient.Create(ctx, sampleEE(name, "1h"))).To(Succeed())

			Eventually(func(g Gomega) {
				ee := &ephemeralv1alpha1.EphemeralEnvironment{}
				g.Expect(k8sClient.Get(ctx, key, ee)).To(Succeed())
				g.Expect(ee.Status.Namespace).NotTo(BeEmpty())
			}, timeout, interval).Should(Succeed())

			ee := &ephemeralv1alpha1.EphemeralEnvironment{}
			Expect(k8sClient.Get(ctx, key, ee)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ee)).To(Succeed())

			By("the CR being fully removed once cleanup completes")
			Eventually(func() bool {
				e := &ephemeralv1alpha1.EphemeralEnvironment{}
				err := k8sClient.Get(ctx, key, e)
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue())
		})
	})

	_ = fmt.Sprintf
})
