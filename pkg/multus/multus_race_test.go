// Copyright (c) 2026 Multus Authors
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

package multus

// HACKCTF: tests for the net1 secondary-network attach race fix in GetPod.
// See the "HACKCTF fix (net1 secondary-network attach race)" block in multus.go.

import (
	"sync/atomic"
	"testing"

	cnitypes "github.com/containernetworking/cni/pkg/types"
	"gopkg.in/k8snetworkplumbingwg/multus-cni.v4/pkg/k8sclient"
	"gopkg.in/k8snetworkplumbingwg/multus-cni.v4/pkg/types"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNetworksAnnot = "k8s.v1.cni.cncf.io/networks"

func racePod(ns, name, netAnnot string) *v1.Pod {
	ann := map[string]string{}
	if netAnnot != "" {
		ann[testNetworksAnnot] = netAnnot
	}
	return &v1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Annotations: ann}}
}

func raceK8sArgs(ns, name string) *types.K8sArgs {
	return &types.K8sArgs{
		K8S_POD_NAMESPACE: cnitypes.UnmarshallableString(ns),
		K8S_POD_NAME:      cnitypes.UnmarshallableString(name),
	}
}

func TestPodHasNoNetworkAnnotation(t *testing.T) {
	if !podHasNoNetworkAnnotation(racePod("ns", "p", "")) {
		t.Error("expected true for pod without networks annotation")
	}
	if podHasNoNetworkAnnotation(racePod("ns", "p", "netA")) {
		t.Error("expected false for pod with networks annotation")
	}
	if podHasNoNetworkAnnotation(nil) {
		t.Error("expected false for nil pod")
	}
}

// GetPod must re-read the pod live when the first read observed an empty
// network-selection annotation (stale read), returning the annotated pod.
func TestGetPodReReadsStaleEmptyAnnotation(t *testing.T) {
	var calls int32
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return true, racePod("ns", "p", ""), nil // first (stale) read: annotation missing
		}
		return true, racePod("ns", "p", "netA"), nil // live re-read: annotation present
	})
	ci := &k8sclient.ClientInfo{Client: client}

	pod, err := GetPod(ci, raceK8sArgs("ns", "p"), false)
	if err != nil {
		t.Fatalf("GetPod returned error: %v", err)
	}
	if podHasNoNetworkAnnotation(pod) {
		t.Fatalf("expected GetPod to return pod WITH networks annotation after live re-read (calls=%d)", atomic.LoadInt32(&calls))
	}
	if c := atomic.LoadInt32(&calls); c < 2 {
		t.Fatalf("expected at least 2 API reads (initial + live re-read), got %d", c)
	}
}

// On CNI DEL, GetPod must NOT run the empty-annotation re-read loop.
func TestGetPodNoReReadOnDel(t *testing.T) {
	var calls int32
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		atomic.AddInt32(&calls, 1)
		return true, racePod("ns", "p", ""), nil
	})
	ci := &k8sclient.ClientInfo{Client: client}

	if _, err := GetPod(ci, raceK8sArgs("ns", "p"), true); err != nil {
		t.Fatalf("GetPod(isDel=true) returned error: %v", err)
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Fatalf("expected exactly 1 read on DEL (no re-read loop), got %d", c)
	}
}
