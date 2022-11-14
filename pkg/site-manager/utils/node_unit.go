package utils

import (
	"context"
	"fmt"

	"github.com/superedge/superedge/pkg/util"
	utilkube "github.com/superedge/superedge/pkg/util/kubeclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	sitev1 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/site-manager/constant"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	crdv1listers "github.com/superedge/superedge/pkg/site-manager/generated/listers/site.superedge.io/v1alpha2"
)

// GetUnitsByNode
func GetUnitsByNode(unitLister crdv1listers.NodeUnitLister, node *corev1.Node) (nodeUnits []*sitev1.NodeUnit, unitList []string, err error) {

	for k, v := range node.Labels {
		if v == constant.NodeUnitSuperedge {
			unitList = append(unitList, k)
		}
	}
	for _, nuName := range unitList {
		nu, err := unitLister.Get(nuName)
		if err != nil {
			return nil, nil, err
		}
		nodeUnits = append(nodeUnits, nu)
	}
	return
}

/*
NodeUit Rate
*/
func AddNodeUitReadyRate(nodeUnit *sitev1.NodeUnit) string {
	unitStatus := nodeUnit.Status
	return fmt.Sprintf("%d/%d", len(unitStatus.ReadyNodes), len(unitStatus.ReadyNodes)+len(unitStatus.NotReadyNodes)+1)
}

func GetNodeUitReadyRate(nodeUnit *sitev1.NodeUnit) string {
	unitStatus := nodeUnit.Status
	return fmt.Sprintf("%d/%d", len(unitStatus.ReadyNodes), len(unitStatus.ReadyNodes)+len(unitStatus.NotReadyNodes))
}

func RemoveUnitSetNode(crdClient *crdClientset.Clientset, units, keys []string) error {
	if len(units) == 0 {
		return nil
	}
	for _, unit := range units {
		nodeUnit, err := crdClient.SiteV1alpha1().NodeUnits().Get(context.TODO(), unit, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Remove unit setNode get nodeUnit error: %#v", err)
			continue
		}
		setNode := &nodeUnit.Spec.SetNode
		for _, key := range keys {
			delete(setNode.Labels, key)
		}
		_, err = crdClient.SiteV1alpha1().NodeUnits().Update(context.TODO(), nodeUnit, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("Remove unit setNode update nodeUnit error: %#v", err)
			continue
		}
	}
	return nil
}

func RemoveSetNode(kubeClient clientset.Interface, nodeUnit *sitev1.NodeUnit, nodes []string) error {
	klog.V(4).Infof("Remove setNode nodeUnit: %s will remove nodes: %s setNode: %s", nodeUnit.Name, nodes, util.ToJson(nodeUnit.Spec.SetNode))
	for _, nodeName := range nodes {
		node, err := kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Get node error: %v", err)
			continue
		}

		setNode := &nodeUnit.Spec.SetNode
		if setNode.Labels != nil && node.Labels != nil {
			for labelKey, _ := range setNode.Labels {
				if _, ok := node.Labels[labelKey]; ok {
					delete(node.Labels, labelKey)
				}
			}
		}
		if setNode.Annotations != nil && node.Annotations != nil {
			for annotationKey, _ := range setNode.Annotations {
				if _, ok := node.Annotations[annotationKey]; ok {
					delete(node.Annotations, annotationKey)
				}
			}
		}
		if setNode.Taints != nil && node.Spec.Taints != nil {
			taints := make(map[string]bool, len(setNode.Taints))
			for _, taint := range setNode.Taints {
				taints[taint.Key] = true
			}
			var taintSlice []corev1.Taint
			for _, taint := range node.Spec.Taints {
				if _, ok := taints[taint.Key]; !ok {
					taintSlice = append(taintSlice, taint)
				}
			}
		}

		if _, err := kubeClient.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Remove setNode update node: %s, error: %#v", node.Name, err)
			continue
		}
	}
	return nil
}

func CaculateNodeUnitStatus(nodeMap map[string]*corev1.Node, nu *sitev1.NodeUnit) (*sitev1.NodeUnitStatus, error) {
	status := &sitev1.NodeUnitStatus{}
	var readyList, nodeReadyList []string
	for k, v := range nodeMap {
		if utilkube.IsReadyNode(v) {
			readyList = append(readyList, k)
		} else {
			nodeReadyList = append(nodeReadyList, k)
		}
	}
	status.ReadyRate = fmt.Sprintf("%d/%d", len(readyList), len(readyList)+len(nodeReadyList))

	// malc TODO: caculate unit cluster status
	return status, nil
}

func ListNodeFromLister(nodeLister corelisters.NodeLister, selector labels.Selector, appendFn cache.AppendFunc) error {

	nodes, err := nodeLister.List(selector)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		appendFn(n)
	}
	return nil
}
