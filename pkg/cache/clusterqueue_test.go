/*
Copyright 2023 The Kubernetes Authors.

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

package cache

import (
	"context"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestClusterQueueUpdateWithFlavors(t *testing.T) {
	rf := utiltesting.MakeResourceFlavor("x86").Obj()
	cq := utiltesting.MakeClusterQueue("cq").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()

	testcases := []struct {
		name       string
		curStatus  metrics.ClusterQueueStatus
		flavors    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		wantStatus metrics.ClusterQueueStatus
	}{
		{
			name:      "Pending clusterQueue updated existent flavors",
			curStatus: pending,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: active,
		},
		{
			name:       "Active clusterQueue updated with not found flavors",
			curStatus:  active,
			flavors:    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{},
			wantStatus: pending,
		},
		{
			name:      "Terminating clusterQueue updated with existent flavors",
			curStatus: terminating,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: terminating,
		},
		{
			name:       "Terminating clusterQueue updated with not found flavors",
			curStatus:  terminating,
			wantStatus: terminating,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.curStatus
			cq.UpdateWithFlavors(tc.flavors)

			if cq.Status != tc.wantStatus {
				t.Fatalf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
			}
		})
	}
}

func TestFitInCohort(t *testing.T) {
	cases := map[string]struct {
		request            resources.FlavorResourceQuantities
		wantFit            bool
		cq                 *ClusterQueueSnapshot
		enableLendingLimit bool
	}{
		"full cohort, empty request": {
			request: resources.FlavorResourceQuantities{},
			wantFit: true,
			cq: &ClusterQueueSnapshot{
				Name: "CQ",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
				},
				ResourceGroups: nil,
			},
		},
		"can fit": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f2", Resource: corev1.ResourceCPU}:    1,
				{Flavor: "f2", Resource: corev1.ResourceMemory}: 1,
			}.Unflatten(),
			wantFit: true,
			cq: &ClusterQueueSnapshot{
				Name: "CQ",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    4,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 4,
					}.Unflatten(),
				},
				ResourceGroups: nil,
			},
		},
		"full cohort, none fit": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f1", Resource: corev1.ResourceCPU}:    1,
				{Flavor: "f1", Resource: corev1.ResourceMemory}: 1,
				{Flavor: "f2", Resource: corev1.ResourceCPU}:    1,
				{Flavor: "f2", Resource: corev1.ResourceMemory}: 1,
			}.Unflatten(),
			wantFit: false,
			cq: &ClusterQueueSnapshot{
				Name: "CQ",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
				},
				ResourceGroups: nil,
			},
		},
		"one cannot fit": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f1", Resource: corev1.ResourceCPU}:    1,
				{Flavor: "f1", Resource: corev1.ResourceMemory}: 1,
				{Flavor: "f2", Resource: corev1.ResourceCPU}:    2,
				{Flavor: "f2", Resource: corev1.ResourceMemory}: 1,
			}.Unflatten(),
			wantFit: false,
			cq: &ClusterQueueSnapshot{
				Name: "CQ",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    4,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 4,
						{Flavor: "f2", Resource: corev1.ResourceCPU}:    4,
						{Flavor: "f2", Resource: corev1.ResourceMemory}: 4,
					}.Unflatten(),
				},
				ResourceGroups: nil,
			},
		},
		"missing flavor": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f2", Resource: corev1.ResourceCPU}:    1,
				{Flavor: "f2", Resource: corev1.ResourceMemory}: 1,
			}.Unflatten(),
			wantFit: false,
			cq: &ClusterQueueSnapshot{
				Name: "CQ",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}:    5,
						{Flavor: "f1", Resource: corev1.ResourceMemory}: 5,
					}.Unflatten(),
				},
				ResourceGroups: nil,
			},
		},
		"missing resource": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f1", Resource: corev1.ResourceCPU}:    1,
				{Flavor: "f1", Resource: corev1.ResourceMemory}: 1,
			}.Unflatten(),
			wantFit: false,
			cq: &ClusterQueueSnapshot{
				Name: "CQ",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}: 5,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}: 3,
					}.Unflatten(),
				},
				ResourceGroups: nil,
			},
		},
		"lendingLimit enabled can't fit": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f1", Resource: corev1.ResourceCPU}: 3,
			}.Unflatten(),
			wantFit: false,
			cq: &ClusterQueueSnapshot{
				Name: "CQ-A",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource:
						// CQ-A has 2 nominal cpu, CQ-B has 3 nominal cpu and 2 lendingLimit,
						// so when lendingLimit enabled, the cohort's RequestableResources is 4 cpu.
						corev1.ResourceCPU}: 4,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource: corev1.ResourceCPU}: 2,
					}.Unflatten(),
				},
				GuaranteedQuota: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "f1", Resource: corev1.ResourceCPU}: 0,
				}.Unflatten(),
			},
			enableLendingLimit: true,
		},
		"lendingLimit enabled can fit": {
			request: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "f1", Resource: corev1.ResourceCPU}: 3,
			}.Unflatten(),
			wantFit: true,
			cq: &ClusterQueueSnapshot{
				Name: "CQ-A",
				Cohort: &CohortSnapshot{
					Name: "C",
					RequestableResources: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource:
						// CQ-A has 2 nominal cpu, CQ-B has 3 nominal cpu and 2 lendingLimit,
						// so when lendingLimit enabled, the cohort's RequestableResources is 4 cpu.
						corev1.ResourceCPU}: 4,
					}.Unflatten(),
					Usage: resources.FlavorResourceQuantitiesFlat{
						{Flavor: "f1", Resource:
						// CQ-B has admitted a workload with 2 cpus, but with 1 GuaranteedQuota,
						// so when lendingLimit enabled, Cohort.Usage should be 2 - 1 = 1.
						corev1.ResourceCPU}: 1,
					}.Unflatten(),
				},
				GuaranteedQuota: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "f1", Resource: corev1.ResourceCPU}: 2,
				}.Unflatten(),
			},
			enableLendingLimit: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer features.SetFeatureGateDuringTest(t, features.LendingLimit, tc.enableLendingLimit)()
			got := tc.cq.FitInCohort(tc.request)
			if got != tc.wantFit {
				t.Errorf("Unexpected result, %v", got)
			}
		})
	}
}

func TestClusterQueueUpdate(t *testing.T) {
	resourceFlavors := []*kueue.ResourceFlavor{
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
	}
	clusterQueue :=
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	newClusterQueue :=
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "100", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	cases := []struct {
		name                         string
		cq                           *kueue.ClusterQueue
		newcq                        *kueue.ClusterQueue
		wantLastAssignmentGeneration int64
	}{
		{
			name:                         "RGs not change",
			cq:                           &clusterQueue,
			newcq:                        clusterQueue.DeepCopy(),
			wantLastAssignmentGeneration: 1,
		},
		{
			name:                         "RGs changed",
			cq:                           &clusterQueue,
			newcq:                        &newClusterQueue,
			wantLastAssignmentGeneration: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
					tc.cq,
				)
			cl := clientBuilder.Build()
			cqCache := New(cl)
			// Workloads are loaded into queues or clusterQueues as we add them.
			for _, rf := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(rf)
			}
			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}
			if err := cqCache.UpdateClusterQueue(tc.newcq); err != nil {
				t.Fatalf("Updating clusterQueue %s in cache: %v", tc.newcq.Name, err)
			}
			snapshot := cqCache.Snapshot()
			if diff := cmp.Diff(
				tc.wantLastAssignmentGeneration,
				snapshot.ClusterQueues["eng-alpha"].AllocatableResourceGeneration); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueUpdateWithAdmissionCheck(t *testing.T) {
	cqWithAC := utiltesting.MakeClusterQueue("cq").
		AdmissionChecks("check1", "check2", "check3").
		Obj()

	cqWithACStrategy := utiltesting.MakeClusterQueue("cq2").
		AdmissionCheckStrategy(
			*utiltesting.MakeAdmissionCheckStrategyRule("check1").Obj(),
			*utiltesting.MakeAdmissionCheckStrategyRule("check2").Obj(),
			*utiltesting.MakeAdmissionCheckStrategyRule("check3").Obj()).
		Obj()

	cqWithACPerFlavor := utiltesting.MakeClusterQueue("cq3").
		AdmissionCheckStrategy(
			*utiltesting.MakeAdmissionCheckStrategyRule("check1", "flavor1", "flavor2", "flavor3").Obj(),
		).
		Obj()

	testcases := []struct {
		name            string
		cq              *kueue.ClusterQueue
		cqStatus        metrics.ClusterQueueStatus
		admissionChecks map[string]AdmissionCheck
		wantStatus      metrics.ClusterQueueStatus
		wantReason      string
	}{
		{
			name:     "Pending clusterQueue updated valid AC list",
			cq:       cqWithAC,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: active,
			wantReason: "Ready",
		},
		{
			name:     "Pending clusterQueue with an AC strategy updated valid AC list",
			cq:       cqWithACStrategy,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: active,
			wantReason: "Ready",
		},
		{
			name:     "Active clusterQueue updated with not found AC",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with not found AC",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue updated with inactive AC",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     false,
					Controller: "controller3",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with inactive AC",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     false,
					Controller: "controller3",
				},
			},
			wantStatus: pending,
			wantReason: "CheckNotFoundOrInactive",
		},
		{
			name:     "Active clusterQueue updated with duplicate single instance AC Controller",
			cq:       cqWithAC,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:                       true,
					Controller:                   "controller1",
					SingleInstanceInClusterQueue: true,
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:                       true,
					Controller:                   "controller2",
					SingleInstanceInClusterQueue: true,
				},
			},
			wantStatus: pending,
			wantReason: "MultipleSingleInstanceControllerChecks",
		},
		{
			name:     "Active clusterQueue with an AC strategy updated with duplicate single instance AC Controller",
			cq:       cqWithACStrategy,
			cqStatus: active,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:                       true,
					Controller:                   "controller1",
					SingleInstanceInClusterQueue: true,
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:                       true,
					Controller:                   "controller2",
					SingleInstanceInClusterQueue: true,
				},
			},
			wantStatus: pending,
			wantReason: "MultipleSingleInstanceControllerChecks",
		},
		{
			name:     "Active clusterQueue with a FlavorIndependent AC applied per ResourceFlavor",
			cq:       cqWithACPerFlavor,
			cqStatus: pending,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:            true,
					Controller:        "controller1",
					FlavorIndependent: true,
				},
			},
			wantStatus: pending,
			wantReason: "FlavorIndependentAdmissionCheckAppliedPerFlavor",
		},
		{
			name:     "Terminating clusterQueue updated with valid AC list",
			cq:       cqWithAC,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
		{
			name:     "Terminating clusterQueue with an AC strategy updated with valid AC list",
			cq:       cqWithACStrategy,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
				"check3": {
					Active:     true,
					Controller: "controller3",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
		{
			name:     "Terminating clusterQueue updated with not found AC",
			cq:       cqWithAC,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
		{
			name:     "Terminating clusterQueue with an AC strategy updated with not found AC",
			cq:       cqWithACStrategy,
			cqStatus: terminating,
			admissionChecks: map[string]AdmissionCheck{
				"check1": {
					Active:     true,
					Controller: "controller1",
				},
				"check2": {
					Active:     true,
					Controller: "controller2",
				},
			},
			wantStatus: terminating,
			wantReason: "Terminating",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(tc.cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.cqStatus

			// Align the admission check related internals to the desired Status.
			if tc.cqStatus == active {
				cq.hasMultipleSingleInstanceControllersChecks = false
				cq.hasMissingOrInactiveAdmissionChecks = false
				cq.hasFlavorIndependentAdmissionCheckAppliedPerFlavor = false
			} else {
				cq.hasMultipleSingleInstanceControllersChecks = true
				cq.hasMissingOrInactiveAdmissionChecks = true
				cq.hasFlavorIndependentAdmissionCheckAppliedPerFlavor = true
			}
			cq.updateWithAdmissionChecks(tc.admissionChecks)

			if cq.Status != tc.wantStatus {
				t.Errorf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
			}

			gotReason, _ := cq.inactiveReason()
			if diff := cmp.Diff(tc.wantReason, gotReason); diff != "" {
				t.Errorf("Unexpected inactiveReason (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestDominantResourceShare(t *testing.T) {
	cases := map[string]struct {
		cq          ClusterQueueSnapshot
		flvResQ     resources.FlavorResourceQuantitiesFlat
		wantDRValue int
		wantDRName  corev1.ResourceName
	}{
		"no cohort": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
					{Flavor: "default", Resource: "example.com/gpu"}:  2,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 2_000,
									},
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
			},
		},
		"usage below nominal": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
					{Flavor: "default", Resource: "example.com/gpu"}:  2,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 2_000,
									},
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
						"example.com/gpu":  10,
					},
				},
			},
		},
		"usage above nominal": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
					{Flavor: "default", Resource: "example.com/gpu"}:  7,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 2_000,
									},
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
						"example.com/gpu":  10,
					},
				},
			},
			wantDRName:  "example.com/gpu",
			wantDRValue: 200, // (7-5)*1000/10
		},
		"one resource above nominal": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
					{Flavor: "default", Resource: "example.com/gpu"}:  3,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 2_000,
									},
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
						"example.com/gpu":  10,
					},
				},
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 100, // (3-2)*1000/10
		},
		"usage with workload above nominal": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
					{Flavor: "default", Resource: "example.com/gpu"}:  2,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 2_000,
									},
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
						"example.com/gpu":  10,
					},
				},
			},
			flvResQ: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 300, // (1+4-2)*1000/10
		},
		"A resource with zero lendable": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
					{Flavor: "default", Resource: "example.com/gpu"}:  1,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 2_000,
									},
									"example.com/gpu": {
										Nominal:      2,
										LendingLimit: ptr.To[int64](0),
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
						"example.com/gpu":  0,
					},
				},
			},
			flvResQ: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 300, // (1+4-2)*1000/10
		},
		"multiple flavors": {
			cq: ClusterQueueSnapshot{
				FairWeight: oneQuantity,
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 15_000,
					{Flavor: "spot", Resource: corev1.ResourceCPU}:      5_000,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "on-demand",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 20_000,
									},
								},
							},
							{
								Name: "spot",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 80_000,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 200_000,
					},
				},
			},
			flvResQ: resources.FlavorResourceQuantitiesFlat{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 25, // ((15+10-20)+0)*1000/200 (spot under nominal)
		},
		"above nominal with integer weight": {
			cq: ClusterQueueSnapshot{
				FairWeight: resource.MustParse("2"),
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: "example.com/gpu"}: 7,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						"example.com/gpu": 10,
					},
				},
			},
			wantDRName:  "example.com/gpu",
			wantDRValue: 100, // ((7-5)*1000/10)/2
		},
		"above nominal with decimal weight": {
			cq: ClusterQueueSnapshot{
				FairWeight: resource.MustParse("0.5"),
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: "example.com/gpu"}: 7,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						"example.com/gpu": 10,
					},
				},
			},
			wantDRName:  "example.com/gpu",
			wantDRValue: 400, // ((7-5)*1000/10)/(1/2)
		},
		"above nominal with zero weight": {
			cq: ClusterQueueSnapshot{
				Usage: resources.FlavorResourceQuantitiesFlat{
					{Flavor: "default", Resource: "example.com/gpu"}: 7,
				}.Unflatten(),
				ResourceGroups: []ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, "example.com/gpu"),
						Flavors: []FlavorQuotas{
							{
								Name: "default",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									"example.com/gpu": {
										Nominal: 5,
									},
								},
							},
						},
					},
				},
				Cohort: &CohortSnapshot{
					Lendable: map[corev1.ResourceName]int64{
						"example.com/gpu": 10,
					},
				},
			},
			wantDRValue: math.MaxInt,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			drValue, drName := tc.cq.DominantResourceShareWith(tc.flvResQ)
			if drValue != tc.wantDRValue {
				t.Errorf("DominantResourceShare(_) returned value %d, want %d", drValue, tc.wantDRValue)
			}
			if drName != tc.wantDRName {
				t.Errorf("DominantResourceShare(_) returned resource %s, want %s", drName, tc.wantDRName)
			}
		})
	}
}

func TestCohortLendable(t *testing.T) {
	cache := New(utiltesting.NewFakeClient())

	cq1 := utiltesting.MakeClusterQueue("cq1").
		ResourceGroup(
			utiltesting.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").LendingLimit("8").Append().
				ResourceQuotaWrapper("example.com/gpu").NominalQuota("3").LendingLimit("3").Append().
				FlavorQuotas,
		).Cohort("test-cohort").
		ClusterQueue

	cq2 := utiltesting.MakeClusterQueue("cq2").
		ResourceGroup(
			utiltesting.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("2").LendingLimit("2").Append().
				FlavorQuotas,
		).Cohort("test-cohort").
		ClusterQueue

	if err := cache.AddClusterQueue(context.Background(), &cq1); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}
	if err := cache.AddClusterQueue(context.Background(), &cq2); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}

	wantLendable := map[corev1.ResourceName]int64{
		corev1.ResourceCPU: 10_000,
		"example.com/gpu":  3,
	}

	lendable := cache.clusterQueues["cq1"].Cohort.CalculateLendable()
	if diff := cmp.Diff(wantLendable, lendable); diff != "" {
		t.Errorf("Unexpected cohort lendable (-want,+got):\n%s", diff)
	}
}
