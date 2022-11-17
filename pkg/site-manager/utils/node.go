package utils

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	sitev1 "github.com/superedge/superedge/pkg/site-manager/apis/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/site-manager/constant"
	crdClientset "github.com/superedge/superedge/pkg/site-manager/generated/clientset/versioned"
	crdv1listers "github.com/superedge/superedge/pkg/site-manager/generated/listers/site.superedge.io/v1alpha2"
	"github.com/superedge/superedge/pkg/util"
	utilkube "github.com/superedge/superedge/pkg/util/kubeclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	clientset "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"

	"k8s.io/klog/v2"
)

// GetUnitsByNode
func GetGroupsByUnit(groupLister crdv1listers.NodeGroupLister, nu *sitev1.NodeUnit) (nodeGroups []*sitev1.NodeGroup, groupList []string, err error) {

	for k, v := range nu.Labels {
		if v == constant.NodeGroupSuperedge {
			groupList = append(groupList, k)
		}
	}
	for _, ngName := range groupList {
		ng, err := groupLister.Get(ngName)
		if err != nil {
			return nil, nil, err
		}
		nodeGroups = append(nodeGroups, ng)
	}
	return
}

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

func SetNodeToNodeUnits(crdClient *crdClientset.Clientset, ng *v1alpha2.NodeGroup, unitMaps map[string]*v1alpha2.NodeUnit) error {
	klog.V(4).InfoS("SetNode to node unit: %s", "nodegroup", ng.Name)

	for _, nu := range unitMaps {
		setNodeReady, ownerLabelReady := true, true
		if nu.Spec.SetNode.Labels != nil && nu.Spec.SetNode.Labels[ng.Name] != nu.Name {
			setNodeReady = false
		}

		if nu.Labels != nil && nu.Labels[ng.Name] != constant.NodeGroupSuperedge {
			ownerLabelReady = false
		}

		if setNodeReady && ownerLabelReady {
			continue
		}
		newNu := nu.DeepCopy()
		// update setnode for add label({nodegroup name}: {nodeunit name}) for node

		if newNu.Spec.SetNode.Labels == nil {
			newNu.Spec.SetNode.Labels = make(map[string]string)
		}
		newNu.Spec.SetNode.Labels[ng.Name] = newNu.Name

		if newNu.Labels == nil {
			newNu.Labels = make(map[string]string)
		}
		// set node group as owner
		newNu.Labels[ng.Name] = constant.NodeGroupSuperedge

		if _, err := crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), newNu, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func GetUnitByGroup(unitLister crdv1listers.NodeUnitLister, ng *sitev1.NodeGroup) (sets.String, map[string]*sitev1.NodeUnit, error) {
	groupUnitSet := sets.NewString()
	unitMap := make(map[string]*sitev1.NodeUnit, 3)
	appendFunc := func(n interface{}) {
		nu, ok := n.(*sitev1.NodeUnit)
		if !ok {
			return
		}
		unitMap[nu.Name] = nu
		groupUnitSet.Insert(nu.Name)
	}
	if len(ng.Spec.NodeUnits) > 0 {
		for _, nuName := range ng.Spec.NodeUnits {
			nu, err := unitLister.Get(nuName)
			if err != nil && errors.IsNotFound(err) {
				klog.V(4).InfoS("Nodegroup units not found", "node group", ng.Name, "node unit", nuName)
				continue
			} else if err != nil {
				return nil, nil, err
			}
			unitMap[nu.Name] = nu
			groupUnitSet.Insert(nu.Name)
		}
	}
	if ng.Spec.Selector != nil {
		labelSelector := &metav1.LabelSelector{
			MatchLabels:      ng.Spec.Selector.MatchLabels,
			MatchExpressions: ng.Spec.Selector.MatchExpressions,
		}
		unitSelector, err := metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return nil, nil, err
		}
		ListNodeUnitFromLister(unitLister, unitSelector, appendFunc)
	}
	if len(ng.Spec.AutoFindNodeKeys) > 0 {
		labelSelector := &metav1.LabelSelector{
			MatchLabels: map[string]string{
				constant.NodeUnitAutoFindLabel: HashAutoFindKeys(ng.Spec.AutoFindNodeKeys),
				ng.Name:                        constant.NodeGroupSuperedge,
			},
		}
		unitSelector, err := metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return nil, nil, err
		}
		ListNodeUnitFromLister(unitLister, unitSelector, appendFunc)
	}

	return groupUnitSet, unitMap, nil

}

