// Copyright 2018 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package podmetrics

import (
	"context"
	"fmt"
	"time"

	"github.com/golang/glog"

	"github.com/kubernetes-incubator/metrics-server/pkg/provider"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	v1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/metrics/pkg/apis/metrics"
	_ "k8s.io/metrics/pkg/apis/metrics/install"
)

// kubernetesCadvisorWindow is the max window used by cAdvisor for calculating
// CPU usage rate.  While it can vary, it's no more than this number, but may be
// as low as half this number (when working with no backoff).  It would be really
// nice if the kubelet told us this in the summary API...
var kubernetesCadvisorWindow = 30 * time.Second

type MetricStorage struct {
	groupResource schema.GroupResource
	prov          provider.PodMetricsProvider
	podLister     v1listers.PodLister
}

var _ rest.KindProvider = &MetricStorage{}
var _ rest.Storage = &MetricStorage{}
var _ rest.Getter = &MetricStorage{}
var _ rest.Lister = &MetricStorage{}

func NewStorage(groupResource schema.GroupResource, prov provider.PodMetricsProvider, podLister v1listers.PodLister) *MetricStorage {
	return &MetricStorage{
		groupResource: groupResource,
		prov:          prov,
		podLister:     podLister,
	}
}

// Storage interface
func (m *MetricStorage) New() runtime.Object {
	return &metrics.PodMetrics{}
}

// KindProvider interface
func (m *MetricStorage) Kind() string {
	return "PodMetrics"
}

// Lister interface
func (m *MetricStorage) NewList() runtime.Object {
	return &metrics.PodMetricsList{}
}

// Lister interface
func (m *MetricStorage) List(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
	labelSelector := labels.Everything()
	if options != nil && options.LabelSelector != nil {
		labelSelector = options.LabelSelector
	}
	namespace := genericapirequest.NamespaceValue(ctx)
	pods, err := m.podLister.Pods(namespace).List(labelSelector)
	if err != nil {
		errMsg := fmt.Errorf("Error while listing pods for selector %v: %v", labelSelector, err)
		glog.Error(errMsg)
		return &metrics.PodMetricsList{}, errMsg
	}

	res := metrics.PodMetricsList{}
	for _, pod := range pods {
		podMetrics, err := m.getPodMetrics(pod)
		if err != nil {
			glog.Errorf("unable to fetch pod metrics for pod %s/%s: %v", pod.Namespace, pod.Name, err)
			continue
		}
		res.Items = append(res.Items, *podMetrics)
	}
	return &res, nil
}

// Getter interface
func (m *MetricStorage) Get(ctx context.Context, name string, opts *metav1.GetOptions) (runtime.Object, error) {
	namespace := genericapirequest.NamespaceValue(ctx)

	pod, err := m.podLister.Pods(namespace).Get(name)
	if err != nil {
		errMsg := fmt.Errorf("Error while getting pod %v: %v", name, err)
		glog.Error(errMsg)
		if errors.IsNotFound(err) {
			// return not-found errors directly
			return &metrics.PodMetrics{}, err
		}
		return &metrics.PodMetrics{}, errMsg
	}
	if pod == nil {
		return &metrics.PodMetrics{}, errors.NewNotFound(v1.Resource("pods"), fmt.Sprintf("%v/%v", namespace, name))
	}

	podMetrics, err := m.getPodMetrics(pod)
	if err != nil {
		glog.Errorf("unable to fetch pod metrics for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		return nil, errors.NewNotFound(m.groupResource, fmt.Sprintf("%v/%v", namespace, name))
	}
	return podMetrics, nil
}

func (m *MetricStorage) getPodMetrics(pod *v1.Pod) (*metrics.PodMetrics, error) {
	ts, containerMetrics, err := m.prov.GetContainerMetrics(apitypes.NamespacedName{
		Name:      pod.Name,
		Namespace: pod.Namespace,
	})
	if err != nil {
		return nil, err
	}

	res := &metrics.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:              pod.Name,
			Namespace:         pod.Namespace,
			CreationTimestamp: metav1.NewTime(time.Now()),
		},
		Timestamp: metav1.NewTime(ts),
		// TODO(directxman12): figure out what the right value is here,
		// we don't get the actual window from cAdvisor, so we could just
		// plumb down metric resolution, but that wouldn't be actually correct.
		Window:     metav1.Duration{Duration: kubernetesCadvisorWindow},
		Containers: containerMetrics,
	}

	return res, nil
}

func (m *MetricStorage) NamespaceScoped() bool {
	return true
}
