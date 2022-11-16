package deleter

import (
	"context"

	sitev1 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	"github.com/superedge/superedge/pkg/site-manager/utils"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
)

// NodeUnitDeleter clean up node label and unit cluster when
// NodeUnit has been deleted
type NodeUnitDeleter struct {
	kubeClient     clientset.Interface
	crdClient      *crdClientset.Clientset
	nodeLister     corelisters.NodeLister
	finalizerToken string
}

func NewNodeUnitDeleter(
	kubeClient clientset.Interface,
	crdClient *crdClientset.Clientset,
	nodeLister corelisters.NodeLister,
	finalizerToken string,
) *NodeUnitDeleter {
	return &NodeUnitDeleter{
		kubeClient,
		crdClient,
		nodeLister,
		finalizerToken,
	}
}

func (d *NodeUnitDeleter) Delete(ctx context.Context, nu *sitev1.NodeUnit) error {
	_, nodeMap, err := utils.GetNodesByUnit(d.nodeLister, nu)
	if err != nil {
		return err
	}
	return utils.DeleteNodesFromSetNode(d.kubeClient, nu, nodeMap)
}
