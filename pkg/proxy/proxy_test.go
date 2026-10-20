package proxy

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"kubesphere.io/tower/pkg/api/cluster/v1alpha1"
	"kubesphere.io/tower/pkg/utils"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestAddClusterKeepsAllocatedPortWhenStatusCleared reproduces kubesphere/project#7417:
// the Cluster CRD declares no status subresource, so an external full-object update
// (for example the console editing a label) wipes status.conditions. Reconcile must
// not treat that as "the cluster is not initialized yet" and allocate a brand new
// proxy port, otherwise the mc-<cluster> Service is repointed to a port that nothing
// listens on and the cluster stays NotReady forever.
func TestAddClusterKeepsAllocatedPortWhenStatusCleared(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}

	// Use ports outside the allocatable range [6000,7000] so the assertions are
	// deterministic no matter which port allocatePort() would have picked.
	const k8sPort, ksPort uint16 = 5000, 15000

	cluster := &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
		Spec: v1alpha1.ClusterSpec{
			JoinFederation: true,
			Connection: v1alpha1.Connection{
				Type:                    v1alpha1.ConnectionTypeProxy,
				Token:                   "fixed-token",
				KubernetesAPIServerPort: k8sPort,
				KubeSphereAPIServerPort: ksPort,
			},
		},
		// status.conditions is intentionally empty, simulating the wiped status.
	}

	clusterClient := crfake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster).Build()
	p := &Proxy{
		clusterClient: clusterClient,
		k8sClientSet:  fake.NewSimpleClientset(),
		agents:        utils.NewAgents(),
	}

	if err := p.addCluster(cluster.DeepCopy()); err != nil {
		t.Fatalf("addCluster returned error: %v", err)
	}

	got := &v1alpha1.Cluster{}
	if err := clusterClient.Get(context.TODO(), types.NamespacedName{Name: "alpha"}, got); err != nil {
		t.Fatalf("failed to get cluster: %v", err)
	}

	if got.Spec.Connection.KubernetesAPIServerPort != k8sPort {
		t.Errorf("kubernetes api server port = %d, want %d (existing port must be reused)",
			got.Spec.Connection.KubernetesAPIServerPort, k8sPort)
	}
	if got.Spec.Connection.KubeSphereAPIServerPort != ksPort {
		t.Errorf("kubesphere api server port = %d, want %d (existing port must be reused)",
			got.Spec.Connection.KubeSphereAPIServerPort, ksPort)
	}
	if got.Spec.Connection.Token != "fixed-token" {
		t.Errorf("token = %q, want %q (existing token must be kept)", got.Spec.Connection.Token, "fixed-token")
	}
	if !isConditionTrue(got, v1alpha1.ClusterInitialized) {
		t.Errorf("expected Initialized condition to be restored after the status was cleared")
	}
}

// TestPortInUseMatchesKubernetesAPIServerPort guards the port collision check used by
// allocatePort. The kubernetes api server port lives in [6000,7000] while the
// kubesphere api server port is offset by 10000, so checking the wrong field silently
// disables collision detection and allocates duplicate ports.
func TestPortInUseMatchesKubernetesAPIServerPort(t *testing.T) {
	clusters := &v1alpha1.ClusterList{
		Items: []v1alpha1.Cluster{
			{
				Spec: v1alpha1.ClusterSpec{
					Connection: v1alpha1.Connection{
						Type:                    v1alpha1.ConnectionTypeProxy,
						KubernetesAPIServerPort: 6001,
						KubeSphereAPIServerPort: 16001,
					},
				},
			},
		},
	}

	if !portInUse(clusters, 6001) {
		t.Errorf("port 6001 is a kubernetes api server port and must be reported as in use")
	}
	if portInUse(clusters, 16001) {
		t.Errorf("port 16001 is a kubesphere api server port and must not collide with a kubernetes api server port")
	}
	if portInUse(clusters, 6002) {
		t.Errorf("port 6002 is free and must not be reported as in use")
	}
}
