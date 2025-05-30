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
// Code generated by informer-gen. DO NOT EDIT.

package v1beta1

import (
	context "context"
	time "time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
	apiskueuev1beta1 "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	versioned "sigs.k8s.io/kueue/client-go/clientset/versioned"
	internalinterfaces "sigs.k8s.io/kueue/client-go/informers/externalversions/internalinterfaces"
	kueuev1beta1 "sigs.k8s.io/kueue/client-go/listers/kueue/v1beta1"
)

// MultiKueueConfigInformer provides access to a shared informer and lister for
// MultiKueueConfigs.
type MultiKueueConfigInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() kueuev1beta1.MultiKueueConfigLister
}

type multiKueueConfigInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewMultiKueueConfigInformer constructs a new informer for MultiKueueConfig type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewMultiKueueConfigInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredMultiKueueConfigInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredMultiKueueConfigInformer constructs a new informer for MultiKueueConfig type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredMultiKueueConfigInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.KueueV1beta1().MultiKueueConfigs().List(context.Background(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.KueueV1beta1().MultiKueueConfigs().Watch(context.Background(), options)
			},
			ListWithContextFunc: func(ctx context.Context, options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.KueueV1beta1().MultiKueueConfigs().List(ctx, options)
			},
			WatchFuncWithContext: func(ctx context.Context, options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.KueueV1beta1().MultiKueueConfigs().Watch(ctx, options)
			},
		},
		&apiskueuev1beta1.MultiKueueConfig{},
		resyncPeriod,
		indexers,
	)
}

func (f *multiKueueConfigInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredMultiKueueConfigInformer(client, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *multiKueueConfigInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apiskueuev1beta1.MultiKueueConfig{}, f.defaultInformer)
}

func (f *multiKueueConfigInformer) Lister() kueuev1beta1.MultiKueueConfigLister {
	return kueuev1beta1.NewMultiKueueConfigLister(f.Informer().GetIndexer())
}