func GetNodesByUnit(nodeLister corelisters.NodeLister, nu *sitev1.NodeUnit) (sets.String, map[string]*corev1.Node, error) {

	unitNodeSet := sets.NewString()
	nodeMap := make(map[string]*corev1.Node, 5)
	appendFunc := func(n interface{}) {
		node, ok := n.(*corev1.Node)
		if !ok {
			return
		}
		nodeMap[node.Name] = node
		unitNodeSet.Insert(node.Name)
	}
	if len(nu.Spec.Nodes) > 0 {
		nodeSelector := labels.NewSelector()
		nRequire, err := labels.NewRequirement(
			corev1.LabelHostname,
			selection.In,
			nu.Spec.Nodes,
		)
		if err != nil {
			return nil, nil, err
		}
		nodeSelector.Add(*nRequire)
		ListNodeFromLister(nodeLister, nodeSelector, appendFunc)
	}

	if nu.Spec.Selector != nil {
		labelSelector := &metav1.LabelSelector{
			MatchLabels:      nu.Spec.Selector.MatchLabels,
			MatchExpressions: nu.Spec.Selector.MatchExpressions,
		}
		nodeSelector, err := metav1.LabelSelectorAsSelector(labelSelector)
		if err != nil {
			return nil, nil, err
		}
		ListNodeFromLister(nodeLister, nodeSelector, appendFunc)
	}
	return unitNodeSet, nodeMap, nil
}

/*
  Nodes Annotations
*/

func AddNodesAnnotations(kubeClient clientset.Interface, nodeNames []string, annotations []string) error {
	for _, nodeName := range nodeNames {
		node, err := kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Get Node: %s, error: %#v", nodeName, err)
			continue
		}

		if err := AddNodeAnnotations(kubeClient, node, annotations); err != nil {
			klog.Errorf("Update Node: %s, error: %#v", node.Name, err)
			return err
		}
	}

	return nil
}

func RemoveNodesAnnotations(kubeClient clientset.Interface, nodeNames []string, annotations []string) {
	for _, nodeName := range nodeNames {
		node, err := kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Get Node: %s, error: %#v", nodeName, err)
			continue
		}

		if err := RemoveNodeAnnotations(kubeClient, node, annotations); err != nil {
			klog.Errorf("Remove node: %s annotations nodeunit: %s flags error: %#v", nodeName, annotations, err)
			continue
		}
	}
}

/*
  Node Annotations
*/

func AddNodeAnnotations(kubeclient clientset.Interface, node *corev1.Node, annotations []string) error {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	var nodeUnits []string
	value, ok := node.Annotations[constant.NodeUnitSuperedge]
	if ok && value != "\"\"" && value != "null" { // nil annotations Unmarshal can be failed
		if err := json.Unmarshal(json.RawMessage(value), &nodeUnits); err != nil {
			klog.Errorf("Unmarshal node: %s annotations: %s, error: %#v", node.Name, util.ToJson(value), err)
			return err
		}
	}

	nodeUnits = append(nodeUnits, annotations...)
	nodeUnits = util.RemoveDuplicateElement(nodeUnits)
	nodeUnits = util.DeleteSliceElement(nodeUnits, "")
	node.Annotations[constant.NodeUnitSuperedge] = util.ToJson(nodeUnits)
	if _, err := kubeclient.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Update Node: %s, error: %#v", node.Name, err)
		return err
	}

	return nil
}

func RemoveNodeAnnotations(kubeclient clientset.Interface, node *corev1.Node, annotations []string) error {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}

	var nodeUnits []string
	value, ok := node.Annotations[constant.NodeUnitSuperedge]
	if ok && value != "\"\"" && value != "null" { // nil annotations Unmarshal can be failed
		if err := json.Unmarshal(json.RawMessage(value), &nodeUnits); err != nil {
			klog.Errorf("Unmarshal node: %s annotations: %s, error: %#v", node.Name, util.ToJson(value), err)
			return err
		}
	}

	for _, annotation := range annotations {
		nodeUnits = util.DeleteSliceElement(nodeUnits, annotation)
	}
	nodeUnits = util.DeleteSliceElement(nodeUnits, "")
	node.Annotations[constant.NodeUnitSuperedge] = util.ToJson(nodeUnits)
	if _, err := kubeclient.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Update Node: %s, error: %#v", node.Name, err)
		return err
	}

	return nil
}

func ResetNodeUnitAnnotations(kubeclient clientset.Interface, node *corev1.Node, annotations []string) error {
	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	annotations = util.DeleteSliceElement(annotations, "")
	node.Annotations[constant.NodeUnitSuperedge] = util.ToJson(annotations)
	if _, err := kubeclient.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Update Node: %s, error: %#v", node.Name, err)
		return err
	}

	return nil
}

