/*
Copyright The Kubernetes Authors.

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
	"sync"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
)

type LocalQueue struct {
	sync.RWMutex
	key                queue.LocalQueueReference
	reservingWorkloads int
	admittedWorkloads  int
	totalReserved      resources.FlavorResourceQuantities
	admittedUsage      resources.FlavorResourceQuantities
}

func (lq *LocalQueue) GetAdmittedUsage() corev1.ResourceList {
	lq.RLock()
	defer lq.RUnlock()
	return lq.admittedUsage.FlattenFlavors().ToResourceList()
}

func (lq *LocalQueue) updateAdmittedUsage(usage resources.FlavorResourceQuantities, op usageOp) {
	lq.Lock()
	defer lq.Unlock()
	updateFlavorUsage(usage, lq.admittedUsage, op)
}
