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

package handlers

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ClusterQueuesGVR defines the GroupVersionResource for ClusterQueues
func ClusterQueuesGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "clusterqueues",
	}
}

// WorkloadsGVR defines the GroupVersionResource for Workloads
func WorkloadsGVR() schema.GroupVersionResource {
	workloadsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "workloads",
	}
	return workloadsGVR
}

// LocalQueuesGVR defines the GroupVersionResource  for LocalQueues
func LocalQueuesGVR() schema.GroupVersionResource {
	localQueuesGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "localqueues",
	}
	return localQueuesGVR
}

// CohortsGVR defines the GroupVersionResource for Cohorts
func CohortsGVR() schema.GroupVersionResource {
	cohortsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "cohorts",
	}
	return cohortsGVR
}

// ResourceFlavorsGVR defines the GroupVersionResource for ResourceFlavors
func ResourceFlavorsGVR() schema.GroupVersionResource {
	resourceFlavorsGVR := schema.GroupVersionResource{
		Group:    "kueue.x-k8s.io",
		Version:  "v1beta1",
		Resource: "resourceflavors",
	}
	return resourceFlavorsGVR
}

// NodesGVR defines the GroupVersionResource for Nodes
func NodesGVR() schema.GroupVersionResource {
	nodeGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "nodes",
	}
	return nodeGVR
}

// EventsGVR defines the GroupVersionResource for Events
func EventsGVR() schema.GroupVersionResource {
	eventsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "events",
	}
	return eventsGVR
}

// PodsGVR defines the GroupVersionResource Pods
func PodsGVR() schema.GroupVersionResource {
	podsGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	return podsGVR
}