const (
	KubernetesEdgeNodeRoleKey   = "node-role.kubernetes.io/Edge"
	KubernetesCloudNodeRoleKey  = "node-role.kubernetes.io/Cloud"
	KubernetesMasterNodeRoleKey = "node-role.kubernetes.io/Master"
)

func SetNodeRole(kubeClient clientset.Interface, node *corev1.Node) error {
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}

	if _, ok := node.Labels[util.EdgeNodeLabelKey]; ok {
		edgeNodeLabel := map[string]string{
			KubernetesEdgeNodeRoleKey: "",
		}
		if err := utilkube.AddNodeLabel(kubeClient, node.Name, edgeNodeLabel); err != nil {
			klog.Errorf("Add edge Node role label error: %v", err)
			return err
		}
	}

	if _, ok := node.Labels[util.CloudNodeLabelKey]; ok {
		cloudNodeLabel := map[string]string{
			KubernetesCloudNodeRoleKey: "",
		}
		if err := utilkube.AddNodeLabel(kubeClient, node.Name, cloudNodeLabel); err != nil {
			klog.Errorf("Add Cloud node label error: %v", err)
			return err
		}
	}

	return nil
}

func SetNodeToNodes(kubeClient clientset.Interface, nu *sitev1.NodeUnit, nodeMaps map[string]*corev1.Node) error {
	klog.V(4).InfoS("SetNodeToNodes SetNode", "nu.Spec.SetNode", util.ToJson(nu.Spec.SetNode))
	for _, node := range nodeMaps {
		// first check if need update node
		labelSet, annoSet, taintSet := true, true, true
		if nu.Spec.SetNode.Labels != nil {
			for k, v := range nu.Spec.SetNode.Labels {
				if node.Labels == nil || node.Labels[k] != v {
					labelSet = false
					break
				}
			}
		}
		if nu.Spec.SetNode.Annotations != nil {
			for k, v := range nu.Spec.SetNode.Annotations {
				if node.Annotations == nil || node.Labels[k] != v {
					annoSet = false
					break
				}
			}
		}

		if nu.Spec.SetNode.Taints != nil {
			// if node.Spec.Taints ==nil || reflect.DeepEqual()
			if node.Spec.Taints == nil {
				taintSet = false
			} else {
				for _, setTaint := range nu.Spec.SetNode.Taints {
					if !TaintInSlices(node.Spec.Taints, setTaint) {
						taintSet = false
						break
					}
				}
			}
		}
		if labelSet && annoSet && taintSet {
			continue
		}
		// deep copy for don't update informer cache
		node := node.DeepCopy()
		// set labels
		if nu.Spec.SetNode.Labels != nil {
			if node.Labels == nil {
				node.Labels = make(map[string]string)
				node.Labels = nu.Spec.SetNode.Labels
			} else {
				for key, val := range nu.Spec.SetNode.Labels {
					node.Labels[key] = val
				}
			}
		}
		klog.V(4).InfoS("SetNodeToNodes node labels", "node.Labels", util.ToJson(node.Labels))

		// setNode annotations
		if nu.Spec.SetNode.Annotations != nil {
			if node.Annotations == nil {
				node.Annotations = make(map[string]string)
				node.Annotations = nu.Spec.SetNode.Annotations
			} else {
				for key, val := range nu.Spec.SetNode.Annotations {
					node.Annotations[key] = val
				}
			}
		}

		// setNode taints
		if nu.Spec.SetNode.Taints != nil {
			if node.Spec.Taints == nil {
				node.Spec.Taints = []corev1.Taint{}
			}
			node.Spec.Taints = append(node.Spec.Taints, nu.Spec.SetNode.Taints...)
		}

		if _, err := kubeClient.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("SetNode to node update Node: %s, error: %#v", node.Name, err)
			return err
		}
	}
	return nil
}

func DeleteNodesFromSetNode(kubeClient clientset.Interface, nu *sitev1.NodeUnit, nodeMaps map[string]*corev1.Node) error {
	for _, node := range nodeMaps {
		newNode := node.DeepCopy()
		if nu.Spec.SetNode.Labels != nil {
			if newNode.Labels != nil {
				for k, _ := range nu.Spec.SetNode.Labels {
					delete(newNode.Labels, k)
				}
			}
		}

		if nu.Spec.SetNode.Annotations != nil {
			if newNode.Annotations != nil {
				for k, _ := range nu.Spec.SetNode.Annotations {
					delete(newNode.Annotations, k)
				}
			}
		}
		newNode.Spec.Taints = deleteTaintItems(newNode.Spec.Taints, nu.Spec.SetNode.Taints)

		if _, err := kubeClient.CoreV1().Nodes().Update(context.TODO(), newNode, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Update Node: %s, error: %#v", node.Name, err)
			return err
		}
	}
	return nil
}

