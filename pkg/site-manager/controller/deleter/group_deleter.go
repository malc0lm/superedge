package deleter

import (
	"context"

	sitev1alpha2 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	clientset "k8s.io/client-go/kubernetes"
)

// NodeUnitDeleter clean up node label and unit cluster when
// NodeUnit has been deleted
type NodeGroupDeleter struct {
	kubeClient     clientset.Interface
	crdClient      *crdClientset.Clientset
	finalizerToken string
}

func NewNodeGroupDeleter(
	kubeClient clientset.Interface,
	crdClient *crdClientset.Clientset,
	finalizerToken string,
) *NodeGroupDeleter {
	return &NodeGroupDeleter{
		kubeClient,
		crdClient,
		finalizerToken,
	}
}

func (d *NodeGroupDeleter) Delete(ctx context.Context, nu *sitev1alpha2.NodeGroup) error {

	return nil
}
