package deleter

import (
	"context"

	sitev1 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	clientset "k8s.io/client-go/kubernetes"
)

// NodeUnitDeleter clean up node label and unit cluster when
// NodeUnit has been deleted
type NodeUnitDeleter struct {
	kubeClient     clientset.Interface
	crdClient      *crdClientset.Clientset
	finalizerToken string
}

func NewNodeUnitDeleter(
	kubeClient clientset.Interface,
	crdClient *crdClientset.Clientset,
	finalizerToken string,
) *NodeUnitDeleter {
	return &NodeUnitDeleter{
		kubeClient,
		crdClient,
		finalizerToken,
	}
}

func (d *NodeUnitDeleter) Delete(ctx context.Context, nu *sitev1.NodeUnit) error {

	return nil
}