func DeleteNodeUnitFromSetNode(crdClient *crdClientset.Clientset, ng *sitev1.NodeGroup, unitMaps map[string]*sitev1.NodeUnit) error {
	for _, nu := range unitMaps {
		newNu := nu.DeepCopy()

		// delete node group owner label
		if newNu.Labels != nil {
			delete(newNu.Labels, ng.Name)
		}

		// delete setNode which will trigger node unit controller clean node label
		if newNu.Spec.SetNode.Labels != nil {
			delete(newNu.Spec.SetNode.Labels, ng.Name)
		}

		_, err := crdClient.SiteV1alpha2().NodeUnits().Update(context.TODO(), newNu, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

	}
	return nil
}

func SetUpdatedValue(oldValues map[string]string, curValues map[string]string, modifyValues *map[string]string) {
	// delete old values
	for k, _ := range oldValues {
		if _, found := (*modifyValues)[k]; found {
			delete((*modifyValues), k)
		}
	}

	// set new values
	for k, v := range curValues {
		(*modifyValues)[k] = v
	}
}

func UpdtateNodeFromSetNode(kubeClient clientset.Interface, oldSetNode sitev1.SetNode, curSetNode sitev1.SetNode, nodeNames []string) {
	for _, nodeName := range nodeNames {
		node, err := kubeClient.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Get node: %s error: %s", node.Name, err)
			continue
		}

		if node.Labels == nil {
			node.Labels = make(map[string]string)
		}
		SetUpdatedValue(oldSetNode.Labels, curSetNode.Labels, &node.Labels)

		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}
		SetUpdatedValue(oldSetNode.Annotations, curSetNode.Annotations, &node.Annotations)

		if node.Spec.Taints == nil {
			node.Spec.Taints = []corev1.Taint{}
			node.Spec.Taints = append(node.Spec.Taints, curSetNode.Taints...)
		} else {
			node.Spec.Taints = updateTaintItems(node.Spec.Taints, oldSetNode.Taints, curSetNode.Taints)
		}

		if _, err := kubeClient.CoreV1().Nodes().Update(context.TODO(), node, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Update Node: %s, error: %#v", node.Name, err)
			continue
		}
	}
}

func updateTaintItems(currTaintItems []corev1.Taint, oldTaintItems []corev1.Taint, newTaintItems []corev1.Taint) []corev1.Taint {

	deletedResults := []corev1.Taint{}
	Results := []corev1.Taint{}

	newMap := make(map[string]bool)
	for _, s := range newTaintItems {
		newMap[s.Key] = true
	}

	deleteMap := make(map[string]bool)
	for _, s := range oldTaintItems {
		deleteMap[s.Key] = true
	}

	// filter old values
	for _, val := range currTaintItems {
		if _, ok := deleteMap[val.Key]; ok {
			continue
		}
		deletedResults = append(deletedResults, val)
	}
	// filter new values
	for _, val := range deletedResults {
		if _, ok := newMap[val.Key]; ok {
			continue
		}
		Results = append(Results, val)
	}

	Results = append(Results, newTaintItems...)
	return Results
}

func deleteTaintItems(currentTaintItems, deleteTaintItems []corev1.Taint) []corev1.Taint {
	result := []corev1.Taint{}
	deleteMap := make(map[string]bool)
	for _, s := range deleteTaintItems {
		deleteMap[s.Key] = true
	}

	for _, val := range currentTaintItems {
		if _, ok := deleteMap[val.Key]; ok {
			continue
		} else {
			result = append(result, val)
		}
	}
	return result
}

func NeedUpdateNode(nodeNamesOld, nodeNamesCur []string) (removeNodes []string, updateNodes []string) {
	curNodesMap := make(map[string]bool, len(nodeNamesCur))
	for _, nodeName := range nodeNamesCur {
		curNodesMap[nodeName] = true
	}

	for _, nodeName := range nodeNamesOld {
		if ok := curNodesMap[nodeName]; ok {
			updateNodes = append(updateNodes, nodeName)
		} else {
			removeNodes = append(removeNodes, nodeName)
		}
	}
	return
}

func HashAutoFindKeys(keyslices []string) string {
	if len(keyslices) == 0 {
		return ""
	}
	sort.Strings(keyslices)

	h := sha1.New()
	for _, key := range keyslices {
		h.Write([]byte(key))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TaintInSlices(taintSlice []corev1.Taint, target corev1.Taint) bool {
	for _, t := range taintSlice {
		if t.Key == target.Key && t.Effect == target.Effect && t.Value == target.Value {
			return true
		}
	}
	return false
}
